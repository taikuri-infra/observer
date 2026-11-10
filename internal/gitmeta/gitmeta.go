// Package gitmeta reports repo freshness: branch, HEAD, dirty, and the
// merge-base against a base branch (for the "changes since" view).
package gitmeta

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
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
// -c flags that disable hooks/fsmonitor/the ext protocol, neutralize any
// repo-configured external diff driver, and pin path quoting/prefix
// behavior, then sets the process env so neither a global nor system
// gitconfig (which, combined with a malicious repo-local include, could
// still inject settings) nor an interactive credential prompt can apply.
//
// diff.external= is a second RCE sink beyond hooks: `git diff` in a mounted
// repo whose .git/config sets diff.external (or a per-driver
// diff.<name>.command keyed from .gitattributes) would otherwise run that
// program on every hunk the moment a user opens the diff overlay. Clearing
// it here (plus --no-ext-diff on the diff calls themselves, belt and
// braces) closes that. core.quotepath=false keeps non-ASCII paths literal
// (not \NNN-escaped) so the overlay can match them; diff.noprefix=false /
// diff.mnemonicPrefix=false pin the "a/"/"b/" prefixes diff.go's hunk
// parser relies on, against a repo that reconfigured them.
func Command(dir string, args ...string) *exec.Cmd {
	hardened := make([]string, 0, len(args)+16)
	hardened = append(hardened,
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "protocol.ext.allow=never",
		"-c", "diff.external=",
		"-c", "core.quotepath=false",
		"-c", "diff.noprefix=false",
		"-c", "diff.mnemonicPrefix=false",
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
	m.MergeBase, _ = MergeBase(dir, base, "HEAD")
	status, _ := git(dir, "status", "--porcelain")
	m.Dirty = status != ""
	return m, nil
}

// MergeBase returns the merge-base commit of base and head in dir's repo —
// the common ancestor every "changes since base" comparison (Changes,
// ChangedGraphModel, DiffAgainst) actually diffs HEAD against, not base
// itself. An empty result with a nil error means no common ancestor was
// found (base doesn't exist locally, or the two histories are unrelated);
// callers that need a hard requirement should treat that themselves, the
// same way Collect's own callers already tolerate it.
func MergeBase(dir, base, head string) (string, error) {
	out, err := git(dir, "merge-base", base, head)
	if err != nil {
		return "", nil
	}
	return out, nil
}

// safeRefRe allowlists exactly the characters a legitimate branch/tag/ref
// name (plus the "^{commit}" peel suffix and "~"/"^" ancestry ops) uses.
// It is the FIRST gate VerifyRef applies, before any git call: git execs
// via an argv array so a ref is never shell-expanded, but a ref that
// starts with "-" would still be parsed by rev-parse as an OPTION, not an
// operand (e.g. "--output=/tmp/x", "-q") — this rejects that class
// outright rather than relying solely on --end-of-options.
var safeRefRe = regexp.MustCompile(`^[A-Za-z0-9._/@^~{}-]+$`)

// VerifyRef resolves ref to its commit hash, confirming it actually names
// a commit (branch, tag, or any other commit-ish) that exists in dir's
// repo. It's the validation gate GET /api/gitdiff runs before using a
// caller-supplied ref for anything else. Defense in depth, three layers:
// (1) reject a ref that isn't in safeRefRe, or that begins with "-", so an
// option-looking ref never reaches git as a flag; (2) pass
// --end-of-options so even a future allowlist gap can't make git treat the
// ref as an option; (3) --verify ... ^{commit} so it must resolve to a
// real commit. Callers should then diff against the RETURNED hash, never
// the raw ref, so no git call downstream re-parses attacker text.
func VerifyRef(dir, ref string) (commit string, err error) {
	if ref == "" || strings.HasPrefix(ref, "-") || !safeRefRe.MatchString(ref) {
		return "", fmt.Errorf("ref %q is not a valid ref name", ref)
	}
	out, err := git(dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("ref %q does not resolve to a commit: %w", ref, err)
	}
	return out, nil
}

// ListRefs returns dir's local branches and tags (short names, e.g.
// "main", "v1.2.3"), each sorted by git's own for-each-ref default order,
// plus the current branch (empty when HEAD is detached). Used by GET
// /api/gitrefs to populate the git-aware overlay's base-ref picker.
func ListRefs(dir string) (current string, branches []string, tags []string, err error) {
	current, _ = git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if current == "HEAD" {
		current = "" // detached HEAD: no "current branch" to default to
	}
	branchOut, err := git(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return current, nil, nil, fmt.Errorf("list branches: %w", err)
	}
	tagOut, err := git(dir, "for-each-ref", "--format=%(refname:short)", "refs/tags/")
	if err != nil {
		return current, nil, nil, fmt.Errorf("list tags: %w", err)
	}
	return current, splitNonEmptyLines(branchOut), splitNonEmptyLines(tagOut), nil
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
