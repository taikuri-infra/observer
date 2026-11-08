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

// DefaultTabs detects repo's structure and returns observer's built-in tab
// set: Overview, Service map + Changes (always; Service map replaces the
// old Go deps tab and sits 2nd, right after Overview), Database (per
// detected SQL migrations dir, also canvas-rendered), Call graph (per
// detected Go *main package*, also canvas-rendered — see goCallGraphTabs),
// Frontend deps (per detected frontend package, also canvas-rendered), and
// Hotspots (always).
//
// Module detection walks repo up to depth 3 (skipping node_modules/vendor/
// .git) for nested go.mod/package.json files. Migrations directories and
// main packages are then searched for within each detected Go module's own
// subtree (any depth — e.g. servicePlus's tenant_migrations lives 4 levels
// under its services/api Go module, and that same module has four
// "package main" directories: cmd/server, cmd/retention-sweeper, and two
// scripts/ one-shot tools).
func DefaultTabs(repo string) ([]manifest.Tab, error) {
	goModules, err := findModuleDirs(repo, "go.mod", 3)
	if err != nil {
		return nil, fmt.Errorf("detect go modules: %w", err)
	}
	frontendModules, err := findModuleDirs(repo, "package.json", 3)
	if err != nil {
		return nil, fmt.Errorf("detect frontend packages: %w", err)
	}
	frontendModules = excludeTestDirs(frontendModules)
	goModules = filterSafeRel(goModules)
	frontendModules = filterSafeRel(frontendModules)

	// Service map is the 2nd sidebar tab — right after Overview, before
	// Changes — so the canvas map is the first thing anyone clicks into
	// after the landing page.
	tabs := []manifest.Tab{overviewTab()}
	tabs = append(tabs, serviceMapTabs(goModules)...)
	tabs = append(tabs, changesTab(goModules))

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
	callGraphTabs, err := goCallGraphTabs(repo, goModules)
	if err != nil {
		return nil, fmt.Errorf("detect go main packages: %w", err)
	}
	tabs = append(tabs, callGraphTabs...)
	tabs = append(tabs, frontendDepsTabs(repo, frontendModules)...)
	tabs = append(tabs, hotspotsTab())

	return tabs, nil
}

// excludeTestDirs drops frontend package dirs that look like test tooling
// (e2e suites, scenario fixtures) rather than an actual app whose
// dependency graph is worth visualizing.
func excludeTestDirs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		segments := strings.Split(strings.ToLower(filepath.ToSlash(d)), "/")
		excluded := false
		for _, seg := range segments {
			if strings.Contains(seg, "test") || seg == "e2e" {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		out = append(out, d)
	}
	return out
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

// mainPkgRef pairs a detected "package main" directory with the Go module
// it lives under, both repo-relative — goCallGraphTabs' unit of work (one
// tab per mainPkgRef, never per module: see its doc comment for why).
type mainPkgRef struct {
	mod string
	dir string // the main package's directory, relative to mod
}

// goCallGraphTabs replaces the old graphviz-SVG "Go call graph" pipeline
// (go-callvis -format svg -file ... ./...), which erred with "found
// multiple packages with name: main" on any module with more than one main
// (e.g. servicePlus's services/api: cmd/server, cmd/retention-sweeper, and
// two scripts/ tools). It emits one renderer:"map" tab PER DETECTED MAIN
// PACKAGE instead of per module — `observer gen callgraphmodel --dir <mod>
// --main <mainDir>` targets exactly one main, which is both how the
// multi-main error is avoided and how the call graph ends up on the canvas
// map renderer like every other tab (service map, database, frontend
// deps) rather than a static graphviz SVG.
//
// No Dir is set on the tab (same reasoning as serviceMapTabs/
// frontendDepsTabs): gen callgraphmodel's --dir/--main are repo-root-
// relative command-line flags, so the command runs from the repo root.
func goCallGraphTabs(repo string, goModules []string) ([]manifest.Tab, error) {
	var mains []mainPkgRef
	for _, mod := range goModules {
		found, err := findMainPackageDirs(filepath.Join(repo, mod))
		if err != nil {
			return nil, fmt.Errorf("detect main packages under %s: %w", mod, err)
		}
		found = filterSafeRel(found)
		for _, d := range found {
			mains = append(mains, mainPkgRef{mod: mod, dir: d})
		}
	}

	var tabs []manifest.Tab
	for _, m := range mains {
		out := outputName("callgraph-map.json", m.dir, len(mains))
		tabs = append(tabs, manifest.Tab{
			Name: tabName("Call graph", m.dir, len(mains)),
			Kind: manifest.KindGenerator,
			Command: fmt.Sprintf(
				"observer gen callgraphmodel --dir '%s' --main '%s' --out 'obs-data/%s'",
				m.mod, m.dir, out,
			),
			Output:   out,
			Renderer: "map",
			Requires: []string{"go-callvis"},
		})
	}
	return tabs, nil
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

// frontendDepsTabs uses the external dependency-cruiser tool (via `observer
// gen frontendmodel`, which shells out to it and parses its JSON output into
// a graph.json) — one per detected frontend package. Scans the package's
// "src" dir when present, else the whole package dir. Unlike the old DOT
// pipeline, no Dir is set: frontendmodel needs the true repo root to
// compute each node's repo-relative location.path, so the command runs from
// the repo root and passes --dir itself (matching serviceMapTabs).
func frontendDepsTabs(repo string, frontendModules []string) []manifest.Tab {
	var tabs []manifest.Tab
	for _, mod := range frontendModules {
		src := "src"
		if _, err := os.Stat(filepath.Join(repo, mod, "src")); err != nil {
			src = "."
		}
		out := outputName("frontend-map.json", mod, len(frontendModules))
		tabs = append(tabs, manifest.Tab{
			Name:     tabName("Frontend deps", mod, len(frontendModules)),
			Kind:     manifest.KindGenerator,
			Command:  fmt.Sprintf("observer gen frontendmodel --dir '%s' --src '%s' --out 'obs-data/%s'", mod, src, out),
			Output:   out,
			Renderer: "map",
			Requires: []string{"depcruise"},
		})
	}
	return tabs
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
