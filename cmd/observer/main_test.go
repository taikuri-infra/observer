package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/taikuri-infra/observer/internal/manifest"
)

func TestWriteManifestIfAbsentWritesDefaultTabs(t *testing.T) {
	repo := t.TempDir()
	// Give the fake repo a go.mod so DefaultTabs has something to detect.
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "observer.yaml")

	wrote, tabs, err := writeManifestIfAbsent(repo, manifestPath)
	if err != nil {
		t.Fatalf("writeManifestIfAbsent: %v", err)
	}
	if !wrote {
		t.Fatal("expected wrote=true on first call")
	}
	if len(tabs) == 0 {
		t.Fatal("expected at least the always-on tabs (Overview, Changes, Hotspots)")
	}

	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read written manifest: %v", err)
	}
	proj, err := manifest.Parse(b)
	if err != nil {
		t.Fatalf("parse written manifest: %v", err)
	}
	if len(proj.Tabs) != len(tabs) {
		t.Fatalf("written manifest has %d tabs, DefaultTabs returned %d", len(proj.Tabs), len(tabs))
	}
}

func TestWriteManifestIfAbsentIsIdempotent(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "observer.yaml")
	const sentinel = "project: hand-written\ntabs: []\n"
	if err := os.WriteFile(manifestPath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	wrote, _, err := writeManifestIfAbsent(repo, manifestPath)
	if err != nil {
		t.Fatalf("writeManifestIfAbsent: %v", err)
	}
	if wrote {
		t.Fatal("must not clobber an existing manifest")
	}

	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != sentinel {
		t.Fatalf("existing manifest content changed: %s", b)
	}
}

func TestPrintDoctorReportListsRequiredTools(t *testing.T) {
	tabs := []manifest.Tab{
		{Name: "A", Requires: []string{"definitely-not-a-real-tool-xyz"}},
		{Name: "B", Requires: []string{"git"}}, // git is always on PATH in this test env
	}
	var buf bytes.Buffer
	if err := printDoctorReport(&buf, tabs); err != nil {
		t.Fatalf("printDoctorReport: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "git: ok") {
		t.Fatalf("missing baseline git check: %s", out)
	}
	if !strings.Contains(out, "definitely-not-a-real-tool-xyz: NOT FOUND") {
		t.Fatalf("missing not-found report for a missing tool: %s", out)
	}
}
