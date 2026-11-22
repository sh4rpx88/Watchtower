package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

// fakeSteam answers Steam's Workshop lookups: collection 9000000 holds items 1010000
// and 1020000, 1020000 requires 1030000, and 1040000 belongs to another game.
func fakeSteam(t *testing.T) *httptest.Server {
	items := map[string]string{
		"9000000": `{"publishedfileid":"9000000","result":1,"title":"My pack","creator_app_id":766,"consumer_app_id":108600}`,
		"1010000": `{"publishedfileid":"1010000","result":1,"title":"Mod A","file_size":"2048","creator_app_id":108600,"consumer_app_id":108600,
			"description":"Great mod.\n[b]Workshop ID:[/b] 1010000\n[b]Mod ID:[/b] ModA"}`,
		"1020000": `{"publishedfileid":"1020000","result":1,"title":"Mod B with a map","file_size":4096,"creator_app_id":108600,"consumer_app_id":108600,
			"description":"Mod ID: ModB, ModB_Extra\r\nMap Folder: MapX"}`,
		"1030000": `{"publishedfileid":"1030000","result":1,"title":"Library","creator_app_id":108600,"consumer_app_id":108600,"description":"Mod ID: Lib"}`,
		"1040000": `{"publishedfileid":"1040000","result":1,"title":"Other game mod","creator_app_id":4000,"consumer_app_id":4000}`,
	}
	children := map[string]string{"9000000": `[{"publishedfileid":"1010000"},{"publishedfileid":"1020000"}]`, "1020000": `[{"publishedfileid":"1030000"}]`}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		var ids []string
		for i := 0; r.PostForm.Has(fmt.Sprintf("publishedfileids[%d]", i)); i++ {
			ids = append(ids, r.PostForm.Get(fmt.Sprintf("publishedfileids[%d]", i)))
		}
		var parts []string
		switch r.URL.Path {
		case "/ISteamRemoteStorage/GetPublishedFileDetails/v1/":
			for _, id := range ids {
				if v, ok := items[id]; ok {
					parts = append(parts, v)
				} else {
					parts = append(parts, `{"publishedfileid":"`+id+`","result":9}`)
				}
			}
			fmt.Fprintf(w, `{"response":{"publishedfiledetails":[%s]}}`, strings.Join(parts, ","))
		case "/ISteamRemoteStorage/GetCollectionDetails/v1/":
			for _, id := range ids {
				if v, ok := children[id]; ok {
					parts = append(parts, `{"publishedfileid":"`+id+`","result":1,"children":`+v+`}`)
				} else {
					parts = append(parts, `{"publishedfileid":"`+id+`","result":9}`)
				}
			}
			fmt.Fprintf(w, `{"response":{"collectiondetails":[%s]}}`, strings.Join(parts, ","))
		default:
			t.Errorf("unexpected Steam call %s", r.URL.Path)
		}
	}))
}

func TestWorkshopMods(t *testing.T) {
	steam := fakeSteam(t)
	defer steam.Close()
	c, done := setupWith(t, func(cfg *Config) { cfg.SteamAPI = steam.URL })
	defer done()
	var in instanceView
	c.do("POST", "/api/v1/instances", map[string]any{
		"name": "Zomboid", "templateId": "project-zomboid", "agent": "local", "settings": map[string]string{"adminpassword": "pw"},
	}, http.StatusCreated, &in)
	c.do("POST", "/api/v1/instances/"+in.ID+"/install", nil, http.StatusOK, nil)
	c.waitState(in.ID, protocol.StateStopped)
	base := "/api/v1/instances/" + in.ID + "/mods"

	var got struct {
		List  protocol.ModList        `json:"list"`
		Items map[string]WorkshopItem `json:"items"`
	}
	c.do("GET", base, nil, http.StatusOK, &got)
	if got.List.Exists || len(got.List.Mods) != 0 || got.List.File != "Zomboid/Server/servertest.ini" {
		t.Fatalf("mods before = %+v", got.List)
	}

	// A collection link and an item ID: the collection's items come out with
	// the library ahead of the mod that needs it, and the other game's item is left out.
	var found struct {
		Items []WorkshopItem `json:"items"`
		Notes []string       `json:"notes"`
	}
	c.do("POST", base+"/lookup", map[string]string{"query": "https://steamcommunity.com/sharedfiles/filedetails/?id=9000000\n1040000"}, http.StatusOK, &found)
	var order []string
	for _, it := range found.Items {
		order = append(order, it.ID)
	}
	if strings.Join(order, ",") != "1010000,1030000,1020000" || len(found.Notes) != 1 || !strings.Contains(found.Notes[0], "different game") {
		t.Fatalf("lookup = %v %v", order, found.Notes)
	}
	b := found.Items[2]
	if !slices.Equal(b.ModIDs, []string{"ModB", "ModB_Extra"}) || !slices.Equal(b.MapFolders, []string{"MapX"}) || b.Size != 4096 ||
		!slices.Equal(b.Requires, []string{"1030000"}) || found.Items[0].ModIDs[0] != "ModA" || found.Items[0].Size != 2048 {
		t.Fatalf("items = %+v", found.Items)
	}
	c.do("POST", base+"/lookup", map[string]string{"query": "no ids here"}, http.StatusBadRequest, nil)

	c.do("PUT", base, protocol.ModList{Mods: []string{"Bad;Mod"}}, http.StatusConflict, nil)
	c.do("PUT", base, protocol.ModList{WorkshopItems: []string{"12ab"}}, http.StatusConflict, nil)
	list := protocol.ModList{WorkshopItems: []string{"1010000", "1030000", "1020000"}, Mods: []string{"ModA", "Lib", "ModB"}, Maps: []string{"MapX", "Muldraugh, KY"}}
	c.do("PUT", base, list, http.StatusOK, nil)
	ini := c.doRaw("GET", "/api/v1/instances/"+in.ID+"/files/content?path=Zomboid/Server/servertest.ini", "", http.StatusOK)
	for _, want := range []string{"WorkshopItems=1010000;1030000;1020000\n", `Mods=\ModA;\Lib;\ModB` + "\n", "Map=MapX;Muldraugh, KY\n"} {
		if !strings.Contains(ini, want) {
			t.Errorf("ini lacks %q:\n%s", want, ini)
		}
	}

	c.do("GET", base, nil, http.StatusOK, &got)
	if !got.List.Exists || !slices.Equal(got.List.Mods, list.Mods) || got.Items["1020000"].Title != "Mod B with a map" {
		t.Fatalf("mods after = %+v %+v", got.List, got.Items)
	}

	// Build 41 has no backslashes, and an empty map list keeps the base map.
	c.do("PUT", "/api/v1/instances/"+in.ID+"/settings", map[string]string{"branch": "legacy41"}, http.StatusOK, nil)
	c.do("PUT", base, protocol.ModList{WorkshopItems: []string{"1010000"}, Mods: []string{"ModA"}}, http.StatusOK, nil)
	ini = c.doRaw("GET", "/api/v1/instances/"+in.ID+"/files/content?path=Zomboid/Server/servertest.ini", "", http.StatusOK)
	if !strings.Contains(ini, "\nMods=ModA\n") || !strings.Contains(ini, "Map=Muldraugh, KY\n") {
		t.Fatalf("ini:\n%s", ini)
	}

	// Minecraft has no Workshop.
	var mc instanceView
	c.do("POST", "/api/v1/instances", map[string]any{
		"name": "MC", "templateId": "minecraft-java", "agent": "local", "settings": map[string]string{"eula": "true"},
	}, http.StatusCreated, &mc)
	c.do("GET", "/api/v1/instances/"+mc.ID+"/mods", nil, http.StatusNotFound, nil)
}
