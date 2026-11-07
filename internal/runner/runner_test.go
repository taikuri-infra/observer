package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/taikuri-infra/observer/internal/manifest"
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

func TestRunWritesArtifact(t *testing.T) {
	repo := initRepo(t)
	tab := manifest.Tab{Name: "Out", Command: "echo hi > obs-data/out.txt", Output: "out.txt", Renderer: "markdown"}
	a, err := Run(context.Background(), repo, "main", tab, time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if a.Tab != "Out" || a.Path != filepath.Join("obs-data", "out.txt") {
		t.Fatalf("artifact = %+v", a)
	}
	if a.GeneratedAt == "" || a.Head == "" {
		t.Fatalf("artifact missing freshness fields: %+v", a)
	}
	body, err := os.ReadFile(filepath.Join(repo, "obs-data", "out.txt"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.TrimSpace(string(body)) != "hi" {
		t.Fatalf("output = %q", body)
	}
}

func TestRunErrorsWhenOutputMissing(t *testing.T) {
	// R8: a command that exits 0 but never produces the declared output must
	// be reported as an error, not silently "succeed".
	repo := initRepo(t)
	tab := manifest.Tab{Name: "NoOp", Command: "true", Output: "nope.txt", Renderer: "markdown"}
	_, err := Run(context.Background(), repo, "main", tab, time.Now())
	if err == nil {
		t.Fatal("expected error for missing declared output")
	}
}

func TestRunUsesTabDir(t *testing.T) {
	// R3: cmd.Dir must be repo/tab.Dir, so a relative command can act on a
	// nested module.
	repo := initRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	tab := manifest.Tab{Name: "Sub", Command: "pwd > ../obs-data/out.txt", Output: "out.txt", Dir: "sub"}
	_, err := Run(context.Background(), repo, "main", tab, time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repo, "obs-data", "out.txt"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(body)), "/sub") {
		t.Fatalf("expected pwd to end in /sub, got %q", body)
	}
}

func TestRewriteSelfToken(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := rewriteSelf("observer gen changes --base main")
	if err != nil {
		t.Fatalf("rewriteSelf: %v", err)
	}
	want := exe + " gen changes --base main"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRewriteSelfLeavesOtherCommands(t *testing.T) {
	got, err := rewriteSelf("go-callvis -focus .")
	if err != nil {
		t.Fatalf("rewriteSelf: %v", err)
	}
	if got != "go-callvis -focus ." {
		t.Fatalf("got %q", got)
	}
}
