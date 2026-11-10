package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitExec runs git directly (not through gitmeta.Command) purely to build
// the fixture repo — the hardening in gitmeta.Command is about defending
// against an untrusted repo under test, not about setting one up.
func gitExec(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// initWhatChangedRepo builds a repo with a main branch (one Go package plus
// one migration) and a feature branch that adds a new package, adds a new
// migration, and modifies the existing file — the fixture
// TestWhatChanged/TestWhatChangedDegraded exercise.
func initWhatChangedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitExec(t, dir, "init", "-q", "-b", "main")
	gitExec(t, dir, "config", "user.email", "t@t")
	gitExec(t, dir, "config", "user.name", "t")

	mustWrite(t, filepath.Join(dir, "pkg", "widget", "widget.go"),
		"package widget\n\nfunc Name() string { return \"widget\" }\n")
	mustWrite(t, filepath.Join(dir, "migrations", "0001_init.sql"),
		"CREATE TABLE widgets (id SERIAL PRIMARY KEY);\n")
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-q", "-m", "init")

	gitExec(t, dir, "checkout", "-q", "-b", "feature")
	mustWrite(t, filepath.Join(dir, "pkg", "gadget", "gadget.go"),
		"package gadget\n\nfunc Name() string { return \"gadget\" }\n")
	mustWrite(t, filepath.Join(dir, "migrations", "0002_add_gadgets.sql"),
		"CREATE TABLE gadgets (id SERIAL PRIMARY KEY);\n")
	mustWrite(t, filepath.Join(dir, "pkg", "widget", "widget.go"),
		"package widget\n\nfunc Name() string { return \"widget-v2\" }\n")
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-q", "-m", "add gadget package + migration")

	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWhatChanged(t *testing.T) {
	repo := initWhatChangedRepo(t)

	md, err := WhatChanged(repo, "main")
	if err != nil {
		t.Fatalf("WhatChanged: %v", err)
	}

	if !strings.Contains(md, "# What changed") {
		t.Fatalf("digest missing title:\n%s", md)
	}
	if !strings.Contains(md, "pkg/gadget") {
		t.Fatalf("digest doesn't name the new changed package pkg/gadget:\n%s", md)
	}
	if !strings.Contains(md, "migrations/0002_add_gadgets.sql") {
		t.Fatalf("digest doesn't flag the new migration:\n%s", md)
	}
	if !strings.Contains(md, "Schema changes") {
		t.Fatalf("digest missing schema-changes heading:\n%s", md)
	}
	// Files changed (incl. uncommitted) must be nonzero: widget.go modified,
	// gadget.go + its migration added, widget's migration untouched.
	if !strings.Contains(md, "| Files changed (incl. uncommitted) |") {
		t.Fatalf("digest missing file-count row:\n%s", md)
	}
	if strings.Contains(md, "| Files changed (incl. uncommitted) | 0 |") {
		t.Fatalf("digest reports zero files changed, expected nonzero:\n%s", md)
	}
}

func TestWhatChangedDegradedBase(t *testing.T) {
	repo := initWhatChangedRepo(t)

	md, err := WhatChanged(repo, "nonexistent-branch")
	if err != nil {
		t.Fatalf("WhatChanged with unresolvable base: unexpected error: %v", err)
	}
	if !strings.Contains(md, "# What changed") {
		t.Fatalf("degraded digest missing title:\n%s", md)
	}
	lower := strings.ToLower(md)
	if !strings.Contains(lower, "not found") && !strings.Contains(lower, "no common history") {
		t.Fatalf("degraded digest doesn't explain the missing base:\n%s", md)
	}
}
