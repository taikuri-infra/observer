package gen

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// depcruiseOutput is the subset of dependency-cruiser's `--output-type json`
// schema FrontendGraphModel needs: the module list, each with its resolved
// source path and the modules it depends on.
type depcruiseOutput struct {
	Modules []depcruiseModule `json:"modules"`
}

type depcruiseModule struct {
	Source       string                `json:"source"`
	Dependencies []depcruiseDependency `json:"dependencies"`
}

type depcruiseDependency struct {
	Resolved        string   `json:"resolved"`
	DependencyTypes []string `json:"dependencyTypes"`
}

// nonLocalDepTypes are dependency-cruiser's dependencyTypes values for
// anything that isn't a file inside the scanned src tree (npm packages,
// Node core modules, …) — edges to these are dropped, since they'd be nodes
// with no source of their own (and dependency-cruiser's own graphs already
// treat them as leaves, not part of "your" module graph).
var nonLocalDepTypes = map[string]bool{
	"npm":          true,
	"npm-dev":      true,
	"npm-optional": true,
	"npm-peer":     true,
	"npm-bundled":  true,
	"npm-no-pkg":   true,
	"npm-unknown":  true,
	"core":         true,
	"unknown":      true,
	"deprecated":   true,
	"undetermined": true,
}

func isLocalDependency(types []string) bool {
	for _, t := range types {
		if nonLocalDepTypes[t] {
			return false
		}
	}
	return true
}

// isNodeModulesPath is a defense-in-depth check alongside isLocalDependency:
// dependency-cruiser tags the *edge* into a package as "npm", but once
// inside node_modules its *own* relative requires are tagged "local" (they
// are, from its own perspective) — so without this, one npm import would
// pull in that whole package's internal dependency graph as nodes. Run with
// `--exclude node_modules` (FrontendGraphModel) so depcruise never even
// traverses there; this only guards a fixture/future caller that omits it.
func isNodeModulesPath(p string) bool {
	return strings.Contains(p, "node_modules/")
}

// classifyFrontendKind derives a frontend module node's `kind` from its
// repo-relative path, per the design spec's "kind by folder": files under a
// "pages" or "app" directory are route/entry points — the frontend
// equivalent of a Go "cmd/" or "http" package — and reuse the existing
// "entry" kind; everything else gets the new "module" kind (added to the
// renderer's KIND map alongside entry/service/adapter/external/table).
func classifyFrontendKind(id string) string {
	for _, seg := range strings.Split(id, "/") {
		if seg == "pages" || seg == "app" {
			return "entry"
		}
	}
	return "module"
}

// ParseFrontendGraph builds a normalized graph.json from dependency-cruiser
// JSON output (depcruiseJSON) already captured elsewhere — kept separate
// from FrontendGraphModel (which shells out to depcruise itself) so it can
// be unit-tested against a fixture without depcruise installed. relModuleDir
// is the frontend package's repo-relative directory ("" or "." for the repo
// root); every node id is relModuleDir joined with the module's source path
// as depcruise reported it (already relative to the directory depcruise was
// run in), giving a repo-relative file path consistent with GraphModel's and
// ERGraphModel's location.path convention.
func ParseFrontendGraph(depcruiseJSON []byte, relModuleDir string) (*Graph, error) {
	var dc depcruiseOutput
	if err := json.Unmarshal(depcruiseJSON, &dc); err != nil {
		return nil, fmt.Errorf("parse depcruise json: %w", err)
	}

	relModuleDir = filepath.ToSlash(relModuleDir)
	toRepoPath := func(p string) string {
		p = filepath.ToSlash(p)
		if relModuleDir == "" || relModuleDir == "." {
			return p
		}
		return relModuleDir + "/" + p
	}

	seen := map[string]bool{}
	indeg := map[string]int{}
	outdeg := map[string]int{}
	var edges []GraphEdge
	for _, m := range dc.Modules {
		if m.Source == "" || isNodeModulesPath(m.Source) {
			continue
		}
		from := toRepoPath(m.Source)
		seen[from] = true
		dedupe := map[string]bool{}
		for _, d := range m.Dependencies {
			if d.Resolved == "" || !isLocalDependency(d.DependencyTypes) || isNodeModulesPath(d.Resolved) {
				continue
			}
			to := toRepoPath(d.Resolved)
			if to == from || dedupe[to] {
				continue
			}
			dedupe[to] = true
			seen[to] = true
			edges = append(edges, GraphEdge{From: from, To: to, Weight: 1})
			outdeg[from]++
			indeg[to]++
		}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	nodes := make([]GraphNode, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, GraphNode{
			ID:    id,
			Label: filepath.Base(id),
			Kind:  classifyFrontendKind(id),
			Metrics: GraphMetrics{
				Dependents: indeg[id],
				Imports:    outdeg[id],
			},
			Location:    GraphLocation{Path: id, Files: 1},
			Description: GraphDescription{Source: "none"},
			Links:       []GraphLink{},
		})
	}

	return &Graph{
		Meta: GraphMeta{
			Source:        "frontend-deps",
			Module:        relModuleDir,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			SizeBy:        "dependents",
			ColorBy:       "imports",
			DefaultLayout: "hierarchical",
		},
		Nodes: nodes,
		Edges: edges,
	}, nil
}

// FrontendGraphModel runs `depcruise --output-type json <src>` inside
// moduleDir and parses the result into a graph.json (via ParseFrontendGraph).
// relModuleDir is moduleDir's repo-relative path, used the same way
// GraphModel uses its module's — see ParseFrontendGraph.
func FrontendGraphModel(moduleDir, relModuleDir, src string) (*Graph, error) {
	cmd := exec.Command("depcruise", "--no-config", "--exclude", "node_modules", "--output-type", "json", src)
	cmd.Dir = moduleDir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("depcruise --output-type json %s in %s: %w\n%s", src, moduleDir, err, ee.Stderr)
		}
		return nil, fmt.Errorf("depcruise --output-type json %s in %s: %w", src, moduleDir, err)
	}
	return ParseFrontendGraph(out, relModuleDir)
}
