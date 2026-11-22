package agent

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// mcSources are the services a Minecraft install talks to. Tests point them
// at a local server.
var mcSources = struct {
	Mojang, Modrinth, Fabric, Quilt, Forge, ForgePromos, NeoForge string
	PackHosts                                                     []string // hosts modpack files may be downloaded from
}{
	Mojang:      "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json",
	Modrinth:    "https://api.modrinth.com",
	Fabric:      "https://meta.fabricmc.net",
	Quilt:       "https://meta.quiltmc.org",
	Forge:       "https://maven.minecraftforge.net",
	ForgePromos: "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json",
	NeoForge:    "https://maven.neoforged.net",
	// The hosts Modrinth allows in .mrpack files.
	PackHosts: []string{"cdn.modrinth.com", "github.com", "raw.githubusercontent.com", "gitlab.com"},
}

const (
	launchFile       = ".watchtower-launch"         // java arguments that start the server, read by the start script
	modpackManifest  = ".watchtower-modpack.json"   // files the last modpack install put down
	modpackDownload  = ".watchtower-modpack.mrpack" // the pack while it installs
	installerJar     = ".watchtower-installer.jar"
	modpackDownloads = 6 // files downloaded at once
)

var (
	validModrinthID = regexp.MustCompile(`^[A-Za-z0-9]{8}$`)
	validMCVersion  = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,40}$`)
)

// installMinecraft installs a Minecraft server: vanilla or on a mod loader
// (Fabric, Quilt, Forge, NeoForge), or a Modrinth modpack with the version
// and loader it names. The start script runs whatever .watchtower-launch says.
func (d DockerRuntime) installMinecraft(ctx context.Context, spec protocol.InstanceSpec, dir string, out func(string)) error {
	vars, err := templateVars(spec, dir)
	if err != nil {
		return err
	}
	version := spec.Template.Install.Version
	if version, err = templates.Render(version, vars); err != nil {
		return fmt.Errorf("minecraft version: %w", err)
	}
	version = strings.TrimSpace(version)
	loader, loaderVersion := vars["loader"], ""
	var pack *mrpack
	if id := strings.TrimSpace(vars["modpack"]); id != "" {
		if pack, err = d.fetchModpack(ctx, id, dir, out); err != nil {
			return err
		}
		defer os.Remove(filepath.Join(dir, modpackDownload))
		version = pack.Dependencies["minecraft"]
		loader, loaderVersion = pack.loader()
	}
	if loader == "" {
		loader = "vanilla"
	}
	rel, err := d.mojangRelease(ctx, version)
	if err != nil {
		return err
	}
	out(fmt.Sprintf("Downloading Minecraft server %s", rel.ID))
	if err := d.download(ctx, rel.ServerURL, filepath.Join(dir, "server.jar"), rel.SHA1); err != nil {
		return err
	}
	launch, err := d.installLoader(ctx, spec, dir, loader, loaderVersion, rel, out)
	if err != nil {
		return err
	}
	if pack != nil {
		if err := d.applyModpack(ctx, dir, pack, out); err != nil {
			return err
		}
	} else if err := removeModpack(dir, out); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, launchFile), []byte(launch+"\n"), 0o644); err != nil {
		return err
	}
	if err := saveInstallVars(dir, map[string]string{"javaVersion": fmt.Sprint(rel.Java)}); err != nil {
		return err
	}
	what := "Minecraft server " + rel.ID
	if loader != "vanilla" {
		what += " on " + loaderName(loader)
	}
	if pack != nil {
		what = pack.Name + " (" + what + ")"
	}
	out(fmt.Sprintf("%s installed (needs Java %d)", what, rel.Java))
	return nil
}

func loaderName(loader string) string {
	switch loader {
	case "neoforge":
		return "NeoForge"
	default:
		return strings.ToUpper(loader[:1]) + loader[1:]
	}
}

type mojangRelease struct {
	ID, ServerURL, SHA1 string
	Java                int
}

func (d DockerRuntime) mojangRelease(ctx context.Context, version string) (mojangRelease, error) {
	var manifest struct {
		Latest   struct{ Release string }
		Versions []struct{ ID, URL string }
	}
	if err := d.getJSON(ctx, mcSources.Mojang, &manifest); err != nil {
		return mojangRelease{}, fmt.Errorf("version manifest: %w", err)
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
		return mojangRelease{}, fmt.Errorf("minecraft version %q not found", version)
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
		return mojangRelease{}, fmt.Errorf("version %s: %w", version, err)
	}
	if meta.Downloads.Server.URL == "" {
		return mojangRelease{}, fmt.Errorf("version %s has no server download", version)
	}
	java := meta.JavaVersion.MajorVersion
	if java == 0 {
		java = 8 // versions before 1.17 don't say
	}
	return mojangRelease{ID: version, ServerURL: meta.Downloads.Server.URL, SHA1: meta.Downloads.Server.SHA1, Java: java}, nil
}

// installLoader puts the mod loader in dir and returns the java arguments
// that start it. An empty loaderVersion picks the newest stable one.
func (d DockerRuntime) installLoader(ctx context.Context, spec protocol.InstanceSpec, dir, loader, loaderVersion string, rel mojangRelease, out func(string)) (string, error) {
	if loaderVersion != "" && !validMCVersion.MatchString(loaderVersion) {
		return "", fmt.Errorf("%q isn't a valid loader version", loaderVersion)
	}
	mc := rel.ID
	switch loader {
	case "vanilla":
		return "-jar server.jar", nil

	case "fabric":
		if loaderVersion == "" {
			var loaders []struct {
				Loader struct {
					Version string
					Stable  bool
				}
			}
			if err := d.getJSON(ctx, mcSources.Fabric+"/v2/versions/loader/"+url.PathEscape(mc), &loaders); err != nil {
				return "", fmt.Errorf("fabric versions: %w", err)
			}
			for _, l := range loaders { // newest first; take the first stable one
				if loaderVersion == "" || l.Loader.Stable {
					loaderVersion = l.Loader.Version
				}
				if l.Loader.Stable {
					break
				}
			}
			if loaderVersion == "" {
				return "", fmt.Errorf("fabric doesn't support Minecraft %s yet", mc)
			}
		}
		var installers []struct {
			Version string
			Stable  bool
		}
		if err := d.getJSON(ctx, mcSources.Fabric+"/v2/versions/installer", &installers); err != nil {
			return "", fmt.Errorf("fabric installer versions: %w", err)
		}
		installer := ""
		for _, i := range installers {
			if installer == "" || i.Stable {
				installer = i.Version
			}
			if i.Stable {
				break
			}
		}
		out("Downloading Fabric loader " + loaderVersion)
		jar := fmt.Sprintf("%s/v2/versions/loader/%s/%s/%s/server/jar", mcSources.Fabric, url.PathEscape(mc), url.PathEscape(loaderVersion), url.PathEscape(installer))
		if err := d.download(ctx, jar, filepath.Join(dir, "fabric-server-launch.jar"), ""); err != nil {
			return "", err
		}
		return "-jar fabric-server-launch.jar", nil

	case "quilt":
		if loaderVersion == "" {
			var loaders []struct{ Loader struct{ Version string } }
			if err := d.getJSON(ctx, mcSources.Quilt+"/v3/versions/loader/"+url.PathEscape(mc), &loaders); err != nil {
				return "", fmt.Errorf("quilt versions: %w", err)
			}
			for _, l := range loaders {
				if !strings.Contains(l.Loader.Version, "-") { // skip betas
					loaderVersion = l.Loader.Version
					break
				}
			}
			if loaderVersion == "" {
				return "", fmt.Errorf("quilt doesn't support Minecraft %s yet", mc)
			}
		}
		var installers []struct{ URL, Version string }
		if err := d.getJSON(ctx, mcSources.Quilt+"/v3/versions/installer", &installers); err != nil || len(installers) == 0 {
			return "", fmt.Errorf("quilt installer versions: %v", err)
		}
		out("Installing Quilt loader " + loaderVersion)
		err := d.runInstaller(ctx, spec, dir, installers[0].URL, rel.Java, out,
			"install", "server", mc, loaderVersion, "--install-dir=/server")
		if err != nil {
			return "", err
		}
		return "-jar quilt-server-launch.jar", nil

	case "forge":
		if loaderVersion == "" {
			var promos struct{ Promos map[string]string }
			if err := d.getJSON(ctx, mcSources.ForgePromos, &promos); err != nil {
				return "", fmt.Errorf("forge versions: %w", err)
			}
			loaderVersion = promos.Promos[mc+"-recommended"]
			if loaderVersion == "" {
				loaderVersion = promos.Promos[mc+"-latest"]
			}
			if loaderVersion == "" {
				return "", fmt.Errorf("forge doesn't support Minecraft %s yet", mc)
			}
		}
		full := mc + "-" + loaderVersion
		out("Installing Forge " + full)
		installer := fmt.Sprintf("%s/net/minecraftforge/forge/%s/forge-%s-installer.jar", mcSources.Forge, full, full)
		if err := d.runInstaller(ctx, spec, dir, installer, rel.Java, out, "--installServer"); err != nil {
			return "", err
		}
		return forgeLaunch(dir, "libraries/net/minecraftforge/forge/"+full, "forge-"+full)

	case "neoforge":
		if loaderVersion == "" {
			var versions struct{ Versions []string }
			if err := d.getJSON(ctx, mcSources.NeoForge+"/api/maven/versions/releases/net/neoforged/neoforge", &versions); err != nil {
				return "", fmt.Errorf("neoforge versions: %w", err)
			}
			prefix := neoForgePrefix(mc)
			for _, v := range versions.Versions {
				if strings.HasPrefix(v, prefix) && !strings.Contains(v, "beta") {
					loaderVersion = v // the list is oldest first
				}
			}
			if loaderVersion == "" {
				return "", fmt.Errorf("neoforge doesn't support Minecraft %s yet", mc)
			}
		}
		out("Installing NeoForge " + loaderVersion)
		installer := fmt.Sprintf("%s/releases/net/neoforged/neoforge/%s/neoforge-%s-installer.jar", mcSources.NeoForge, loaderVersion, loaderVersion)
		if err := d.runInstaller(ctx, spec, dir, installer, rel.Java, out, "--installServer"); err != nil {
			return "", err
		}
		return forgeLaunch(dir, "libraries/net/neoforged/neoforge/"+loaderVersion, "neoforge-"+loaderVersion)

	default:
		return "", fmt.Errorf("unknown mod loader %q", loader)
	}
}

// neoForgePrefix maps a Minecraft version to NeoForge's numbering:
// 1.21.1 is 21.1.x, 1.21 is 21.0.x, and year versions like 26.1 are 26.1.x.
func neoForgePrefix(mc string) string {
	if rest, ok := strings.CutPrefix(mc, "1."); ok {
		if !strings.Contains(rest, ".") {
			rest += ".0"
		}
		return rest + "."
	}
	return mc + "."
}

// forgeLaunch finds how to start an installed Forge or NeoForge server: an
// argument file on newer versions, a jar on older ones.
func forgeLaunch(dir, libDir, jarPrefix string) (string, error) {
	args := path.Join(libDir, "unix_args.txt")
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(args))); err == nil {
		return "@" + args, nil
	}
	jars, _ := filepath.Glob(filepath.Join(dir, jarPrefix+"*.jar"))
	for _, j := range jars {
		if name := filepath.Base(j); !strings.Contains(name, "installer") {
			return "-jar " + name, nil
		}
	}
	return "", errors.New("the installer finished but left no server to start")
}

// runInstaller downloads a loader's installer jar and runs it with Java in a
// container, so the agent needs no Java of its own.
func (d DockerRuntime) runInstaller(ctx context.Context, spec protocol.InstanceSpec, dir, jarURL string, java int, out func(string), args ...string) error {
	jar := filepath.Join(dir, installerJar)
	if err := d.download(ctx, jarURL, jar, ""); err != nil {
		return err
	}
	defer os.Remove(jar)
	return runStreaming(exec.CommandContext(ctx, "docker", installerArgs(spec, dir, java, args...)...), out)
}

func installerArgs(spec protocol.InstanceSpec, dir string, java int, args ...string) []string {
	return append([]string{"run", "--rm",
		"--name", containerName(spec.ID) + "-install",
		"-v", dir + ":/server", "-w", "/server",
		fmt.Sprintf("eclipse-temurin:%d-jre", java),
		"java", "-jar", installerJar}, args...)
}

// mrpack is a Modrinth modpack's modrinth.index.json.
type mrpack struct {
	Name         string `json:"name"`
	VersionID    string `json:"versionId"`
	Files        []mrpackFile
	Dependencies map[string]string `json:"dependencies"`
	zipPath      string
}

type mrpackFile struct {
	Path      string            `json:"path"`
	Hashes    map[string]string `json:"hashes"`
	Env       map[string]string `json:"env"`
	Downloads []string          `json:"downloads"`
}

// loader returns the mod loader the pack needs and its version.
func (p *mrpack) loader() (string, string) {
	for _, l := range []struct{ dep, name string }{
		{"fabric-loader", "fabric"}, {"quilt-loader", "quilt"}, {"neoforge", "neoforge"}, {"forge", "forge"},
	} {
		if v := p.Dependencies[l.dep]; v != "" {
			return l.name, v
		}
	}
	return "vanilla", ""
}

// fetchModpack downloads the .mrpack for a Modrinth version ID and reads its index.
func (d DockerRuntime) fetchModpack(ctx context.Context, versionID, dir string, out func(string)) (*mrpack, error) {
	if !validModrinthID.MatchString(versionID) {
		return nil, fmt.Errorf("%q isn't a Modrinth version ID", versionID)
	}
	var v struct {
		Name          string `json:"name"`
		VersionNumber string `json:"version_number"`
		Files         []struct {
			URL      string            `json:"url"`
			Filename string            `json:"filename"`
			Primary  bool              `json:"primary"`
			Hashes   map[string]string `json:"hashes"`
		} `json:"files"`
	}
	if err := d.getJSON(ctx, mcSources.Modrinth+"/v2/version/"+versionID, &v); err != nil {
		return nil, fmt.Errorf("modpack version: %w", err)
	}
	var fileURL, sum string
	for _, f := range v.Files {
		if strings.HasSuffix(f.Filename, ".mrpack") && (fileURL == "" || f.Primary) {
			fileURL, sum = f.URL, f.Hashes["sha1"]
		}
	}
	if fileURL == "" {
		return nil, errors.New("that Modrinth version has no .mrpack file")
	}
	out("Downloading modpack " + v.Name)
	zipPath := filepath.Join(dir, modpackDownload)
	if err := d.download(ctx, fileURL, zipPath, sum); err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("reading the modpack: %w", err)
	}
	defer zr.Close()
	f, err := zr.Open("modrinth.index.json")
	if err != nil {
		return nil, errors.New("the modpack has no modrinth.index.json")
	}
	defer f.Close()
	var pack mrpack
	if err := json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&pack); err != nil {
		return nil, fmt.Errorf("modrinth.index.json: %w", err)
	}
	if pack.Dependencies["minecraft"] == "" {
		return nil, errors.New("the modpack doesn't say which Minecraft version it needs")
	}
	if pack.Name == "" {
		pack.Name = v.Name
	}
	pack.zipPath = zipPath
	return &pack, nil
}

// packPath turns a path from a modpack into one inside dir, refusing any
// that would land outside it.
func packPath(dir, rel string) (string, error) {
	slashed := strings.ReplaceAll(rel, `\`, "/")
	clean := path.Clean("/" + slashed)
	if clean == "/" || strings.HasPrefix(slashed, "/") || slices.Contains(strings.Split(slashed, "/"), "..") {
		return "", fmt.Errorf("modpack file %q has an unsafe path", rel)
	}
	return filepath.Join(dir, filepath.FromSlash(clean)), nil
}

func packHostAllowed(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && slices.Contains(mcSources.PackHosts, u.Hostname())
}

// applyModpack downloads the pack's server files, copies its overrides, and
// removes files the previous pack version had that this one dropped.
func (d DockerRuntime) applyModpack(ctx context.Context, dir string, pack *mrpack, out func(string)) error {
	type job struct {
		dest, sum string
		urls      []string
	}
	var jobs []job
	keep := map[string]bool{}
	for _, f := range pack.Files {
		if f.Env["server"] == "unsupported" {
			continue
		}
		dest, err := packPath(dir, f.Path)
		if err != nil {
			return err
		}
		keep[f.Path] = true
		if sum := f.Hashes["sha1"]; sum != "" && fileSHA1(dest) == sum {
			continue // unchanged since the last install
		}
		var urls []string
		for _, u := range f.Downloads {
			if packHostAllowed(u) {
				urls = append(urls, u)
			}
		}
		if len(urls) == 0 {
			return fmt.Errorf("modpack file %s has no download from an allowed site", f.Path)
		}
		jobs = append(jobs, job{dest, f.Hashes["sha1"], urls})
	}

	out(fmt.Sprintf("Downloading %d of the pack's %d server files", len(jobs), len(keep)))
	var (
		mu       sync.Mutex
		firstErr error
		done     int
		wg       sync.WaitGroup
		queue    = make(chan job)
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for range modpackDownloads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				var err error
				if err = os.MkdirAll(filepath.Dir(j.dest), 0o755); err == nil {
					for _, u := range j.urls {
						if err = d.download(ctx, u, j.dest, j.sum); err == nil {
							break
						}
					}
				}
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				done++
				if done%25 == 0 {
					out(fmt.Sprintf("Downloaded %d of %d", done, len(jobs)))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		queue <- j
	}
	close(queue)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	if err := extractOverrides(pack.zipPath, dir); err != nil {
		return err
	}

	// Drop files from the previous pack that this version no longer has,
	// such as mods it updated to a new file name.
	for _, old := range readModpackManifest(dir) {
		if !keep[old] {
			if p, err := packPath(dir, old); err == nil && os.Remove(p) == nil {
				out("Removed " + old)
			}
		}
	}
	files := make([]string, 0, len(keep))
	for f := range keep {
		files = append(files, f)
	}
	slices.Sort(files)
	data, _ := json.MarshalIndent(map[string]any{"name": pack.Name, "versionId": pack.VersionID, "files": files}, "", "  ")
	return os.WriteFile(filepath.Join(dir, modpackManifest), data, 0o644)
}

// extractOverrides copies the pack's overrides/ and then server-overrides/
// folders into dir, so server-specific files win.
func extractOverrides(zipPath, dir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, prefix := range []string{"overrides/", "server-overrides/"} {
		for _, f := range zr.File {
			rel, ok := strings.CutPrefix(f.Name, prefix)
			if !ok || rel == "" || f.FileInfo().IsDir() {
				continue
			}
			dest, err := packPath(dir, rel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			if err := extractFile(f, dest); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractFile(f *zip.File, dest string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, io.LimitReader(r, 2<<30))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

func readModpackManifest(dir string) []string {
	var m struct{ Files []string }
	data, err := os.ReadFile(filepath.Join(dir, modpackManifest))
	if err == nil {
		json.Unmarshal(data, &m)
	}
	return m.Files
}

// removeModpack deletes the files a previous modpack installed, when the
// server no longer uses one. Worlds and overridden configs stay.
func removeModpack(dir string, out func(string)) error {
	files := readModpackManifest(dir)
	if len(files) == 0 {
		return nil
	}
	out(fmt.Sprintf("Removing the %d files the previous modpack installed", len(files)))
	for _, f := range files {
		if p, err := packPath(dir, f); err == nil {
			os.Remove(p)
		}
	}
	return os.Remove(filepath.Join(dir, modpackManifest))
}

func fileSHA1(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
