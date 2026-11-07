// Command observer is the CLI entrypoint: serve the dashboard API, run a
// native generator directly, or inspect the local setup.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/taikuri-infra/observer/internal/gen"
	"github.com/taikuri-infra/observer/internal/manifest"
	"github.com/taikuri-infra/observer/internal/plugins"
	"github.com/taikuri-infra/observer/internal/server"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "gen":
		err = runGen(args)
	case "init":
		err = runInit(args)
	case "doctor":
		err = runDoctor(args)
	case "version":
		fmt.Println("observer " + version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "observer: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "observer: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `observer — local code-observability dashboard

Usage:
  observer serve [--repo .] [--base main] [--addr 127.0.0.1] [--port 7979]
  observer gen changes --base main --out <path> [--repo .]
  observer gen er --migrations <dir> --out <er.mmd> --dbml-out <er.dbml> [--repo .]
  observer gen godeps --dir <moduleDir> --out <godeps.dot> [--repo .]
  observer gen hotspots --out <hotspots.json> [--repo .] [--since <ref>]
  observer gen overview --out <index.md> [--repo .] [--base main]
  observer init [--repo .] [--out <path>]
  observer doctor [--repo .]
  observer version
`)
}

// runServe starts the HTTP API + dashboard for one repo. R4: binds
// 127.0.0.1 by default — never all interfaces, since POST /api/update runs
// a shell command.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo to observe")
	base := fs.String("base", "main", "branch to compare 'changes since' against")
	addr := fs.String("addr", "127.0.0.1", "bind address (host-only by design; never 0.0.0.0)")
	port := fs.Int("port", 7979, "port to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	token := os.Getenv("OBSERVER_TOKEN")
	if token == "" {
		token, err = generateToken()
		if err != nil {
			return fmt.Errorf("generate API token: %w", err)
		}
	}
	h := server.New(absRepo, *base, token)
	listenAddr := fmt.Sprintf("%s:%d", *addr, *port)
	fmt.Printf("observer serving %s on http://%s (base=%s)\n", absRepo, listenAddr, *base)
	fmt.Printf("Observer running: http://%s/?token=%s\n", listenAddr, token)
	fmt.Printf("API token: %s\n", token)
	return http.ListenAndServe(listenAddr, h)
}

// generateToken returns a 32-byte random token, hex-encoded, used as the
// per-session bearer token for /api/* requests (OBSERVER_TOKEN overrides
// this when set, e.g. for scripted/CI use).
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// runGen invokes a native generator directly and writes its output (R2:
// native generators write the file themselves via --out, no stdout
// capture) — this is also what manifest "observer gen ..." commands end up
// calling, once runner.rewriteSelf resolves "observer" to this binary.
func runGen(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("gen: missing generator name (changes)")
	}
	switch args[0] {
	case "changes":
		return runGenChanges(args[1:])
	case "er":
		return runGenER(args[1:])
	case "godeps":
		return runGenGoDeps(args[1:])
	case "hotspots":
		return runGenHotspots(args[1:])
	case "overview":
		return runGenOverview(args[1:])
	default:
		return fmt.Errorf("gen: unknown generator %q (available: changes, er, godeps, hotspots, overview)", args[0])
	}
}

func runGenChanges(args []string) error {
	fs := flag.NewFlagSet("gen changes", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo")
	base := fs.String("base", "main", "branch to compare against")
	out := fs.String("out", "", "output file path (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("gen changes: --out is required")
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	md, err := gen.Changes(absRepo, *base)
	if err != nil {
		return fmt.Errorf("gen changes: %w", err)
	}
	return writeOut(absRepo, *out, md)
}

// runGenER is R2/R10: gen er takes --migrations plus two --out flags and
// writes both files itself (mermaid erDiagram + DBML snapshot).
func runGenER(args []string) error {
	fs := flag.NewFlagSet("gen er", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo")
	migrations := fs.String("migrations", "", "path to the SQL migrations directory (required)")
	out := fs.String("out", "", "mermaid output file path (required)")
	dbmlOut := fs.String("dbml-out", "", "DBML output file path (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *migrations == "" {
		return fmt.Errorf("gen er: --migrations is required")
	}
	if *out == "" || *dbmlOut == "" {
		return fmt.Errorf("gen er: --out and --dbml-out are required")
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	absMigrations := *migrations
	if !filepath.IsAbs(absMigrations) {
		absMigrations = filepath.Join(absRepo, absMigrations)
	}
	mermaid, dbml, err := gen.ER(absMigrations)
	if err != nil {
		return fmt.Errorf("gen er: %w", err)
	}
	if err := writeOut(absRepo, *out, mermaid); err != nil {
		return err
	}
	return writeOut(absRepo, *dbmlOut, dbml)
}

// runGenGoDeps wires gen.GoDeps: --dir is the Go module directory (relative
// to --repo unless absolute), --out is where the DOT graph is written.
func runGenGoDeps(args []string) error {
	fs := flag.NewFlagSet("gen godeps", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo")
	dir := fs.String("dir", ".", "path to the Go module directory (relative to --repo unless absolute)")
	out := fs.String("out", "", "output file path (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("gen godeps: --out is required")
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	absDir := *dir
	if !filepath.IsAbs(absDir) {
		absDir = filepath.Join(absRepo, absDir)
	}
	dot, err := gen.GoDeps(absDir)
	if err != nil {
		return fmt.Errorf("gen godeps: %w", err)
	}
	return writeOut(absRepo, *out, dot)
}

// runGenHotspots wires gen.Hotspots: aggregates git log --numstat churn per
// file and writes the top files as JSON.
func runGenHotspots(args []string) error {
	fs := flag.NewFlagSet("gen hotspots", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo")
	since := fs.String("since", "", "only consider commits since this git ref/date (optional)")
	top := fs.Int("top", 50, "max number of files to report, by churn")
	out := fs.String("out", "", "output file path (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("gen hotspots: --out is required")
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	stats, err := gen.Hotspots(absRepo, *since, *top)
	if err != nil {
		return fmt.Errorf("gen hotspots: %w", err)
	}
	b, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return fmt.Errorf("gen hotspots: marshal: %w", err)
	}
	return writeOut(absRepo, *out, string(b))
}

// runGenOverview wires gen.Overview: a markdown landing-page summary of repo
// freshness + the tab/artifact inventory from obs-data/_index.json.
func runGenOverview(args []string) error {
	fs := flag.NewFlagSet("gen overview", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo")
	base := fs.String("base", "main", "branch to compare against for freshness")
	out := fs.String("out", "", "output file path (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("gen overview: --out is required")
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	md, err := gen.Overview(absRepo, *base)
	if err != nil {
		return fmt.Errorf("gen overview: %w", err)
	}
	return writeOut(absRepo, *out, md)
}

func writeOut(repo, out, content string) error {
	path := out
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func absPath(p string) (string, error) {
	return filepath.Abs(p)
}

// runInit scaffolds obs-data/observer.yaml from plugins.DefaultTabs.
// Idempotent: never clobbers an existing manifest. --out overrides the
// destination, e.g. for writing to a temp location without touching the
// target repo's obs-data/ (used by M3's acceptance check).
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo")
	out := fs.String("out", "", "write the manifest here instead of <repo>/obs-data/observer.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	manifestPath := *out
	if manifestPath == "" {
		manifestPath = filepath.Join(absRepo, "obs-data", "observer.yaml")
	}
	wrote, tabs, err := writeManifestIfAbsent(absRepo, manifestPath)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if !wrote {
		fmt.Printf("observer.yaml already exists at %s — leaving it untouched\n", manifestPath)
		return nil
	}
	fmt.Printf("wrote %s (%d tabs)\n", manifestPath, len(tabs))
	return nil
}

// writeManifestIfAbsent detects repo's default tabs and writes them as
// YAML to manifestPath, unless manifestPath already exists. Returns
// whether it wrote the file (false means "left untouched").
func writeManifestIfAbsent(repo, manifestPath string) (wrote bool, tabs []manifest.Tab, err error) {
	if _, statErr := os.Stat(manifestPath); statErr == nil {
		return false, nil, nil
	} else if !os.IsNotExist(statErr) {
		return false, nil, statErr
	}
	tabs, err = plugins.DefaultTabs(repo)
	if err != nil {
		return false, nil, fmt.Errorf("detect default tabs: %w", err)
	}
	proj := manifest.Project{Project: filepath.Base(repo), Tabs: tabs}
	b, err := yaml.Marshal(proj)
	if err != nil {
		return false, nil, fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return false, nil, err
	}
	if err := os.WriteFile(manifestPath, b, 0o644); err != nil {
		return false, nil, err
	}
	return true, tabs, nil
}

// runDoctor loads the repo's manifest (or detects plugins.DefaultTabs if
// none exists yet) and reports git's availability plus, for every distinct
// external tool any tab Requires, whether it's found on PATH.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	tabs, err := loadOrDetectTabs(absRepo)
	if err != nil {
		return fmt.Errorf("doctor: %w", err)
	}
	return printDoctorReport(os.Stdout, tabs)
}

// loadOrDetectTabs reads repo's obs-data/observer.yaml if present,
// otherwise falls back to plugins.DefaultTabs so `doctor` is useful even
// before `init` has ever run.
func loadOrDetectTabs(repo string) ([]manifest.Tab, error) {
	b, err := os.ReadFile(filepath.Join(repo, "obs-data", "observer.yaml"))
	if err == nil {
		proj, perr := manifest.Parse(b)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Join(repo, "obs-data", "observer.yaml"), perr)
		}
		return proj.Tabs, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	return plugins.DefaultTabs(repo)
}

// printDoctorReport writes git's availability, plus every distinct
// external tool required by tabs and whether exec.LookPath finds it, to w.
func printDoctorReport(w io.Writer, tabs []manifest.Tab) error {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Fprintln(w, "git: NOT FOUND on PATH (required)")
	} else {
		fmt.Fprintln(w, "git: ok")
	}

	seen := map[string]bool{}
	var tools []string
	for _, t := range tabs {
		for _, tool := range t.Requires {
			if !seen[tool] {
				seen[tool] = true
				tools = append(tools, tool)
			}
		}
	}
	sort.Strings(tools)
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Fprintf(w, "%s: NOT FOUND on PATH\n", tool)
		} else {
			fmt.Fprintf(w, "%s: ok\n", tool)
		}
	}
	return nil
}
