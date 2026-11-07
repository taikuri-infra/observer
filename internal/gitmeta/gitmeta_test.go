package gitmeta

import (
	"os/exec"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestHeadAndBranch(t *testing.T) {
	dir := initRepo(t)
	m, err := Collect(dir, "main")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(m.Head) < 7 {
		t.Fatalf("head = %q", m.Head)
	}
	if m.Branch != "main" {
		t.Fatalf("branch = %q", m.Branch)
	}
	if m.Dirty {
		t.Fatalf("expected clean")
	}
}
