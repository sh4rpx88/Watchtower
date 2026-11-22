package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

func sha1hex(b []byte) string { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }

// makePack builds a .mrpack with the given index files and zip entries.
func makePack(t *testing.T, files []map[string]any, deps map[string]string, entries map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	index, _ := json.Marshal(map[string]any{"formatVersion": 1, "game": "minecraft", "name": "Test Pack", "versionId": "1.0", "files": files, "dependencies": deps})
	w, _ := zw.Create("modrinth.index.json")
	w.Write(index)
	for name, body := range entries {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeMinecraftWorld serves Mojang, Fabric and Modrinth. packs maps a
// Modrinth version ID to its .mrpack bytes.
func fakeMinecraftWorld(t *testing.T, packs map[string][]byte, blobs map[string][]byte) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/mojang/manifest":
			fmt.Fprintf(w, `{"latest":{"release":"1.21.1"},"versions":[{"id":"1.21.1","url":"%s/mojang/1.21.1.json"}]}`, srv.URL)
		case p == "/mojang/1.21.1.json":
			fmt.Fprintf(w, `{"downloads":{"server":{"url":"%s/blob/server.jar","sha1":"%s"}},"javaVersion":{"majorVersion":21}}`, srv.URL, sha1hex(blobs["server.jar"]))
		case p == "/fabric/v2/versions/loader/1.21.1":
			fmt.Fprint(w, `[{"loader":{"version":"0.17.0-beta","stable":false}},{"loader":{"version":"0.16.9","stable":true}}]`)
		case p == "/fabric/v2/versions/installer":
			fmt.Fprint(w, `[{"version":"1.1.0","stable":true}]`)
		case strings.HasPrefix(p, "/fabric/v2/versions/loader/1.21.1/"):
			if p != "/fabric/v2/versions/loader/1.21.1/0.16.5/1.1.0/server/jar" {
				t.Errorf("fabric jar %s", p)
			}
			w.Write([]byte("fabric launcher"))
		case strings.HasPrefix(p, "/modrinth/v2/version/"):
			id := strings.TrimPrefix(p, "/modrinth/v2/version/")
			pack, ok := packs[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"name":"Test Pack %s","files":[{"url":"%s/blob/%s.mrpack","filename":"pack.mrpack","primary":true,"hashes":{"sha1":"%s"}}]}`,
				id, srv.URL, id, sha1hex(pack))
		case strings.HasPrefix(p, "/blob/"):
			name := strings.TrimPrefix(p, "/blob/")
			if pack, ok := packs[strings.TrimSuffix(name, ".mrpack")]; ok {
				w.Write(pack)
			} else if b, ok := blobs[name]; ok {
				w.Write(b)
			} else {
				http.NotFound(w, r)
			}
		default:
			t.Errorf("unexpected request %s", p)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := mcSources
	t.Cleanup(func() { mcSources = old })
	mcSources.Mojang = srv.URL + "/mojang/manifest"
	mcSources.Fabric = srv.URL + "/fabric"
	mcSources.Modrinth = srv.URL + "/modrinth"
	mcSources.PackHosts = []string{"127.0.0.1"}
	blobs["base"] = []byte(srv.URL)
}

func TestInstallModrinthPack(t *testing.T) {
	blobs := map[string][]byte{"server.jar": []byte("vanilla server"), "sodium.jar": []byte("server mod"), "lithium-1.jar": []byte("lithium v1"),
		"lithium-2.jar": []byte("lithium v2"), "client.jar": []byte("client only")}
	packs := map[string][]byte{}
	fakeMinecraftWorld(t, packs, blobs)
	base := string(blobs["base"])
	file := func(path, blob string, env map[string]string) map[string]any {
		return map[string]any{"path": path, "hashes": map[string]string{"sha1": sha1hex(blobs[blob])}, "env": env,
			"downloads": []string{"https://evil.example/" + blob, base + "/blob/" + blob}}
	}
	deps := map[string]string{"minecraft": "1.21.1", "fabric-loader": "0.16.5"}
	packs["AAAAAAAA"] = makePack(t, []map[string]any{
		file("mods/sodium.jar", "sodium.jar", map[string]string{"client": "required", "server": "optional"}),
		file("mods/lithium-1.jar", "lithium-1.jar", nil),
		file("mods/client.jar", "client.jar", map[string]string{"client": "required", "server": "unsupported"}),
	}, deps, map[string]string{"overrides/config/a.toml": "from overrides", "overrides/config/b.toml": "client value",
		"server-overrides/config/b.toml": "server value"})
	packs["BBBBBBBB"] = makePack(t, []map[string]any{
		file("mods/sodium.jar", "sodium.jar", nil),
		file("mods/lithium-2.jar", "lithium-2.jar", nil),
	}, deps, nil)
	packs["CCCCCCCC"] = makePack(t, []map[string]any{file("../escape.jar", "sodium.jar", nil)}, deps, nil)

	tpl := loadTemplate(t, "minecraft-java")
	dir := t.TempDir()
	install := func(settings map[string]string) error {
		settings["eula"] = "true"
		spec := protocol.InstanceSpec{ID: "abcdef0123456789", Template: tpl, Settings: tpl.Settings(settings)}
		var log []string
		err := DockerRuntime{}.Install(context.Background(), spec, dir, func(l string) { log = append(log, l) })
		t.Logf("install log:\n%s", strings.Join(log, "\n"))
		return err
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<missing>"
		}
		return strings.TrimSpace(string(b))
	}

	if err := install(map[string]string{"modpack": "AAAAAAAA"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"server.jar": "vanilla server", "fabric-server-launch.jar": "fabric launcher", "mods/sodium.jar": "server mod",
		"mods/lithium-1.jar": "lithium v1", "mods/client.jar": "<missing>", "config/a.toml": "from overrides", "config/b.toml": "server value",
		launchFile: "-jar fabric-server-launch.jar", installVarsFile: `{"javaVersion":"21"}`, modpackDownload: "<missing>",
	} {
		if got := read(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// The next version drops lithium-1 for lithium-2.
	if err := install(map[string]string{"modpack": "BBBBBBBB"}); err != nil {
		t.Fatal(err)
	}
	if read("mods/lithium-1.jar") != "<missing>" || read("mods/lithium-2.jar") != "lithium v2" || read("mods/sodium.jar") != "server mod" {
		t.Error("update didn't swap lithium")
	}

	// Paths outside the server folder and bad IDs are refused.
	if err := install(map[string]string{"modpack": "CCCCCCCC"}); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Errorf("escape: %v", err)
	}
	if err := install(map[string]string{"modpack": "../../x"}); err == nil {
		t.Error("bad modpack ID accepted")
	}

	// Going back to vanilla removes the pack's mods but keeps configs.
	if err := install(map[string]string{"modpack": ""}); err != nil {
		t.Fatal(err)
	}
	if read("mods/sodium.jar") != "<missing>" || read("config/a.toml") != "from overrides" || read(launchFile) != "-jar server.jar" {
		t.Error("vanilla install left the pack behind")
	}
}

func TestPackPath(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "../x", "mods/../../x", `mods\..\..\x`} {
		if _, err := packPath("/srv", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if p, err := packPath("/srv", "mods/a..b.jar"); err != nil || p != "/srv/mods/a..b.jar" {
		t.Errorf("got %q %v", p, err)
	}
}

func TestLoaderHelpers(t *testing.T) {
	for mc, want := range map[string]string{"1.21.1": "21.1.", "1.21": "21.0.", "1.20.4": "20.4.", "26.1": "26.1."} {
		if got := neoForgePrefix(mc); got != want {
			t.Errorf("neoForgePrefix(%s) = %s", mc, got)
		}
	}
	dir := t.TempDir()
	if _, err := forgeLaunch(dir, "libraries/net/minecraftforge/forge/1.20.1-47.3.0", "forge-1.20.1-47.3.0"); err == nil {
		t.Error("empty folder had a launch")
	}
	os.WriteFile(filepath.Join(dir, "forge-1.12.2-14.23.5.2860-installer.jar"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "forge-1.12.2-14.23.5.2860.jar"), nil, 0o644)
	if got, _ := forgeLaunch(dir, "libraries/net/minecraftforge/forge/1.12.2-14.23.5.2860", "forge-1.12.2-14.23.5.2860"); got != "-jar forge-1.12.2-14.23.5.2860.jar" {
		t.Errorf("old forge launch %q", got)
	}
	args := filepath.Join(dir, "libraries/net/neoforged/neoforge/21.1.77/unix_args.txt")
	os.MkdirAll(filepath.Dir(args), 0o755)
	os.WriteFile(args, nil, 0o644)
	if got, _ := forgeLaunch(dir, "libraries/net/neoforged/neoforge/21.1.77", "neoforge-21.1.77"); got != "@libraries/net/neoforged/neoforge/21.1.77/unix_args.txt" {
		t.Errorf("neoforge launch %q", got)
	}
	got := strings.Join(installerArgs(protocol.InstanceSpec{ID: "abcdef0123456789"}, "/d", 17, "--installServer"), " ")
	if got != "run --rm --name watchtower-abcdef0123456789-install -v /d:/server -w /server eclipse-temurin:17-jre java -jar .watchtower-installer.jar --installServer" {
		t.Errorf("installer args %s", got)
	}
}

// The start script runs whatever the install chose, or vanilla for servers
// installed before mod loaders.
func TestMinecraftStartScript(t *testing.T) {
	tpl := loadTemplate(t, "minecraft-java")
	dir := t.TempDir()
	spec := protocol.InstanceSpec{ID: "abcdef0123456789", Template: tpl, Settings: tpl.Settings(map[string]string{"eula": "true"})}
	if err := writeFiles(spec, dir, spec.Settings); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "java"), []byte("#!/bin/sh\necho \"$@\"\n"), 0o755)
	run := func() string {
		cmd := exec.Command("sh", "./watchtower-start.sh", "4G")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := run(); got != "-Xmx4G -jar server.jar nogui" {
		t.Errorf("vanilla: %s", got)
	}
	os.WriteFile(filepath.Join(dir, launchFile), []byte("@libraries/net/neoforged/neoforge/21.1.77/unix_args.txt\n"), 0o644)
	if got := run(); got != "-Xmx4G @libraries/net/neoforged/neoforge/21.1.77/unix_args.txt nogui" {
		t.Errorf("neoforge: %s", got)
	}
}

func zipOf(t *testing.T, entries map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallCurseForgePack(t *testing.T) {
	blobs := map[string][]byte{"server.jar": []byte("vanilla server")}
	fakeMinecraftWorld(t, map[string][]byte{}, blobs)
	base := string(blobs["base"])
	client := zipOf(t, map[string]string{"manifest.json": `{"name":"All the Mods 10","minecraft":{"version":"1.21.1","modLoaders":[{"id":"fabric-0.16.5","primary":true}]}}`})
	server1 := zipOf(t, map[string]string{
		"Server-Files-1.0/mods/alltheores.jar": "ores", "Server-Files-1.0/mods/old.jar": "old",
		"Server-Files-1.0/config/a.toml": "pack config", "Server-Files-1.0/server.properties": "motd=pack",
		"Server-Files-1.0/world/level.dat": "pack world",
	})
	server2 := zipOf(t, map[string]string{"mods/alltheores.jar": "ores v2", "startserver.sh": "#!/bin/sh"})
	files := map[string]string{"10": "client.zip", "11": "server1.zip", "20": "client.zip", "21": "server2.zip", "30": "client.zip"}
	blobs["client.zip"], blobs["server1.zip"], blobs["server2.zip"] = client, server1, server2
	serverOf := map[string]int{"10": 11, "20": 21, "30": 0}

	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "cf-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/mods/925200/files/")
		blob, ok := files[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"data":{"id":%s,"displayName":"ATM10 %s","downloadUrl":"%s/blob/%s","serverPackFileId":%d,"hashes":[{"value":"%s","algo":1}]}}`,
			id, id, base, blob, serverOf[id], sha1hex(blobs[blob]))
	}))
	defer cf.Close()
	mcSources.CurseForge = cf.URL
	mcSources.CurseForgeHosts = []string{"127.0.0.1"}

	tpl := loadTemplate(t, "minecraft-java")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "server.properties"), []byte("motd=mine"), 0o644)
	install := func(pack, key string) error {
		spec := protocol.InstanceSpec{ID: "abcdef0123456789", Template: tpl, CurseForgeKey: key,
			Settings: tpl.Settings(map[string]string{"eula": "true", "modpack": pack})}
		return DockerRuntime{}.Install(context.Background(), spec, dir, func(string) {})
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}

	if err := install("cf:925200:10", ""); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("no key: %v", err)
	}
	if err := install("cf:925200:30", "cf-key"); err == nil || !strings.Contains(err.Error(), "no server files") {
		t.Errorf("no server pack: %v", err)
	}
	if err := install("cf:925200:10", "cf-key"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"mods/alltheores.jar": "ores", "mods/old.jar": "old", "config/a.toml": "pack config", "server.properties": "motd=mine",
		"world/level.dat": "<missing>", launchFile: "-jar fabric-server-launch.jar\n", ".watchtower-modpack-client.zip": "<missing>", curseForgeDownload: "<missing>",
	} {
		if got := read(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if err := install("cf:925200:20", "cf-key"); err != nil {
		t.Fatal(err)
	}
	if read("mods/alltheores.jar") != "ores v2" || read("mods/old.jar") != "<missing>" || read("startserver.sh") != "#!/bin/sh" {
		t.Error("update didn't replace the pack's mods")
	}
	if err := install("cf:../1:2", "cf-key"); err == nil {
		t.Error("bad pack setting accepted")
	}
}
