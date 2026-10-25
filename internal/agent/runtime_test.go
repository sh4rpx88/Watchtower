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
	args, err := dockerRunArgs(spec, "/srv/watchtower/instances/abcdef0123456789", spec.Settings)
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
		"debian:12-slim sh -c exec ./start-server.sh -servername 'servertest' -adminpassword 'secret'",
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
	if err := writeFiles(spec, dir, spec.Settings); err != nil {
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

func TestMinecraftImageUsesInstalledJavaVersion(t *testing.T) {
	tpl := loadTemplate(t, "minecraft-java")
	dir := t.TempDir()
	spec := protocol.InstanceSpec{ID: "abcdef0123456789", Template: tpl, Settings: tpl.Settings(map[string]string{"eula": "true"})}

	// Before an install has recorded the Java version, starting fails clearly.
	vars, err := templateVars(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dockerRunArgs(spec, dir, vars); err == nil {
		t.Fatal("expected an error without javaVersion")
	}

	if err := saveInstallVars(dir, map[string]string{"javaVersion": "25"}); err != nil {
		t.Fatal(err)
	}
	vars, err = templateVars(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	args, err := dockerRunArgs(spec, dir, vars)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); !strings.Contains(got, "eclipse-temurin:25-jre sh -c exec java -Xmx'2G' -jar server.jar nogui") {
		t.Fatalf("unexpected args: %s", got)
	}
}

func TestSetKeyValuesKeepsOtherLines(t *testing.T) {
	in := "#Minecraft server properties\nmotd=Old\nlevel-seed=\nmax-players=20\n"
	got := setKeyValues(in, []string{"motd", "pvp"}, map[string]string{"motd": "Hello = world", "pvp": "false"})
	want := "#Minecraft server properties\nmotd=Hello = world\nlevel-seed=\nmax-players=20\npvp=false\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := setKeyValues("", []string{"a"}, map[string]string{"a": "1"}); got != "a=1\n" {
		t.Fatalf("new file: %q", got)
	}
}

func TestWriteConfigFilesZomboidIni(t *testing.T) {
	tpl := loadTemplate(t, "project-zomboid")
	dir := t.TempDir()
	spec := protocol.InstanceSpec{ID: "abcdef0123456789", Template: tpl,
		Settings: tpl.Settings(map[string]string{"adminpassword": "x", "MaxPlayers": "8"})}
	if err := writeConfigFiles(spec, dir, spec.Settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Zomboid/Server/servertest.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "MaxPlayers=8\n") || !strings.Contains(string(data), "PublicName=My PZ Server\n") {
		t.Fatalf("unexpected ini:\n%s", data)
	}
}

func TestStartCommandQuotesSettings(t *testing.T) {
	tpl := loadTemplate(t, "project-zomboid")
	spec := protocol.InstanceSpec{ID: "abcdef0123456789", Template: tpl,
		Settings: tpl.Settings(map[string]string{"adminpassword": "it's a secret; rm -rf /"})}
	args, err := dockerRunArgs(spec, "/d", spec.Settings)
	if err != nil {
		t.Fatal(err)
	}
	want := `exec ./start-server.sh -servername 'servertest' -adminpassword 'it'\''s a secret; rm -rf /'`
	if got := args[len(args)-1]; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestParseDockerStats(t *testing.T) {
	got := parseDockerStats("watchtower-abcdef0123456789\t12.50%\t1.5GiB / 15.5GiB\nsomething-else\t1%\t1MiB / 2MiB\n")
	if len(got) != 1 || got[0].ID != "abcdef0123456789" || got[0].CPUPercent != 12.5 || got[0].MemoryBytes != int64(1.5*(1<<30)) {
		t.Fatalf("got %+v", got)
	}
}
