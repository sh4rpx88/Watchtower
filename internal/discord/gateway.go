package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Ready describes the bot once it has logged in.
type Ready struct {
	User        User `json:"user"`
	Application struct {
		ID string `json:"id"`
	} `json:"application"`
	Guilds []struct {
		ID string `json:"id"`
	} `json:"guilds"`
}

// Gateway keeps a connection to Discord's gateway and hands each event to
// its callbacks. Run reconnects until the context ends or the token is
// rejected.
type Gateway struct {
	Token         string
	URL           string // defaults to the live gateway
	OnReady       func(Ready)
	OnInteraction func(Interaction)
	OnStatus      func(connected bool, err error) // connection changes, for the settings page
}

// ErrBadToken means Discord refused the token; retrying won't help.
var ErrBadToken = errors.New("Discord rejected the bot token")

type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatAck   = 11
)

func (g *Gateway) status(connected bool, err error) {
	if g.OnStatus != nil {
		g.OnStatus(connected, err)
	}
}

func (g *Gateway) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		started := time.Now()
		err := g.session(ctx)
		if ctx.Err() != nil {
			g.status(false, nil)
			return ctx.Err()
		}
		g.status(false, err)
		if errors.Is(err, ErrBadToken) {
			return err
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("discord gateway disconnected", "err", err, "retry", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}

// session runs one connection: hello, identify, then events and heartbeats.
func (g *Gateway) session(ctx context.Context) error {
	url := g.URL
	if url == "" {
		url = "wss://gateway.discord.gg/?v=10&encoding=json"
	}
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	cancel()
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32 << 20) // READY can be large in big servers

	var hello gatewayPayload
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		return err
	}
	if hello.Op != opHello {
		return fmt.Errorf("expected hello, got op %d", hello.Op)
	}
	var h struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	json.Unmarshal(hello.D, &h)
	if h.HeartbeatInterval <= 0 {
		return errors.New("gateway sent no heartbeat interval")
	}

	identify := map[string]any{
		"token":   g.Token,
		"intents": 1, // GUILDS; interactions arrive without any intent
		"properties": map[string]string{
			"os": "linux", "browser": "watchtower", "device": "watchtower",
		},
	}
	if err := send(ctx, conn, opIdentify, identify); err != nil {
		return err
	}

	sessCtx, stop := context.WithCancel(ctx)
	defer stop()
	var seq int64
	seqCh := make(chan int64, 16)
	acks := make(chan struct{}, 1)
	beatErr := make(chan error, 1)
	go func() {
		interval := time.Duration(h.HeartbeatInterval) * time.Millisecond
		// The first beat waits a random part of the interval, as Discord asks.
		timer := time.NewTimer(time.Duration(rand.Float64() * float64(interval)))
		defer timer.Stop()
		var last int64
		awaiting := false
		for {
			select {
			case <-sessCtx.Done():
				return
			case s := <-seqCh:
				last = s
			case <-acks:
				awaiting = false
			case <-timer.C:
				if awaiting {
					beatErr <- errors.New("no heartbeat acknowledgement")
					conn.Close(websocket.StatusCode(4000), "zombied connection")
					return
				}
				var d any
				if last > 0 {
					d = last
				}
				if err := send(sessCtx, conn, opHeartbeat, d); err != nil {
					beatErr <- err
					return
				}
				awaiting = true
				timer.Reset(interval)
			}
		}
	}()

	for {
		var p gatewayPayload
		if err := wsjson.Read(ctx, conn, &p); err != nil {
			select {
			case berr := <-beatErr:
				return berr
			default:
			}
			switch websocket.CloseStatus(err) {
			case 4004:
				return ErrBadToken
			case 4014:
				return errors.New("the bot asked for intents it isn't allowed")
			}
			return err
		}
		if p.S != nil {
			seq = *p.S
			select {
			case seqCh <- seq:
			default:
			}
		}
		switch p.Op {
		case opHeartbeatAck:
			select {
			case acks <- struct{}{}:
			default:
			}
		case opHeartbeat:
			send(ctx, conn, opHeartbeat, seq)
		case opReconnect:
			return errors.New("Discord asked to reconnect")
		case opInvalidSession:
			time.Sleep(time.Duration(1+rand.IntN(4)) * time.Second)
			return errors.New("invalid session")
		case opDispatch:
			switch p.T {
			case "READY":
				var r Ready
				if err := json.Unmarshal(p.D, &r); err == nil {
					g.status(true, nil)
					if g.OnReady != nil {
						g.OnReady(r)
					}
				}
			case "INTERACTION_CREATE":
				var in Interaction
				if err := json.Unmarshal(p.D, &in); err == nil && g.OnInteraction != nil {
					go g.OnInteraction(in)
				}
			}
		}
	}
}

func send(ctx context.Context, conn *websocket.Conn, op int, d any) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, conn, gatewayPayload{Op: op, D: raw})
}
