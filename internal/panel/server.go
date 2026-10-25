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
	cfg          Config
	store        *store
	consoles     *consoles
	statsTracker *statsTracker
	tokens       *jsonList[APIToken]
	webhooks     *jsonList[Webhook]
	events       chan Event
	activity     *activity

	mu       sync.Mutex
	agents   map[string]*agentConn
	sessions map[string]time.Time // session token -> expiry
}

func New(cfg Config) (*Server, error) {
	st, err := openStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	tokens, err := openJSONList[APIToken](cfg.DataDir, "tokens.json")
	if err != nil {
		return nil, err
	}
	webhooks, err := openJSONList[Webhook](cfg.DataDir, "webhooks.json")
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: st, consoles: newConsoles(), statsTracker: newStatsTracker(),
		tokens: tokens, webhooks: webhooks, events: make(chan Event, 256), activity: &activity{},
		agents: map[string]*agentConn{}, sessions: map[string]time.Time{}}
	go s.deliverEvents(context.Background())
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	read, control, admin := func(h http.HandlerFunc) http.HandlerFunc { return s.auth(levelRead, h) },
		func(h http.HandlerFunc) http.HandlerFunc { return s.auth(levelControl, h) },
		func(h http.HandlerFunc) http.HandlerFunc { return s.auth(levelAdmin, h) }

	mux.HandleFunc("GET /api/v1/agents", read(s.handleAgents))
	mux.HandleFunc("GET /api/v1/templates", read(s.handleTemplates))
	mux.HandleFunc("GET /api/v1/stats", read(s.handleAllStats))
	mux.HandleFunc("GET /api/v1/activity", read(s.handleActivity))
	mux.HandleFunc("GET /api/v1/instances", read(s.handleListInstances))
	mux.HandleFunc("GET /api/v1/instances/{id}", read(s.handleGetInstance))
	mux.HandleFunc("GET /api/v1/instances/{id}/stats", read(s.handleStats))

	mux.HandleFunc("POST /api/v1/instances/{id}/{action}", control(s.handleAction))
	mux.HandleFunc("GET /api/v1/instances/{id}/console", control(s.handleConsole))

	mux.HandleFunc("POST /api/v1/instances", admin(s.handleCreateInstance))
	mux.HandleFunc("DELETE /api/v1/instances/{id}", admin(s.handleDeleteInstance))
	mux.HandleFunc("PUT /api/v1/instances/{id}/settings", admin(s.handleUpdateSettings))
	mux.HandleFunc("GET /api/v1/instances/{id}/files", admin(s.handleListFiles))
	mux.HandleFunc("DELETE /api/v1/instances/{id}/files", admin(s.handleDeleteFile))
	mux.HandleFunc("POST /api/v1/instances/{id}/files/mkdir", admin(s.handleMkdir))
	mux.HandleFunc("GET /api/v1/instances/{id}/files/content", admin(s.handleReadFile))
	mux.HandleFunc("PUT /api/v1/instances/{id}/files/content", admin(s.handleWriteFile))
	mux.HandleFunc("GET /api/v1/instances/{id}/backups", admin(s.handleListBackups))
	mux.HandleFunc("POST /api/v1/instances/{id}/backups", admin(s.handleCreateBackup))
	mux.HandleFunc("POST /api/v1/instances/{id}/backups/{name}/restore", admin(s.handleRestoreBackup))
	mux.HandleFunc("DELETE /api/v1/instances/{id}/backups/{name}", admin(s.handleDeleteBackup))
	mux.HandleFunc("GET /api/v1/tokens", admin(s.handleListTokens))
	mux.HandleFunc("POST /api/v1/tokens", admin(s.handleCreateToken))
	mux.HandleFunc("DELETE /api/v1/tokens/{tokenId}", admin(s.handleDeleteToken))
	mux.HandleFunc("GET /api/v1/webhooks", admin(s.handleListWebhooks))
	mux.HandleFunc("POST /api/v1/webhooks", admin(s.handleCreateWebhook))
	mux.HandleFunc("DELETE /api/v1/webhooks/{webhookId}", admin(s.handleDeleteWebhook))
	mux.HandleFunc("POST /api/v1/webhooks/{webhookId}/test", admin(s.handleTestWebhook))
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
