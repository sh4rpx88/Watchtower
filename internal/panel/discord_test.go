package panel

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/sh4rpx88/Watchtower/internal/discord"
	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

// fakeDiscord plays Discord's gateway and REST API.
type fakeDiscord struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []fakeCall
	identify chan map[string]any
	send     chan any // gateway dispatches to send
}

type fakeCall struct {
	Method, Path string
	Body         map[string]any
	Files        []string
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{t: t, identify: make(chan map[string]any, 4), send: make(chan any, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDiscord) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/gateway" {
		f.gateway(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bot good-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	call := fakeCall{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/api")}
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "multipart/form-data" {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(part)
			if part.FormName() == "payload_json" {
				json.Unmarshal(data, &call.Body)
			} else {
				call.Files = append(call.Files, part.FileName())
			}
		}
	} else {
		json.NewDecoder(r.Body).Decode(&call.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if r.Method == "POST" && strings.HasSuffix(call.Path, "/messages") {
		json.NewEncoder(w).Encode(map[string]string{"id": "900000000000000001"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeDiscord) gateway(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 45000}})
	var id struct {
		Op int            `json:"op"`
		D  map[string]any `json:"d"`
	}
	if err := wsjson.Read(ctx, conn, &id); err != nil {
		return
	}
	f.identify <- id.D
	if id.D["token"] != "good-token" {
		conn.Close(4004, "Authentication failed")
		return
	}
	go func() {
		for {
			var p map[string]any
			if wsjson.Read(ctx, conn, &p) != nil {
				return
			}
		}
	}()
	seq := 1
	ready := map[string]any{"user": map[string]string{"id": "1", "username": "Watchtower"}, "application": map[string]string{"id": "700000000000000001"},
		"guilds": []map[string]string{{"id": "800000000000000001"}}}
	wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": seq, "d": ready})
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-f.send:
			seq++
			wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "INTERACTION_CREATE", "s": seq, "d": d})
		}
	}
}

// waitCall waits for a REST call matching method and path prefix.
func (f *fakeDiscord) waitCall(method, path string, after int) (fakeCall, int) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for i := after; i < len(f.calls); i++ {
			if c := f.calls[i]; c.Method == method && strings.HasPrefix(c.Path, path) {
				f.mu.Unlock()
				return c, i + 1
			}
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("no %s %s call; got %+v", method, path, f.calls)
	return fakeCall{}, 0
}

func interaction(typ int, data map[string]any, perms string, roles ...string) map[string]any {
	return map[string]any{
		"id": "600000000000000001", "application_id": "700000000000000001", "type": typ, "token": "itoken",
		"guild_id": "800000000000000001", "channel_id": "500000000000000001", "data": data,
		"member": map[string]any{"user": map[string]string{"id": "2", "username": "james"}, "roles": roles, "permissions": perms},
	}
}

func TestDiscordBot(t *testing.T) {
	fake := newFakeDiscord(t)
	c, done := setupWith(t, func(cfg *Config) {
		cfg.DiscordAPI = fake.srv.URL + "/api"
		cfg.DiscordGateway = "ws" + strings.TrimPrefix(fake.srv.URL, "http") + "/gateway"
	})
	defer done()
	var in instanceView
	c.do("POST", "/api/v1/instances", map[string]any{
		"name": "Survival", "templateId": "minecraft-java", "agent": "local", "settings": map[string]string{"eula": "true"},
	}, http.StatusCreated, &in)
	c.do("POST", "/api/v1/instances/"+in.ID+"/install", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateStopped)

	c.do("PUT", "/api/v1/discord", map[string]any{"enabled": true, "channelId": "123"}, http.StatusBadRequest, nil)
	c.do("PUT", "/api/v1/discord", map[string]any{"enabled": true}, http.StatusBadRequest, nil) // no token yet
	var view struct {
		Settings DiscordSettings
		HasToken bool
	}
	c.do("PUT", "/api/v1/discord", map[string]any{
		"enabled": true, "token": "Bot good-token", "channelId": "500000000000000001", "controlRoleId": "400000000000000001",
	}, http.StatusOK, &view)
	if !view.HasToken || view.Settings.Token != "" {
		t.Fatalf("the token should be saved but not shown: %+v", view)
	}
	if id := <-fake.identify; id["token"] != "good-token" {
		t.Fatalf("identify = %v", id)
	}

	// Slash commands are registered in the bot's server and the board is posted.
	fake.waitCall("PUT", "/applications/700000000000000001/guilds/800000000000000001/commands", 0)
	board, _ := fake.waitCall("POST", "/channels/500000000000000001/messages", 0)
	raw, _ := json.Marshal(board.Body)
	if !strings.Contains(string(raw), "Survival") || !strings.Contains(string(raw), "wt:pick") {
		t.Errorf("board = %s", raw)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got struct{ Settings DiscordSettings }
		c.do("GET", "/api/v1/discord", nil, http.StatusOK, &got)
		if got.Settings.BoardMessageID == "900000000000000001" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("board message ID never saved")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Someone without the role can look but not start.
	_, n := fake.waitCall("POST", "/channels/", 0)
	fake.send <- interaction(discord.InteractionComponent, map[string]any{"custom_id": "wt:act:start:" + in.ID}, "0")
	call, n := fake.waitCall("POST", "/interactions/600000000000000001/itoken/callback", n)
	if raw, _ := json.Marshal(call.Body); !strings.Contains(string(raw), "You need") || !strings.Contains(string(raw), `"flags":64`) {
		t.Errorf("refusal = %s", raw)
	}

	// With the role, start works and the panel message is updated afterwards.
	fake.send <- interaction(discord.InteractionComponent, map[string]any{"custom_id": "wt:act:start:" + in.ID}, "0", "400000000000000001")
	call, n = fake.waitCall("POST", "/interactions/600000000000000001/itoken/callback", n)
	if call.Body["type"] != float64(discord.ResponseDeferUpdate) {
		t.Errorf("start response = %v", call.Body)
	}
	edit, n := fake.waitCall("PATCH", "/webhooks/700000000000000001/itoken/messages/@original", n)
	if raw, _ := json.Marshal(edit.Body); !strings.Contains(string(raw), "started") || !strings.Contains(string(raw), "james") {
		t.Errorf("panel edit = %s", raw)
	}
	c.waitState(in.ID, protocol.StateRunning)

	// Stop asks first.
	fake.send <- interaction(discord.InteractionComponent, map[string]any{"custom_id": "wt:act:stop:" + in.ID}, "32")
	call, n = fake.waitCall("POST", "/interactions/600000000000000001/itoken/callback", n)
	if raw, _ := json.Marshal(call.Body); !strings.Contains(string(raw), "wt:confirm:stop:"+in.ID) {
		t.Errorf("stop confirmation = %s", raw)
	}

	// /server opens a private panel with the game art attached.
	fake.send <- interaction(discord.InteractionCommand, map[string]any{"name": "server", "options": []map[string]any{{"name": "name", "type": 3, "value": "survival"}}}, "0")
	call, n = fake.waitCall("POST", "/interactions/600000000000000001/itoken/callback", n)
	if raw, _ := json.Marshal(call.Body); !strings.Contains(string(raw), "wt:act:restart:"+in.ID) || !strings.Contains(string(raw), `"flags":64`) {
		t.Errorf("/server = %s", raw)
	}

	// Autocomplete suggests servers by name.
	fake.send <- interaction(discord.InteractionAutocomplete, map[string]any{"name": "server", "options": []map[string]any{{"name": "name", "type": 3, "value": "surv", "focused": true}}}, "0")
	call, _ = fake.waitCall("POST", "/interactions/600000000000000001/itoken/callback", n)
	if raw, _ := json.Marshal(call.Body); !strings.Contains(string(raw), in.ID) {
		t.Errorf("autocomplete = %s", raw)
	}

	// The activity log names the Discord member.
	var acts []Event
	c.do("GET", "/api/v1/activity?kind=action", nil, http.StatusOK, &acts)
	found := false
	for _, a := range acts {
		if a.Actor == "james (Discord)" && a.Message == "started the server" {
			found = true
		}
	}
	if !found {
		t.Errorf("activity = %+v", acts)
	}
}

func TestDiscordBadToken(t *testing.T) {
	fake := newFakeDiscord(t)
	c, done := setupWith(t, func(cfg *Config) {
		cfg.DiscordAPI = fake.srv.URL + "/api"
		cfg.DiscordGateway = "ws" + strings.TrimPrefix(fake.srv.URL, "http") + "/gateway"
	})
	defer done()
	c.do("PUT", "/api/v1/discord", map[string]any{"enabled": true, "token": "wrong"}, http.StatusOK, nil)
	<-fake.identify
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got struct{ Status discordStatus }
		c.do("GET", "/api/v1/discord", nil, http.StatusOK, &got)
		if strings.Contains(got.Status.Error, "rejected the bot token") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v", got.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBoardLooks(t *testing.T) {
	if bar(0) != "▱▱▱▱▱▱▱▱▱▱" || bar(0.5) != "▰▰▰▰▰▱▱▱▱▱" || bar(2) != "▰▰▰▰▰▰▰▰▰▰" {
		t.Error("bar")
	}
	if memoryLimit("6G") != 6<<30 || memoryLimit("512m") != 512<<20 || memoryLimit("lots") != 0 {
		t.Error("memoryLimit")
	}
	if shortDuration(223380) != "2d 14h" || shortDuration(18660) != "5h 11m" {
		t.Error(shortDuration(223380), shortDuration(18660))
	}
	if !isSnowflake("800000000000000001") || isSnowflake("123") || isSnowflake("80000000000000000a") {
		t.Error("isSnowflake")
	}
}
