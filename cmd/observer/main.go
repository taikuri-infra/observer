// Command observer is the CLI entrypoint: serve the dashboard API, run a
// native generator directly, or inspect the local setup.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/taikuri-infra/observer/internal/gen"
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
  observer init [--repo .]
  observer doctor
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
	h := server.New(absRepo, *base)
	listenAddr := fmt.Sprintf("%s:%d", *addr, *port)
	fmt.Printf("observer serving %s on http://%s (base=%s)\n", absRepo, listenAddr, *base)
	return http.ListenAndServe(listenAddr, h)
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

// runInit scaffolds obs-data/observer.yaml. Full language-detection
// (DefaultTabs) ships in M3 — for M1 this just makes sure the command
// exists per R13 and reports what it would do.
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	repo := fs.String("repo", ".", "path to the repo")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRepo, err := absPath(*repo)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(absRepo, "obs-data", "observer.yaml")
	if _, err := os.Stat(manifestPath); err == nil {
		fmt.Printf("observer.yaml already exists at %s — leaving it untouched\n", manifestPath)
		return nil
	}
	fmt.Println("observer init: default-tab detection ships in M3; create obs-data/observer.yaml by hand for now")
	return nil
}

// runDoctor is a stub in M1 (full tool-presence checks ship with M3's
// DefaultTabs); for now it just confirms git is reachable, since every
// generator in this milestone depends on it.
func runDoctor(_ []string) error {
	_, err := exec.LookPath("git")
	if err != nil {
		fmt.Println("git: NOT FOUND on PATH (required)")
		return err
	}
	fmt.Println("git: ok")
	return nil
}
