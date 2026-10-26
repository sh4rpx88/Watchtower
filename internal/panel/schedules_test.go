package panel

import (
	"net/http"
	"testing"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

func TestSchedules(t *testing.T) {
	c, done := setup(t)
	defer done()

	var in instanceView
	c.do("POST", "/api/v1/instances", map[string]any{
		"name": "Survival", "templateId": "minecraft-java", "agent": "local", "settings": map[string]string{"eula": "true"},
	}, http.StatusCreated, &in)
	base := "/api/v1/instances/" + in.ID + "/schedules"

	for _, bad := range []map[string]any{
		{"action": "explode", "cron": "0 4 * * *"},
		{"action": "restart", "cron": "0 25 * * *"},
		{"action": "restart", "cron": "0 0 30 2 *"}, // never runs
		{"action": "command", "cron": "0 4 * * *"},
		{"action": "command", "cron": "0 4 * * *", "command": "say a\nstop"},
	} {
		c.do("POST", base, bad, http.StatusBadRequest, nil)
	}

	var restart, backup Schedule
	c.do("POST", base, map[string]any{"name": "Nightly restart", "action": "restart", "cron": "0  4 * * *", "warn": true, "enabled": true, "keep": 5},
		http.StatusCreated, &restart)
	if restart.Cron != "0 4 * * *" || restart.Keep != 0 || !restart.Warn || restart.NextRun == nil {
		t.Fatalf("restart schedule = %+v", restart)
	}
	if h := restart.NextRun.In(time.Local); h.Hour() != 4 || h.Minute() != 0 {
		t.Fatalf("next run %s", h)
	}
	c.do("POST", base, map[string]any{"action": "backup", "cron": "30 3 * * *", "keep": 2, "warn": true}, http.StatusCreated, &backup)
	if backup.Name != "Backup" || backup.Warn || backup.Enabled || backup.NextRun != nil {
		t.Fatalf("backup schedule = %+v", backup)
	}

	// Running a restart now skips a stopped server.
	var res map[string]string
	c.do("POST", base+"/"+restart.ID+"/run", nil, http.StatusOK, &res)
	if res["result"] != "skipped: server wasn't running" {
		t.Fatalf("run = %v", res)
	}

	// A backup runs on a stopped server and reports back when it's done.
	c.do("POST", "/api/v1/instances/"+in.ID+"/install", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateStopped)
	c.do("POST", base+"/"+backup.ID+"/run", nil, http.StatusOK, &res)
	if res["result"] != "backup started" {
		t.Fatalf("run = %v", res)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var log []Event
		c.do("GET", "/api/v1/activity?instance="+in.ID+"&kind=game", nil, http.StatusOK, &log)
		if len(log) > 0 && log[0].Type == EventBackupDone && log[0].Backup != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no backup.done event: %+v", log)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var log []Event
	c.do("GET", "/api/v1/activity?instance="+in.ID+"&kind=action", nil, http.StatusOK, &log)
	if log[0].Actor != "admin" || log[0].Message != "made a backup (Backup)" {
		t.Fatalf("activity = %+v", log[0])
	}

	// With the server running, restart works and records its result.
	c.do("POST", "/api/v1/instances/"+in.ID+"/start", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateRunning)
	c.do("POST", base+"/"+restart.ID+"/run", nil, http.StatusOK, &res)
	if res["result"] != "restarted" {
		t.Fatalf("run = %v", res)
	}
	c.waitState(in.ID, protocol.StateRunning)
	var list []Schedule
	c.do("GET", base, nil, http.StatusOK, &list)
	if len(list) != 2 || list[0].LastResult != "restarted" || list[0].LastRun == nil {
		t.Fatalf("list = %+v", list)
	}

	// Edits keep the history; deletes and deleting the server remove schedules.
	restart.Cron, restart.Enabled = "15 5 * * 1-5", false
	var edited Schedule
	c.do("PUT", base+"/"+restart.ID, restart, http.StatusOK, &edited)
	if edited.Cron != "15 5 * * 1-5" || edited.LastResult != "restarted" || edited.NextRun != nil {
		t.Fatalf("edited = %+v", edited)
	}
	c.do("DELETE", base+"/"+backup.ID, nil, http.StatusNoContent, nil)
	c.do("DELETE", base+"/nope", nil, http.StatusNotFound, nil)
	c.do("POST", "/api/v1/instances/"+in.ID+"/stop", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateStopped)
	c.do("DELETE", "/api/v1/instances/"+in.ID, nil, http.StatusNoContent, nil)
	c.do("GET", base, nil, http.StatusOK, &list)
	if len(list) != 0 {
		t.Fatalf("schedules left after deleting the server: %+v", list)
	}
}

func TestServerDetailsAndPanelSettings(t *testing.T) {
	c, done := setup(t)
	defer done()
	var in instanceView
	c.do("POST", "/api/v1/instances", map[string]any{
		"name": "Survival", "templateId": "minecraft-java", "agent": "local", "settings": map[string]string{"eula": "true"},
	}, http.StatusCreated, &in)

	c.do("PATCH", "/api/v1/instances/"+in.ID, map[string]string{"connectAddress": "bad address"}, http.StatusBadRequest, nil)
	c.do("PATCH", "/api/v1/instances/"+in.ID, map[string]string{"name": " "}, http.StatusBadRequest, nil)
	var got instanceView
	c.do("PATCH", "/api/v1/instances/"+in.ID, map[string]string{"connectAddress": "cool-name.gl.joinmc.link"}, http.StatusOK, &got)
	if got.ConnectAddress != "cool-name.gl.joinmc.link" || got.Name != "Survival" {
		t.Fatalf("got %+v", got)
	}
	c.do("PATCH", "/api/v1/instances/"+in.ID, map[string]string{"name": "Hardcore"}, http.StatusOK, &got)
	if got.Name != "Hardcore" || got.ConnectAddress == "" {
		t.Fatalf("got %+v", got)
	}

	c.do("PUT", "/api/v1/settings", map[string]string{"publicAddress": "http://x"}, http.StatusBadRequest, nil)
	c.do("PUT", "/api/v1/settings", map[string]string{"publicAddress": "mc.example.com", "lanAddress": "192.168.1.180"}, http.StatusOK, nil)
	var settings struct {
		Settings PanelSettings
		TimeZone string
	}
	c.do("GET", "/api/v1/settings", nil, http.StatusOK, &settings)
	if settings.Settings.PublicAddress != "mc.example.com" || settings.Settings.LANAddress != "192.168.1.180" || settings.TimeZone == "" {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{5 * time.Minute: "5 minutes", time.Minute: "1 minute", 10 * time.Second: "10 seconds"} {
		if got := humanDuration(d); got != want {
			t.Errorf("%s = %q", d, got)
		}
	}
}
