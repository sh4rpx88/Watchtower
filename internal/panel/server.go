// Package panel is the central server: it serves the API and web UI and keeps
// track of connected agents.
package panel

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

const Version = "0.1.0-dev"

type Config struct {
	AdminPassword string // login password for the admin user
	AgentToken    string // shared secret agents present when connecting
	WebDir        string // built web UI, served at /
	Templates     []templates.Template
}

type Agent struct {
	Name          string    `json:"name"`
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	Version       string    `json:"version"`
	ConnectedAt   time.Time `json:"connectedAt"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	NumCPU        int       `json:"numCpu"`
}

type Server struct {
	cfg      Config
	mu       sync.Mutex
	agents   map[string]*Agent
	sessions map[string]time.Time // session token -> expiry
}

func New(cfg Config) *Server {
	return &Server{cfg: cfg, agents: map[string]*Agent{}, sessions: map[string]time.Time{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("GET /api/v1/agents", s.requireAuth(s.handleAgents))
	mux.HandleFunc("GET /api/v1/templates", s.requireAuth(s.handleTemplates))
	mux.HandleFunc("GET /agent/connect", s.handleAgentConnect)
	if s.cfg.WebDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.cfg.WebDir)))
	}
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.cfg.AdminPassword)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
		return
	}
	token := randomToken()
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		expiry, ok := s.sessions[token]
		s.mu.Unlock()
		if !ok || time.Now().After(expiry) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not logged in"})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	list := make([]Agent, 0, len(s.agents))
	for _, a := range s.agents {
		list = append(list, *a)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Templates)
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
	ctx := r.Context()

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
	welcome, _ := protocol.New(protocol.TypeWelcome, protocol.Welcome{PanelVersion: Version})
	if err := wsjson.Write(ctx, conn, welcome); err != nil {
		return
	}

	now := time.Now()
	agent := &Agent{Name: hello.AgentName, Hostname: hello.Hostname, OS: hello.OS, Arch: hello.Arch,
		Version: hello.Version, ConnectedAt: now, LastHeartbeat: now}
	s.mu.Lock()
	s.agents[agent.Name] = agent
	s.mu.Unlock()
	slog.Info("agent connected", "name", agent.Name, "host", agent.Hostname)
	defer func() {
		s.mu.Lock()
		if s.agents[agent.Name] == agent {
			delete(s.agents, agent.Name)
		}
		s.mu.Unlock()
		slog.Info("agent disconnected", "name", agent.Name)
	}()

	for {
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			return
		}
		switch env.Type {
		case protocol.TypeHeartbeat:
			var hb protocol.Heartbeat
			if json.Unmarshal(env.Payload, &hb) == nil {
				s.mu.Lock()
				agent.LastHeartbeat = time.Now()
				agent.NumCPU = hb.NumCPU
				s.mu.Unlock()
			}
		default:
			slog.Warn("unknown message from agent", "type", env.Type, "agent", agent.Name)
		}
	}
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	slog.Info("panel listening", "addr", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
