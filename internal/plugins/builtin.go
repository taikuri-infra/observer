package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/taikuri-infra/observer/internal/manifest"
)

// safeRelPathRe allows only characters that cannot alter shell parsing, so
// auto-detected repo directory names are safe to interpolate into the tab
// commands below (which the runner executes via `sh -c`). A hostile repo could
// otherwise ship a directory named e.g. `$(…)` or `;rm -rf ~` and get code
// execution at `init`/analysis time.
var safeRelPathRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// filterSafeRel drops any detected path with shell metacharacters, whitespace,
// or "..". Real source directories are always within the allowed charset;
// anything else is pathological (or hostile) and is skipped.
func filterSafeRel(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" && safeRelPathRe.MatchString(p) && !strings.Contains(p, "..") {
			out = append(out, p)
		}
	}
	return out
}

// DefaultTabs detects repo's structure and returns observer's built-in,
// deliberately LEAN tab set, highest-signal first: Overview, AI context (a
// paste-ready repo brief), Service map (Go package dependency map, with a
// churn↔coverage color overlay), Changes (the map with packages differing
// vs base highlighted), What changed (a textual review digest), API routes
// (Fiber route table, per Fiber module), Database (ER canvas, per
// migrations dir), TODOs (marker scan), and Hotspots (churn).
//
// Intentionally NOT emitted (removed 2026-11): the per-main-package "Call
// graph" tabs and the per-frontend-package "Frontend deps" tabs. A
// function-level call graph for a whole binary is a dense hairball that
// rarely answers a question the package Service map doesn't already answer
// at a readable altitude, and both fanned out to one tab per main/app and
// needed go-callvis/depcruise installed or the tab just errored — pure
// sidebar clutter. The gen callgraphmodel / gen frontendmodel generators
// stay in the CLI for anyone who wants to run one by hand.
//
// Module detection walks repo up to depth 3 (skipping node_modules/vendor/
// .git) for nested go.mod files. Migrations directories are then searched
// for within each detected Go module's own subtree (any depth — e.g.
// servicePlus's tenant_migrations lives 4 levels under its services/api Go
// module).
func DefaultTabs(repo string) ([]manifest.Tab, error) {
	goModules, err := findModuleDirs(repo, "go.mod", 3)
	if err != nil {
		return nil, fmt.Errorf("detect go modules: %w", err)
	}
	goModules = filterSafeRel(goModules)

	// Service map is the primary canvas tab, right after Overview + the AI
	// context brief.
	tabs := []manifest.Tab{overviewTab()}
	tabs = append(tabs, contextPackTab(goModules)...)
	tabs = append(tabs, serviceMapTabs(goModules)...)
	tabs = append(tabs, changesTab(goModules))
	tabs = append(tabs, whatChangedTab())
	tabs = append(tabs, apiRoutesTabs(repo, goModules)...)

	var migrationsDirs []string
	for _, mod := range goModules {
		found, err := findMigrationsDirs(filepath.Join(repo, mod))
		if err != nil {
			return nil, fmt.Errorf("detect migrations under %s: %w", mod, err)
		}
		for _, d := range found {
			migrationsDirs = append(migrationsDirs, filepath.ToSlash(filepath.Join(mod, d)))
		}
	}
	sort.Strings(migrationsDirs)
	migrationsDirs = filterSafeRel(migrationsDirs)

	tabs = append(tabs, databaseTabs(migrationsDirs)...)
	tabs = append(tabs, todosTab())
	tabs = append(tabs, hotspotsTab())

	return tabs, nil
}

func overviewTab() manifest.Tab {
	return manifest.Tab{
		Name:     "Overview",
		Kind:     manifest.KindGenerator,
		Command:  "observer gen overview --out obs-data/index.md",
		Output:   "index.md",
		Renderer: "markdown",
	}
}

// contextPackTab is the "AI context" tab (native, `observer gen
// contextpack`): a paste-ready Markdown brief of the whole repo for
// dropping into an LLM prompt. Sits right after Overview — the 2nd sidebar
// tab — since it's the first thing a developer reaches for when handing a
// repo to an AI assistant. Only emitted when at least one Go module was
// detected (the brief's architecture/data-model/API sections all need a Go
// module to analyze); the first detected module is used, matching
// serviceMapTabs/apiRoutesTabs for repos with more than one.
func contextPackTab(goModules []string) []manifest.Tab {
	if len(goModules) == 0 {
		return nil
	}
	mod := goModules[0]
	return []manifest.Tab{{
		Name:     "AI context",
		Kind:     manifest.KindGenerator,
		Command:  fmt.Sprintf("observer gen contextpack --dir '%s' --out obs-data/context.md", mod),
		Output:   "context.md",
		Renderer: "markdown",
	}}
}

// changesTab is the "Changes" tab: on a repo with at least one detected Go
// module, it's a canvas service map (gen.ChangedGraphModel, via `observer
// gen changedgraph`) with changed packages highlighted — the first
// detected Go module is used (matching serviceMapTabs/goCallGraphTabs,
// which likewise don't disambiguate across multiple modules for a single
// shared tab). Repos with no Go module fall back to the original markdown
// diffstat report, since the highlighted map has nothing to draw without
// package nodes.
func changesTab(goModules []string) manifest.Tab {
	if len(goModules) == 0 {
		return manifest.Tab{
			Name:     "Changes",
			Kind:     manifest.KindGenerator,
			Command:  "observer gen changes --base main --out obs-data/changes.md",
			Output:   "changes.md",
			Renderer: "markdown",
		}
	}
	mod := goModules[0]
	const out = "changes-map.json"
	return manifest.Tab{
		Name:     "Changes",
		Kind:     manifest.KindGenerator,
		Command:  fmt.Sprintf("observer gen changedgraph --dir '%s' --base main --out 'obs-data/%s'", mod, out),
		Output:   out,
		Renderer: "map",
	}
}

// whatChangedTab is the "What changed" tab: a Markdown review digest
// (gen.WhatChanged, via `observer gen whatchanged`) — the textual
// complement to the "Changes" tab's highlighted service map, oriented for
// pasting into an AI reviewer or a PR description. Always emitted,
// regardless of detected modules (it degrades gracefully with no Go
// module, same as the markdown fallback of changesTab).
func whatChangedTab() manifest.Tab {
	return manifest.Tab{
		Name:     "What changed",
		Kind:     manifest.KindGenerator,
		Command:  "observer gen whatchanged --base main --out obs-data/whatchanged.md",
		Output:   "whatchanged.md",
		Renderer: "markdown",
	}
}

// apiRoutesTabs is native (observer gen apiroutes) — one per detected Go
// module whose go.mod imports gofiber/fiber. No Dir is set: apiroutes'
// --dir is a repo-root-relative command-line flag, so the command runs
// from the repo root and passes --dir itself (matching serviceMapTabs).
func apiRoutesTabs(repo string, goModules []string) []manifest.Tab {
	var fiberModules []string
	for _, mod := range goModules {
		if moduleImportsFiber(filepath.Join(repo, mod)) {
			fiberModules = append(fiberModules, mod)
		}
	}

	var tabs []manifest.Tab
	for _, mod := range fiberModules {
		out := outputName("api-routes.json", mod, len(fiberModules))
		tabs = append(tabs, manifest.Tab{
			Name:     tabName("API routes", mod, len(fiberModules)),
			Kind:     manifest.KindGenerator,
			Command:  fmt.Sprintf("observer gen apiroutes --dir '%s' --out 'obs-data/%s'", mod, out),
			Output:   out,
			Renderer: "table",
		})
	}
	return tabs
}

// moduleImportsFiber reports whether moduleDir's go.mod requires
// gofiber/fiber (any major version) — the signal that its routes are worth
// statically parsing for the API routes tab.
func moduleImportsFiber(moduleDir string) bool {
	b, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "gofiber/fiber")
}

// databaseTabs renders the ER model on the canvas map (tables as "table"
// nodes, foreign keys as edges) via `observer gen er --json-out`, which
// reuses the same CREATE TABLE parse that also writes the mermaid/DBML
// artifacts (still produced, just no longer what the tab renders).
func databaseTabs(migrationsDirs []string) []manifest.Tab {
	var tabs []manifest.Tab
	for _, dir := range migrationsDirs {
		mmd := outputName("er.mmd", filepath.Base(dir), len(migrationsDirs))
		dbml := outputName("er.dbml", filepath.Base(dir), len(migrationsDirs))
		jsonOut := outputName("er-map.json", filepath.Base(dir), len(migrationsDirs))
		tabs = append(tabs, manifest.Tab{
			Name: tabName("Database", filepath.Base(dir), len(migrationsDirs)),
			Kind: manifest.KindGenerator,
			Command: fmt.Sprintf(
				"observer gen er --migrations '%s' --out 'obs-data/%s' --dbml-out 'obs-data/%s' --json-out 'obs-data/%s'",
				dir, mmd, dbml, jsonOut,
			),
			Output:   jsonOut,
			Renderer: "map",
		})
	}
	return tabs
}

// serviceMapTabs is native (observer gen graphmodel) — one per detected Go
// module; it replaces the old DOT-rendered "Go deps" tab. No Dir is set:
// graphmodel needs the true repo root (to anchor obs-data/hotspots.json and
// each node's location.path), so the command runs from the repo root and
// passes --dir itself.
func serviceMapTabs(goModules []string) []manifest.Tab {
	var tabs []manifest.Tab
	for _, mod := range goModules {
		out := outputName("map.json", mod, len(goModules))
		tabs = append(tabs, manifest.Tab{
			Name:     tabName("Service map", mod, len(goModules)),
			Kind:     manifest.KindGenerator,
			Command:  fmt.Sprintf("observer gen graphmodel --dir '%s' --out 'obs-data/%s'", mod, out),
			Output:   out,
			Renderer: "map",
		})
	}
	return tabs
}

// todosTab is native (observer gen todos) — always emitted, every repo:
// scans git-tracked source files for TODO/FIXME/HACK/XXX/BUG marker
// comments and renders them as a flat table.
func todosTab() manifest.Tab {
	return manifest.Tab{
		Name:     "TODOs",
		Kind:     manifest.KindGenerator,
		Command:  "observer gen todos --out obs-data/todos.json",
		Output:   "todos.json",
		Renderer: "table",
	}
}

func hotspotsTab() manifest.Tab {
	return manifest.Tab{
		Name:     "Hotspots",
		Kind:     manifest.KindGenerator,
		Command:  "observer gen hotspots --out obs-data/hotspots.json",
		Output:   "hotspots.json",
		Renderer: "table",
	}
}
