package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	return nil
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
	p.out("echo: " + line)
	if line == p.stopCmd {
		p.once.Do(func() { close(p.done) })
	}
	return nil
}
func (p *fakeProcess) Wait() error      { <-p.done; return nil }
func (p *fakeProcess) Kill() error      { p.once.Do(func() { close(p.done) }); return nil }
func (p *fakeProcess) Terminate() error { return p.Kill() }

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

	// Running servers can't be deleted.
	c.do("DELETE", "/api/v1/instances/"+in.ID, nil, http.StatusConflict, nil)
	c.do("POST", "/api/v1/instances/"+in.ID+"/stop", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateStopped)
	c.do("DELETE", "/api/v1/instances/"+in.ID, nil, http.StatusNoContent, nil)
	c.do("GET", "/api/v1/instances/"+in.ID, nil, http.StatusNotFound, nil)
}
