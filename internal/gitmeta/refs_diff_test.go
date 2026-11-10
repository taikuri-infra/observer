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
	if len(newFile.Hunks) != 1 || newFile.Hunks[0][0] != 1 || newFile.Hunks[0][1] != 3 {
		t.Errorf("new.go hunks = %v, want [[1 3]]", newFile.Hunks)
	}

	keepFile, ok := files["keep.go"]
	if !ok {
		t.Fatalf("missing keep.go in %+v", files)
	}
	if keepFile.Status != "M" {
		t.Errorf("keep.go status = %q, want M", keepFile.Status)
	}
	// The appended blank line + "func C() {}" landed at lines 6-7 of the
	// new content, two lines added.
	if len(keepFile.Hunks) != 1 || keepFile.Hunks[0][0] != 6 || keepFile.Hunks[0][1] != 2 {
		t.Errorf("keep.go hunks = %v, want [[6 2]]", keepFile.Hunks)
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
		t.Errorf("gone.go hunks = %v, want none", gone.Hunks)
	}
}
