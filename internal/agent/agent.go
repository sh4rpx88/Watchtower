// Package agent runs on each machine that hosts game servers. It connects to
// the panel and carries out its requests.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

const Version = "0.2.0-dev"

type Config struct {
	PanelURL string // e.g. ws://localhost:8080/agent/connect
	Token    string
	Name     string
	DataDir  string  // where instance files live; must be the same path on the Docker host
	Runtime  Runtime // defaults to DockerRuntime
}

// Run connects to the panel and reconnects with backoff until ctx is cancelled.
// Game servers keep running while the agent is disconnected.
func Run(ctx context.Context, cfg Config) error {
	rt := cfg.Runtime
	if rt == nil {
		rt = DockerRuntime{}
	}
	mgr := NewManager(cfg.DataDir, rt)
	if err := mgr.Recover(); err != nil {
		slog.Warn("could not check for running servers", "err", err)
	}
	started := time.Now()
	backoff := time.Second
	for {
		connected, err := session(ctx, cfg, mgr, started)
		if ctx.Err() != nil {
			return nil
		}
		if connected {
			backoff = time.Second
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

func session(ctx context.Context, cfg Config, mgr *Manager, started time.Time) (connected bool, err error) {
	conn, _, err := websocket.Dial(ctx, cfg.PanelURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Token}},
	})
	if err != nil {
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	host, _ := os.Hostname()
	hello, _ := protocol.New(protocol.TypeHello, protocol.Hello{
		AgentName: cfg.Name, Version: Version, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH,
	})
	if err := wsjson.Write(ctx, conn, hello); err != nil {
		return false, err
	}
	var env protocol.Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil {
		return false, err
	}
	if env.Type != protocol.TypeWelcome {
		return false, fmt.Errorf("expected welcome, got %q", env.Type)
	}
	slog.Info("connected to panel", "url", cfg.PanelURL)

	var writeMu sync.Mutex
	send := func(e protocol.Envelope) {
		writeMu.Lock()
		defer writeMu.Unlock()
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := wsjson.Write(wctx, conn, e); err != nil {
			slog.Debug("write to panel failed", "err", err)
		}
	}
	mgr.SetSend(send)
	defer mgr.SetSend(nil)

	readErr := make(chan error, 1)
	go func() {
		for {
			var in protocol.Envelope
			if err := wsjson.Read(ctx, conn, &in); err != nil {
				readErr <- err
				return
			}
			go handle(mgr, in, send)
		}
	}()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		hb, _ := protocol.New(protocol.TypeHeartbeat, hostStats(int64(time.Since(started).Seconds()), cfg.DataDir))
		send(hb)
		go mgr.reportResources()
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "agent shutting down")
			return true, nil
		case err := <-readErr:
			return true, err
		case <-ticker.C:
		}
	}
}

// handle carries out one request from the panel and sends the response.
func handle(mgr *Manager, in protocol.Envelope, send func(protocol.Envelope)) {
	data, err := dispatch(mgr, in)
	resp := protocol.Response{OK: err == nil}
	if err != nil {
		resp.Error = err.Error()
	} else if data != nil {
		resp.Data, err = json.Marshal(data)
		if err != nil {
			resp = protocol.Response{Error: err.Error()}
		}
	}
	out, _ := protocol.New(protocol.TypeResponse, resp)
	out.ID = in.ID
	send(out)
}

// decode unmarshals a request payload into a new T.
func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}

func dispatch(mgr *Manager, in protocol.Envelope) (any, error) {
	switch in.Type {
	case protocol.TypeInstall, protocol.TypeStart:
		spec, err := decode[protocol.InstanceSpec](in.Payload)
		if err != nil {
			return nil, err
		}
		if in.Type == protocol.TypeInstall {
			return nil, mgr.Install(spec)
		}
		return nil, mgr.Start(spec)
	case protocol.TypeBackupCreate:
		req, err := decode[protocol.BackupCreate](in.Payload)
		if err != nil {
			return nil, err
		}
		return nil, mgr.CreateBackup(req.InstanceSpec, req.Keep)
	case protocol.TypeStop, protocol.TypeDelete, protocol.TypeBackupList:
		ref, err := decode[protocol.InstanceRef](in.Payload)
		if err != nil {
			return nil, err
		}
		switch in.Type {
		case protocol.TypeStop:
			return nil, mgr.Stop(ref.ID)
		case protocol.TypeDelete:
			return nil, mgr.Delete(ref.ID)
		default:
			return mgr.ListBackups(ref.ID)
		}
	case protocol.TypeCommand:
		c, err := decode[protocol.Command](in.Payload)
		if err != nil {
			return nil, err
		}
		return nil, mgr.Command(c.ID, c.Line)
	case protocol.TypeFileList, protocol.TypeFileRead, protocol.TypeFileDelete, protocol.TypeFileMkdir:
		req, err := decode[protocol.FileRequest](in.Payload)
		if err != nil {
			return nil, err
		}
		switch in.Type {
		case protocol.TypeFileList:
			return mgr.ListFiles(req)
		case protocol.TypeFileRead:
			return mgr.ReadFile(req)
		case protocol.TypeFileDelete:
			return nil, mgr.DeleteFile(req)
		default:
			return nil, mgr.Mkdir(req)
		}
	case protocol.TypeFileWrite:
		req, err := decode[protocol.FileContent](in.Payload)
		if err != nil {
			return nil, err
		}
		return nil, mgr.WriteFile(req)
	case protocol.TypeBackupRestore, protocol.TypeBackupDelete:
		req, err := decode[protocol.BackupRequest](in.Payload)
		if err != nil {
			return nil, err
		}
		if in.Type == protocol.TypeBackupRestore {
			return nil, mgr.RestoreBackup(req)
		}
		return nil, mgr.DeleteBackup(req)
	default:
		return nil, fmt.Errorf("unknown request %q", in.Type)
	}
}
