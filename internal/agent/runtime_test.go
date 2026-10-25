package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

func loadTemplate(t *testing.T, id string) templates.Template {
	t.Helper()
	tpls, err := templates.LoadDir("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range tpls {
		if tpl.ID == id {
			return tpl
		}
	}
	t.Fatalf("template %s not found", id)
	return templates.Template{}
}

func TestDockerRunArgsZomboid(t *testing.T) {
	tpl := loadTemplate(t, "project-zomboid")
	spec := protocol.InstanceSpec{
		ID: "abcdef0123456789", Template: tpl,
		Settings: tpl.Settings(map[string]string{"adminpassword": "secret"}),
		Ports:    map[string]int{"game": 17261, "direct": 16262},
	}
	args, err := dockerRunArgs(spec, "/srv/watchtower/instances/abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	for _, want := range []string{
		"--name watchtower-abcdef0123456789",
		"-v /srv/watchtower/instances/abcdef0123456789:/server",
		"-e HOME=/server",
		"-p 17261:16261/udp",
		"-p 16262:16262/udp",
		"debian:12-slim sh -c exec ./start-server.sh -servername servertest -adminpassword secret",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestWriteFilesMinecraftEULA(t *testing.T) {
	tpl := loadTemplate(t, "minecraft-java")
	dir := t.TempDir()
	spec := protocol.InstanceSpec{ID: "abcdef0123456789", Template: tpl, Settings: tpl.Settings(map[string]string{"eula": "true"})}
	if err := writeFiles(spec, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "eula.txt"))
	if err != nil || string(data) != "eula=true\n" {
		t.Fatalf("eula.txt = %q, %v", data, err)
	}
}

func TestRenderMissingKeyFails(t *testing.T) {
	if _, err := templates.Render("java -Xmx{{.memory}}", map[string]string{}); err == nil {
		t.Fatal("expected an error for a missing setting")
	}
}
