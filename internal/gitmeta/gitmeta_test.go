package gitmeta

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestCommandHardensArgsAndEnv locks in the exact hardening Command applies:
// the -c flags come before caller args, and the env disables global/system
// config plus interactive credential prompts.
func TestCommandHardensArgsAndEnv(t *testing.T) {
	cmd := Command("/tmp", "status", "--porcelain")
	want := []string{
		"git",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "protocol.ext.allow=never",
		"-c", "diff.external=",
		"-c", "core.quotepath=false",
		"-c", "diff.noprefix=false",
		"-c", "diff.mnemonicPrefix=false",
		"status", "--porcelain",
	}
	if strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	for _, wantEnv := range []string{
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	} {
		found := false
		for _, e := range cmd.Env {
			if e == wantEnv {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("env missing %q, got %v", wantEnv, cmd.Env)
		}
	}
}

// TestCommandIgnoresRepoLocalHooksPath is the CVE-2022-24765 class
// regression test: a mounted/untrusted repo's local core.hooksPath must not
// be honored. It points core.hooksPath at a hook that would prove it ran by
// touching a marker file, then confirms a hardened commit never runs it.
func TestCommandIgnoresRepoLocalHooksPath(t *testing.T) {
	dir := initRepo(t)

	hooksDir := filepath.Join(dir, "evil-hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "pwned")
	hookScript := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hookScript), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := exec.Command("git", "config", "--local", "core.hooksPath", hooksDir)
	cfg.Dir = dir
	if out, err := cfg.CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath: %v %s", err, out)
	}

	// A hardened commit must still work (git calls aren't broken by the
	// hardening)...
	cmd := Command(dir, "commit", "--allow-empty", "-q", "-m", "trigger hook")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hardened commit failed: %v %s", err, out)
	}

	// ...but the malicious repo-local hook must never have run.
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("malicious pre-commit hook ran despite core.hooksPath override")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
