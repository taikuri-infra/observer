package atref_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/taikuri-infra/observer/internal/atref"
	"github.com/taikuri-infra/observer/internal/manifest"
)

// --- fixture helpers -------------------------------------------------------

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main")
	gitCmd(t, dir, "config", "user.email", "t@t")
	gitCmd(t, dir, "config", "user.name", "t")
	return dir
}

func writeFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	p := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, repo, msg string) string {
	t.Helper()
	gitCmd(t, repo, "add", "-A")
	gitCmd(t, repo, "commit", "-q", "-m", msg)
	return gitCmd(t, repo, "rev-parse", "HEAD")
}

// assertOnlyMainWorktree fails the test unless repo's "git worktree list"
// shows exactly one entry (the main worktree) — i.e. no ephemeral
// atref worktree was left behind.
func assertOnlyMainWorktree(t *testing.T, repo string) {
	t.Helper()
	out := gitCmd(t, repo, "worktree", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected only the main worktree, got:\n%s", out)
	}
}

// shq single-quotes s for safe interpolation into a `sh -c` command string
// (the same style builtin.go uses for manifest commands).
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// observerBinary builds the real cmd/observer binary once per test run (not
// the go-test binary, which doesn't understand "gen graphmodel" — see
// runner.rewriteSelf's self-exec convention, which only resolves correctly
// for the actual compiled observer) so tests can exercise GraphAtRef against
// a real generator pipeline end to end.
var (
	buildBinOnce sync.Once
	observerBin  string
	buildBinErr  error
)

func observerBinary(t *testing.T) string {
	t.Helper()
	buildBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "observer-atref-bin-*")
		if err != nil {
			buildBinErr = err
			return
		}
		bin := filepath.Join(dir, "observer")
		cmd := exec.Command("go", "build", "-o", bin, "github.com/taikuri-infra/observer/cmd/observer")
		cmd.Env = append(os.Environ(), "GO111MODULE=on")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildBinErr = fmt.Errorf("go build observer: %w: %s", err, out)
			return
		}
		observerBin = bin
	})
	if buildBinErr != nil {
		t.Fatalf("build observer binary: %v", buildBinErr)
	}
	return observerBin
}

// --- tests -------------------------------------------------------------

// TestGraphAtRefDiffersBetweenCommits builds a tiny 2-commit Go module where
// a package ("bar") exists only from the 2nd commit onward, and asserts
// GraphAtRef at each commit returns a graph.json reflecting exactly that
// commit's code — not the working tree, not the other commit.
func TestGraphAtRefDiffersBetweenCommits(t *testing.T) {
	bin := observerBinary(t)
	repo := initRepo(t)

	writeFile(t, repo, "go.mod", "module example.com/atreffixture\n\ngo 1.22\n")
	writeFile(t, repo, "foo/foo.go", "package foo\n\nfunc A() int { return 1 }\n")
	sha1 := commitAll(t, repo, "commit 1: foo only")

	writeFile(t, repo, "bar/bar.go", "package bar\n\nfunc B() int { return 2 }\n")
	writeFile(t, repo, "foo/foo.go", "package foo\n\nimport \"example.com/atreffixture/bar\"\n\nfunc A() int { return bar.B() }\n")
	sha2 := commitAll(t, repo, "commit 2: add bar, foo imports bar")

	tab := manifest.Tab{
		Name:     "Service map",
		Kind:     manifest.KindGenerator,
		Command:  fmt.Sprintf("%s gen graphmodel --dir . --out obs-data/map.json", shq(bin)),
		Output:   "map.json",
		Renderer: "map",
	}

	g1, err := atref.GraphAtRef(repo, tab, sha1)
	if err != nil {
		t.Fatalf("GraphAtRef at commit1: %v", err)
	}
	g2, err := atref.GraphAtRef(repo, tab, sha2)
	if err != nil {
		t.Fatalf("GraphAtRef at commit2: %v", err)
	}
	if bytes.Equal(g1, g2) {
		t.Fatalf("expected graph at commit1 to differ from commit2, both were:\n%s", g1)
	}

	var graph1, graph2 struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(g1, &graph1); err != nil {
		t.Fatalf("unmarshal commit1 graph: %v\n%s", err, g1)
	}
	if err := json.Unmarshal(g2, &graph2); err != nil {
		t.Fatalf("unmarshal commit2 graph: %v\n%s", err, g2)
	}
	if len(graph1.Nodes) != 1 {
		t.Fatalf("commit1: expected 1 node (foo only), got %d: %s", len(graph1.Nodes), g1)
	}
	if len(graph2.Nodes) != 2 {
		t.Fatalf("commit2: expected 2 nodes (foo+bar), got %d: %s", len(graph2.Nodes), g2)
	}

	assertOnlyMainWorktree(t, repo)
}

// TestGraphAtRefCachesAndSkipsRebuild proves caching by making a rebuild
// detectably fail: the tab's command refuses to run a second time (guarded
// by a marker file outside the repo). A broken cache would re-run the
// command on the second GraphAtRef call and that second call would then
// fail — so the second call succeeding, with byte-identical output, is the
// proof a rebuild never happened.
func TestGraphAtRefCachesAndSkipsRebuild(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "README.md", "x\n")
	sha := commitAll(t, repo, "init")

	marker := filepath.Join(t.TempDir(), "ran.marker")
	tab := manifest.Tab{
		Name: "Probe",
		Kind: manifest.KindGenerator,
		Command: fmt.Sprintf(
			"test ! -f %s && printf x > %s && mkdir -p obs-data && printf '{\"built\":true}' > obs-data/probe.json",
			shq(marker), shq(marker),
		),
		Output:   "probe.json",
		Renderer: "map",
	}

	b1, err := atref.GraphAtRef(repo, tab, sha)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if string(b1) != `{"built":true}` {
		t.Fatalf("unexpected first build output: %s", b1)
	}
	assertOnlyMainWorktree(t, repo)

	b2, err := atref.GraphAtRef(repo, tab, sha)
	if err != nil {
		t.Fatalf("second call should have been served from cache, got a (re)build error: %v", err)
	}
	if string(b2) != string(b1) {
		t.Fatalf("cached bytes differ: %s vs %s", b1, b2)
	}
	assertOnlyMainWorktree(t, repo)
}

// TestGraphAtRefCleansUpOnBuildError asserts that a build failure still
// removes the ephemeral worktree and leaves the main repo's HEAD, index,
// and status byte-for-byte unchanged.
func TestGraphAtRefCleansUpOnBuildError(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "README.md", "x\n")
	sha := commitAll(t, repo, "init")

	statusBefore := gitCmd(t, repo, "status", "--porcelain")
	headBefore := gitCmd(t, repo, "rev-parse", "HEAD")

	tab := manifest.Tab{
		Name:    "Boom",
		Kind:    manifest.KindGenerator,
		Command: "exit 1",
		Output:  "nope.json",
	}

	if _, err := atref.GraphAtRef(repo, tab, sha); err == nil {
		t.Fatal("expected an error for a failing generator")
	}

	assertOnlyMainWorktree(t, repo)

	statusAfter := gitCmd(t, repo, "status", "--porcelain")
	headAfter := gitCmd(t, repo, "rev-parse", "HEAD")
	if statusAfter != statusBefore {
		t.Fatalf("repo status changed: before=%q after=%q", statusBefore, statusAfter)
	}
	if headAfter != headBefore {
		t.Fatalf("HEAD changed: before=%q after=%q", headBefore, headAfter)
	}
}

// TestGraphAtRefWorkingTreeServesExistingArtifact asserts that ref == "" and
// ref == atref.WorkingRef both serve the EXISTING repo/obs-data/<output>
// artifact as-is — no generator run (a marker-guarded command would run at
// most zero times), no worktree — and that a missing artifact is a clear
// error, not a silent rebuild.
func TestGraphAtRefWorkingTreeServesExistingArtifact(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "README.md", "x\n")
	commitAll(t, repo, "init")

	// A command that, if ever run, would FAIL the test by proving a working
	// ref rebuilt instead of serving the existing file.
	tab := manifest.Tab{
		Name:    "Direct",
		Kind:    manifest.KindGenerator,
		Command: "exit 3",
		Output:  "direct.json",
	}

	// Missing artifact -> clear error, no generator run.
	if _, err := atref.GraphAtRef(repo, tab, ""); err == nil {
		t.Fatal("expected an error when the working-tree artifact doesn't exist yet")
	}

	// Pre-generate the artifact (as /api/update would), then both working
	// refs must serve it verbatim.
	writeFile(t, repo, "obs-data/direct.json", `{"ok":true}`)
	for _, ref := range []string{"", atref.WorkingRef} {
		b, err := atref.GraphAtRef(repo, tab, ref)
		if err != nil {
			t.Fatalf("ref %q: %v", ref, err)
		}
		if string(b) != `{"ok":true}` {
			t.Fatalf("ref %q: unexpected output %s", ref, b)
		}
	}

	assertOnlyMainWorktree(t, repo)
}

// TestGraphAtRefBuildTimeoutCleansUp shrinks BuildTimeout and points a tab
// at a generator that sleeps far longer, proving the build is killed on
// timeout (returns promptly with an error) and the ephemeral worktree is
// still cleaned up afterward — a hung analyzer can't wedge the server.
func TestGraphAtRefBuildTimeoutCleansUp(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "README.md", "x\n")
	sha := commitAll(t, repo, "init")

	prev := atref.BuildTimeout
	atref.BuildTimeout = 300 * time.Millisecond
	defer func() { atref.BuildTimeout = prev }()

	tab := manifest.Tab{
		Name:    "Slow",
		Kind:    manifest.KindGenerator,
		Command: "sleep 30 && mkdir -p obs-data && printf '{}' > obs-data/slow.json",
		Output:  "slow.json",
	}

	start := time.Now()
	_, err := atref.GraphAtRef(repo, tab, sha)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("build was not bounded by BuildTimeout: took %v", elapsed)
	}
	assertOnlyMainWorktree(t, repo)
}

// TestGraphAtRefRejectsInvalidRef asserts an unresolvable ref fails fast
// with ErrInvalidRef (the 400 signal for the HTTP handler) and never
// attempts a worktree build.
func TestGraphAtRefRejectsInvalidRef(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "README.md", "x\n")
	commitAll(t, repo, "init")

	tab := manifest.Tab{Name: "X", Kind: manifest.KindGenerator, Command: "true", Output: "x.json"}
	_, err := atref.GraphAtRef(repo, tab, "not-a-real-ref-at-all")
	if err == nil || !errors.Is(err, atref.ErrInvalidRef) {
		t.Fatalf("expected ErrInvalidRef, got %v", err)
	}
	assertOnlyMainWorktree(t, repo)
}
