package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

var (
	validWorkshopID = regexp.MustCompile(`^[0-9]{1,20}$`)
	// Mod IDs and map folders end up in a key=value line split on semicolons.
	validModEntry = regexp.MustCompile(`^[^;=\r\n]{1,200}$`)
)

// modsFile resolves the template's Workshop mods file for the instance.
func (m *Manager) modsFile(spec protocol.InstanceSpec) (*templates.Mods, map[string]string, string, string, error) {
	mods := spec.Template.Mods
	if mods == nil || mods.Kind != "workshop" {
		return nil, nil, "", "", errors.New("this game has no Workshop mods")
	}
	vars, err := templateVars(spec, m.dir(spec.ID))
	if err != nil {
		return nil, nil, "", "", err
	}
	rel, err := templates.Render(mods.File, vars)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("mods file path: %w", err)
	}
	abs, err := m.resolve(spec.ID, rel)
	return mods, vars, rel, abs, err
}

// splitList reads a semicolon list such as "\ModA;\ModB", dropping blanks and
// the prefix before each mod ID (a backslash, or "123\" naming its Workshop item).
func splitList(v string, stripPrefix bool) []string {
	out := []string{}
	for _, s := range strings.Split(v, ";") {
		s = strings.TrimSpace(s)
		if stripPrefix {
			if i := strings.LastIndex(s, `\`); i >= 0 {
				s = s[i+1:]
			}
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (m *Manager) ReadMods(spec protocol.InstanceSpec) (protocol.ModList, error) {
	mods, _, rel, abs, err := m.modsFile(spec)
	if err != nil {
		return protocol.ModList{}, err
	}
	list := protocol.ModList{File: rel, WorkshopItems: []string{}, Mods: []string{}, Maps: []string{}}
	data, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		return list, nil
	}
	if err != nil {
		return list, err
	}
	list.Exists = true
	values := keyValues(string(data))
	list.WorkshopItems = splitList(values[mods.WorkshopKey], false)
	list.Mods = splitList(values[mods.ModKey], true)
	if mods.MapKey != "" {
		list.Maps = splitList(values[mods.MapKey], false)
	}
	return list, nil
}

// WriteMods replaces the mod lists. The game downloads new Workshop items
// itself the next time it starts.
func (m *Manager) WriteMods(req protocol.ModsRequest) error {
	if req.List == nil {
		return errors.New("no mod list given")
	}
	in, err := m.get(req.InstanceSpec)
	if err != nil {
		return err
	}
	m.mu.Lock()
	busy := in.proc != nil || in.busy != "" || in.state == protocol.StateInstalling
	m.mu.Unlock()
	if busy {
		return errors.New("stop the server first: the game rewrites its settings file while it runs, so changes made now would be lost")
	}
	mods, vars, _, abs, err := m.modsFile(req.InstanceSpec)
	if err != nil {
		return err
	}
	l := req.List
	for _, id := range l.WorkshopItems {
		if !validWorkshopID.MatchString(id) {
			return fmt.Errorf("%q isn't a Workshop item ID", id)
		}
	}
	for _, s := range append(append([]string{}, l.Mods...), l.Maps...) {
		if !validModEntry.MatchString(s) || strings.Contains(s, `\`) {
			return fmt.Errorf("%q can't be used as a mod ID or map folder", s)
		}
	}
	prefix, err := templates.Render(mods.ModPrefix, vars)
	if err != nil {
		return fmt.Errorf("mod prefix: %w", err)
	}
	prefixed := make([]string, len(l.Mods))
	for i, id := range l.Mods {
		prefixed[i] = prefix + id
	}
	keys := []string{mods.WorkshopKey, mods.ModKey}
	values := map[string]string{
		mods.WorkshopKey: strings.Join(l.WorkshopItems, ";"),
		mods.ModKey:      strings.Join(prefixed, ";"),
	}
	if mods.MapKey != "" {
		maps := l.Maps
		if len(maps) == 0 && mods.BaseMap != "" {
			maps = []string{mods.BaseMap}
		}
		keys = append(keys, mods.MapKey)
		values[mods.MapKey] = strings.Join(maps, ";")
	}
	existing, err := os.ReadFile(abs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp := abs + ".watchtower"
	if err := os.WriteFile(tmp, []byte(setKeyValues(string(existing), keys, values)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, abs)
}

// keyValues reads key=value lines, skipping comments.
func keyValues(contents string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}
