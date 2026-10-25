// Package panel is the central server: it serves the API and web UI, keeps
// track of connected agents, and tells them what to run.
package panel

import (
	"context"
	"crypto/rand"
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
	AdminPassword string // password for the first admin user, created when there are no users
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
	users        *jsonList[User]
	events       chan Event
	activity     *activity

	mu       sync.Mutex
	agents   map[string]*agentConn
	sessions map[string]session // by session token
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
	users, err := openJSONList[User](cfg.DataDir, "users.json")
	if err != nil {
		return nil, err
	}
	act, err := openActivity(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: st, consoles: newConsoles(), statsTracker: newStatsTracker(),
		tokens: tokens, webhooks: webhooks, events: make(chan Event, 256), activity: act, users: users,
		agents: map[string]*agentConn{}, sessions: map[string]session{}}
	if err := s.ensureAdmin(); err != nil {
		return nil, err
	}
	go s.deliverEvents(context.Background())
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", s.handleLogout)
	read, control, manage, admin := func(h http.HandlerFunc) http.HandlerFunc { return s.auth(levelRead, h) },
		func(h http.HandlerFunc) http.HandlerFunc { return s.auth(levelControl, h) },
		func(h http.HandlerFunc) http.HandlerFunc { return s.auth(levelManage, h) },
		func(h http.HandlerFunc) http.HandlerFunc { return s.auth(levelAdmin, h) }
	// logged records a change in the activity log once it succeeds.
	logged := func(what string, h http.HandlerFunc) http.HandlerFunc { return s.logged(what, h) }

	mux.HandleFunc("GET /api/v1/me", read(s.handleMe))
	mux.HandleFunc("PUT /api/v1/me/password", read(s.handleChangeOwnPassword))
	mux.HandleFunc("GET /api/v1/agents", read(s.handleAgents))
	mux.HandleFunc("GET /api/v1/templates", read(s.handleTemplates))
	mux.HandleFunc("GET /api/v1/stats", read(s.handleAllStats))
	mux.HandleFunc("GET /api/v1/activity", read(s.handleActivity))
	mux.HandleFunc("GET /api/v1/instances", read(s.handleListInstances))
	mux.HandleFunc("GET /api/v1/instances/{id}", read(s.handleGetInstance))
	mux.HandleFunc("GET /api/v1/instances/{id}/stats", read(s.handleStats))

	mux.HandleFunc("POST /api/v1/instances/{id}/{action}", control(logged("{action}", s.handleAction)))
	mux.HandleFunc("GET /api/v1/instances/{id}/console", control(s.handleConsole))

	mux.HandleFunc("PUT /api/v1/instances/{id}/settings", manage(logged("changed the settings", s.handleUpdateSettings)))
	mux.HandleFunc("GET /api/v1/instances/{id}/files", manage(s.handleListFiles))
	mux.HandleFunc("DELETE /api/v1/instances/{id}/files", manage(logged("deleted {path}", s.handleDeleteFile)))
	mux.HandleFunc("POST /api/v1/instances/{id}/files/mkdir", manage(logged("made the folder {path}", s.handleMkdir)))
	mux.HandleFunc("GET /api/v1/instances/{id}/files/content", manage(s.handleReadFile))
	mux.HandleFunc("PUT /api/v1/instances/{id}/files/content", manage(logged("saved {path}", s.handleWriteFile)))
	mux.HandleFunc("GET /api/v1/instances/{id}/backups", manage(s.handleListBackups))
	mux.HandleFunc("POST /api/v1/instances/{id}/backups", manage(logged("made a backup", s.handleCreateBackup)))
	mux.HandleFunc("POST /api/v1/instances/{id}/backups/{name}/restore", manage(logged("restored the backup {name}", s.handleRestoreBackup)))
	mux.HandleFunc("DELETE /api/v1/instances/{id}/backups/{name}", manage(logged("deleted the backup {name}", s.handleDeleteBackup)))

	mux.HandleFunc("POST /api/v1/instances", admin(logged("created the server", s.handleCreateInstance)))
	mux.HandleFunc("DELETE /api/v1/instances/{id}", admin(logged("deleted the server", s.handleDeleteInstance)))
	mux.HandleFunc("GET /api/v1/users", admin(s.handleListUsers))
	mux.HandleFunc("POST /api/v1/users", admin(s.handleCreateUser))
	mux.HandleFunc("PATCH /api/v1/users/{userId}", admin(s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/v1/users/{userId}", admin(s.handleDeleteUser))
	mux.HandleFunc("GET /api/v1/tokens", admin(s.handleListTokens))
	mux.HandleFunc("POST /api/v1/tokens", admin(logged("created the API token {note}", s.handleCreateToken)))
	mux.HandleFunc("DELETE /api/v1/tokens/{tokenId}", admin(logged("revoked the API token {note}", s.handleDeleteToken)))
	mux.HandleFunc("GET /api/v1/webhooks", admin(s.handleListWebhooks))
	mux.HandleFunc("POST /api/v1/webhooks", admin(logged("added the webhook {note}", s.handleCreateWebhook)))
	mux.HandleFunc("DELETE /api/v1/webhooks/{webhookId}", admin(logged("removed the webhook {note}", s.handleDeleteWebhook)))
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
