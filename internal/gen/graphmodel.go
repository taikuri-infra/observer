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
}

type GraphMetrics struct {
	Dependents int `json:"dependents"`
	Imports    int `json:"imports"`
	Churn      int `json:"churn"`
	LOC        int `json:"loc"`
}

type GraphLocation struct {
	Path        string `json:"path"`
	PrimaryFile string `json:"primaryFile,omitempty"`
	Files       int    `json:"files"`
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

type GraphNode struct {
	ID          string           `json:"id"`
	Label       string           `json:"label"`
	Kind        string           `json:"kind"`
	Metrics     GraphMetrics     `json:"metrics"`
	Location    GraphLocation    `json:"location"`
	Description GraphDescription `json:"description"`
	Links       []GraphLink      `json:"links"`
}

type GraphEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Weight int    `json:"weight"`
}

// goListDepsPkg is the subset of `go list -json -deps` fields GraphModel
// needs per package.
type goListDepsPkg struct {
	ImportPath   string
	Dir          string
	Imports      []string
	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
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
		if !strings.HasPrefix(p.ImportPath, modulePath) {
			continue // stdlib or third-party: not a node in this module's own map
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
			if !strings.HasPrefix(imp, modulePath) {
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

		var links []GraphLink
		if doc.Task != "" {
			links = append(links, GraphLink{Type: InferLinkType(doc.Task), ID: doc.Task})
		}

		nodes = append(nodes, GraphNode{
			ID:    id,
			Label: labelFor(id),
			Kind:  ClassifyKind(id),
			Metrics: GraphMetrics{
				Dependents: indeg[id],
				Imports:    outdeg[id],
				Churn:      churnByDir[locPath],
				LOC:        sumLines(pi.dir),
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

// churnByPackageDir loads repoDir/obs-data/hotspots.json (if present) and
// sums each file's churn (commit count) onto its containing directory —
// the per-package churn metric (design spec §5.2). Packages with no
// matching hotspot entry simply get 0 via the zero value of the returned
// map, so a missing/absent hotspots.json is not an error.
func churnByPackageDir(repoDir string) (map[string]int, error) {
	b, err := os.ReadFile(filepath.Join(repoDir, "obs-data", "hotspots.json"))
	if os.IsNotExist(err) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read obs-data/hotspots.json: %w", err)
	}
	var stats []FileStat
	if err := json.Unmarshal(b, &stats); err != nil {
		return nil, fmt.Errorf("parse obs-data/hotspots.json: %w", err)
	}
	byDir := map[string]int{}
	for _, s := range stats {
		dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(s.Path)))
		byDir[dir] += s.Churn
	}
	return byDir, nil
}

// countGoFiles returns how many *.go files sit directly in dir (not
// recursive) — location.files.
func countGoFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			n++
		}
	}
	return n
}

// sumLines returns the total line count across every *.go file directly in
// dir — metrics.loc. Best-effort: unreadable files are skipped rather than
// failing the whole generator.
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
		for sc.Scan() {
			total++
		}
		f.Close()
	}
	return total
}
