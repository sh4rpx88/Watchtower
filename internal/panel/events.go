package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Event types sent to webhooks.
const (
	EventPlayerJoin    = "player.join"
	EventPlayerLeave   = "player.leave"
	EventServerReady   = "server.ready"
	EventServerStopped = "server.stopped"
	EventServerCrashed = "server.crashed"
	EventBackupDone    = "backup.done"
	EventBackupFailed  = "backup.failed"
	// EventAction is something a person or API token did, for the activity
	// log. Webhooks don't receive these.
	EventAction = "action"
)

var eventTypes = []string{EventPlayerJoin, EventPlayerLeave, EventServerReady, EventServerStopped, EventServerCrashed,
	EventBackupDone, EventBackupFailed}

type EventInstance struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Game string `json:"game"`
}

type Event struct {
	Type        string        `json:"type"`
	Time        time.Time     `json:"time"`
	Instance    EventInstance `json:"instance,omitzero"`
	Player      string        `json:"player,omitempty"`
	PlayerCount int           `json:"playerCount"`
	MaxPlayers  int           `json:"maxPlayers,omitempty"`
	Error       string        `json:"error,omitempty"`
	Actor       string        `json:"actor,omitempty"`   // for actions: the username, or "token <name>"
	Message     string        `json:"message,omitempty"` // for actions: what they did, e.g. "started the server"
	Backup      string        `json:"backup,omitempty"`  // for backup events: the archive's name
	Size        int64         `json:"size,omitempty"`    // for backup.done: its size in bytes
}

// Text is a one-line summary, used for Discord messages.
func (e Event) Text() string {
	online := fmt.Sprintf("%d online", e.PlayerCount)
	if e.MaxPlayers > 0 {
		online = fmt.Sprintf("%d/%d online", e.PlayerCount, e.MaxPlayers)
	}
	name := "**" + e.Instance.Name + "**"
	switch e.Type {
	case EventPlayerJoin:
		return fmt.Sprintf("%s: %s joined (%s)", name, e.Player, online)
	case EventPlayerLeave:
		return fmt.Sprintf("%s: %s left (%s)", name, e.Player, online)
	case EventServerReady:
		return fmt.Sprintf("%s is up and ready for players", name)
	case EventServerStopped:
		return fmt.Sprintf("%s stopped", name)
	case EventServerCrashed:
		return fmt.Sprintf("%s crashed: %s", name, e.Error)
	case EventBackupDone:
		return fmt.Sprintf("%s: backup saved (%.1f MB)", name, float64(e.Size)/(1<<20))
	case EventBackupFailed:
		return fmt.Sprintf("%s: backup failed: %s", name, e.Error)
	case EventAction:
		if e.Instance.Name == "" {
			return e.Actor + " " + e.Message
		}
		return fmt.Sprintf("%s: %s %s", name, e.Actor, e.Message)
	}
	return name + ": " + e.Type
}

// Webhook posts events to a URL. Format "discord" sends a Discord message
// (paste a Discord channel webhook URL); "json" sends the Event as JSON.
type Webhook struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Format    string    `json:"format"`
	Events    []string  `json:"events"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) emit(e Event) {
	e = s.activity.add(e)
	s.pokeBoard()
	select {
	case s.events <- e:
	default:
		slog.Warn("event queue full, dropping event", "type", e.Type)
	}
}

// deliverEvents posts each event to the webhooks that want it.
func (s *Server) deliverEvents(ctx context.Context) {
	client := &http.Client{Timeout: 10 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.events:
			for _, h := range s.webhooks.all() {
				if slices.Contains(h.Events, e.Type) {
					if err := postEvent(ctx, client, h, e); err != nil {
						slog.Warn("webhook failed", "webhook", h.Name, "err", err)
					}
				}
			}
		}
	}
}

func postEvent(ctx context.Context, client *http.Client, h Webhook, e Event) error {
	var payload any = e
	if h.Format == "discord" {
		payload = map[string]any{"content": e.Text(), "allowed_mentions": map[string]any{"parse": []string{}}}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", h.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Watchtower/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s answered %s", h.URL, resp.Status)
	}
	return nil
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.webhooks.all())
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var h Webhook
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	h.Name = strings.TrimSpace(h.Name)
	u, err := url.Parse(h.URL)
	switch {
	case h.Name == "":
		writeError(w, http.StatusBadRequest, "name is required")
		return
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		writeError(w, http.StatusBadRequest, "URL must start with https:// or http://")
		return
	case h.Format != "discord" && h.Format != "json":
		writeError(w, http.StatusBadRequest, "format must be discord or json")
		return
	}
	if len(h.Events) == 0 {
		h.Events = eventTypes
	}
	for _, e := range h.Events {
		if !slices.Contains(eventTypes, e) {
			writeError(w, http.StatusBadRequest, "unknown event "+e)
			return
		}
	}
	h.ID, h.CreatedAt = randomToken()[:16], time.Now().UTC()
	if err := s.webhooks.change(func(hs []Webhook) []Webhook { return append(hs, h) }); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	noteFor(r).text = h.Name
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("webhookId")
	err := s.webhooks.change(func(hs []Webhook) []Webhook {
		return slices.DeleteFunc(hs, func(h Webhook) bool {
			if h.ID == id {
				noteFor(r).text = h.Name
			}
			return h.ID == id
		})
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestWebhook sends a sample event right away and reports the result.
func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("webhookId")
	for _, h := range s.webhooks.all() {
		if h.ID != id {
			continue
		}
		e := Event{Type: EventServerReady, Time: time.Now().UTC(), Instance: EventInstance{Name: "Watchtower test"}}
		client := &http.Client{Timeout: 10 * time.Second}
		if err := postEvent(r.Context(), client, h, e); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	writeError(w, http.StatusNotFound, "no such webhook")
}
