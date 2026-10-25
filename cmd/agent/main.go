package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"serverpanel/internal/agent"
)

func main() {
	host, _ := os.Hostname()
	cfg := agent.Config{
		PanelURL: getenv("AGENT_PANEL_URL", "ws://localhost:8080/agent/connect"),
		Token:    os.Getenv("AGENT_TOKEN"),
		Name:     getenv("AGENT_NAME", host),
	}
	if cfg.Token == "" {
		slog.Error("AGENT_TOKEN must be set")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx, cfg); err != nil {
		slog.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
