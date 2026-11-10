package gitmeta

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRefRejectsBogusRef(t *testing.T) {
	dir := initRepo(t)
	if _, err := VerifyRef(dir, "does-not-exist-anywhere"); err == nil {
		t.Fatal("expected an error for a ref that doesn't resolve")
	}
}

func TestVerifyRefResolvesRealRef(t *testing.T) {
	dir := initRepo(t)
	run(t, dir, "tag", "v1.0.0")
	commit, err := VerifyRef(dir, "v1.0.0")
	if err != nil {
		t.Fatalf("VerifyRef: %v", err)
	}
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if commit != head {
		t.Fatalf("VerifyRef(v1.0.0) = %q, want HEAD %q", commit, head)
	}
}

func TestListRefs(t *testing.T) {
	dir := initRepo(t)
	run(t, dir, "tag", "v1.0.0")
	run(t, dir, "branch", "feature")

	current, branches, tags, err := ListRefs(dir)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if current != "main" {
		t.Errorf("current = %q, want main", current)
	}
	sort.Strings(branches)
	if !reflect.DeepEqual(branches, []string{"feature", "main"}) {
		t.Errorf("branches = %v, want [feature main]", branches)
	}
	if !reflect.DeepEqual(tags, []string{"v1.0.0"}) {
		t.Errorf("tags = %v, want [v1.0.0]", tags)
	}
}

func TestMergeBaseNoCommonAncestorDegradesToEmpty(t *testing.T) {
	dir := initRepo(t)
	mb, err := MergeBase(dir, "no-such-branch", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if mb != "" {
		t.Fatalf("MergeBase = %q, want empty (no such ref)", mb)
	}
}

// TestDiffAgainst exercises DiffAgainst end to end on a small real diff: a
// new file (A), a modified file (M) with a line appended at the end, and
// confirms the HEAD-side hunk range for the modified file is parsed
// correctly out of `git diff --unified=0`.
func TestDiffAgainst(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "keep.go"), "package x\n\nfunc A() {}\n\nfunc B() {}\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "base content")
	run(t, dir, "branch", "base-point") // a ref pointing at this exact commit

	writeFile(t, filepath.Join(dir, "new.go"), "package x\n\nfunc New() {}\n")
	writeFile(t, filepath.Join(dir, "keep.go"), "package x\n\nfunc A() {}\n\nfunc B() {}\n\nfunc C() {}\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "add new.go, append to keep.go")

	mergeBase, err := MergeBase(dir, "base-point", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if mergeBase == "" {
		t.Fatal("expected a merge-base")
	}

	files, err := DiffAgainst(dir, mergeBase)
	if err != nil {
		t.Fatalf("DiffAgainst: %v", err)
	}

	newFile, ok := files["new.go"]
	if !ok {
		t.Fatalf("missing new.go in %+v", files)
	}
	if newFile.Status != "A" {
		t.Errorf("new.go status = %q, want A", newFile.Status)
	}
	if len(newFile.Hunks) != 1 || newFile.Hunks[0].Start != 1 || newFile.Hunks[0].Count != 3 {
		t.Errorf("new.go hunks = %+v, want [{1 3 0}]", newFile.Hunks)
	}
	// A brand-new file is a pure insertion: oldCount 0.
	if newFile.Hunks[0].OldCount != 0 {
		t.Errorf("new.go hunk oldCount = %d, want 0", newFile.Hunks[0].OldCount)
	}

	keepFile, ok := files["keep.go"]
	if !ok {
		t.Fatalf("missing keep.go in %+v", files)
	}
	if keepFile.Status != "M" {
		t.Errorf("keep.go status = %q, want M", keepFile.Status)
	}
	// The appended blank line + "func C() {}" landed at lines 6-7 of the
	// new content, two lines added, nothing removed (oldCount 0).
	if len(keepFile.Hunks) != 1 || keepFile.Hunks[0].Start != 6 || keepFile.Hunks[0].Count != 2 {
		t.Errorf("keep.go hunks = %+v, want [{6 2 0}]", keepFile.Hunks)
	}
	if keepFile.Hunks[0].OldCount != 0 {
		t.Errorf("keep.go hunk oldCount = %d, want 0 (pure append)", keepFile.Hunks[0].OldCount)
	}
}

// TestDiffAgainstRewriteCarriesOldCount proves a line REPLACED in place
// (not appended) reports a non-zero oldCount — the signal app.js uses to
// class a rewritten existing function "changed", not "added".
func TestDiffAgainstRewriteCarriesOldCount(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "k.go"), "package x\n\nfunc A() { return }\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "base")
	run(t, dir, "branch", "base-point")

	// Replace the body line in place (same line count, different content).
	writeFile(t, filepath.Join(dir, "k.go"), "package x\n\nfunc A() { panic(1) }\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "rewrite A's body")

	mergeBase, err := MergeBase(dir, "base-point", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	files, err := DiffAgainst(dir, mergeBase)
	if err != nil {
		t.Fatalf("DiffAgainst: %v", err)
	}
	k := files["k.go"]
	if len(k.Hunks) != 1 {
		t.Fatalf("k.go hunks = %+v, want exactly one", k.Hunks)
	}
	if k.Hunks[0].OldCount == 0 {
		t.Errorf("rewrite hunk oldCount = 0, want > 0 (a line was replaced, not purely inserted)")
	}
}

// TestDiffAgainstRename proves a rename (`git diff --name-status` reports
// "R100\told\tnew") collapses to status "M" at the DESTINATION path.
func TestDiffAgainstRename(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "old.go"), "package x\n\nfunc A() {}\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "add old.go")
	run(t, dir, "branch", "base-point")

	run(t, dir, "mv", "old.go", "new.go")
	run(t, dir, "commit", "-q", "-m", "rename old.go to new.go")

	mergeBase, err := MergeBase(dir, "base-point", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	// Force rename detection so --name-status yields an R-status line.
	files, err := DiffAgainst(dir, mergeBase)
	if err != nil {
		t.Fatalf("DiffAgainst: %v", err)
	}
	newFile, ok := files["new.go"]
	if !ok {
		t.Fatalf("rename destination new.go missing: %+v", files)
	}
	if newFile.Status != "M" {
		t.Errorf("rename new.go status = %q, want M", newFile.Status)
	}
	if _, ok := files["old.go"]; ok {
		t.Errorf("rename source old.go should not appear as its own entry: %+v", files)
	}
}

// TestDiffAgainstUntrackedFile proves a brand-new, uncommitted,
// not-ignored file shows up as a synthetic add (status "A") even though it
// isn't in any commit — so its nodes classify "added", not "unchanged".
func TestDiffAgainstUntrackedFile(t *testing.T) {
	dir := initRepo(t)
	run(t, dir, "branch", "base-point")
	writeFile(t, filepath.Join(dir, "untracked.go"), "package x\n\nfunc U() {}\n")

	mergeBase, err := MergeBase(dir, "base-point", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	files, err := DiffAgainst(dir, mergeBase)
	if err != nil {
		t.Fatalf("DiffAgainst: %v", err)
	}
	u, ok := files["untracked.go"]
	if !ok {
		t.Fatalf("untracked.go missing from diff: %+v", files)
	}
	if u.Status != "A" {
		t.Errorf("untracked.go status = %q, want A", u.Status)
	}
}

// TestDiffAgainstUnicodePath proves a non-ASCII filename appears literally
// (not octal-escaped) in the diff — the -c core.quotepath=false hardening
// (gitmeta.go Command) at work; without it git would emit "p\303\244iv\303\244.go".
func TestDiffAgainstUnicodePath(t *testing.T) {
	dir := initRepo(t)
	run(t, dir, "branch", "base-point")
	writeFile(t, filepath.Join(dir, "päivä.go"), "package x\n\nfunc P() {}\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "add unicode-named file")

	mergeBase, err := MergeBase(dir, "base-point", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	files, err := DiffAgainst(dir, mergeBase)
	if err != nil {
		t.Fatalf("DiffAgainst: %v", err)
	}
	if _, ok := files["päivä.go"]; !ok {
		t.Fatalf("unicode path päivä.go missing (quotepath not disabled?): %+v", files)
	}
}

// TestDiffAgainstIgnoresRepoLocalDiffExternal is the RCE regression test:
// a mounted repo whose .git/config sets diff.external to a program that
// proves it ran (by touching a marker) must NEVER have that program
// invoked when the overlay diffs it — the -c diff.external= hardening
// (gitmeta.go Command) plus --no-ext-diff (diff.go) close this.
func TestDiffAgainstIgnoresRepoLocalDiffExternal(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "f.go"), "package x\n\nfunc A() {}\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "base")
	run(t, dir, "branch", "base-point")
	writeFile(t, filepath.Join(dir, "f.go"), "package x\n\nfunc A() { _ = 1 }\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "touch f.go")

	marker := filepath.Join(dir, "pwned")
	evil := filepath.Join(dir, "evil-diff.sh")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A repo-local diff.external points at the malicious program — exactly
	// what an attacker ships in a mounted repo's .git/config.
	cfg := exec.Command("git", "config", "--local", "diff.external", evil)
	cfg.Dir = dir
	if out, err := cfg.CombinedOutput(); err != nil {
		t.Fatalf("git config diff.external: %v %s", err, out)
	}

	mergeBase, err := MergeBase(dir, "base-point", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if _, err := DiffAgainst(dir, mergeBase); err != nil {
		t.Fatalf("DiffAgainst: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("malicious diff.external driver ran despite -c diff.external= / --no-ext-diff")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// TestParseHunkHeaderBareCount covers a hunk header with a bare "+N" (no
// comma) — unified diff's convention for a one-line count.
func TestParseHunkHeaderBareCount(t *testing.T) {
	h, ok := parseHunkHeader("@@ -4 +7 @@ func C() {}")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if h.Start != 7 || h.Count != 1 || h.OldCount != 1 {
		t.Fatalf("bare-count hunk = %+v, want {7 1 1}", h)
	}

	h2, ok := parseHunkHeader("@@ -0,0 +1,3 @@")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if h2.Start != 1 || h2.Count != 3 || h2.OldCount != 0 {
		t.Fatalf("insertion hunk = %+v, want {1 3 0}", h2)
	}
}

// TestVerifyRefRejectsOptionLikeRef proves an option-looking base
// ("--output=/tmp/x", "-q") is rejected by the allowlist BEFORE any git
// call, so it can never reach git as a flag.
func TestVerifyRefRejectsOptionLikeRef(t *testing.T) {
	dir := initRepo(t)
	for _, bad := range []string{"--output=/tmp/x", "-q", "--upload-pack=touch /tmp/pwn", "../etc", "a b", ""} {
		if _, err := VerifyRef(dir, bad); err == nil {
			t.Errorf("VerifyRef(%q) = nil error, want rejection", bad)
		}
	}
}

// TestDiffAgainstNoHunksForDeletedFile proves a deleted file's entry
// carries no hunks (there's nothing on the HEAD side to highlight).
func TestDiffAgainstNoHunksForDeletedFile(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "gone.go"), "package x\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "add gone.go")
	run(t, dir, "branch", "base-point")

	run(t, dir, "rm", "-q", "gone.go")
	run(t, dir, "commit", "-q", "-m", "remove gone.go")

	mergeBase, err := MergeBase(dir, "base-point", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	files, err := DiffAgainst(dir, mergeBase)
	if err != nil {
		t.Fatalf("DiffAgainst: %v", err)
	}
	gone, ok := files["gone.go"]
	if !ok {
		t.Fatalf("missing gone.go in %+v", files)
	}
	if gone.Status != "D" {
		t.Errorf("gone.go status = %q, want D", gone.Status)
	}
	if len(gone.Hunks) != 0 {
		t.Errorf("gone.go hunks = %+v, want none", gone.Hunks)
	}
}
