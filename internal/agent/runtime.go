package agent

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// Runtime installs and runs game servers. DockerRuntime is the real one; tests use a fake.
type Runtime interface {
	// Install puts the game's files into dir. It blocks until done.
	Install(ctx context.Context, spec protocol.InstanceSpec, dir string, out func(string)) error
	// Start launches the server and streams its output to out until it exits.
	Start(spec protocol.InstanceSpec, dir string, out func(string)) (Process, error)
	// Running reattaches to servers left running by an earlier agent process,
	// keyed by instance ID. out gives each one's console sink.
	Running(out func(id string) func(string)) (map[string]Process, error)
}

type Process interface {
	WriteLine(line string) error // send a console command
	Wait() error                 // block until the server exits; call once
	Kill() error                 // stop it now
	Terminate() error            // ask it to stop (SIGTERM), killing it after a grace period
}

// DockerRuntime runs each instance as a container through the docker CLI.
// dir must be a path the Docker daemon can see, so the agent's data folder is
// mounted at the same path on the host and in the agent container.
type DockerRuntime struct {
	HTTP *http.Client
}

func containerName(id string) string { return "watchtower-" + id }

func (d DockerRuntime) Install(ctx context.Context, spec protocol.InstanceSpec, dir string, out func(string)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	inst := spec.Template.Install
	switch inst.Method {
	case "mojang":
		return d.installMojang(ctx, inst.Version, dir, out)
	case "steamcmd":
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
			"--name", containerName(spec.ID)+"-install",
			"-v", dir+":/server",
			"steamcmd/steamcmd:latest",
			"+force_install_dir", "/server", "+login", "anonymous",
			"+app_update", fmt.Sprint(inst.SteamAppID), "validate", "+quit")
		return runStreaming(cmd, out)
	case "download":
		name := path.Base(inst.DownloadURL)
		out("Downloading " + inst.DownloadURL)
		return d.download(ctx, inst.DownloadURL, filepath.Join(dir, name), "")
	default:
		return fmt.Errorf("unknown install method %q", inst.Method)
	}
}

const mojangManifest = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

func (d DockerRuntime) installMojang(ctx context.Context, version, dir string, out func(string)) error {
	var manifest struct {
		Latest   struct{ Release string }
		Versions []struct{ ID, URL string }
	}
	if err := d.getJSON(ctx, mojangManifest, &manifest); err != nil {
		return fmt.Errorf("version manifest: %w", err)
	}
	if version == "" || version == "latest" {
		version = manifest.Latest.Release
	}
	var versionURL string
	for _, v := range manifest.Versions {
		if v.ID == version {
			versionURL = v.URL
		}
	}
	if versionURL == "" {
		return fmt.Errorf("minecraft version %q not found", version)
	}
	var meta struct {
		Downloads struct {
			Server struct{ URL, SHA1 string }
		}
		JavaVersion struct {
			MajorVersion int `json:"majorVersion"`
		} `json:"javaVersion"`
	}
	if err := d.getJSON(ctx, versionURL, &meta); err != nil {
		return fmt.Errorf("version %s: %w", version, err)
	}
	if meta.Downloads.Server.URL == "" {
		return fmt.Errorf("version %s has no server download", version)
	}
	out("Downloading Minecraft server " + version)
	if err := d.download(ctx, meta.Downloads.Server.URL, filepath.Join(dir, "server.jar"), meta.Downloads.Server.SHA1); err != nil {
		return err
	}
	java := meta.JavaVersion.MajorVersion
	if java == 0 {
		java = 8 // versions before 1.17 don't say
	}
	if err := saveInstallVars(dir, map[string]string{"javaVersion": fmt.Sprint(java)}); err != nil {
		return err
	}
	out(fmt.Sprintf("Minecraft server %s installed (needs Java %d)", version, java))
	return nil
}

// installVarsFile holds values learned during install, such as the Java
// version a Minecraft release needs. Templates use them like settings.
const installVarsFile = ".watchtower-install.json"

func saveInstallVars(dir string, vars map[string]string) error {
	data, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, installVarsFile), data, 0o644)
}

// templateVars merges install vars with the instance's settings; settings win.
func templateVars(spec protocol.InstanceSpec, dir string) (map[string]string, error) {
	vars := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, installVarsFile))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &vars); err != nil {
			return nil, fmt.Errorf("%s: %w", installVarsFile, err)
		}
	}
	for k, v := range spec.Settings {
		vars[k] = v
	}
	return vars, nil
}

func (d DockerRuntime) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (d DockerRuntime) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// download writes url to dest, checking the SHA-1 when one is given.
func (d DockerRuntime) download(ctx context.Context, url, dest, wantSHA1 string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha1.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if wantSHA1 != "" && hex.EncodeToString(h.Sum(nil)) != wantSHA1 {
		os.Remove(tmp)
		return fmt.Errorf("checksum mismatch for %s", url)
	}
	return os.Rename(tmp, dest)
}

func (d DockerRuntime) Start(spec protocol.InstanceSpec, dir string, out func(string)) (Process, error) {
	vars, err := templateVars(spec, dir)
	if err != nil {
		return nil, err
	}
	args, err := dockerRunArgs(spec, dir, vars)
	if err != nil {
		return nil, fmt.Errorf("%w (if the game was installed by an older version, press Update)", err)
	}
	if err := writeFiles(spec, dir, vars); err != nil {
		return nil, err
	}
	if err := writeConfigFiles(spec, dir, vars); err != nil {
		return nil, err
	}
	// Clear a container left behind by a crash.
	exec.Command("docker", "rm", "-f", containerName(spec.ID)).Run()
	if b, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(b)))
	}
	return attach(spec.ID, out)
}

// The server runs detached with stdin open, and the agent attaches to it. If
// the agent restarts, the server keeps running and Running reattaches.
func attach(id string, out func(string)) (Process, error) {
	cmd := exec.Command("docker", "attach", "--sig-proxy=false", containerName(id))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		done <- err
	}()
	go scanLines(pr, out)
	return &dockerProcess{id: id, stdin: stdin, done: done}, nil
}

func (d DockerRuntime) Running(out func(id string) func(string)) (map[string]Process, error) {
	b, err := exec.Command("docker", "ps", "--filter", "label=watchtower.instance",
		"--format", `{{.Label "watchtower.instance"}}`).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	procs := map[string]Process{}
	for _, id := range strings.Fields(string(b)) {
		p, err := attach(id, out(id))
		if err != nil {
			return nil, err
		}
		procs[id] = p
	}
	return procs, nil
}

// dockerRunArgs builds the docker run command line for an instance.
func dockerRunArgs(spec protocol.InstanceSpec, dir string, vars map[string]string) ([]string, error) {
	t := spec.Template
	start, err := templates.RenderShell(t.Start, vars)
	if err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}
	image, err := templates.Render(t.Image, vars)
	if err != nil {
		return nil, fmt.Errorf("image: %w", err)
	}
	args := []string{"run", "-d", "-i", "--rm", "--init",
		"--name", containerName(spec.ID),
		"--label", "watchtower.instance=" + spec.ID,
		"-v", dir + ":/server", "-w", "/server"}
	for _, k := range sortedKeys(t.Env) {
		v, err := templates.Render(t.Env[k], vars)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		args = append(args, "-e", k+"="+v)
	}
	for _, p := range t.Ports {
		host := p.Port
		if h, ok := spec.Ports[p.Name]; ok {
			host = h
		}
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		// The server listens on the template port inside its container.
		args = append(args, "-p", fmt.Sprintf("%d:%d/%s", host, p.Port, proto))
	}
	args = append(args, image, "sh", "-c", "exec "+start)
	return args, nil
}

// writeFiles writes the template's files (such as eula.txt) into dir.
func writeFiles(spec protocol.InstanceSpec, dir string, vars map[string]string) error {
	for name, contents := range spec.Template.Files {
		rendered, err := templates.Render(contents, vars)
		if err != nil {
			return fmt.Errorf("file %s: %w", name, err)
		}
		dest := filepath.Join(dir, filepath.Clean("/"+name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(rendered), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeConfigFiles sets each config field that names a file as a key=value
// line in that file, keeping every other line as it is. If the file doesn't
// exist yet it is created; the game fills in the rest on first start.
func writeConfigFiles(spec protocol.InstanceSpec, dir string, vars map[string]string) error {
	byFile := map[string][]templates.ConfigField{}
	var order []string
	for _, f := range spec.Template.Config {
		if f.File == "" {
			continue
		}
		name, err := templates.Render(f.File, vars)
		if err != nil {
			return fmt.Errorf("config file for %s: %w", f.Key, err)
		}
		if _, seen := byFile[name]; !seen {
			order = append(order, name)
		}
		byFile[name] = append(byFile[name], f)
	}
	for _, name := range order {
		dest := filepath.Join(dir, filepath.Clean("/"+name))
		existing, err := os.ReadFile(dest)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		values := map[string]string{}
		var keys []string
		for _, f := range byFile[name] {
			values[f.Key] = vars[f.Key]
			keys = append(keys, f.Key)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(setKeyValues(string(existing), keys, values)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// setKeyValues updates key=value lines in contents, appending keys that are
// missing in the given order. Comments, blank lines, and other keys are kept.
func setKeyValues(contents string, keys []string, values map[string]string) string {
	done := map[string]bool{}
	lines := strings.Split(strings.TrimRight(contents, "\n"), "\n")
	if contents == "" {
		lines = nil
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		key, _, found := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		if v, ok := values[key]; ok && found {
			lines[i] = key + "=" + v
			done[key] = true
		}
	}
	for _, k := range keys {
		if !done[k] {
			lines = append(lines, k+"="+values[k])
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

type dockerProcess struct {
	id    string
	stdin io.WriteCloser
	done  chan error
}

func (p *dockerProcess) WriteLine(line string) error {
	_, err := io.WriteString(p.stdin, strings.TrimRight(line, "\r\n")+"\n")
	return err
}

// Wait must be called once.
func (p *dockerProcess) Wait() error { return <-p.done }

func (p *dockerProcess) Kill() error {
	return exec.Command("docker", "kill", containerName(p.id)).Run()
}

// Terminate asks Docker to stop the server with SIGTERM, for servers whose
// template has no stop command or that were recovered without one.
func (p *dockerProcess) Terminate() error {
	return exec.Command("docker", "stop", "-t", "60", containerName(p.id)).Run()
}

func runStreaming(cmd *exec.Cmd, out func(string)) error {
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	scanned := make(chan struct{})
	go func() { scanLines(pr, out); close(scanned) }()
	err := cmd.Wait()
	pw.Close()
	<-scanned
	return err
}

func scanLines(r io.Reader, out func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		out(sc.Text())
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
