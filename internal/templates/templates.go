// Package templates loads game templates. A template describes how to install,
// configure, start, and read stats from one game, so adding a game needs no code.
package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Key      string `yaml:"key" json:"key"`
	Label    string `yaml:"label" json:"label"`
	Type     string `yaml:"type" json:"type"` // string, int, bool, select
	Default  string `yaml:"default" json:"default"`
	File     string `yaml:"file,omitempty" json:"file,omitempty"`         // config file the value is written to (milestone 3)
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"` // bools must be "true", others non-empty
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
	Stop        string            `yaml:"stop" json:"stop"` // console command for a clean shutdown
	Ports       []Port            `yaml:"ports" json:"ports"`
	Config      []ConfigField     `yaml:"config" json:"config"`
	Stats       Stats             `yaml:"stats" json:"stats"`
}

type Install struct {
	Method      string `yaml:"method" json:"method"` // steamcmd, mojang, or download
	Version     string `yaml:"version,omitempty" json:"version,omitempty"`
	SteamAppID  int    `yaml:"steamAppId,omitempty" json:"steamAppId,omitempty"`
	DownloadURL string `yaml:"downloadUrl,omitempty" json:"downloadUrl,omitempty"`
}

// Stats says how the agent reads player counts and similar figures.
type Stats struct {
	Method string `yaml:"method" json:"method"` // rcon, query, or log
	Port   string `yaml:"port" json:"port"`     // name of a port above
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
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Settings returns the template's config defaults overlaid with the given values.
func (t Template) Settings(values map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range t.Config {
		out[f.Key] = f.Default
	}
	for k, v := range values {
		out[k] = v
	}
	return out
}

// Render fills {{.key}} placeholders in s from vars. A missing key is an error,
// so a typo in a template fails loudly instead of starting a broken server.
func Render(s string, vars map[string]string) (string, error) {
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

// Validate checks settings against the template's required fields.
func (t Template) Validate(settings map[string]string) error {
	for _, f := range t.Config {
		if !f.Required {
			continue
		}
		v := settings[f.Key]
		if (f.Type == "bool" && v != "true") || v == "" {
			return fmt.Errorf("%s is required", f.Label)
		}
	}
	return nil
}
