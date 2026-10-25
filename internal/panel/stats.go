package panel

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// Stats is what the stats API reports for one server.
type Stats struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Game          string   `json:"game"`
	State         string   `json:"state"`
	Ready         bool     `json:"ready"` // accepting players
	Players       []string `json:"players"`
	PlayerCount   int      `json:"playerCount"`
	MaxPlayers    int      `json:"maxPlayers,omitempty"`
	UptimeSeconds int64    `json:"uptimeSeconds,omitempty"`
	CPUPercent    float64  `json:"cpuPercent"`
	MemoryBytes   int64    `json:"memoryBytes"`
}

// liveStats is what the panel learns while a server runs. It resets when the
// server stops.
type liveStats struct {
	since   time.Time
	ready   bool
	players map[string]time.Time // name -> joined at
	cpu     float64
	memory  int64
}

type statsTracker struct {
	mu       sync.Mutex
	live     map[string]*liveStats
	patterns map[string]*regexp.Regexp // compiled stats patterns by source
}

func newStatsTracker() *statsTracker {
	return &statsTracker{live: map[string]*liveStats{}, patterns: map[string]*regexp.Regexp{}}
}

// re compiles a pattern once. Templates are checked when loaded, so errors
// here only come from templates edited while running; those just don't match.
func (t *statsTracker) re(pattern string) *regexp.Regexp {
	if pattern == "" {
		return nil
	}
	if r, ok := t.patterns[pattern]; ok {
		return r
	}
	r, _ := regexp.Compile(pattern)
	t.patterns[pattern] = r
	return r
}

// observe reads one console line and reports a player who joined or left.
func (t *statsTracker) observe(id, line string, st templates.Stats) (joined, left string, ready bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ls := t.live[id]
	if ls == nil {
		return "", "", false // not running
	}
	line = plainText(line)
	if r := t.re(st.Ready); r != nil && !ls.ready && r.MatchString(line) {
		ls.ready = true
		ready = true
	}
	if name := matchPlayer(t.re(st.Join), line); name != "" {
		if _, already := ls.players[name]; !already {
			ls.players[name] = time.Now()
			joined = name
		}
	}
	if name := matchPlayer(t.re(st.Leave), line); name != "" {
		if _, was := ls.players[name]; was {
			delete(ls.players, name)
			left = name
		}
	}
	return joined, left, ready
}

// formatting matches ANSI escape sequences and Minecraft's § color codes,
// which games may put around player names in console output.
var formatting = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07|§[0-9a-fk-orA-FK-OR]|\r`)

// plainText removes formatting so stats patterns match what a person sees.
func plainText(line string) string {
	return formatting.ReplaceAllString(line, "")
}

func matchPlayer(r *regexp.Regexp, line string) string {
	if r == nil {
		return ""
	}
	m := r.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	if i := r.SubexpIndex("player"); i > 0 {
		return m[i]
	}
	return ""
}

// stateChanged starts or clears live stats and returns the players who were
// online when the server stopped.
func (t *statsTracker) stateChanged(id, state string) (dropped []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch state {
	case protocol.StateRunning:
		if t.live[id] == nil {
			t.live[id] = &liveStats{since: time.Now(), players: map[string]time.Time{}}
		}
	case protocol.StateStopping:
	default:
		if ls := t.live[id]; ls != nil {
			for name := range ls.players {
				dropped = append(dropped, name)
			}
			sort.Strings(dropped)
			delete(t.live, id)
		}
	}
	return dropped
}

func (t *statsTracker) resources(id string, cpu float64, mem int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ls := t.live[id]; ls != nil {
		ls.cpu, ls.memory = cpu, mem
	}
}

func (s *Server) stats(in Instance) Stats {
	tpl, _ := s.template(in.TemplateID)
	settings := tpl.Settings(in.Settings)
	out := Stats{ID: in.ID, Name: in.Name, Game: tpl.Name, State: in.State, Players: []string{}}
	out.MaxPlayers, _ = strconv.Atoi(settings[tpl.Stats.MaxPlayers])

	s.statsTracker.mu.Lock()
	defer s.statsTracker.mu.Unlock()
	if ls := s.statsTracker.live[in.ID]; ls != nil {
		for name := range ls.players {
			out.Players = append(out.Players, name)
		}
		sort.Strings(out.Players)
		out.Ready = ls.ready || tpl.Stats.Ready == ""
		out.UptimeSeconds = int64(time.Since(ls.since).Seconds())
		out.CPUPercent, out.MemoryBytes = ls.cpu, ls.memory
	}
	out.PlayerCount = len(out.Players)
	return out
}

func (s *Server) handleAllStats(w http.ResponseWriter, r *http.Request) {
	list := s.store.list()
	out := make([]Stats, 0, len(list))
	for _, in := range list {
		out = append(out, s.stats(in))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	in, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return
	}
	writeJSON(w, http.StatusOK, s.stats(in))
}
