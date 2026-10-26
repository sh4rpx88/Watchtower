package agent

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// Game config files are settings files the game writes itself on first start,
// with a comment above each option. Reading the comments gives the editor
// every option the installed version has, with its range and choices, and
// writing changes only the value on each line keeps everything else as is.

type gameSetting struct {
	protocol.GameSetting
	table  string // lua: dotted path of the enclosing table, "" at the top
	line   int    // index into gameConfigDoc.lines
	indent string // lua: leading whitespace
	suffix string // lua: what follows the value, such as "," or ", -- note"
}

type gameConfigDoc struct {
	format   string
	lines    []string
	settings []gameSetting
}

var (
	choiceComment  = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)\s*=\s*(.+)$`)
	minComment     = regexp.MustCompile(`(?i)\bmin(?:imum)?\s*[=:]\s*(-?\d+(?:\.\d+)?)`)
	maxComment     = regexp.MustCompile(`(?i)\bmax(?:imum)?\s*[=:]\s*(-?\d+(?:\.\d+)?)`)
	defaultComment = regexp.MustCompile(`(?i)\bdefault\s*[=:]\s*(.*)$`)
	luaTableStart  = regexp.MustCompile(`^\s*([A-Za-z_]\w*)\s*=\s*\{\s*(--.*)?$`)
	luaTableEnd    = regexp.MustCompile(`^\s*\}\s*,?\s*(--.*)?$`)
	luaAssign      = regexp.MustCompile(`^(\s*)([A-Za-z_]\w*)\s*=\s*(.*)$`)
)

func parseGameConfig(format, text string) *gameConfigDoc {
	doc := &gameConfigDoc{format: format, lines: strings.Split(strings.TrimRight(text, "\n"), "\n")}
	if text == "" {
		doc.lines = nil
	}
	var comments []string
	var tables []string // lua table stack; the first is the root, such as SandboxVars
	for i, line := range doc.lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			comments = nil
			continue
		}
		if format == "ini" {
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
				comments = append(comments, strings.TrimSpace(strings.TrimLeft(trimmed, "#;")))
				continue
			}
			key, value, ok := strings.Cut(trimmed, "=")
			if !ok || strings.HasPrefix(trimmed, "[") {
				comments = nil
				continue
			}
			s := gameSetting{line: i}
			s.Key, s.Value = strings.TrimSpace(key), strings.TrimSpace(value)
			describe(&s.GameSetting, comments)
			inferType(&s.GameSetting, false, true)
			doc.settings = append(doc.settings, s)
			comments = nil
			continue
		}
		// lua
		switch {
		case strings.HasPrefix(trimmed, "--"):
			comments = append(comments, strings.TrimSpace(strings.TrimPrefix(trimmed, "--")))
			continue
		case luaTableStart.MatchString(line):
			tables = append(tables, luaTableStart.FindStringSubmatch(line)[1])
		case luaTableEnd.MatchString(line):
			if len(tables) > 0 {
				tables = tables[:len(tables)-1]
			}
		default:
			if m := luaAssign.FindStringSubmatch(line); m != nil {
				value, quoted, suffix, ok := splitLuaValue(m[3])
				if ok {
					s := gameSetting{line: i, indent: m[1], suffix: suffix}
					if len(tables) > 1 {
						s.table = strings.Join(tables[1:], ".")
					}
					s.Key, s.Value = m[2], value
					if s.table != "" {
						s.Key = s.table + "." + m[2]
					}
					describe(&s.GameSetting, comments)
					inferType(&s.GameSetting, quoted, false)
					doc.settings = append(doc.settings, s)
				}
			}
		}
		comments = nil
	}
	return doc
}

// splitLuaValue reads a number, boolean or string at the start of rest. It
// skips anything else, such as inline tables.
func splitLuaValue(rest string) (value string, quoted bool, suffix string, ok bool) {
	if strings.HasPrefix(rest, `"`) {
		var b strings.Builder
		for i := 1; i < len(rest); i++ {
			switch c := rest[i]; {
			case c == '\\' && i+1 < len(rest):
				i++
				b.WriteByte(rest[i])
			case c == '"':
				return b.String(), true, rest[i+1:], true
			default:
				b.WriteByte(c)
			}
		}
		return "", false, "", false
	}
	end := len(rest)
	if i := strings.Index(rest, ","); i >= 0 {
		end = i
	}
	if i := strings.Index(rest, "--"); i >= 0 && i < end {
		end = i
	}
	value = strings.TrimSpace(rest[:end])
	if value == "" || strings.ContainsAny(value, "{}\"'") {
		return "", false, "", false
	}
	return value, false, rest[len(rest[:end]):], true
}

// describe reads the comment lines above a setting: "1 = Insane" lines are
// choices, Minimum/Maximum/Default give the range, and the rest describes it.
func describe(s *protocol.GameSetting, comments []string) {
	var desc []string
	for _, c := range comments {
		if idx := choiceStart.FindAllStringIndex(c, -1); choiceComment.MatchString(c) || len(idx) >= 2 {
			// "1 = Insane" on its own line, or "Shown to: 1=Hidden 2=Friends" inline.
			s.Choices = append(s.Choices, splitChoices(c[idx[0][0]:])...)
			if c = strings.TrimSpace(c[:idx[0][0]]); c != "" {
				desc = append(desc, c)
			}
			continue
		}
		if m := minComment.FindStringSubmatch(c); m != nil {
			s.Min = parseNumber(m[1])
			c = minComment.ReplaceAllString(c, "")
		}
		if m := maxComment.FindStringSubmatch(c); m != nil {
			s.Max = parseNumber(m[1])
			c = maxComment.ReplaceAllString(c, "")
		}
		if m := defaultComment.FindStringSubmatch(c); m != nil {
			s.Default = strings.TrimSpace(m[1])
			c = defaultComment.ReplaceAllString(c, "")
		}
		if c = strings.TrimSpace(c); c != "" {
			desc = append(desc, c)
		}
	}
	s.Description = strings.Join(desc, " ")
}

var choiceStart = regexp.MustCompile(`(?:^|\s)(-?\d+(?:\.\d+)?)\s*=\s*`)

// splitChoices reads "1 = Insane", or several on one line like "1=Hidden 2=Friends".
func splitChoices(line string) []templates.Choice {
	idx := choiceStart.FindAllStringSubmatchIndex(line, -1)
	var out []templates.Choice
	for i, m := range idx {
		end := len(line)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out = append(out, templates.Choice{Value: line[m[2]:m[3]], Label: strings.TrimSpace(line[m[1]:end])})
	}
	return out
}

func parseNumber(v string) *float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

// inferType picks the editor widget. With strict (ini files, where every
// value is text) a value only counts as a number when its comment gives a
// range or a numeric default, so a password that happens to be digits stays text.
func inferType(s *protocol.GameSetting, quoted, strict bool) {
	isBool := func(v string) bool { return strings.EqualFold(v, "true") || strings.EqualFold(v, "false") }
	_, valueNum := strconv.ParseFloat(s.Value, 64)
	numeric := valueNum == nil && (!strict || s.Min != nil || s.Max != nil || parseNumber(s.Default) != nil)
	switch {
	case quoted:
		s.Type = "string"
	case len(s.Choices) > 0:
		s.Type = "select"
	case isBool(s.Value):
		s.Type = "bool"
	case numeric && !strings.Contains(s.Value, ".") && !strings.Contains(s.Default, "."):
		s.Type = "int"
	case numeric:
		s.Type = "number"
	default:
		s.Type = "string"
	}
}

// set validates and applies values, keyed like GameSetting.Key.
func (d *gameConfigDoc) set(values map[string]string, locked map[string]string) error {
	byKey := map[string]*gameSetting{}
	for i := range d.settings {
		byKey[d.settings[i].Key] = &d.settings[i]
	}
	for key, v := range values {
		s, ok := byKey[key]
		if !ok {
			return fmt.Errorf("%s isn't a setting in this file", key)
		}
		if reason := locked[key]; reason != "" {
			return fmt.Errorf("%s can't be changed here: %s", key, reason)
		}
		if err := checkValue(s.GameSetting, v); err != nil {
			return err
		}
	}
	for key, v := range values {
		s := byKey[key]
		if s.Type == "bool" {
			v = strings.ToLower(v)
		}
		s.Value = v
		if d.format == "ini" {
			d.lines[s.line] = strings.SplitN(strings.TrimSpace(d.lines[s.line]), "=", 2)[0] + "=" + v
			continue
		}
		if s.Type == "string" {
			v = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
		}
		name := key[strings.LastIndex(key, ".")+1:]
		d.lines[s.line] = s.indent + name + " = " + v + s.suffix
	}
	return nil
}

func checkValue(s protocol.GameSetting, v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("%s can't contain line breaks", s.Key)
	}
	switch s.Type {
	case "bool":
		if !strings.EqualFold(v, "true") && !strings.EqualFold(v, "false") {
			return fmt.Errorf("%s must be true or false", s.Key)
		}
	case "select":
		for _, c := range s.Choices {
			if c.Value == v {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of the listed choices", s.Key)
	case "int", "number":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || (s.Type == "int" && strings.ContainsAny(v, ".eE")) {
			return fmt.Errorf("%s must be a number", s.Key)
		}
		if s.Min != nil && f < *s.Min || s.Max != nil && f > *s.Max {
			return fmt.Errorf("%s must be between %s and %s", s.Key, formatBound(s.Min), formatBound(s.Max))
		}
	}
	return nil
}

func formatBound(f *float64) string {
	if f == nil {
		return "any"
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

func (d *gameConfigDoc) String() string {
	if len(d.lines) == 0 {
		return ""
	}
	return strings.Join(d.lines, "\n") + "\n"
}

// sections sorts settings into the template's groups, then one section per
// lua table, then everything else.
func (d *gameConfigDoc) sections(gc templates.GameConfig, locked map[string]string) []protocol.GameConfigGroup {
	groups := make([]protocol.GameConfigGroup, len(gc.Groups))
	matchers := make([]*regexp.Regexp, len(gc.Groups))
	for i, g := range gc.Groups {
		groups[i].Name = g.Name
		matchers[i] = regexp.MustCompile(g.Match) // checked when the template loaded
	}
	tableIndex := map[string]int{}
	var tables []protocol.GameConfigGroup
	other := protocol.GameConfigGroup{Name: "Other settings"}
	for _, s := range d.settings {
		gs := s.GameSetting
		gs.Locked = locked[gs.Key]
		if s.table != "" {
			i, ok := tableIndex[s.table]
			if !ok {
				name := gc.Tables[s.table]
				if name == "" {
					name = humanize(s.table)
				}
				i = len(tables)
				tableIndex[s.table] = i
				tables = append(tables, protocol.GameConfigGroup{Name: name})
			}
			tables[i].Settings = append(tables[i].Settings, gs)
			continue
		}
		placed := false
		for i, m := range matchers {
			if m.MatchString(gs.Key) {
				groups[i].Settings = append(groups[i].Settings, gs)
				placed = true
				break
			}
		}
		if !placed {
			other.Settings = append(other.Settings, gs)
		}
	}
	var out []protocol.GameConfigGroup
	for _, g := range append(append(groups, tables...), other) {
		if len(g.Settings) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// humanize turns "ZombieLore.Speed" into "Zombie lore speed".
func humanize(s string) string {
	var b strings.Builder
	for i, r := range strings.ReplaceAll(s, ".", " ") {
		if i > 0 && unicode.IsUpper(r) && !strings.HasSuffix(b.String(), " ") {
			b.WriteRune(' ')
		}
		if i > 0 {
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// gameConfigFile resolves the template's game config file at index.
func (m *Manager) gameConfigFile(spec protocol.InstanceSpec, index int) (templates.GameConfig, string, string, error) {
	files := spec.Template.GameConfig
	if index < 0 || index >= len(files) {
		return templates.GameConfig{}, "", "", errors.New("no such settings file")
	}
	gc := files[index]
	vars, err := templateVars(spec, m.dir(spec.ID))
	if err != nil {
		return gc, "", "", err
	}
	rel, err := templates.Render(gc.Path, vars)
	if err != nil {
		return gc, "", "", fmt.Errorf("settings file path: %w", err)
	}
	abs, err := m.resolve(spec.ID, rel)
	return gc, rel, abs, err
}

// lockedKeys are the keys the editor won't change: those the template locks,
// and those a template setting writes before each start.
func lockedKeys(spec protocol.InstanceSpec, gc templates.GameConfig, rel string) map[string]string {
	locked := map[string]string{}
	for k, v := range gc.Locked {
		locked[k] = v
	}
	for _, f := range spec.Template.Config {
		if f.File == "" {
			continue
		}
		if name, err := templates.Render(f.File, spec.Settings); err == nil && strings.TrimPrefix(name, "/") == strings.TrimPrefix(rel, "/") {
			locked[f.Key] = "Change this in Setup above (" + f.Label + ")"
		}
	}
	return locked
}

func (m *Manager) ReadGameConfig(spec protocol.InstanceSpec) ([]protocol.GameConfigFile, error) {
	out := []protocol.GameConfigFile{}
	for i := range spec.Template.GameConfig {
		gc, rel, abs, err := m.gameConfigFile(spec, i)
		if err != nil {
			return nil, err
		}
		f := protocol.GameConfigFile{Label: gc.Label, Path: rel, Help: gc.Help, Sections: []protocol.GameConfigGroup{}}
		data, err := os.ReadFile(abs)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			return nil, err
		case len(data) > maxFileSize:
			return nil, fmt.Errorf("%s is too big to edit here", rel)
		default:
			f.Exists = true
			f.Sections = parseGameConfig(gc.Format, string(data)).sections(gc, lockedKeys(spec, gc, rel))
		}
		out = append(out, f)
	}
	return out, nil
}

func (m *Manager) WriteGameConfig(req protocol.GameConfigRequest) error {
	in, err := m.get(req.InstanceSpec)
	if err != nil {
		return err
	}
	m.mu.Lock()
	busy := in.proc != nil || in.busy != "" || in.state == protocol.StateInstalling
	m.mu.Unlock()
	if busy {
		return errors.New("stop the server first: the game rewrites this file while it runs, so changes made now would be lost")
	}
	gc, rel, abs, err := m.gameConfigFile(req.InstanceSpec, req.File)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		return errors.New("start the server once so the game creates this file, then stop it and change settings here")
	}
	if err != nil {
		return err
	}
	doc := parseGameConfig(gc.Format, string(data))
	if err := doc.set(req.Values, lockedKeys(req.InstanceSpec, gc, rel)); err != nil {
		return err
	}
	tmp := abs + ".watchtower"
	if err := os.WriteFile(tmp, []byte(doc.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, abs)
}
