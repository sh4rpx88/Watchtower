package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// WorkshopItem is a Steam Workshop item as the Mods tab shows it. ModIDs and
// MapFolders are read from the item's description, where most Project
// Zomboid mods list them ("Mod ID: Foo").
type WorkshopItem struct {
	ID         string   `json:"id"`
	Found      bool     `json:"found"` // false when Steam doesn't know it or couldn't be asked
	Title      string   `json:"title,omitempty"`
	PreviewURL string   `json:"previewUrl,omitempty"`
	URL        string   `json:"url"`
	ModIDs     []string `json:"modIds"`
	MapFolders []string `json:"mapFolders"`
	Requires   []string `json:"requires"` // Workshop items this one needs
	Size       int64    `json:"size,omitempty"`
	UpdatedAt  int64    `json:"updatedAt,omitempty"` // unix seconds
	AppID      int      `json:"appId,omitempty"`
	collection bool
	children   []string // a collection's items
}

const (
	workshopCacheFor   = 30 * time.Minute
	steamCollectionApp = 766 // Steam's app ID on Workshop collections
	maxLookupItems     = 300
)

type workshopCache struct {
	mu    sync.Mutex
	items map[string]cachedItem
}

type cachedItem struct {
	item WorkshopItem
	at   time.Time
}

func (s *Server) steamAPI() string {
	if s.cfg.SteamAPI != "" {
		return s.cfg.SteamAPI
	}
	return "https://api.steampowered.com"
}

// steamPost calls one of Steam's public Workshop methods, which need no key.
func (s *Server) steamPost(ctx context.Context, method, countKey string, ids []string, out any) error {
	form := url.Values{countKey: {strconv.Itoa(len(ids))}}
	for i, id := range ids {
		form.Set(fmt.Sprintf("publishedfileids[%d]", i), id)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.steamAPI()+"/ISteamRemoteStorage/"+method+"/v1/", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach Steam: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("steam answered %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// workshopItems looks up items on Steam, using answers from the last half hour.
// Items Steam doesn't know come back with Found false.
func (s *Server) workshopItems(ctx context.Context, ids []string) (map[string]WorkshopItem, error) {
	out := map[string]WorkshopItem{}
	var missing []string
	s.workshop.mu.Lock()
	for _, id := range ids {
		if c, ok := s.workshop.items[id]; ok && time.Since(c.at) < workshopCacheFor {
			out[id] = c.item
		} else if !slices.Contains(missing, id) {
			missing = append(missing, id)
		}
	}
	s.workshop.mu.Unlock()

	for len(missing) > 0 {
		batch := missing[:min(len(missing), 100)]
		missing = missing[len(batch):]
		var details struct {
			Response struct {
				Details []struct {
					ID          string `json:"publishedfileid"`
					Result      int    `json:"result"`
					Title       string `json:"title"`
					Description string `json:"description"`
					Preview     string `json:"preview_url"`
					Size        any    `json:"file_size"` // a string or a number
					Updated     int64  `json:"time_updated"`
					ConsumerApp int    `json:"consumer_app_id"`
					CreatorApp  int    `json:"creator_app_id"`
				} `json:"publishedfiledetails"`
			} `json:"response"`
		}
		if err := s.steamPost(ctx, "GetPublishedFileDetails", "itemcount", batch, &details); err != nil {
			return out, err
		}
		// Collection details list a collection's items, and for an ordinary
		// item the items it requires.
		var coll struct {
			Response struct {
				Details []struct {
					ID       string `json:"publishedfileid"`
					Children []struct {
						ID string `json:"publishedfileid"`
					} `json:"children"`
				} `json:"collectiondetails"`
			} `json:"response"`
		}
		if err := s.steamPost(ctx, "GetCollectionDetails", "collectioncount", batch, &coll); err != nil {
			return out, err
		}
		children := map[string][]string{}
		for _, c := range coll.Response.Details {
			for _, ch := range c.Children {
				children[c.ID] = append(children[c.ID], ch.ID)
			}
		}
		found := map[string]WorkshopItem{}
		for _, d := range details.Response.Details {
			if d.Result != 1 {
				continue
			}
			size, _ := strconv.ParseInt(fmt.Sprint(d.Size), 10, 64)
			item := WorkshopItem{ID: d.ID, Found: true, Title: d.Title, PreviewURL: d.Preview, URL: workshopURL(d.ID),
				Size: size, UpdatedAt: d.Updated, AppID: d.ConsumerApp,
				ModIDs: descriptionList(d.Description, modIDLine), MapFolders: descriptionList(d.Description, mapFolderLine),
				Requires: []string{}}
			if d.CreatorApp == steamCollectionApp {
				item.collection, item.children = true, children[d.ID]
			} else {
				item.Requires = append(item.Requires, children[d.ID]...)
			}
			found[d.ID] = item
		}
		s.workshop.mu.Lock()
		if s.workshop.items == nil {
			s.workshop.items = map[string]cachedItem{}
		}
		for _, id := range batch {
			item, ok := found[id]
			if !ok {
				item = WorkshopItem{ID: id, URL: workshopURL(id), ModIDs: []string{}, MapFolders: []string{}, Requires: []string{}}
			}
			out[id] = item
			s.workshop.items[id] = cachedItem{item, time.Now()}
		}
		s.workshop.mu.Unlock()
	}
	return out, nil
}

func workshopURL(id string) string {
	return "https://steamcommunity.com/sharedfiles/filedetails/?id=" + id
}

var (
	bbcode        = regexp.MustCompile(`\[[^\]]{0,40}\]`)
	modIDLine     = regexp.MustCompile(`(?im)^\s*Mod\s*IDs?\s*:\s*(.+)$`)
	mapFolderLine = regexp.MustCompile(`(?im)^\s*Map\s*Folders?\s*:\s*(.+)$`)
	workshopIDs   = regexp.MustCompile(`\b[0-9]{6,20}\b`)
)

// descriptionList finds lines like "Mod ID: Foo" in a Workshop description,
// which may list several IDs separated by commas or semicolons.
func descriptionList(desc string, line *regexp.Regexp) []string {
	out := []string{}
	for _, m := range line.FindAllStringSubmatch(bbcode.ReplaceAllString(desc, " "), -1) {
		for _, v := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ';' }) {
			v = strings.TrimSpace(v)
			if v != "" && len(v) <= 100 && !strings.ContainsAny(v, `=\`) && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

func (s *Server) workshopTemplate(w http.ResponseWriter, in Instance) (templates.Template, bool) {
	tpl, ok := s.template(in.TemplateID)
	if !ok || tpl.Mods == nil || tpl.Mods.Kind != "workshop" {
		writeError(w, http.StatusNotFound, "this game has no Workshop mods")
		return tpl, false
	}
	return tpl, true
}

// handleMods returns the server's Workshop mods with each item's details
// from Steam. Steam being unreachable isn't an error; items just show IDs.
func (s *Server) handleMods(w http.ResponseWriter, r *http.Request) {
	in, a, ok := s.instanceAgent(w, r)
	if !ok {
		return
	}
	if _, ok := s.workshopTemplate(w, in); !ok {
		return
	}
	spec, err := s.spec(in)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	data, err := a.call(r.Context(), protocol.TypeModsRead, protocol.ModsRequest{InstanceSpec: spec})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	var list protocol.ModList
	if err := json.Unmarshal(data, &list); err != nil {
		writeError(w, http.StatusBadGateway, "bad reply from agent")
		return
	}
	items, err := s.workshopItems(r.Context(), list.WorkshopItems)
	resp := map[string]any{"list": list, "items": items, "baseMap": spec.Template.Mods.BaseMap}
	if err != nil {
		resp["steamError"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLookupMods finds the Workshop items in pasted links or IDs. A
// collection becomes its items, and each item is preceded by the items it
// requires, so they load first.
func (s *Server) handleLookupMods(w http.ResponseWriter, r *http.Request) {
	in, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return
	}
	tpl, ok := s.workshopTemplate(w, in)
	if !ok {
		return
	}
	var body struct {
		Query string `json:"query"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	ids := workshopIDs.FindAllString(body.Query, 100)
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "paste a Workshop link, an item ID, or a collection link")
		return
	}

	var order []string
	var notes []string
	known := map[string]WorkshopItem{}
	seen := map[string]bool{}
	var add func(id string, depth int) error
	add = func(id string, depth int) error {
		if seen[id] || len(order) >= maxLookupItems {
			return nil
		}
		seen[id] = true
		if _, ok := known[id]; !ok {
			got, err := s.workshopItems(r.Context(), []string{id})
			if err != nil {
				return err
			}
			known[id] = got[id]
		}
		item := known[id]
		switch {
		case !item.Found:
			notes = append(notes, fmt.Sprintf("Steam doesn't know a Workshop item %s, or it's private.", id))
			return nil
		case item.collection:
			// Fetch the collection's items together, then add them in order.
			got, err := s.workshopItems(r.Context(), item.children)
			if err != nil {
				return err
			}
			for k, v := range got {
				known[k] = v
			}
			for _, ch := range item.children {
				if err := add(ch, depth); err != nil {
					return err
				}
			}
			return nil
		case tpl.Mods.AppID != 0 && item.AppID != tpl.Mods.AppID:
			notes = append(notes, fmt.Sprintf("%s is for a different game.", item.Title))
			return nil
		}
		if depth < 4 {
			for _, req := range item.Requires {
				if err := add(req, depth+1); err != nil {
					return err
				}
			}
		}
		order = append(order, id)
		return nil
	}
	for _, id := range ids {
		if err := add(id, 0); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	items := make([]WorkshopItem, 0, len(order))
	for _, id := range order {
		items = append(items, known[id])
	}
	if len(order) >= maxLookupItems {
		notes = append(notes, fmt.Sprintf("Only the first %d items were added.", maxLookupItems))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "notes": notes})
}

// handleUpdateMods replaces the server's Workshop items, mods and maps.
func (s *Server) handleUpdateMods(w http.ResponseWriter, r *http.Request) {
	in, a, ok := s.instanceAgent(w, r)
	if !ok {
		return
	}
	if _, ok := s.workshopTemplate(w, in); !ok {
		return
	}
	spec, err := s.spec(in)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	var list protocol.ModList
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	for _, l := range []*[]string{&list.WorkshopItems, &list.Mods, &list.Maps} {
		if *l == nil {
			*l = []string{}
		}
	}
	noteFor(r).text = fmt.Sprintf("%d Workshop item(s), %d mod(s)", len(list.WorkshopItems), len(list.Mods))
	relay(w, r, a, protocol.TypeModsWrite, protocol.ModsRequest{InstanceSpec: spec, List: &list})
}
