package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverviewNoIndex(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")
	run(t, dir, "commit", "--allow-empty", "-q", "-m", "init")

	md, err := Overview(dir, "main")
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if !strings.Contains(md, "main") {
		t.Fatalf("missing branch: %s", md)
	}
	if !strings.Contains(strings.ToLower(md), "no tabs") {
		t.Fatalf("expected a note about no tabs yet: %s", md)
	}
}

func TestOverviewWithIndex(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")
	run(t, dir, "commit", "--allow-empty", "-q", "-m", "init")

	if err := os.MkdirAll(filepath.Join(dir, "obs-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	indexJSON := `{"tabs":{"Changes":{"tab":"Changes","path":"obs-data/changes.md","renderer":"markdown","generated_at":"2026-11-08T00:00:00Z","head":"abcdef1234","branch":"main"}}}`
	if err := os.WriteFile(filepath.Join(dir, "obs-data", "_index.json"), []byte(indexJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	md, err := Overview(dir, "main")
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if !strings.Contains(md, "Changes") {
		t.Fatalf("missing tab name in overview: %s", md)
	}
	if !strings.Contains(md, "markdown") {
		t.Fatalf("missing renderer in overview: %s", md)
	}
}
