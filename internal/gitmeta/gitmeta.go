// Package gitmeta reports repo freshness: branch, HEAD, dirty, and the
// merge-base against a base branch (for the "changes since" view).
package gitmeta

import (
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

func git(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
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
