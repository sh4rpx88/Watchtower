package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// loginAs logs in and returns a client for that user, or nil when refused.
func loginAs(c *client, username, password string) *client {
	c.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(c.base+"/api/v1/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var out struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&out)
	return &client{t: c.t, base: c.base, token: out.Token}
}

func TestUsersAndRoles(t *testing.T) {
	admin, done := setup(t)
	defer done()

	var in instanceView
	admin.do("POST", "/api/v1/instances", map[string]any{
		"name": "Survival", "templateId": "minecraft-java", "agent": "local", "settings": map[string]string{"eula": "true"},
	}, http.StatusCreated, &in)

	admin.do("POST", "/api/v1/users", map[string]string{"username": "op", "password": "short", "role": "operator"}, http.StatusBadRequest, nil)
	admin.do("POST", "/api/v1/users", map[string]string{"username": "op", "password": "operator1", "role": "king"}, http.StatusBadRequest, nil)
	var op User
	admin.do("POST", "/api/v1/users", map[string]string{"username": "op", "password": "operator1", "role": "operator"}, http.StatusCreated, &op)
	admin.do("POST", "/api/v1/users", map[string]string{"username": "OP", "password": "operator1", "role": "viewer"}, http.StatusConflict, nil)
	var viewer User
	admin.do("POST", "/api/v1/users", map[string]string{"username": "vi", "password": "viewer12", "role": "viewer"}, http.StatusCreated, &viewer)
	if op.PasswordHash != "" {
		t.Fatal("password hash returned")
	}

	if loginAs(admin, "op", "wrong-password") != nil {
		t.Fatal("wrong password accepted")
	}
	opc, vic := loginAs(admin, "op", "operator1"), loginAs(admin, "vi", "viewer12")

	var me map[string]string
	vic.do("GET", "/api/v1/me", nil, http.StatusOK, &me)
	if me["name"] != "vi" || me["role"] != "viewer" {
		t.Fatalf("me = %v", me)
	}

	// Viewers look but don't touch.
	vic.do("GET", "/api/v1/instances", nil, http.StatusOK, nil)
	vic.do("GET", "/api/v1/activity", nil, http.StatusOK, nil)
	vic.do("POST", "/api/v1/instances/"+in.ID+"/install", nil, http.StatusForbidden, nil)
	vic.do("PUT", "/api/v1/instances/"+in.ID+"/settings", map[string]string{"eula": "true"}, http.StatusForbidden, nil)

	// Operators run servers but don't create them or manage people.
	opc.do("POST", "/api/v1/instances/"+in.ID+"/install", nil, http.StatusOK, nil)
	opc.waitState(in.ID, "stopped")
	opc.do("PUT", "/api/v1/instances/"+in.ID+"/settings", map[string]string{"eula": "true", "motd": "hi"}, http.StatusOK, nil)
	opc.do("GET", "/api/v1/instances/"+in.ID+"/backups", nil, http.StatusOK, nil)
	opc.do("POST", "/api/v1/instances", map[string]any{"name": "x", "templateId": "minecraft-java", "agent": "local"}, http.StatusForbidden, nil)
	opc.do("DELETE", "/api/v1/instances/"+in.ID, nil, http.StatusForbidden, nil)
	opc.do("GET", "/api/v1/users", nil, http.StatusForbidden, nil)
	opc.do("GET", "/api/v1/tokens", nil, http.StatusForbidden, nil)

	// Their actions are in the activity log under their name.
	var log []Event
	admin.do("GET", "/api/v1/activity?kind=action&instance="+in.ID, nil, http.StatusOK, &log)
	if len(log) < 2 || log[0].Actor != "op" || log[0].Message != "changed the settings" || log[0].Instance.Name != "Survival" {
		t.Fatalf("activity = %+v", log)
	}
	if log[1].Message != "installed or updated the server" {
		t.Fatalf("activity = %+v", log)
	}

	// The last admin can't be removed or demoted, and nobody can remove themselves.
	var users []User
	admin.do("GET", "/api/v1/users", nil, http.StatusOK, &users)
	var adminID string
	for _, u := range users {
		if u.Username == "admin" {
			adminID = u.ID
		}
	}
	admin.do("PATCH", "/api/v1/users/"+adminID, map[string]string{"role": "viewer"}, http.StatusConflict, nil)
	admin.do("DELETE", "/api/v1/users/"+adminID, nil, http.StatusConflict, nil)

	// Promoting takes effect on the next request.
	admin.do("PATCH", "/api/v1/users/"+op.ID, map[string]string{"role": "admin"}, http.StatusOK, nil)
	opc.do("GET", "/api/v1/users", nil, http.StatusOK, nil)
	opc.do("DELETE", "/api/v1/users/"+op.ID, nil, http.StatusConflict, nil)

	// Changing your password keeps this session and ends the others.
	other := loginAs(admin, "vi", "viewer12")
	vic.do("PUT", "/api/v1/me/password", map[string]string{"current": "nope", "new": "viewer-new"}, http.StatusForbidden, nil)
	vic.do("PUT", "/api/v1/me/password", map[string]string{"current": "viewer12", "new": "viewer-new"}, http.StatusNoContent, nil)
	vic.do("GET", "/api/v1/me", nil, http.StatusOK, nil)
	other.do("GET", "/api/v1/me", nil, http.StatusUnauthorized, nil)
	if loginAs(admin, "vi", "viewer12") != nil || loginAs(admin, "vi", "viewer-new") == nil {
		t.Fatal("password change didn't take")
	}

	// Removed users are logged out.
	admin.do("DELETE", "/api/v1/users/"+viewer.ID, nil, http.StatusNoContent, nil)
	vic.do("GET", "/api/v1/me", nil, http.StatusUnauthorized, nil)

	// Logging out ends the session.
	opc.do("POST", "/api/v1/logout", nil, http.StatusNoContent, nil)
	opc.do("GET", "/api/v1/me", nil, http.StatusUnauthorized, nil)
}

func TestUsersAndActivityPersist(t *testing.T) {
	dir := t.TempDir()
	open := func(password string) *client {
		s, err := New(Config{AdminPassword: password, AgentToken: "x", DataDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(s.Handler())
		t.Cleanup(srv.Close)
		return &client{t: t, base: srv.URL}
	}
	first := loginAs(open("first-pw"), "admin", "first-pw")
	first.do("POST", "/api/v1/users", map[string]string{"username": "op", "password": "operator1", "role": "operator"}, http.StatusCreated, nil)

	// The admin password only creates the first admin; after that, users.json wins.
	again := open("second-pw")
	if loginAs(again, "admin", "second-pw") != nil {
		t.Fatal("changed PANEL_ADMIN_PASSWORD replaced the saved password")
	}
	op := loginAs(again, "op", "operator1")
	if op == nil {
		t.Fatal("user not saved")
	}
	var log []Event
	op.do("GET", "/api/v1/activity?kind=action", nil, http.StatusOK, &log)
	if len(log) < 2 || log[0].Message != "logged in" || log[1].Message != "added user op as operator" {
		t.Fatalf("activity = %+v", log)
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(h, "correct horse") || checkPassword(h, "wrong horse") || checkPassword("garbage", "x") {
		t.Fatal("password check is wrong")
	}
}

func TestMain(m *testing.M) {
	passwordIterations = 1000 // hashing at full strength makes the suite slow under -race
	dummyHash, _ = hashPassword("not a real password")
	os.Exit(m.Run())
}
