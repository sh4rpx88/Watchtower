// Package templates loads game templates. A template describes how to install,
// configure, start, and read stats from one game, so adding a game needs no code.
package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

type Port struct {
	Name     string `yaml:"name" json:"name"`
	Port     int    `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol" json:"protocol"` // tcp or udp
}

type ConfigField struct {
	Key      string   `yaml:"key" json:"key"`
	Label    string   `yaml:"label" json:"label"`
	Type     string   `yaml:"type" json:"type"` // string, int, bool, select
	Default  string   `yaml:"default" json:"default"`
	Options  []string `yaml:"options,omitempty" json:"options,omitempty"`   // choices for select
	Choices  []Choice `yaml:"choices,omitempty" json:"choices,omitempty"`   // choices for select with labels; used instead of Options
	Help     string   `yaml:"help,omitempty" json:"help,omitempty"`         // shown under the field
	File     string   `yaml:"file,omitempty" json:"file,omitempty"`         // key=value config file the value is written to before each start
	Required bool     `yaml:"required,omitempty" json:"required,omitempty"` // bools must be "true", others non-empty
}

type Choice struct {
	Value string `yaml:"value" json:"value"`
	Label string `yaml:"label" json:"label"`
}

// GameConfig is a settings file the game writes itself, such as Project
// Zomboid's server .ini and SandboxVars.lua. The panel reads every option
// from it, with the descriptions, ranges and choices in the file's comments,
// so the editor always matches the installed game version.
type GameConfig struct {
	Label  string            `yaml:"label" json:"label"`
	Path   string            `yaml:"path" json:"path"`     // inside the server folder; may use settings like {{.servername}}
	Format string            `yaml:"format" json:"format"` // ini (key=value) or lua (nested tables)
	Help   string            `yaml:"help,omitempty" json:"help,omitempty"`
	Groups []ConfigGroup     `yaml:"groups,omitempty" json:"groups,omitempty"` // sorts top-level keys into sections, first match wins
	Tables map[string]string `yaml:"tables,omitempty" json:"tables,omitempty"` // section names for lua tables
	Locked map[string]string `yaml:"locked,omitempty" json:"locked,omitempty"` // keys Watchtower manages, with the reason shown
}

type ConfigGroup struct {
	Name  string `yaml:"name" json:"name"`
	Match string `yaml:"match" json:"match"` // regular expression on the key
}

type Template struct {
	ID          string            `yaml:"id" json:"id"`
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description" json:"description"`
	Image       string            `yaml:"image" json:"image"` // Docker image the instance runs in
	Install     Install           `yaml:"install" json:"install"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Files       map[string]string `yaml:"files,omitempty" json:"files,omitempty"` // path -> contents, written before each start
	Start       string            `yaml:"start" json:"start"`
	Stop        string            `yaml:"stop" json:"stop"`                     // console command for a clean shutdown
	Save        string            `yaml:"save,omitempty" json:"save,omitempty"` // console command that flushes the world to disk, sent before backups
	Say         string            `yaml:"say,omitempty" json:"say,omitempty"`   // console command that broadcasts {{.message}} to players, for restart warnings
	Ports       []Port            `yaml:"ports" json:"ports"`
	Config      []ConfigField     `yaml:"config" json:"config"`
	GameConfig  []GameConfig      `yaml:"gameConfig,omitempty" json:"gameConfig,omitempty"`
	Stats       Stats             `yaml:"stats" json:"stats"`
}

type Install struct {
	Method      string `yaml:"method" json:"method"` // steamcmd, mojang, or download
	Version     string `yaml:"version,omitempty" json:"version,omitempty"`
	SteamAppID  int    `yaml:"steamAppId,omitempty" json:"steamAppId,omitempty"`
	SteamBeta   string `yaml:"steamBeta,omitempty" json:"steamBeta,omitempty"` // Steam branch, may use settings like {{.branch}}; empty or "public" is the default
	DownloadURL string `yaml:"downloadUrl,omitempty" json:"downloadUrl,omitempty"`
}

// Stats says how the panel follows players and readiness from console output.
// Join and Leave must capture the player's name in a group named "player".
type Stats struct {
	Ready      string `yaml:"ready,omitempty" json:"ready,omitempty"`           // line that means the server is accepting players
	Join       string `yaml:"join,omitempty" json:"join,omitempty"`             // line that means a player joined
	Leave      string `yaml:"leave,omitempty" json:"leave,omitempty"`           // line that means a player left
	MaxPlayers string `yaml:"maxPlayers,omitempty" json:"maxPlayers,omitempty"` // setting key holding the player limit
}

// LoadDir reads every *.yaml file in dir, sorted by ID.
func LoadDir(dir string) ([]Template, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	var out []Template
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var t Template
		if err := yaml.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if t.ID == "" {
			return nil, fmt.Errorf("%s: missing id", f)
		}
		for _, gc := range t.GameConfig {
			if gc.Format != "ini" && gc.Format != "lua" {
				return nil, fmt.Errorf("%s: game config %s: format must be ini or lua", f, gc.Path)
			}
			for _, g := range gc.Groups {
				if _, err := regexp.Compile(g.Match); err != nil {
					return nil, fmt.Errorf("%s: group %s: %w", f, g.Name, err)
				}
			}
		}
		for _, re := range []string{t.Stats.Ready, t.Stats.Join, t.Stats.Leave} {
			if _, err := regexp.Compile(re); err != nil {
				return nil, fmt.Errorf("%s: stats pattern %q: %w", f, re, err)
			}
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Settings returns the template's config defaults overlaid with the given
// values. Keys the template doesn't define are dropped.
func (t Template) Settings(values map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range t.Config {
		out[f.Key] = f.Default
		if v, ok := values[f.Key]; ok {
			out[f.Key] = v
		}
	}
	return out
}

// Render fills {{.key}} placeholders in s from vars. A missing key is an error,
// so a typo in a template fails loudly instead of starting a broken server.
func Render(s string, vars map[string]string) (string, error) {
	return render(s, vars)
}

// RenderShell is Render for shell command lines: each value is single-quoted,
// so spaces and symbols in settings can't split arguments or run commands.
// -Xmx{{.memory}} becomes -Xmx'2G', which the shell reads as -Xmx2G.
func RenderShell(s string, vars map[string]string) (string, error) {
	quoted := make(map[string]string, len(vars))
	for k, v := range vars {
		quoted[k] = "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
	}
	return render(s, quoted)
}

func render(s string, vars map[string]string) (string, error) {
	tpl, err := template.New("").Option("missingkey=error").Parse(s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tpl.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Validate checks settings against each field's type and the required fields.
func (t Template) Validate(settings map[string]string) error {
	for _, f := range t.Config {
		v := settings[f.Key]
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%s can't contain line breaks", f.Label)
		}
		switch f.Type {
		case "bool":
			if v != "true" && v != "false" {
				return fmt.Errorf("%s must be true or false", f.Label)
			}
		case "int":
			if _, err := strconv.Atoi(v); err != nil && v != "" {
				return fmt.Errorf("%s must be a whole number", f.Label)
			}
		case "select":
			options := slices.Clone(f.Options)
			for _, c := range f.Choices {
				options = append(options, c.Value)
			}
			if v != "" && len(options) > 0 && !slices.Contains(options, v) {
				return fmt.Errorf("%s must be one of %s", f.Label, strings.Join(options, ", "))
			}
		}
		if f.Required && (v == "" || (f.Type == "bool" && v != "true")) {
			return fmt.Errorf("%s is required", f.Label)
		}
	}
	return nil
}
