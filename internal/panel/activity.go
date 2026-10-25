package panel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// activity is the activity log: game events and what people did, newest
// first. It keeps the latest activityKeep entries, saved one JSON object per
// line so a crash loses at most the line being written.
type activity struct {
	mu      sync.Mutex
	path    string
	events  []Event // newest first
	written int     // lines in the file
}

const activityKeep = 1000

func openActivity(dir string) (*activity, error) {
	a := &activity{}
	if dir == "" {
		return a, nil
	}
	a.path = filepath.Join(dir, "activity.jsonl")
	f, err := os.Open(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			a.events = append(a.events, e)
		}
		a.written++
	}
	// The file is oldest first.
	for i, j := 0, len(a.events)-1; i < j; i, j = i+1, j-1 {
		a.events[i], a.events[j] = a.events[j], a.events[i]
	}
	if len(a.events) > activityKeep {
		a.events = a.events[:activityKeep]
	}
	return a, sc.Err()
}

// add stamps the event with the time and records it. Stamping under the lock
// keeps the log in order when events arrive together.
func (a *activity) add(e Event) Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	e.Time = time.Now().UTC()
	a.events = append([]Event{e}, a.events...)
	if len(a.events) > activityKeep {
		a.events = a.events[:activityKeep]
	}
	if a.path == "" {
		return e
	}
	var err error
	if a.written >= 2*activityKeep {
		err = a.rewrite()
	} else {
		err = a.append(e)
	}
	if err != nil {
		slog.Error("saving activity log", "err", err)
	}
	return e
}

func (a *activity) append(e Event) error {
	line, _ := json.Marshal(e)
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	a.written++
	return nil
}

// rewrite trims the file to the entries kept in memory.
func (a *activity) rewrite() error {
	var b strings.Builder
	for i := len(a.events) - 1; i >= 0; i-- {
		line, _ := json.Marshal(a.events[i])
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	a.written = len(a.events)
	return nil
}

// list returns up to limit entries, optionally for one server or one kind:
// "action" for what people did, "game" for everything else.
func (a *activity) list(limit int, instanceID, kind string) []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []Event{}
	for _, e := range a.events {
		if len(out) == limit {
			break
		}
		if instanceID != "" && e.Instance.ID != instanceID {
			continue
		}
		if (kind == "action" && e.Type != EventAction) || (kind == "game" && e.Type == EventAction) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	writeJSON(w, http.StatusOK, s.activity.list(min(limit, activityKeep), q.Get("instance"), q.Get("kind")))
}

// audit records something a person or token did.
func (s *Server) audit(p principal, in *Instance, message string) {
	e := Event{Type: EventAction, Actor: p.Name, Message: message}
	if in != nil {
		e.Instance = EventInstance{ID: in.ID, Name: in.Name, Game: s.stats(*in).Game}
	}
	s.emit(e)
}

// auditNote lets a handler add details to its activity log entry: a name for
// {note}, or the server it created.
type auditNote struct {
	text     string
	instance *Instance
}

type auditNoteKey struct{}

func noteFor(r *http.Request) *auditNote {
	if n, ok := r.Context().Value(auditNoteKey{}).(*auditNote); ok {
		return n
	}
	return &auditNote{} // not a logged route; the note goes nowhere
}

var actionMessages = map[string]string{
	"install": "installed or updated the server",
	"start":   "started the server",
	"stop":    "stopped the server",
	"restart": "restarted the server",
	"command": "ran {note}",
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logged wraps a handler so a successful request is recorded in the activity
// log. In what, {action}, {path}, {name} and {note} are filled in from the
// request and from what the handler noted.
func (s *Server) logged(what string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in *Instance
		if found, ok := s.store.get(r.PathValue("id")); ok {
			in = &found // looked up first, so deletes are still named
		}
		note := &auditNote{}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r.WithContext(context.WithValue(r.Context(), auditNoteKey{}, note)))
		if rec.status >= 300 {
			return
		}
		if note.instance != nil {
			in = note.instance
		}
		msg := strings.NewReplacer("{action}", actionMessages[r.PathValue("action")]).Replace(what)
		msg = strings.NewReplacer(
			"{path}", r.URL.Query().Get("path"),
			"{name}", r.PathValue("name"),
			"{note}", note.text,
		).Replace(msg)
		s.audit(principalOf(r), in, strings.TrimSpace(msg))
	}
}
