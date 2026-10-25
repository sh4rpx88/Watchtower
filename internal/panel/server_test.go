package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sh4rpx88/Watchtower/internal/agent"
	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// fakeRuntime stands in for Docker: install succeeds at once and a started
// server echoes commands until it receives its stop command.
type fakeRuntime struct{}

func (fakeRuntime) Install(ctx context.Context, spec protocol.InstanceSpec, dir string, out func(string)) error {
	out("fake install of " + spec.Template.ID)
	return os.MkdirAll(dir, 0o755)
}

func (fakeRuntime) Start(spec protocol.InstanceSpec, dir string, out func(string)) (agent.Process, error) {
	p := &fakeProcess{stopCmd: spec.Template.Stop, out: out, done: make(chan struct{})}
	out("server started")
	return p, nil
}

type fakeProcess struct {
	stopCmd string
	out     func(string)
	once    sync.Once
	done    chan struct{}
}

func (p *fakeProcess) WriteLine(line string) error {
	// "fake-out X" makes the server print X, to simulate game output.
	if rest, ok := strings.CutPrefix(line, "fake-out "); ok {
		p.out(rest)
		return nil
	}
	p.out("echo: " + line)
	if line == p.stopCmd {
		p.once.Do(func() { close(p.done) })
	}
	return nil
}
func (p *fakeProcess) Wait() error      { <-p.done; return nil }
func (p *fakeProcess) Kill() error      { p.once.Do(func() { close(p.done) }); return nil }
func (p *fakeProcess) Terminate() error { return p.Kill() }

func (fakeRuntime) Resources(ids []string) ([]protocol.Resources, error) { return nil, nil }

func (fakeRuntime) Running(func(string) func(string)) (map[string]agent.Process, error) {
	return nil, nil
}

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c *client) do(method, path string, body any, want int, out any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: got %d want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if out != nil {
		json.Unmarshal(data, out)
	}
}

// doRaw sends body as-is and returns the response body as a string.
func (c *client) doRaw(method, path, body string, want int) string {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: got %d want %d: %s", method, path, resp.StatusCode, want, data)
	}
	return string(data)
}

func (c *client) waitState(id, want string) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var in instanceView
		c.do("GET", "/api/v1/instances/"+id, nil, http.StatusOK, &in)
		if in.State == want {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("instance never reached %q, last state %q (%s)", want, in.State, in.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func setup(t *testing.T) (*client, func()) {
	tpls, err := templates.LoadDir("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{AdminPassword: "pw", AgentToken: "agent-secret", Templates: tpls})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())

	resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"password":"pw"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %v", err)
	}
	var login struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&login)
	c := &client{t: t, base: srv.URL, token: login.Token}

	ctx, cancel := context.WithCancel(context.Background())
	go agent.Run(ctx, agent.Config{
		PanelURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/connect",
		Token:    "agent-secret", Name: "local", DataDir: t.TempDir(), Runtime: fakeRuntime{},
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		var agents []Agent
		c.do("GET", "/api/v1/agents", nil, http.StatusOK, &agents)
		if len(agents) == 1 && agents[0].Name == "local" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent never registered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c, func() { cancel(); srv.Close() }
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	s, _ := New(Config{AdminPassword: "pw", AgentToken: "x"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"password":"nope"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestAgentBadToken(t *testing.T) {
	s, _ := New(Config{AdminPassword: "pw", AgentToken: "agent-secret"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/agent/connect", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %v %v", err, resp.StatusCode)
	}
}

func TestInstanceLifecycle(t *testing.T) {
	c, done := setup(t)
	defer done()

	// The Minecraft EULA must be accepted.
	c.do("POST", "/api/v1/instances", map[string]any{"name": "Survival", "templateId": "minecraft-java", "agent": "local"},
		http.StatusBadRequest, nil)

	var in instanceView
	c.do("POST", "/api/v1/instances", map[string]any{
		"name": "Survival", "templateId": "minecraft-java", "agent": "local",
		"settings": map[string]string{"eula": "true"},
	}, http.StatusCreated, &in)
	if in.State != protocol.StateNotInstalled || in.Ports["game"] != 25565 || in.Settings["memory"] != "2G" {
		t.Fatalf("unexpected new instance: %+v", in)
	}

	// A second server on the same port is refused; on another port it's fine.
	c.do("POST", "/api/v1/instances", map[string]any{
		"name": "Creative", "templateId": "minecraft-java", "agent": "local", "settings": map[string]string{"eula": "true"},
	}, http.StatusConflict, nil)
	c.do("POST", "/api/v1/instances", map[string]any{
		"name": "Creative", "templateId": "minecraft-java", "agent": "local", "settings": map[string]string{"eula": "true"},
		"ports": map[string]int{"game": 25566},
	}, http.StatusCreated, nil)

	// Settings: a partial update keeps the rest, and bad values are refused.
	var updated instanceView
	c.do("PUT", "/api/v1/instances/"+in.ID+"/settings", map[string]string{"motd": "Hello", "nonsense": "x"}, http.StatusOK, &updated)
	if updated.Settings["motd"] != "Hello" || updated.Settings["eula"] != "true" || updated.Settings["nonsense"] != "" {
		t.Fatalf("unexpected settings: %+v", updated.Settings)
	}
	c.do("PUT", "/api/v1/instances/"+in.ID+"/settings", map[string]string{"difficulty": "nightmare"}, http.StatusBadRequest, nil)
	c.do("PUT", "/api/v1/instances/"+in.ID+"/settings", map[string]string{"max-players": "lots"}, http.StatusBadRequest, nil)
	c.do("PUT", "/api/v1/instances/"+in.ID+"/settings", map[string]string{"eula": "false"}, http.StatusBadRequest, nil)

	// Starting before installing fails.
	c.do("POST", "/api/v1/instances/"+in.ID+"/start", nil, http.StatusConflict, nil)

	c.do("POST", "/api/v1/instances/"+in.ID+"/install", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateStopped)
	c.do("POST", "/api/v1/instances/"+in.ID+"/start", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateRunning)

	// Open the console, send a command, and see it echoed.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(c.base, "http")+"/api/v1/instances/"+in.ID+"/console?access_token="+c.token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	c.do("POST", "/api/v1/instances/"+in.ID+"/command", map[string]string{"line": "say hi"}, http.StatusOK, nil)
	for {
		var line string
		if err := wsjson.Read(ctx, ws, &line); err != nil {
			t.Fatalf("console: %v", err)
		}
		if line == "echo: say hi" {
			break
		}
	}

	// Files: write, read back, list.
	c.doRaw("PUT", "/api/v1/instances/"+in.ID+"/files/content?path=ops.json", "[]", http.StatusOK)
	if got := c.doRaw("GET", "/api/v1/instances/"+in.ID+"/files/content?path=ops.json", "", http.StatusOK); got != "[]" {
		t.Fatalf("read back %q", got)
	}
	var files []protocol.FileInfo
	c.do("GET", "/api/v1/instances/"+in.ID+"/files?path=/", nil, http.StatusOK, &files)
	if len(files) != 1 || files[0].Name != "ops.json" {
		t.Fatalf("files: %+v", files)
	}

	// Restart stops the server and starts it again.
	c.do("POST", "/api/v1/instances/"+in.ID+"/restart", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateRunning)
	for {
		var line string
		if err := wsjson.Read(ctx, ws, &line); err != nil {
			t.Fatalf("console: %v", err)
		}
		if line == "server started" {
			break
		}
	}

	// Running servers can't be deleted.
	c.do("DELETE", "/api/v1/instances/"+in.ID, nil, http.StatusConflict, nil)
	c.do("POST", "/api/v1/instances/"+in.ID+"/stop", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateStopped)
	// Backups: create one while stopped, see it listed, restore it, delete it.
	c.do("POST", "/api/v1/instances/"+in.ID+"/backups", nil, http.StatusOK, nil)
	var backups []protocol.BackupInfo
	deadline := time.Now().Add(5 * time.Second)
	for len(backups) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("backup never appeared")
		}
		time.Sleep(20 * time.Millisecond)
		c.do("GET", "/api/v1/instances/"+in.ID+"/backups", nil, http.StatusOK, &backups)
	}
	c.doRaw("PUT", "/api/v1/instances/"+in.ID+"/files/content?path=ops.json", "changed", http.StatusOK)
	restoreDeadline := time.Now().Add(5 * time.Second)
	for {
		// The backup job may still be finishing; restore waits for it.
		req, _ := http.NewRequest("POST", c.base+"/api/v1/instances/"+in.ID+"/backups/"+backups[0].Name+"/restore", nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(restoreDeadline) {
			t.Fatal("restore never accepted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for c.doRaw("GET", "/api/v1/instances/"+in.ID+"/files/content?path=ops.json", "", http.StatusOK) != "[]" {
		if time.Now().After(restoreDeadline) {
			t.Fatal("restore didn't bring back the old file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.do("DELETE", "/api/v1/instances/"+in.ID+"/backups/"+backups[0].Name, nil, http.StatusOK, nil)

	c.do("DELETE", "/api/v1/instances/"+in.ID, nil, http.StatusNoContent, nil)
	c.do("GET", "/api/v1/instances/"+in.ID, nil, http.StatusNotFound, nil)
}

func TestStatsTokensAndWebhooks(t *testing.T) {
	c, done := setup(t)
	defer done()

	// Empty lists are [] rather than null, which the web UI relies on.
	if got := strings.TrimSpace(c.doRaw("GET", "/api/v1/tokens", "", http.StatusOK)); got != "[]" {
		t.Fatalf("empty tokens list = %s", got)
	}

	// A webhook receiver.
	got := make(chan Event, 10)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		json.NewDecoder(r.Body).Decode(&e)
		got <- e
	}))
	defer hook.Close()
	c.do("POST", "/api/v1/webhooks", map[string]any{"name": "test", "url": hook.URL, "format": "json",
		"events": []string{EventPlayerJoin, EventPlayerLeave, EventServerReady}}, http.StatusCreated, nil)
	c.do("POST", "/api/v1/webhooks", map[string]any{"name": "bad", "url": "ftp://x", "format": "json"}, http.StatusBadRequest, nil)

	var in instanceView
	c.do("POST", "/api/v1/instances", map[string]any{"name": "Survival", "templateId": "minecraft-java", "agent": "local",
		"settings": map[string]string{"eula": "true", "max-players": "10"}}, http.StatusCreated, &in)
	c.do("POST", "/api/v1/instances/"+in.ID+"/install", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateStopped)
	c.do("POST", "/api/v1/instances/"+in.ID+"/start", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateRunning)

	say := func(line string) {
		c.do("POST", "/api/v1/instances/"+in.ID+"/command", map[string]string{"line": "fake-out " + line}, http.StatusOK, nil)
	}
	expect := func(eventType, player string) {
		t.Helper()
		select {
		case e := <-got:
			if e.Type != eventType || e.Player != player || e.Instance.Name != "Survival" {
				t.Fatalf("got event %+v, want %s %s", e, eventType, player)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s event", eventType)
		}
	}
	say("[12:00:00] [Server thread/INFO]: Done (3.2s)! For help, type \"help\"")
	expect(EventServerReady, "")
	say("[12:00:01] [Server thread/INFO]: Steve joined the game")
	expect(EventPlayerJoin, "Steve")
	say("[12:00:02] [Server thread/INFO]: <Steve> Alex joined the game") // chat, not a join
	say("[12:00:03] [Server thread/INFO]: Alex joined the game")
	expect(EventPlayerJoin, "Alex")

	var st Stats
	c.do("GET", "/api/v1/instances/"+in.ID+"/stats", nil, http.StatusOK, &st)
	if !st.Ready || st.PlayerCount != 2 || st.MaxPlayers != 10 || strings.Join(st.Players, ",") != "Alex,Steve" {
		t.Fatalf("unexpected stats: %+v", st)
	}

	// API tokens: read can see stats but not control; control can run commands but not admin.
	var created struct {
		Token  APIToken
		Secret string
	}
	c.do("POST", "/api/v1/tokens", map[string]string{"name": "discord bot", "scope": "read"}, http.StatusCreated, &created)
	if !strings.HasPrefix(created.Secret, "wt_") || created.Token.Hash != "" {
		t.Fatalf("unexpected token: %+v", created)
	}
	reader := &client{t: t, base: c.base, token: created.Secret}
	var all []Stats
	reader.do("GET", "/api/v1/stats", nil, http.StatusOK, &all)
	if len(all) != 1 || all[0].PlayerCount != 2 {
		t.Fatalf("stats via token: %+v", all)
	}
	reader.do("POST", "/api/v1/instances/"+in.ID+"/stop", nil, http.StatusForbidden, nil)
	c.do("POST", "/api/v1/tokens", map[string]string{"name": "ops bot", "scope": "control"}, http.StatusCreated, &created)
	controller := &client{t: t, base: c.base, token: created.Secret}
	controller.do("POST", "/api/v1/instances/"+in.ID+"/command", map[string]string{"line": "fake-out [12:00:04] [Server thread/INFO]: Steve left the game"}, http.StatusOK, nil)
	expect(EventPlayerLeave, "Steve")
	controller.do("GET", "/api/v1/tokens", nil, http.StatusForbidden, nil)
	controller.do("DELETE", "/api/v1/instances/"+in.ID, nil, http.StatusForbidden, nil)

	// Stopping drops the remaining players.
	c.do("POST", "/api/v1/instances/"+in.ID+"/stop", nil, http.StatusOK, nil)
	expect(EventPlayerLeave, "Alex")
	c.waitState(in.ID, protocol.StateStopped)

	// Revoked tokens stop working.
	var tokens []APIToken
	c.do("GET", "/api/v1/tokens", nil, http.StatusOK, &tokens)
	for _, tok := range tokens {
		c.do("DELETE", "/api/v1/tokens/"+tok.ID, nil, http.StatusNoContent, nil)
	}
	reader.do("GET", "/api/v1/stats", nil, http.StatusUnauthorized, nil)
}

func TestDiscordText(t *testing.T) {
	e := Event{Type: EventPlayerJoin, Instance: EventInstance{Name: "Survival"}, Player: "Steve", PlayerCount: 3, MaxPlayers: 20}
	if got := e.Text(); got != "**Survival**: Steve joined (3/20 online)" {
		t.Fatalf("got %q", got)
	}
}
