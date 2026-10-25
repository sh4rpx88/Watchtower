package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/agent"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

func TestLoginAndAgentConnect(t *testing.T) {
	tpls, err := templates.LoadDir("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Config{AdminPassword: "pw", AgentToken: "agent-secret", Templates: tpls}).Handler())
	defer srv.Close()

	// Wrong password is rejected.
	resp, _ := http.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"password":"nope"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/api/v1/login", "application/json", bytes.NewBufferString(`{"password":"pw"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %v %v", err, resp.StatusCode)
	}
	var login struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&login)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agent.Run(ctx, agent.Config{PanelURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/connect", Token: "agent-secret", Name: "test-agent"})

	deadline := time.Now().Add(5 * time.Second)
	for {
		var agents []Agent
		getJSON(t, srv.URL+"/api/v1/agents", login.Token, &agents)
		if len(agents) == 1 && agents[0].Name == "test-agent" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent never registered, got %+v", agents)
		}
		time.Sleep(50 * time.Millisecond)
	}

	var got []templates.Template
	getJSON(t, srv.URL+"/api/v1/templates", login.Token, &got)
	if len(got) != 2 || got[0].ID != "minecraft-java" || got[1].ID != "project-zomboid" {
		t.Fatalf("unexpected templates: %+v", got)
	}
}

func TestAgentBadToken(t *testing.T) {
	srv := httptest.NewServer(New(Config{AdminPassword: "pw", AgentToken: "agent-secret"}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/agent/connect", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %v %v", err, resp.StatusCode)
	}
}

func getJSON(t *testing.T, url, token string, v any) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %v %v", url, err, resp.StatusCode)
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(v)
}
