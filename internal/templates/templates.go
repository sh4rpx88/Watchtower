// Package templates loads game templates. A template describes how to install,
// configure, start, and read stats from one game, so adding a game needs no code.
package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

type Port struct {
	Name     string `yaml:"name" json:"name"`
	Port     int    `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol" json:"protocol"` // tcp or udp
}

type ConfigField struct {
	Key     string `yaml:"key" json:"key"`
	Label   string `yaml:"label" json:"label"`
	Type    string `yaml:"type" json:"type"` // string, int, bool, select
	Default string `yaml:"default" json:"default"`
	File    string `yaml:"file" json:"file"` // config file the value is written to
}

type Template struct {
	ID          string        `yaml:"id" json:"id"`
	Name        string        `yaml:"name" json:"name"`
	Description string        `yaml:"description" json:"description"`
	Image       string        `yaml:"image" json:"image"` // Docker image the instance runs in
	Install     Install       `yaml:"install" json:"install"`
	Start       string        `yaml:"start" json:"start"`
	Stop        string        `yaml:"stop" json:"stop"` // console command for a clean shutdown
	Ports       []Port        `yaml:"ports" json:"ports"`
	Config      []ConfigField `yaml:"config" json:"config"`
	Stats       Stats         `yaml:"stats" json:"stats"`
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
