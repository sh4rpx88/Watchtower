// Package agent runs on each machine that hosts game servers. It connects to
// the panel and carries out its requests.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"serverpanel/internal/protocol"
)

const Version = "0.1.0-dev"

type Config struct {
	PanelURL string // e.g. ws://localhost:8080/agent/connect
	Token    string
	Name     string
}

// Run connects to the panel and reconnects with backoff until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	started := time.Now()
	backoff := time.Second
	for {
		err := session(ctx, cfg, started)
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("disconnected from panel, retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func session(ctx context.Context, cfg Config, started time.Time) error {
	conn, _, err := websocket.Dial(ctx, cfg.PanelURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Token}},
	})
	if err != nil {
		return err
	}
	defer conn.CloseNow()

	host, _ := os.Hostname()
	hello, _ := protocol.New(protocol.TypeHello, protocol.Hello{
		AgentName: cfg.Name, Version: Version, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH,
	})
	if err := wsjson.Write(ctx, conn, hello); err != nil {
		return err
	}
	var env protocol.Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil {
		return err
	}
	if env.Type != protocol.TypeWelcome {
		return fmt.Errorf("expected welcome, got %q", env.Type)
	}
	slog.Info("connected to panel", "url", cfg.PanelURL)

	// Reads keep the connection's control frames flowing and surface disconnects.
	readErr := make(chan error, 1)
	go func() {
		for {
			var in protocol.Envelope
			if err := wsjson.Read(ctx, conn, &in); err != nil {
				readErr <- err
				return
			}
			slog.Debug("message from panel", "type", in.Type)
		}
	}()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		hb, _ := protocol.New(protocol.TypeHeartbeat, protocol.Heartbeat{
			UptimeSeconds: int64(time.Since(started).Seconds()), NumCPU: runtime.NumCPU(),
		})
		if err := wsjson.Write(ctx, conn, hb); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "agent shutting down")
			return nil
		case err := <-readErr:
			return err
		case <-ticker.C:
		}
	}
}
