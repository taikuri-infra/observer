package gen

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Graph is the normalized model behind the "map" renderer (graph.json) —
// the contract between analyzers and the canvas service map. See
// docs/superpowers/specs/2026-11-08-canvas-service-map-design.md §4.
type Graph struct {
	Meta  GraphMeta   `json:"meta"`
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphMeta struct {
	Source        string `json:"source"`
	Module        string `json:"module"`
	GeneratedAt   string `json:"generatedAt"`
	SizeBy        string `json:"sizeBy"`
	ColorBy       string `json:"colorBy"`
	DefaultLayout string `json:"defaultLayout"`
	// Base and AnyChanged are only set by ChangedGraphModel (the "Changes"
	// tab's highlighted map): Base names the branch nodes were compared
	// against, AnyChanged tells the renderer whether to dim non-changed
	// nodes or fall back to a "no changes vs base" note.
	Base       string `json:"base,omitempty"`
	AnyChanged bool   `json:"anyChanged,omitempty"`
}

type GraphMetrics struct {
	Dependents int `json:"dependents"`
	Imports    int `json:"imports"`
	Churn      int `json:"churn"`
	LOC        int `json:"loc"`
	// Columns/FKOut/FKIn are only populated for "table" nodes (the database
	// ER map, see er.go's ERGraphModel) — omitempty keeps them out of every
	// other graph.json's nodes.
	Columns int `json:"columns,omitempty"`
	FKOut   int `json:"fkOut,omitempty"`
	FKIn    int `json:"fkIn,omitempty"`
	// Callers/Callees are only populated for "func" nodes (the Go call
	// graph, see callgraphmodel.go's CallGraphModel): call-graph in-degree
	// (how many functions call this one) and out-degree (how many
	// functions this one calls) — omitempty keeps them out of every other
	// graph.json's nodes. meta.sizeBy/colorBy reference these by name
	// ("callers"), so the key must match the JSON tag exactly.
	Callers int `json:"callers,omitempty"`
	Callees int `json:"callees,omitempty"`
	// Coverage is a 0-100 STATIC test-coverage proxy (see testednessProxy),
	// set only on GraphModel's package nodes (the Service map). It is a
	// POINTER so that 0% (an untested package — the exact thing the
	// coverage overlay highlights) is still emitted, while every other
	// generator's nodes (tables, funcs) that have no coverage concept leave
	// it nil → omitempty drops it. The map's "color by coverage" overlay
	// reads this key.
	Coverage *int `json:"coverage,omitempty"`
}

type GraphLocation struct {
	Path        string `json:"path"`
	PrimaryFile string `json:"primaryFile,omitempty"`
	Files       int    `json:"files"`
	// LineStart/LineEnd are only populated for "func" nodes whose exact
	// declaration CallGraphModel could resolve via go/ast (see funcloc.go):
	// the 1-based [LineStart,LineEnd] span of the function/method
	// declaration inside PrimaryFile, from the `func` keyword through its
	// closing brace. omitempty keeps them out of every node that doesn't
	// have file:line precision (package/table/module-level nodes, and any
	// call-graph func this repo couldn't resolve — closures, third-party
	// functions, or an AST id mismatch).
	LineStart int `json:"lineStart,omitempty"`
	LineEnd   int `json:"lineEnd,omitempty"`
}

// GraphDescription.Source is one of "godoc" | "jsdoc" | "observer-annotation" | "none".
type GraphDescription struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type GraphLink struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// TableColumn is one column of a database table node (kind "table"): its
// name, SQL type, and whether it's a primary / foreign key — the data the
// renderer draws as a row inside the ER table card.
type TableColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
	PK   bool   `json:"pk,omitempty"`
	FK   bool   `json:"fk,omitempty"`
}

// TableDetail carries a "table" node's full column list so the canvas can
// draw it as a proper ER card (header + one row per column) instead of a
// bare circle. Only ERGraphModel populates it; metrics.columns still holds
// the count for sizing/coloring.
type TableDetail struct {
	Columns []TableColumn `json:"columns"`
}

type GraphNode struct {
	ID          string           `json:"id"`
	Label       string           `json:"label"`
	Kind        string           `json:"kind"`
	Metrics     GraphMetrics     `json:"metrics"`
	Location    GraphLocation    `json:"location"`
	Description GraphDescription `json:"description"`
	Links       []GraphLink      `json:"links"`
	// Changed/ChangedFiles are only populated by ChangedGraphModel: whether
	// this package owns a file that differs vs the "Changes" tab's base
	// branch, and which files those are (for the detail panel's "Changed
	// files" section).
	Changed      bool     `json:"changed,omitempty"`
	ChangedFiles []string `json:"changedFiles,omitempty"`
	// Table is only populated for "table" nodes (ERGraphModel): the full
	// column list the renderer draws as an ER card. omitempty + pointer keep
	// it out of every other kind's JSON.
	Table *TableDetail `json:"table,omitempty"`
}

type GraphEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Weight int    `json:"weight"`
}

// goListDepsPkg is the subset of `go list -json -deps` fields GraphModel
// needs per package. Error is only populated under `go list -e` (see
// funcloc.go's listModulePackages): a package that failed to load still
// gets emitted, with the failure here, instead of aborting the whole list.
type goListDepsPkg struct {
	ImportPath   string
	Dir          string
	Imports      []string
	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
	Error        *goListPkgError
}

// goListPkgError is `go list`'s per-package Error object (only its message
// matters here).
type goListPkgError struct {
	Err string
}

// GraphModel builds the normalized graph.json for moduleDir's own Go
// packages: one node per package (kind, in/out-degree metrics, churn from
// obs-data/hotspots.json, godoc/annotation description, location) and one
// edge per direct intra-module import. repoDir anchors
// obs-data/hotspots.json and every node's location.path (both are relative
// to the repo root, not the module).
func GraphModel(repoDir, moduleDir string) (*Graph, error) {
	modulePath, err := readModulePath(moduleDir)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("go", "list", "-json", "-deps", "./...")
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list -json -deps ./... in %s: %w\n%s", moduleDir, err, ee.Stderr)
		}
		return nil, fmt.Errorf("go list -json -deps ./... in %s: %w", moduleDir, err)
	}

	type pkgInfo struct {
		dir     string
		goFiles []string
		imports []string
	}
	pkgs := map[string]pkgInfo{}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p goListDepsPkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if !underModule(p.ImportPath, modulePath) {
			continue // stdlib, third-party, or a sibling module: not a node here
		}
		id := trimModulePrefix(p.ImportPath, modulePath)
		goFiles := make([]string, 0, len(p.GoFiles)+len(p.TestGoFiles)+len(p.XTestGoFiles))
		goFiles = append(goFiles, p.GoFiles...)
		goFiles = append(goFiles, p.TestGoFiles...)
		goFiles = append(goFiles, p.XTestGoFiles...)
		pkgs[id] = pkgInfo{dir: p.Dir, goFiles: goFiles, imports: p.Imports}
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages found for module %s in %s", modulePath, moduleDir)
	}

	relModuleDir, err := filepath.Rel(repoDir, moduleDir)
	if err != nil {
		return nil, fmt.Errorf("relativize module dir %s against repo %s: %w", moduleDir, repoDir, err)
	}
	relModuleDir = filepath.ToSlash(relModuleDir)

	churnByDir, err := churnByPackageDir(repoDir)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(pkgs))
	for id := range pkgs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	indeg := map[string]int{}
	outdeg := map[string]int{}
	var edges []GraphEdge
	for _, id := range ids {
		seen := map[string]bool{}
		for _, imp := range pkgs[id].imports {
			if !underModule(imp, modulePath) {
				continue
			}
			to := trimModulePrefix(imp, modulePath)
			if to == id || seen[to] {
				continue
			}
			if _, ok := pkgs[to]; !ok {
				continue
			}
			seen[to] = true
			edges = append(edges, GraphEdge{From: id, To: to, Weight: 1})
			outdeg[id]++
			indeg[to]++
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	nodes := make([]GraphNode, 0, len(ids))
	for _, id := range ids {
		pi := pkgs[id]
		locPath := repoRelPath(relModuleDir, id)

		doc, err := ExtractPackageDoc(pi.dir, pi.goFiles)
		if err != nil {
			return nil, fmt.Errorf("extract doc for %s: %w", id, err)
		}

		desc := GraphDescription{Source: "none"}
		switch {
		case doc.Summary != "":
			desc = GraphDescription{Text: doc.Summary, Source: "observer-annotation"}
		case doc.GodocText != "":
			desc = GraphDescription{Text: doc.GodocText, Source: "godoc"}
		}

		// Always a JSON array, never null: the renderer iterates links[]
		// and a null would force a defensive guard in the (security-
		// sensitive) front end for no reason.
		links := []GraphLink{}
		if doc.Task != "" {
			links = append(links, GraphLink{Type: InferLinkType(doc.Task), ID: doc.Task})
		}

		// Fresh per-iteration variable so &cov is a distinct pointer per
		// node (the Metrics.Coverage field is *int).
		cov := testednessProxy(pi.dir, pi.goFiles)

		nodes = append(nodes, GraphNode{
			ID:    id,
			Label: labelFor(id),
			Kind:  ClassifyKind(id),
			Metrics: GraphMetrics{
				Dependents: indeg[id],
				Imports:    outdeg[id],
				Churn:      churnByDir[locPath],
				LOC:        sumLines(pi.dir),
				Coverage:   &cov,
			},
			Location: GraphLocation{
				Path:        locPath,
				PrimaryFile: doc.PrimaryFile,
				Files:       countGoFiles(pi.dir),
			},
			Description: desc,
			Links:       links,
		})
	}

	return &Graph{
		Meta: GraphMeta{
			Source:        "go-deps",
			Module:        relModuleDir,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			SizeBy:        "dependents",
			ColorBy:       "churn",
			DefaultLayout: "hierarchical",
		},
		Nodes: nodes,
		Edges: edges,
	}, nil
}

// repoRelPath joins a module's repo-relative directory with a package id
// ("." for the module's own root package), "/"-normalized.
func repoRelPath(relModuleDir, id string) string {
	if id == "." {
		if relModuleDir == "." {
			return "."
		}
		return relModuleDir
	}
	if relModuleDir == "." {
		return id
	}
	return relModuleDir + "/" + id
}

// underModule reports whether importPath is the module itself or a package
// within it — a prefix test that, unlike strings.HasPrefix(imp,
// modulePath), won't mistake a sibling module "example.com/foobar" for a
// package of "example.com/foo".
func underModule(importPath, modulePath string) bool {
	return importPath == modulePath || strings.HasPrefix(importPath, modulePath+"/")
}

// churnByPackageDir computes per-directory churn directly from git history
// (reusing Hotspots' aggregation with no file-count cap), summing each
// file's commit count onto its containing directory — the per-package
// churn metric (design spec §5.2).
//
// It deliberately does NOT read obs-data/hotspots.json: the Service map tab
// is generated before the Hotspots tab, so that artifact may be stale or
// absent on a fresh repo, which would make the whole map render churn 0.
// Computing from git keeps the map self-sufficient and always fresh. If git
// is unavailable (e.g. the target isn't a git repo at all), churn degrades
// to 0 rather than failing the generator — churn is an enrichment, not a
// prerequisite for the map.
func churnByPackageDir(repoDir string) (map[string]int, error) {
	byDir := map[string]int{}
	stats, err := Hotspots(repoDir, "", 0) // top=0: every file, no cap
	if err != nil {
		return byDir, nil // non-git / no history: churn 0 everywhere, not fatal
	}
	for _, s := range stats {
		dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(s.Path)))
		byDir[dir] += s.Churn
	}
	return byDir, nil
}

// countGoFiles returns how many non-test *.go files sit directly in dir
// (not recursive) — location.files. Test files are excluded to match the
// `go list` GoFiles set the rest of the model is built from.
func countGoFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			n++
		}
	}
	return n
}

// sumLines returns the total line count across every *.go file directly in
// dir — metrics.loc. Best-effort: unreadable files are skipped rather than
// failing the whole generator. The scanner uses a large line buffer so
// files with very long lines (generated code, embedded data) are counted,
// not truncated at bufio's 64 KB default.
func sumLines(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			total++
		}
		f.Close()
	}
	return total
}
