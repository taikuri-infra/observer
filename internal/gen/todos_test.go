package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// initTODORepo creates a tiny git repo in a tempdir with a couple of
// tracked files containing marker comments, plus one line with no marker,
// and one gitignored file whose marker must never be scanned. Modeled on
// internal/runner/runner_test.go's initRepo helper.
func initTODORepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	mainGo := `package main

// TODO: wire up the real client
func main() {
	// FIXME(alice): this panics on empty input
	doWork()
	// just a normal comment, no marker here
}

// HACK: temporary workaround until upstream fixes the bug
func doWork() {}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	readme := `# demo

<!-- XXX: this section is stale -->
` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	bugFile := `package pkg

// BUG: off-by-one on the last page
func Paginate() {}
`
	if err := os.WriteFile(filepath.Join(dir, "pkg.go"), []byte(bugFile), 0o644); err != nil {
		t.Fatalf("write pkg.go: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.go\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	ignored := `package pkg

// TODO: this must NEVER show up — the file is gitignored
func Ignored() {}
`
	if err := os.WriteFile(filepath.Join(dir, "ignored.go"), []byte(ignored), 0o644); err != nil {
		t.Fatalf("write ignored.go: %v", err)
	}

	run("add", "main.go", "README.md", "pkg.go", ".gitignore")
	run("commit", "-q", "-m", "init")

	return dir
}

func TestTODOsFindsMarkersInTrackedFiles(t *testing.T) {
	dir := initTODORepo(t)

	markers, err := TODOs(dir, 0)
	if err != nil {
		t.Fatalf("TODOs: %v", err)
	}
	if len(markers) == 0 {
		t.Fatalf("expected markers, got none")
	}

	byKey := map[string]Marker{}
	for _, m := range markers {
		byKey[m.Marker+"|"+m.Path+"|"+strconv.Itoa(m.Line)] = m
	}

	want := []Marker{
		{Marker: "TODO", Path: "main.go", Line: 3, Text: "wire up the real client"},
		{Marker: "FIXME", Path: "main.go", Line: 5, Text: "alice): this panics on empty input"},
		{Marker: "HACK", Path: "main.go", Line: 10, Text: "temporary workaround until upstream fixes the bug"},
		{Marker: "BUG", Path: "pkg.go", Line: 3, Text: "off-by-one on the last page"},
	}
	for _, w := range want {
		got, ok := byKey[w.Marker+"|"+w.Path+"|"+strconv.Itoa(w.Line)]
		if !ok {
			t.Fatalf("missing marker %+v in %+v", w, markers)
		}
		if got.Text != w.Text {
			t.Errorf("marker %s %s:%d text = %q, want %q", w.Marker, w.Path, w.Line, got.Text, w.Text)
		}
	}
}

func TestTODOsIgnoresGitignoredFiles(t *testing.T) {
	dir := initTODORepo(t)

	markers, err := TODOs(dir, 0)
	if err != nil {
		t.Fatalf("TODOs: %v", err)
	}
	for _, m := range markers {
		if m.Path == "ignored.go" {
			t.Fatalf("gitignored file ignored.go must not be scanned, got marker %+v", m)
		}
	}
}

func TestTODOsSortsFixmeBeforeTodo(t *testing.T) {
	dir := initTODORepo(t)

	markers, err := TODOs(dir, 0)
	if err != nil {
		t.Fatalf("TODOs: %v", err)
	}
	if len(markers) < 2 {
		t.Fatalf("expected at least 2 markers, got %d", len(markers))
	}

	fixmeIdx, todoIdx := -1, -1
	for i, m := range markers {
		if m.Marker == "FIXME" && fixmeIdx == -1 {
			fixmeIdx = i
		}
		if m.Marker == "TODO" && todoIdx == -1 {
			todoIdx = i
		}
	}
	if fixmeIdx == -1 || todoIdx == -1 {
		t.Fatalf("expected both FIXME and TODO in results: %+v", markers)
	}
	if fixmeIdx > todoIdx {
		t.Fatalf("expected FIXME (idx %d) to sort before TODO (idx %d)", fixmeIdx, todoIdx)
	}
}

func TestTODOsTopCap(t *testing.T) {
	dir := initTODORepo(t)

	all, err := TODOs(dir, 0)
	if err != nil {
		t.Fatalf("TODOs: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("need at least 2 markers in fixture for a meaningful cap test, got %d", len(all))
	}

	capped, err := TODOs(dir, 1)
	if err != nil {
		t.Fatalf("TODOs top=1: %v", err)
	}
	if len(capped) != 1 {
		t.Fatalf("expected exactly 1 marker with top=1, got %d", len(capped))
	}
	if capped[0] != all[0] {
		t.Fatalf("top=1 result %+v should match the first of the unlimited result %+v", capped[0], all[0])
	}
}
