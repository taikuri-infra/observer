// Package manifest defines the per-repo observer.yaml schema (the tabs/plugins).
package manifest

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Kind string

const (
	KindGenerator Kind = "generator" // command -> artifact file -> rendered inline
	KindService   Kind = "service"   // v2: start a sidecar, iframe its URL
	KindLink      Kind = "link"      // v2: external deep link
)

// Tab is one entry in observer.yaml — one tab in the dashboard.
type Tab struct {
	Name     string   `yaml:"name" json:"name"`
	Kind     Kind     `yaml:"kind" json:"kind"`
	Command  string   `yaml:"command" json:"command,omitempty"`             // generator
	Output   string   `yaml:"output" json:"output,omitempty"`               // generator: file under obs-data/
	Dir      string   `yaml:"dir,omitempty" json:"dir,omitempty"`           // generator: subdir (relative to repo) to run the command in
	Renderer string   `yaml:"renderer" json:"renderer,omitempty"`           // markdown|svg|mermaid|dbml|graph|table
	Requires []string `yaml:"requires,omitempty" json:"requires,omitempty"` // external tools needed
	URL      string   `yaml:"url,omitempty" json:"url,omitempty"`           // service/link
	Start    string   `yaml:"start,omitempty" json:"start,omitempty"`       // service
}

// Project is the top-level observer.yaml document.
type Project struct {
	Project string `yaml:"project" json:"project"`
	Tabs    []Tab  `yaml:"tabs" json:"tabs"`
}

// Parse decodes and validates an observer.yaml document.
func Parse(b []byte) (*Project, error) {
	var p Project
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	for i, t := range p.Tabs {
		switch t.Kind {
		case KindGenerator, KindService, KindLink:
		default:
			return nil, fmt.Errorf("tab %d (%q): unknown kind %q", i, t.Name, t.Kind)
		}
		if t.Name == "" {
			return nil, fmt.Errorf("tab %d: name is required", i)
		}
		if err := safeRelPath(t.Output); err != nil {
			return nil, fmt.Errorf("tab %d (%q): output: %w", i, t.Name, err)
		}
		if err := safeRelPath(t.Dir); err != nil {
			return nil, fmt.Errorf("tab %d (%q): dir: %w", i, t.Name, err)
		}
	}
	return &p, nil
}

// safeRelPath rejects empty-ok, but otherwise absolute or path-escaping
// values. An empty string is always allowed (field not set).
func safeRelPath(p string) error {
	if p == "" {
		return nil
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("must be a relative path, got %q", p)
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("must not escape the repo (contains ..): %q", p)
	}
	return nil
}
