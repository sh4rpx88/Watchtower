package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCurseForge(t *testing.T) {
	var lastQuery string
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "my-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v1/mods/search":
			lastQuery = r.URL.RawQuery
			fmt.Fprint(w, `{"data":[{"id":925200,"name":"All the Mods 10","slug":"all-the-mods-10","summary":"Kitchen sink","downloadCount":5000000,
				"logo":{"thumbnailUrl":"https://media.forgecdn.net/x.png"},"authors":[{"name":"ATMTeam"}],"links":{"websiteUrl":"https://www.curseforge.com/minecraft/modpacks/all-the-mods-10"}}]}`)
		case "/v1/mods/925200/files":
			fmt.Fprint(w, `{"data":[
				{"id":1,"displayName":"ATM10 1.0","fileDate":"2025-01-01T00:00:00Z","releaseType":1,"gameVersions":["1.21.1","NeoForge"],"serverPackFileId":2},
				{"id":2,"displayName":"Server Files 1.0","fileDate":"2025-01-01T00:00:00Z","releaseType":1,"isServerPack":true},
				{"id":3,"displayName":"ATM10 1.1 beta","fileDate":"2025-02-01T00:00:00Z","releaseType":2,"gameVersions":["1.21.1"],"serverPackFileId":0}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer cf.Close()
	c, done := setupWith(t, func(cfg *Config) { cfg.CurseForgeAPI = cf.URL })
	defer done()

	c.do("GET", "/api/v1/curseforge/search?q=atm", nil, http.StatusConflict, nil)

	// The key is saved but never shown, and an empty key keeps it.
	var view struct {
		Settings         map[string]any `json:"settings"`
		HasCurseForgeKey bool           `json:"hasCurseForgeKey"`
	}
	c.do("PUT", "/api/v1/settings", map[string]string{"publicAddress": "play.example.com", "curseforgeKey": "bad key"}, http.StatusBadRequest, nil)
	c.do("PUT", "/api/v1/settings", map[string]string{"publicAddress": "play.example.com", "curseforgeKey": "my-key"}, http.StatusOK, &view)
	c.do("PUT", "/api/v1/settings", map[string]string{"publicAddress": "play2.example.com"}, http.StatusOK, nil)
	c.do("GET", "/api/v1/settings", nil, http.StatusOK, &view)
	if !view.HasCurseForgeKey || view.Settings["curseforgeKey"] != nil || view.Settings["publicAddress"] != "play2.example.com" {
		t.Fatalf("settings = %+v", view)
	}

	var packs []CurseForgePack
	c.do("GET", "/api/v1/curseforge/search?q=atm", nil, http.StatusOK, &packs)
	if len(packs) != 1 || packs[0].Name != "All the Mods 10" || packs[0].Author != "ATMTeam" || packs[0].Logo == "" ||
		!strings.Contains(lastQuery, "searchFilter=atm") || !strings.Contains(lastQuery, "classId=4471") {
		t.Fatalf("search = %+v (%s)", packs, lastQuery)
	}
	c.do("GET", "/api/v1/curseforge/search?q=https://www.curseforge.com/minecraft/modpacks/all-the-mods-10", nil, http.StatusOK, &packs)
	if !strings.Contains(lastQuery, "slug=all-the-mods-10") {
		t.Fatalf("link search query %s", lastQuery)
	}

	var files []CurseForgeFile
	c.do("GET", "/api/v1/curseforge/mods/925200/files", nil, http.StatusOK, &files)
	if len(files) != 2 || files[0].ID != 3 || files[0].ServerPack || files[0].ReleaseType != "beta" || !files[1].ServerPack {
		t.Fatalf("files = %+v", files)
	}
	c.do("GET", "/api/v1/curseforge/mods/../files", nil, http.StatusNotFound, nil)
	c.do("GET", "/api/v1/curseforge/mods/123/files", nil, http.StatusNotFound, nil)

	c.do("PUT", "/api/v1/settings", map[string]any{"clearCurseForgeKey": true}, http.StatusOK, &view)
	if view.HasCurseForgeKey {
		t.Fatal("key not cleared")
	}
}
