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
// set: Overview + Changes (always), Database (per detected SQL migrations
// dir), Go deps + Go call graph (per detected Go module), Frontend deps
// (per detected frontend package), and Hotspots (always).
//
// Module detection walks repo up to depth 3 (skipping node_modules/vendor/
// .git) for nested go.mod/package.json files. Migrations directories are
// then searched for within each detected Go module's own subtree (any
// depth — e.g. servicePlus's tenant_migrations lives 4 levels under its
// services/api Go module).
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

	tabs := []manifest.Tab{overviewTab(), changesTab()}

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
	tabs = append(tabs, goDepsTabs(goModules)...)
	tabs = append(tabs, goCallGraphTabs(repo, goModules)...)
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

func changesTab() manifest.Tab {
	return manifest.Tab{
		Name:     "Changes",
		Kind:     manifest.KindGenerator,
		Command:  "observer gen changes --base main --out obs-data/changes.md",
		Output:   "changes.md",
		Renderer: "markdown",
	}
}

func databaseTabs(migrationsDirs []string) []manifest.Tab {
	var tabs []manifest.Tab
	for _, dir := range migrationsDirs {
		mmd := outputName("er.mmd", filepath.Base(dir), len(migrationsDirs))
		dbml := outputName("er.dbml", filepath.Base(dir), len(migrationsDirs))
		tabs = append(tabs, manifest.Tab{
			Name: tabName("Database", filepath.Base(dir), len(migrationsDirs)),
			Kind: manifest.KindGenerator,
			Command: fmt.Sprintf(
				"observer gen er --migrations '%s' --out 'obs-data/%s' --dbml-out 'obs-data/%s'",
				dir, mmd, dbml,
			),
			Output:   mmd,
			Renderer: "mermaid",
		})
	}
	return tabs
}

// goDepsTabs is native (observer gen godeps) — one per detected Go module.
// upPath(mod) is embedded in --out so the file always lands in the
// top-level obs-data/, matching the verification runner.Run does against
// repo/obs-data/<Output> regardless of the tab's Dir.
func goDepsTabs(goModules []string) []manifest.Tab {
	var tabs []manifest.Tab
	for _, mod := range goModules {
		out := outputName("godeps.dot", mod, len(goModules))
		tabs = append(tabs, manifest.Tab{
			Name:     tabName("Go deps", mod, len(goModules)),
			Kind:     manifest.KindGenerator,
			Command:  fmt.Sprintf("observer gen godeps --dir . --out '%sobs-data/%s'", upPath(mod), out),
			Output:   out,
			Dir:      mod,
			Renderer: "graph",
		})
	}
	return tabs
}

// goCallGraphTabs uses the external go-callvis tool. go-callvis analyses a
// single binary entry point — its default -focus is the `main` package — so a
// module with more than one main (e.g. servicePlus's services/api, which has
// cmd/server plus a handful of one-off command/script mains) can't be graphed
// with a single `./...` invocation: that fails with "found multiple packages
// with name: main". Instead we detect each module's main packages and emit one
// focused call-graph tab per main, each targeting that package's directory.
// R13 still holds: the command omits an explicit -focus (a single-main target
// already disambiguates it).
func goCallGraphTabs(repo string, goModules []string) []manifest.Tab {
	type entry struct{ module, mainRel, label string }
	var entries []entry
	for _, mod := range goModules {
		mains, err := findMainPackages(filepath.Join(repo, mod))
		if err != nil {
			continue // best-effort: a module we can't scan simply gets no call-graph tab
		}
		for _, m := range filterSafeRel(mains) {
			// label disambiguates both the tab name and the artifact
			// filename, using the main package's repo-relative path so
			// sibling mains (cmd/server vs cmd/worker) never collide.
			label := m
			if mod != "" && mod != "." {
				label = mod
				if m != "." {
					label = mod + "/" + m
				}
			}
			entries = append(entries, entry{module: mod, mainRel: m, label: label})
		}
	}

	var tabs []manifest.Tab
	for _, e := range entries {
		out := outputName("callgraph.svg", e.label, len(entries))
		stem := strings.TrimSuffix(out, ".svg")
		target := "./" + e.mainRel
		if e.mainRel == "." {
			target = "."
		}
		tabs = append(tabs, manifest.Tab{
			Name:     tabName("Go call graph", e.label, len(entries)),
			Kind:     manifest.KindGenerator,
			Command:  fmt.Sprintf("go-callvis -format svg -file '%sobs-data/%s' -nostd '%s'", upPath(e.module), stem, target),
			Output:   out,
			Dir:      e.module,
			Renderer: "svg",
			Requires: []string{"go-callvis"},
		})
	}
	return tabs
}

// frontendDepsTabs uses the external dependency-cruiser tool — one per
// detected frontend package. Scans the package's "src" dir when present,
// else the whole package dir.
func frontendDepsTabs(repo string, frontendModules []string) []manifest.Tab {
	var tabs []manifest.Tab
	for _, mod := range frontendModules {
		src := "src"
		if _, err := os.Stat(filepath.Join(repo, mod, "src")); err != nil {
			src = "."
		}
		out := outputName("web-deps.dot", mod, len(frontendModules))
		tabs = append(tabs, manifest.Tab{
			Name:     tabName("Frontend deps", mod, len(frontendModules)),
			Kind:     manifest.KindGenerator,
			Command:  fmt.Sprintf("depcruise --no-config --output-type dot '%s' > '%sobs-data/%s'", src, upPath(mod), out),
			Output:   out,
			Dir:      mod,
			Renderer: "graph",
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
