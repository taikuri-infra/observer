// Package runner executes a tab's generator command inside the repo and
// writes its artifact under obs-data/.
//
// Trust model: this executes commands the *repo itself* declares in its
// committed obs-data/observer.yaml — the same trust boundary as running that
// repo's Makefile or any other checked-in script. It is designed for local,
// single-user use (see R4: the HTTP server binds 127.0.0.1 only). In Docker
// mode (M4) the container mounts the repo's source read-only and only
// obs-data/ writable, so even a malicious manifest can't touch source.
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/taikuri-infra/observer/internal/gitmeta"
	"github.com/taikuri-infra/observer/internal/manifest"
)

// Artifact describes one tab's generated output and the freshness (git
// state) it was generated from. Also the schema for obs-data/_index.json's
// per-tab entries (R7).
type Artifact struct {
	Tab         string `json:"tab"`
	Path        string `json:"path"` // relative to repo, under obs-data/
	Renderer    string `json:"renderer"`
	GeneratedAt string `json:"generated_at"`
	Head        string `json:"head"`
	Branch      string `json:"branch"`
	Err         string `json:"error,omitempty"`
}

const outDir = "obs-data"

// Run executes tab's command inside repo and returns the resulting
// Artifact. On success, the command's declared Output file is verified to
// exist (R8) — a command that exits 0 without producing it is an error.
func Run(ctx context.Context, repo, base string, t manifest.Tab, now time.Time) (Artifact, error) {
	if err := os.MkdirAll(filepath.Join(repo, outDir), 0o755); err != nil {
		return Artifact{}, fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	meta, _ := gitmeta.Collect(repo, base)
	a := Artifact{
		Tab:         t.Name,
		Path:        filepath.Join(outDir, t.Output),
		Renderer:    t.Renderer,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Head:        meta.Head,
		Branch:      meta.Branch,
	}

	command, err := rewriteSelf(t.Command)
	if err != nil {
		a.Err = err.Error()
		return a, err
	}

	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	cmd.Dir = filepath.Join(repo, t.Dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		a.Err = fmt.Sprintf("%v: %s", err, out)
		return a, err
	}

	if t.Output != "" {
		outPath := filepath.Join(repo, outDir, t.Output)
		if _, err := os.Stat(outPath); err != nil {
			a.Err = fmt.Sprintf("command exited 0 but did not produce %s: %v", a.Path, err)
			return a, fmt.Errorf("verify output: %w", err)
		}
	}
	return a, nil
}

// rewriteSelf resolves R1 (self-exec PATH): native "observer gen …" manifest
// commands have no "observer" binary on PATH in host mode, since the running
// process IS that binary. If command's first token is literally "observer",
// replace it with the absolute path to the running executable.
func rewriteSelf(command string) (string, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 || fields[0] != "observer" {
		return command, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve self executable: %w", err)
	}
	return exe + strings.TrimPrefix(command, "observer"), nil
}
