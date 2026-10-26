package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // so TZ works in slim containers without zoneinfo

	"github.com/sh4rpx88/Watchtower/internal/panel"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

func main() {
	cfg := panel.Config{
		AdminPassword: os.Getenv("PANEL_ADMIN_PASSWORD"),
		AgentToken:    os.Getenv("PANEL_AGENT_TOKEN"),
		WebDir:        getenv("PANEL_WEB_DIR", "web/dist"),
		DataDir:       getenv("PANEL_DATA_DIR", "data/panel"),
	}
	if cfg.AdminPassword == "" || cfg.AgentToken == "" {
		slog.Error("PANEL_ADMIN_PASSWORD and PANEL_AGENT_TOKEN must be set")
		os.Exit(1)
	}
	tpls, err := templates.LoadDir(getenv("PANEL_TEMPLATES_DIR", "templates"))
	if err != nil {
		slog.Error("loading templates", "err", err)
		os.Exit(1)
	}
	cfg.Templates = tpls
	slog.Info("templates loaded", "count", len(tpls))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := panel.New(cfg)
	if err != nil {
		slog.Error("starting panel", "err", err)
		os.Exit(1)
	}
	if err := srv.Run(ctx, getenv("PANEL_ADDR", ":8080")); err != nil {
		slog.Error("panel stopped", "err", err)
		os.Exit(1)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
