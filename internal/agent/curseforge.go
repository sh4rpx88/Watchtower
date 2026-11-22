package agent

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const curseForgeDownload = ".watchtower-modpack.zip"

var validCurseForgePack = regexp.MustCompile(`^cf:([0-9]{1,12}):([0-9]{1,12})$`)

// cfPack is a CurseForge modpack version ready to install: the Minecraft
// version and loader from its manifest, and its server files.
type cfPack struct {
	Name, MC, Loader, LoaderVersion string
	zipPath                         string // the server pack
}

type cfFile struct {
	ID               int    `json:"id"`
	DisplayName      string `json:"displayName"`
	DownloadURL      string `json:"downloadUrl"`
	ServerPackFileID int    `json:"serverPackFileId"`
	Hashes           []struct {
		Value string `json:"value"`
		Algo  int    `json:"algo"` // 1 is SHA-1
	} `json:"hashes"`
}

func (f cfFile) sha1() string {
	for _, h := range f.Hashes {
		if h.Algo == 1 {
			return h.Value
		}
	}
	return ""
}

func (d DockerRuntime) cfGet(ctx context.Context, key, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", mcSources.CurseForge+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Watchtower (https://github.com/sh4rpx88/Watchtower)")
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("CurseForge refused the API key (%s); check it under Settings", resp.Status)
		}
		return fmt.Errorf("CurseForge answered %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// cfDownload downloads one of a pack's files from CurseForge's CDN.
func (d DockerRuntime) cfDownload(ctx context.Context, key, modID string, f cfFile, dest string) error {
	link := f.DownloadURL
	if link == "" {
		var res struct{ Data string }
		if err := d.cfGet(ctx, key, fmt.Sprintf("/v1/mods/%s/files/%d/download-url", modID, f.ID), &res); err != nil || res.Data == "" {
			return errors.New("the pack's author doesn't allow downloads outside the CurseForge app")
		}
		link = res.Data
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !slices.Contains(mcSources.CurseForgeHosts, u.Hostname()) {
		return fmt.Errorf("CurseForge gave an unexpected download address %q", link)
	}
	return d.download(ctx, link, dest, f.sha1())
}

// fetchCurseForge reads a pack version's manifest for its Minecraft version
// and loader, then downloads its server pack.
func (d DockerRuntime) fetchCurseForge(ctx context.Context, key, setting, dir string, out func(string)) (*cfPack, error) {
	m := validCurseForgePack.FindStringSubmatch(setting)
	if m == nil {
		return nil, fmt.Errorf("%q isn't a CurseForge pack version", setting)
	}
	if key == "" {
		return nil, errors.New("add a CurseForge API key under Settings to install CurseForge packs")
	}
	modID, fileID := m[1], m[2]
	var file struct{ Data cfFile }
	if err := d.cfGet(ctx, key, "/v1/mods/"+modID+"/files/"+fileID, &file); err != nil {
		return nil, fmt.Errorf("modpack version: %w", err)
	}
	if file.Data.ServerPackFileID == 0 {
		return nil, fmt.Errorf("%s has no server files on CurseForge, so it can't run as a server; try another version", file.Data.DisplayName)
	}

	// The client pack's manifest names the exact loader version.
	out("Reading " + file.Data.DisplayName)
	clientZip := filepath.Join(dir, ".watchtower-modpack-client.zip")
	defer os.Remove(clientZip)
	if err := d.cfDownload(ctx, key, modID, file.Data, clientZip); err != nil {
		return nil, err
	}
	pack, err := readCurseForgeManifest(clientZip)
	if err != nil {
		return nil, err
	}

	var server struct{ Data cfFile }
	if err := d.cfGet(ctx, key, fmt.Sprintf("/v1/mods/%s/files/%d", modID, file.Data.ServerPackFileID), &server); err != nil {
		return nil, fmt.Errorf("server pack: %w", err)
	}
	out("Downloading the server pack " + server.Data.DisplayName)
	pack.zipPath = filepath.Join(dir, curseForgeDownload)
	if err := d.cfDownload(ctx, key, modID, server.Data, pack.zipPath); err != nil {
		return nil, err
	}
	if pack.Name == "" {
		pack.Name = file.Data.DisplayName
	}
	return pack, nil
}

func readCurseForgeManifest(zipPath string) (*cfPack, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("reading the modpack: %w", err)
	}
	defer zr.Close()
	f, err := zr.Open("manifest.json")
	if err != nil {
		return nil, errors.New("the modpack has no manifest.json")
	}
	defer f.Close()
	var man struct {
		Name      string `json:"name"`
		Minecraft struct {
			Version    string `json:"version"`
			ModLoaders []struct {
				ID      string `json:"id"` // like neoforge-21.1.77
				Primary bool   `json:"primary"`
			} `json:"modLoaders"`
		} `json:"minecraft"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&man); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	pack := &cfPack{Name: man.Name, MC: man.Minecraft.Version, Loader: "vanilla"}
	if pack.MC == "" {
		return nil, errors.New("the modpack doesn't say which Minecraft version it needs")
	}
	for _, l := range man.Minecraft.ModLoaders {
		name, version, _ := strings.Cut(l.ID, "-")
		if !slices.Contains([]string{"forge", "neoforge", "fabric", "quilt"}, name) {
			return nil, fmt.Errorf("the modpack needs %s, which Watchtower can't install", l.ID)
		}
		if l.Primary || pack.Loader == "vanilla" {
			pack.Loader, pack.LoaderVersion = name, version
		}
	}
	return pack, nil
}

// keepIfPresent are server files a pack may ship that hold the owner's own
// settings, so an existing copy is never replaced.
var keepIfPresent = []string{"server.properties", "eula.txt", "ops.json", "whitelist.json", "banned-players.json", "banned-ips.json", "usercache.json"}

// applyServerPack unpacks a CurseForge server pack into dir. Most packs put
// everything in one top folder, which is skipped. Worlds are never touched.
func applyServerPack(dir string, pack *cfPack, out func(string)) error {
	zr, err := zip.OpenReader(pack.zipPath)
	if err != nil {
		return fmt.Errorf("reading the server pack: %w", err)
	}
	defer zr.Close()
	top := ""
	for i, f := range zr.File {
		first, _, nested := strings.Cut(strings.ReplaceAll(f.Name, `\`, "/"), "/")
		if !nested || (i > 0 && first+"/" != top) {
			top = ""
			break
		}
		top = first + "/"
	}
	keep := map[string]bool{}
	count := 0
	for _, f := range zr.File {
		rel := strings.TrimPrefix(strings.ReplaceAll(f.Name, `\`, "/"), top)
		if rel == "" || f.FileInfo().IsDir() || strings.HasPrefix(rel, "world/") {
			continue
		}
		dest, err := packPath(dir, rel)
		if err != nil {
			return err
		}
		if slices.Contains(keepIfPresent, rel) {
			if _, err := os.Stat(dest); err == nil {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, dest); err != nil {
			return err
		}
		if strings.HasPrefix(rel, "mods/") {
			keep[rel] = true
		}
		count++
	}
	out(fmt.Sprintf("Unpacked %d server files, %d of them mods", count, len(keep)))
	return recordPack(dir, pack.Name, keep, out)
}
