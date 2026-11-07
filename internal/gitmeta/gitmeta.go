// Package gitmeta reports repo freshness: branch, HEAD, dirty, and the
// merge-base against a base branch (for the "changes since" view).
package gitmeta

import (
	"os"
	"os/exec"
	"strings"
)

// Meta holds the git-derived freshness facts for a repo at a point in time.
type Meta struct {
	Branch    string
	Head      string
	MergeBase string // empty if no common ancestor with base was found
	Dirty     bool
}

// Command builds a hardened *exec.Cmd for running git against dir. Observer
// may analyze untrusted, mounted repos, and a plain `git` invocation trusts
// that repo's local config and hooks — a malicious core.hooksPath,
// core.fsmonitor, core.sshCommand, core.pager, or alias can execute
// arbitrary code on an otherwise read-only command (CVE-2022-24765 class).
//
// Every git exec in this codebase must go through Command, never
// exec.Command("git", ...) directly: it prefixes every invocation with
// -c flags that disable hooks/fsmonitor/the ext protocol, and sets the
// process env so neither a global nor system gitconfig (which, combined
// with a malicious repo-local include, could still inject settings) nor an
// interactive credential prompt can apply.
func Command(dir string, args ...string) *exec.Cmd {
	hardened := make([]string, 0, len(args)+6)
	hardened = append(hardened,
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "protocol.ext.allow=never",
	)
	hardened = append(hardened, args...)
	c := exec.Command("git", hardened...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	return c
}

func git(dir string, args ...string) (string, error) {
	out, err := Command(dir, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// Collect gathers branch/head/dirty/merge-base facts for dir, comparing
// against base (e.g. "main"). A missing merge-base (base branch not found,
// or no common ancestor) is tolerated: MergeBase is left empty.
func Collect(dir, base string) (Meta, error) {
	var m Meta
	m.Branch, _ = git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return m, err
	}
	m.Head = head
	m.MergeBase, _ = git(dir, "merge-base", base, "HEAD")
	status, _ := git(dir, "status", "--porcelain")
	m.Dirty = status != ""
	return m, nil
}
