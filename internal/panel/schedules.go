package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// Schedule runs an action on a server at times given by a cron expression,
// in the panel's time zone.
type Schedule struct {
	ID         string     `json:"id"`
	InstanceID string     `json:"instanceId"`
	Name       string     `json:"name"`
	Action     string     `json:"action"` // restart, stop, start, backup or command
	Cron       string     `json:"cron"`
	Command    string     `json:"command,omitempty"` // for action "command"
	Keep       int        `json:"keep,omitempty"`    // for "backup": keep this many newest backups; 0 keeps all
	Warn       bool       `json:"warn"`              // for "restart" and "stop": warn players in game first
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastRun    *time.Time `json:"lastRun,omitempty"`
	LastResult string     `json:"lastResult,omitempty"`
	NextRun    *time.Time `json:"nextRun,omitempty"` // filled in for responses, not stored
}

var scheduleActions = []string{"restart", "stop", "start", "backup", "command"}

// warnings are how long before a scheduled restart or stop players are told.
var warnings = []time.Duration{5 * time.Minute, time.Minute, 10 * time.Second}

// scheduler runs one goroutine per enabled schedule.
type scheduler struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func (s *Server) startSchedules() {
	for _, sc := range s.schedules.all() {
		s.reschedule(sc.ID)
	}
}

// reschedule stops a schedule's goroutine and starts a new one if the
// schedule still exists and is enabled. Call it after any change.
func (s *Server) reschedule(id string) {
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	if cancel, ok := s.sched.running[id]; ok {
		cancel()
		delete(s.sched.running, id)
	}
	sc, ok := s.schedule(id)
	if !ok || !sc.Enabled {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.sched.running[id] = cancel
	go s.runLoop(ctx, id)
}

func (s *Server) schedule(id string) (Schedule, bool) {
	for _, sc := range s.schedules.all() {
		if sc.ID == id {
			return sc, true
		}
	}
	return Schedule{}, false
}

func nextRun(sc Schedule, after time.Time) time.Time {
	spec, err := parseCron(sc.Cron)
	if err != nil {
		return time.Time{}
	}
	return spec.next(after)
}

// sleepUntil waits for t, returning false if ctx ends first.
func sleepUntil(ctx context.Context, t time.Time) bool {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) runLoop(ctx context.Context, id string) {
	for {
		sc, ok := s.schedule(id)
		if !ok {
			return
		}
		at := nextRun(sc, time.Now())
		if at.IsZero() {
			return
		}
		if sc.Warn && (sc.Action == "restart" || sc.Action == "stop") {
			for _, w := range warnings {
				if warnAt := at.Add(-w); warnAt.After(time.Now()) {
					if !sleepUntil(ctx, warnAt) {
						return
					}
					s.warnPlayers(sc, w)
				}
			}
		}
		if !sleepUntil(ctx, at) {
			return
		}
		s.runSchedule(sc, principal{Name: "Scheduler"})
	}
}

// warnPlayers announces an upcoming restart or stop in game, when the game's
// template says how and the server is running.
func (s *Server) warnPlayers(sc Schedule, before time.Duration) {
	in, ok := s.store.get(sc.InstanceID)
	if !ok || in.State != protocol.StateRunning {
		return
	}
	tpl, ok := s.template(in.TemplateID)
	a := s.agent(in.Agent)
	if !ok || tpl.Say == "" || a == nil {
		return
	}
	verb := "restarting"
	if sc.Action == "stop" {
		verb = "shutting down"
	}
	line, err := templates.Render(tpl.Say, map[string]string{"message": fmt.Sprintf("Server %s in %s", verb, humanDuration(before))})
	if err != nil {
		return
	}
	s.consoles.append(in.ID, "> "+line)
	if err := a.request(context.Background(), protocol.TypeCommand, protocol.Command{ID: in.ID, Line: line}); err != nil {
		slog.Warn("schedule warning failed", "schedule", sc.Name, "err", err)
	}
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		if d == time.Minute {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", d/time.Minute)
	default:
		return fmt.Sprintf("%d seconds", d/time.Second)
	}
}

// runSchedule does the schedule's action now and records the result.
func (s *Server) runSchedule(sc Schedule, by principal) string {
	result, message := s.doScheduledAction(sc)
	now := time.Now().UTC()
	s.schedules.change(func(list []Schedule) []Schedule {
		for i := range list {
			if list[i].ID == sc.ID {
				list[i].LastRun, list[i].LastResult = &now, result
			}
		}
		return list
	})
	if in, ok := s.store.get(sc.InstanceID); ok && message != "" {
		s.audit(by, &in, message+" ("+sc.Name+")")
	}
	return result
}

// doScheduledAction returns a short result for the schedule list, and an
// activity message when something was done.
func (s *Server) doScheduledAction(sc Schedule) (result, message string) {
	in, ok := s.store.get(sc.InstanceID)
	if !ok {
		return "server no longer exists", ""
	}
	a := s.agent(in.Agent)
	if a == nil {
		return "skipped: machine offline", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	running := in.State == protocol.StateRunning || in.State == protocol.StateStarting
	var err error
	switch sc.Action {
	case "restart":
		if in.State != protocol.StateRunning {
			return "skipped: server wasn't running", ""
		}
		if err = a.request(ctx, protocol.TypeStop, protocol.InstanceRef{ID: in.ID}); err == nil {
			go s.startWhenStopped(in.ID)
			return "restarted", "restarted the server"
		}
	case "stop":
		if !running {
			return "skipped: server wasn't running", ""
		}
		if err = a.request(ctx, protocol.TypeStop, protocol.InstanceRef{ID: in.ID}); err == nil {
			return "stopped", "stopped the server"
		}
	case "start":
		if in.State != protocol.StateStopped && in.State != protocol.StateError {
			return "skipped: server was " + strings.ReplaceAll(in.State, "_", " "), ""
		}
		var spec protocol.InstanceSpec
		if spec, err = s.spec(in); err == nil {
			if err = a.request(ctx, protocol.TypeStart, spec); err == nil {
				return "started", "started the server"
			}
		}
	case "backup":
		var spec protocol.InstanceSpec
		if spec, err = s.spec(in); err == nil {
			if err = a.request(ctx, protocol.TypeBackupCreate, protocol.BackupCreate{InstanceSpec: spec, Keep: sc.Keep}); err == nil {
				return "backup started", "made a backup"
			}
		}
	case "command":
		if in.State != protocol.StateRunning {
			return "skipped: server wasn't running", ""
		}
		s.consoles.append(in.ID, "> "+sc.Command)
		if err = a.request(ctx, protocol.TypeCommand, protocol.Command{ID: in.ID, Line: sc.Command}); err == nil {
			return "sent", "ran " + fmt.Sprintf("%q", sc.Command)
		}
	}
	return "failed: " + err.Error(), ""
}

func (s *Server) scheduleView(sc Schedule) Schedule {
	if sc.Enabled {
		if at := nextRun(sc, time.Now()); !at.IsZero() {
			sc.NextRun = &at
		}
	}
	return sc
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	out := []Schedule{}
	for _, sc := range s.schedules.all() {
		if sc.InstanceID == id {
			out = append(out, s.scheduleView(sc))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// validSchedule checks and tidies a schedule from a request.
func validSchedule(sc *Schedule) error {
	sc.Name = strings.TrimSpace(sc.Name)
	sc.Cron = strings.Join(strings.Fields(sc.Cron), " ")
	sc.Command = strings.TrimSpace(sc.Command)
	if !slices.Contains(scheduleActions, sc.Action) {
		return fmt.Errorf("action must be one of %s", strings.Join(scheduleActions, ", "))
	}
	if _, err := parseCron(sc.Cron); err != nil {
		return err
	}
	if nextRun(*sc, time.Now()).IsZero() {
		return fmt.Errorf("that schedule never runs")
	}
	if sc.Action == "command" && sc.Command == "" {
		return fmt.Errorf("a command is required")
	}
	if strings.ContainsAny(sc.Command, "\r\n") {
		return fmt.Errorf("the command must be one line")
	}
	if sc.Keep < 0 || sc.Keep > 1000 {
		return fmt.Errorf("keep must be between 0 and 1000")
	}
	if sc.Name == "" {
		sc.Name = strings.ToUpper(sc.Action[:1]) + sc.Action[1:]
	}
	if sc.Action != "command" {
		sc.Command = ""
	}
	if sc.Action != "backup" {
		sc.Keep = 0
	}
	if sc.Action != "restart" && sc.Action != "stop" {
		sc.Warn = false
	}
	return nil
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	in, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return
	}
	var sc Schedule
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := validSchedule(&sc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc.ID, sc.InstanceID, sc.CreatedAt = randomToken()[:16], in.ID, time.Now().UTC()
	sc.LastRun, sc.LastResult, sc.NextRun = nil, "", nil
	if err := s.schedules.change(func(list []Schedule) []Schedule { return append(list, sc) }); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reschedule(sc.ID)
	noteFor(r).text = sc.Name
	writeJSON(w, http.StatusCreated, s.scheduleView(sc))
}

// scheduleFor finds the schedule in the path, checking it belongs to the server.
func (s *Server) scheduleFor(w http.ResponseWriter, r *http.Request) (Schedule, bool) {
	sc, ok := s.schedule(r.PathValue("scheduleId"))
	if !ok || sc.InstanceID != r.PathValue("id") {
		writeError(w, http.StatusNotFound, "no such schedule")
		return Schedule{}, false
	}
	noteFor(r).text = sc.Name
	return sc, true
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	old, ok := s.scheduleFor(w, r)
	if !ok {
		return
	}
	var sc Schedule
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := validSchedule(&sc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc.ID, sc.InstanceID, sc.CreatedAt = old.ID, old.InstanceID, old.CreatedAt
	sc.LastRun, sc.LastResult, sc.NextRun = old.LastRun, old.LastResult, nil
	err := s.schedules.change(func(list []Schedule) []Schedule {
		for i := range list {
			if list[i].ID == sc.ID {
				list[i] = sc
			}
		}
		return list
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reschedule(sc.ID)
	noteFor(r).text = sc.Name
	writeJSON(w, http.StatusOK, s.scheduleView(sc))
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scheduleFor(w, r)
	if !ok {
		return
	}
	if err := s.removeSchedules(func(x Schedule) bool { return x.ID == sc.ID }); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeSchedules(match func(Schedule) bool) error {
	var removed []string
	err := s.schedules.change(func(list []Schedule) []Schedule {
		return slices.DeleteFunc(list, func(x Schedule) bool {
			if match(x) {
				removed = append(removed, x.ID)
				return true
			}
			return false
		})
	})
	for _, id := range removed {
		s.reschedule(id)
	}
	return err
}

func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scheduleFor(w, r)
	if !ok {
		return
	}
	result := s.runSchedule(sc, principalOf(r))
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}
