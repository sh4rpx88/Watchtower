package panel

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

type Agent struct {
	Name          string    `json:"name"`
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	Version       string    `json:"version"`
	ConnectedAt   time.Time `json:"connectedAt"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	NumCPU        int       `json:"numCpu"`
	MemoryTotal   int64     `json:"memoryTotal,omitempty"`
	DiskTotal     int64     `json:"diskTotal,omitempty"`
	DiskFree      int64     `json:"diskFree,omitempty"`
}

// agentConn is a connected agent and the requests waiting on its replies.
type agentConn struct {
	info    Agent
	conn    *websocket.Conn
	ctx     context.Context
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan protocol.Response
}

var errAgentOffline = errors.New("agent is offline")

// request sends a request to the agent and waits for its response.
func (a *agentConn) request(ctx context.Context, msgType string, payload any) error {
	_, err := a.call(ctx, msgType, payload)
	return err
}

// call is request for requests that return data.
func (a *agentConn) call(ctx context.Context, msgType string, payload any) (json.RawMessage, error) {
	env, err := protocol.New(msgType, payload)
	if err != nil {
		return nil, err
	}
	env.ID = randomToken()[:16]
	reply := make(chan protocol.Response, 1)
	a.mu.Lock()
	a.pending[env.ID] = reply
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, env.ID)
		a.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a.writeMu.Lock()
	err = wsjson.Write(ctx, a.conn, env)
	a.writeMu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case r := <-reply:
		if !r.OK {
			return nil, errors.New(r.Error)
		}
		return r.Data, nil
	case <-ctx.Done():
		return nil, errors.New("agent did not answer in time")
	case <-a.ctx.Done():
		return nil, errAgentOffline
	}
}

func (s *Server) agent(name string) *agentConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents[name]
}

// handleAgentConnect upgrades an agent's request to a WebSocket. Agents dial
// the panel, so machines behind NAT need no open ports.
func (s *Server) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AgentToken)) != 1 {
		http.Error(w, "bad agent token", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var env protocol.Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil || env.Type != protocol.TypeHello {
		conn.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	var hello protocol.Hello
	if err := json.Unmarshal(env.Payload, &hello); err != nil || hello.AgentName == "" {
		conn.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}
	// Live states recorded before this connection may be stale (the agent or
	// panel restarted). The agent replays the real state of every server it
	// knows right after the welcome, which overrides this.
	s.resetLiveStates(hello.AgentName)
	welcome, _ := protocol.New(protocol.TypeWelcome, protocol.Welcome{PanelVersion: Version})
	if err := wsjson.Write(ctx, conn, welcome); err != nil {
		return
	}

	now := time.Now()
	a := &agentConn{
		info: Agent{Name: hello.AgentName, Hostname: hello.Hostname, OS: hello.OS, Arch: hello.Arch,
			Version: hello.Version, ConnectedAt: now, LastHeartbeat: now},
		conn: conn, ctx: ctx, pending: map[string]chan protocol.Response{},
	}
	s.mu.Lock()
	if old := s.agents[a.info.Name]; old != nil {
		old.conn.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
	}
	s.agents[a.info.Name] = a
	s.mu.Unlock()
	slog.Info("agent connected", "name", a.info.Name, "host", a.info.Hostname)
	defer func() {
		s.mu.Lock()
		if s.agents[a.info.Name] == a {
			delete(s.agents, a.info.Name)
		}
		s.mu.Unlock()
		slog.Info("agent disconnected", "name", a.info.Name)
	}()

	for {
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			return
		}
		s.handleAgentMessage(a, env)
	}
}

func (s *Server) handleAgentMessage(a *agentConn, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeHeartbeat:
		var hb protocol.Heartbeat
		if json.Unmarshal(env.Payload, &hb) == nil {
			s.mu.Lock()
			a.info.LastHeartbeat = time.Now()
			a.info.NumCPU, a.info.MemoryTotal = hb.NumCPU, hb.MemoryTotal
			a.info.DiskTotal, a.info.DiskFree = hb.DiskTotal, hb.DiskFree
			s.mu.Unlock()
		}
	case protocol.TypeResponse:
		var r protocol.Response
		if json.Unmarshal(env.Payload, &r) == nil {
			a.mu.Lock()
			ch := a.pending[env.ID]
			a.mu.Unlock()
			if ch != nil {
				ch <- r
			}
		}
	case protocol.TypeStatus:
		var st protocol.Status
		if json.Unmarshal(env.Payload, &st) == nil {
			s.setInstanceState(a.info.Name, st)
		}
	case protocol.TypeConsole:
		var line protocol.ConsoleLine
		if json.Unmarshal(env.Payload, &line) == nil {
			if in, ok := s.store.get(line.ID); ok && in.Agent == a.info.Name {
				s.consoles.append(line.ID, line.Line)
				s.observeLine(in, line.Line)
			}
		}
	case protocol.TypeResources:
		var res protocol.Resources
		if json.Unmarshal(env.Payload, &res) == nil {
			if in, ok := s.store.get(res.ID); ok && in.Agent == a.info.Name {
				s.statsTracker.resources(res.ID, res.CPUPercent, res.MemoryBytes)
			}
		}
	default:
		slog.Warn("unknown message from agent", "type", env.Type, "agent", a.info.Name)
	}
}

// setInstanceState records a status reported by the agent that owns the instance.
func (s *Server) setInstanceState(agentName string, st protocol.Status) {
	in, ok := s.store.get(st.ID)
	if !ok || in.Agent != agentName {
		return
	}
	if _, err := s.store.update(st.ID, func(in *Instance) { in.State, in.Error = st.State, st.Error }); err != nil {
		slog.Error("saving instance state", "err", err)
	}
	if st.State == in.State {
		return // a replay after reconnecting
	}
	prev := in.State
	dropped := s.statsTracker.stateChanged(in.ID, st.State)
	in.State, in.Error = st.State, st.Error
	for _, name := range dropped {
		s.emit(s.event(in, EventPlayerLeave, name))
	}
	wasUp := prev == protocol.StateStarting || prev == protocol.StateRunning || prev == protocol.StateStopping
	switch {
	case st.State == protocol.StateError && wasUp:
		e := s.event(in, EventServerCrashed, "")
		e.Error = st.Error
		s.emit(e)
	case st.State == protocol.StateStopped && wasUp:
		s.emit(s.event(in, EventServerStopped, ""))
	}
}

// observeLine follows players and readiness from a console line.
func (s *Server) observeLine(in Instance, line string) {
	tpl, ok := s.template(in.TemplateID)
	if !ok {
		return
	}
	joined, left, ready := s.statsTracker.observe(in.ID, line, tpl.Stats)
	if ready {
		s.emit(s.event(in, EventServerReady, ""))
	}
	if joined != "" {
		s.emit(s.event(in, EventPlayerJoin, joined))
	}
	if left != "" {
		s.emit(s.event(in, EventPlayerLeave, left))
	}
}

func (s *Server) event(in Instance, eventType, player string) Event {
	st := s.stats(in)
	return Event{Type: eventType, Instance: EventInstance{ID: in.ID, Name: in.Name, Game: st.Game},
		Player: player, PlayerCount: st.PlayerCount, MaxPlayers: st.MaxPlayers}
}

func (s *Server) resetLiveStates(agentName string) {
	for _, in := range s.store.list() {
		if in.Agent != agentName {
			continue
		}
		switch in.State {
		case protocol.StateInstalling:
			s.store.update(in.ID, func(in *Instance) { in.State, in.Error = protocol.StateError, "install was interrupted; run it again" })
		case protocol.StateStarting, protocol.StateRunning, protocol.StateStopping:
			s.store.update(in.ID, func(in *Instance) { in.State, in.Error = protocol.StateStopped, "" })
		}
	}
}
