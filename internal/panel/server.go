// Package panel is the central server: it serves the API and web UI, keeps
// track of connected agents, and tells them what to run.
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

	"github.com/sh4rpx88/Watchtower/internal/templates"
)

const Version = "0.2.0-dev"

type Config struct {
	AdminPassword string // login password for the admin user
	AgentToken    string // shared secret agents present when connecting
	WebDir        string // built web UI, served at /
	DataDir       string // where the panel keeps its state; empty keeps it in memory
	Templates     []templates.Template
}

type Server struct {
	cfg      Config
	store    *store
	consoles *consoles

	mu       sync.Mutex
	agents   map[string]*agentConn
	sessions map[string]time.Time // session token -> expiry
}

func New(cfg Config) (*Server, error) {
	st, err := openStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, store: st, consoles: newConsoles(),
		agents: map[string]*agentConn{}, sessions: map[string]time.Time{}}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("GET /api/v1/agents", s.requireAuth(s.handleAgents))
	mux.HandleFunc("GET /api/v1/templates", s.requireAuth(s.handleTemplates))
	mux.HandleFunc("GET /api/v1/instances", s.requireAuth(s.handleListInstances))
	mux.HandleFunc("POST /api/v1/instances", s.requireAuth(s.handleCreateInstance))
	mux.HandleFunc("GET /api/v1/instances/{id}", s.requireAuth(s.handleGetInstance))
	mux.HandleFunc("DELETE /api/v1/instances/{id}", s.requireAuth(s.handleDeleteInstance))
	mux.HandleFunc("PUT /api/v1/instances/{id}/settings", s.requireAuth(s.handleUpdateSettings))
	mux.HandleFunc("POST /api/v1/instances/{id}/{action}", s.requireAuth(s.handleAction))
	mux.HandleFunc("GET /api/v1/instances/{id}/console", s.requireAuth(s.handleConsole))
	mux.HandleFunc("GET /api/v1/instances/{id}/files", s.requireAuth(s.handleListFiles))
	mux.HandleFunc("DELETE /api/v1/instances/{id}/files", s.requireAuth(s.handleDeleteFile))
	mux.HandleFunc("POST /api/v1/instances/{id}/files/mkdir", s.requireAuth(s.handleMkdir))
	mux.HandleFunc("GET /api/v1/instances/{id}/files/content", s.requireAuth(s.handleReadFile))
	mux.HandleFunc("PUT /api/v1/instances/{id}/files/content", s.requireAuth(s.handleWriteFile))
	mux.HandleFunc("GET /api/v1/instances/{id}/backups", s.requireAuth(s.handleListBackups))
	mux.HandleFunc("POST /api/v1/instances/{id}/backups", s.requireAuth(s.handleCreateBackup))
	mux.HandleFunc("POST /api/v1/instances/{id}/backups/{name}/restore", s.requireAuth(s.handleRestoreBackup))
	mux.HandleFunc("DELETE /api/v1/instances/{id}/backups/{name}", s.requireAuth(s.handleDeleteBackup))
	mux.HandleFunc("GET /agent/connect", s.handleAgentConnect)
	if s.cfg.WebDir != "" {
		mux.Handle("/", noCacheHTML(http.FileServer(http.Dir(s.cfg.WebDir))))
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
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.cfg.AdminPassword)) != 1 {
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	token := randomToken()
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// requireAuth checks the session token. Browsers can't set headers on a
// WebSocket, so the token may also come as ?access_token=.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("access_token")
		}
		s.mu.Lock()
		expiry, ok := s.sessions[token]
		s.mu.Unlock()
		if !ok || time.Now().After(expiry) {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	list := make([]Agent, 0, len(s.agents))
	for _, a := range s.agents {
		list = append(list, a.info)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Templates)
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

// noCacheHTML makes browsers revalidate the page itself, so they pick up a new
// build right away. Files under /assets/ have hashed names and cache as usual.
func noCacheHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
