package gen

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v %s", args, err, out)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestChanges(t *testing.T) {
	// NOTE: deviates from the plan's literal test body — the plan commits
	// "base" then "add new" both directly on main, so merge-base(main, HEAD)
	// == HEAD and the diff is empty by construction. The plan's own prose
	// ("a new file on a branch") implies a real branch, so we create one here
	// to actually exercise the merge-base diff.
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")
	run(t, dir, "commit", "--allow-empty", "-q", "-m", "base")
	run(t, dir, "checkout", "-q", "-b", "feature")
	if err := writeFile(dir+"/new.go", "package x\n"); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "add new")
	md, err := Changes(dir, "main")
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	if !strings.Contains(md, "new.go") {
		t.Fatalf("missing file in: %s", md)
	}
	if !strings.Contains(md, "main") {
		t.Fatalf("missing branch/base reference in: %s", md)
	}
}

func TestChangesNoMergeBase(t *testing.T) {
	// A repo where "main" doesn't exist at all: no merge-base. Changes must
	// not error — it should note the absence and fall back gracefully.
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "trunk")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")
	run(t, dir, "commit", "--allow-empty", "-q", "-m", "init")
	md, err := Changes(dir, "main")
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	if !strings.Contains(strings.ToLower(md), "no merge-base") {
		t.Fatalf("expected a note about missing merge-base, got: %s", md)
	}
}
