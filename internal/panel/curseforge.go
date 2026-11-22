package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"
)

// CurseForge's API needs a key, so the browser searches through the panel.
// Minecraft is game 432 and modpacks are class 4471.
const (
	curseForgeMinecraft = "432"
	curseForgeModpacks  = "4471"
)

var curseForgeID = regexp.MustCompile(`^[0-9]{1,12}$`)

// CurseForgePack is a CurseForge modpack as the Mods tab shows it.
type CurseForgePack struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Summary   string `json:"summary"`
	Downloads int64  `json:"downloads"`
	Logo      string `json:"logo,omitempty"`
	Author    string `json:"author,omitempty"`
	URL       string `json:"url"`
}

// CurseForgeFile is one version of a pack. Only versions with ServerPack
// set can be installed, because the client files include mods that would
// crash a server.
type CurseForgeFile struct {
	ID           int      `json:"id"`
	Name         string   `json:"name"`
	GameVersions []string `json:"gameVersions"`
	ReleaseType  string   `json:"releaseType"` // release, beta or alpha
	Date         string   `json:"date"`
	ServerPack   bool     `json:"serverPack"`
}

type cfMod struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Summary       string `json:"summary"`
	DownloadCount int64  `json:"downloadCount"`
	ClassID       int    `json:"classId"`
	Logo          *struct {
		ThumbnailURL string `json:"thumbnailUrl"`
	} `json:"logo"`
	Authors []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Links struct {
		WebsiteURL string `json:"websiteUrl"`
	} `json:"links"`
}

func (m cfMod) pack() CurseForgePack {
	p := CurseForgePack{ID: m.ID, Name: m.Name, Slug: m.Slug, Summary: m.Summary, Downloads: m.DownloadCount, URL: m.Links.WebsiteURL}
	if m.Logo != nil {
		p.Logo = m.Logo.ThumbnailURL
	}
	if len(m.Authors) > 0 {
		p.Author = m.Authors[0].Name
	}
	if p.URL == "" {
		p.URL = "https://www.curseforge.com/minecraft/modpacks/" + m.Slug
	}
	return p
}

func (s *Server) curseForgeAPI() string {
	if s.cfg.CurseForgeAPI != "" {
		return s.cfg.CurseForgeAPI
	}
	return "https://api.curseforge.com"
}

// curseForge calls the CurseForge API and writes an error response itself
// when it fails.
func (s *Server) curseForge(w http.ResponseWriter, ctx context.Context, path string, query url.Values, out any) bool {
	key := s.panelSettings().CurseForgeKey
	if key == "" {
		writeError(w, http.StatusConflict, "add a CurseForge API key under Settings first")
		return false
	}
	u := s.curseForgeAPI() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "couldn't reach CurseForge: "+err.Error())
		return false
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		writeError(w, http.StatusBadGateway, "CurseForge refused the API key; check it under Settings")
		return false
	case resp.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "CurseForge doesn't have that")
		return false
	case resp.StatusCode != http.StatusOK:
		writeError(w, http.StatusBadGateway, "CurseForge answered "+resp.Status)
		return false
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		writeError(w, http.StatusBadGateway, "bad reply from CurseForge")
		return false
	}
	return true
}

// handleCurseForgeSearch finds modpacks, most popular first. With a
// curseforge.com link in q it finds that pack by its slug.
func (s *Server) handleCurseForgeSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	query := url.Values{"gameId": {curseForgeMinecraft}, "classId": {curseForgeModpacks}, "sortField": {"2"}, "sortOrder": {"desc"}, "pageSize": {"20"}}
	if m := regexp.MustCompile(`curseforge\.com/minecraft/modpacks/([A-Za-z0-9_-]+)`).FindStringSubmatch(q); m != nil {
		query.Set("slug", m[1])
	} else if q != "" {
		query.Set("searchFilter", q)
	}
	var res struct{ Data []cfMod }
	if !s.curseForge(w, r.Context(), "/v1/mods/search", query, &res) {
		return
	}
	out := []CurseForgePack{}
	for _, m := range res.Data {
		out = append(out, m.pack())
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCurseForgePack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("modId")
	if !curseForgeID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad pack ID")
		return
	}
	var res struct{ Data cfMod }
	if s.curseForge(w, r.Context(), "/v1/mods/"+id, nil, &res) {
		writeJSON(w, http.StatusOK, res.Data.pack())
	}
}

// handleCurseForgeFiles lists a pack's versions, newest first, leaving out
// the server packs themselves.
func (s *Server) handleCurseForgeFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("modId")
	if !curseForgeID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad pack ID")
		return
	}
	var res struct {
		Data []struct {
			ID               int      `json:"id"`
			DisplayName      string   `json:"displayName"`
			FileDate         string   `json:"fileDate"`
			ReleaseType      int      `json:"releaseType"`
			GameVersions     []string `json:"gameVersions"`
			IsServerPack     bool     `json:"isServerPack"`
			ServerPackFileID int      `json:"serverPackFileId"`
		}
	}
	if !s.curseForge(w, r.Context(), "/v1/mods/"+id+"/files", url.Values{"pageSize": {"50"}}, &res) {
		return
	}
	out := []CurseForgeFile{}
	for _, f := range res.Data {
		if f.IsServerPack {
			continue
		}
		out = append(out, CurseForgeFile{ID: f.ID, Name: f.DisplayName, GameVersions: f.GameVersions, Date: f.FileDate,
			ReleaseType: []string{"", "release", "beta", "alpha"}[min(max(f.ReleaseType, 0), 3)], ServerPack: f.ServerPackFileID != 0})
	}
	slices.SortStableFunc(out, func(a, b CurseForgeFile) int {
		if a.Date > b.Date {
			return -1
		}
		if a.Date < b.Date {
			return 1
		}
		return 0
	})
	writeJSON(w, http.StatusOK, out)
}
