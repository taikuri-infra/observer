package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDepcruiseArgsTSConfig: when the module has a tsconfig.json, the
// depcruise args must include `--ts-config tsconfig.json` so path aliases
// (@/components/...) resolve and intra-app edges aren't silently dropped;
// node_modules stays excluded either way. Without a tsconfig, --ts-config
// must be absent.
func TestDepcruiseArgsTSConfig(t *testing.T) {
	withTS := t.TempDir()
	if err := os.WriteFile(filepath.Join(withTS, "tsconfig.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := depcruiseArgs(withTS, "src")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--ts-config tsconfig.json") {
		t.Fatalf("args = %v, want --ts-config tsconfig.json present", args)
	}
	if !strings.Contains(joined, "--exclude node_modules") {
		t.Fatalf("args = %v, want --exclude node_modules present", args)
	}
	if args[len(args)-1] != "src" {
		t.Fatalf("args = %v, want src as the final positional", args)
	}

	withoutTS := t.TempDir()
	args2 := depcruiseArgs(withoutTS, "src")
	if strings.Contains(strings.Join(args2, " "), "--ts-config") {
		t.Fatalf("args = %v, want no --ts-config when tsconfig.json is absent", args2)
	}

	// stripTSConfig removes the flag AND its value, leaving an otherwise
	// identical arg vector (the fallback when a --ts-config run fails).
	stripped := stripTSConfig(args)
	joinedStripped := strings.Join(stripped, " ")
	if strings.Contains(joinedStripped, "--ts-config") || strings.Contains(joinedStripped, "tsconfig.json") {
		t.Fatalf("stripTSConfig(%v) = %v, still has --ts-config/value", args, stripped)
	}
	if len(stripped) != len(args)-2 {
		t.Fatalf("stripTSConfig dropped %d args, want exactly 2 (flag + value)", len(args)-len(stripped))
	}
	if !strings.Contains(joinedStripped, "--exclude node_modules") || stripped[len(stripped)-1] != "src" {
		t.Fatalf("stripTSConfig mangled the rest of the args: %v", stripped)
	}
}

// fixtureDepcruiseJSON is a minimal dependency-cruiser `--output-type json`
// payload: src/pages/index.tsx (a route entry) imports src/components/Foo.tsx
// (a component) and the npm package "react" (dropped — not local); Foo.tsx
// imports src/lib/util.ts. Shaped like the real schema's "modules" array,
// trimmed to only the fields ParseFrontendGraph reads.
const fixtureDepcruiseJSON = `{
  "modules": [
    {
      "source": "src/pages/index.tsx",
      "dependencies": [
        {"resolved": "src/components/Foo.tsx", "dependencyTypes": ["local"]},
        {"resolved": "node_modules/react/index.js", "dependencyTypes": ["npm"]}
      ]
    },
    {
      "source": "src/components/Foo.tsx",
      "dependencies": [
        {"resolved": "src/lib/util.ts", "dependencyTypes": ["local"]}
      ]
    },
    {
      "source": "src/lib/util.ts",
      "dependencies": []
    }
  ]
}`

func TestParseFrontendGraph(t *testing.T) {
	g, err := ParseFrontendGraph([]byte(fixtureDepcruiseJSON), "apps/web")
	if err != nil {
		t.Fatalf("ParseFrontendGraph: %v", err)
	}

	if g.Meta.Source != "frontend-deps" || g.Meta.Module != "apps/web" {
		t.Errorf("meta = %+v", g.Meta)
	}
	if g.Meta.SizeBy != "dependents" || g.Meta.ColorBy != "imports" {
		t.Errorf("meta sizeBy/colorBy = %+v", g.Meta)
	}

	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	page, ok := byID["apps/web/src/pages/index.tsx"]
	if !ok {
		t.Fatalf("missing page node: %+v", g.Nodes)
	}
	if page.Kind != "entry" {
		t.Errorf("page kind = %q, want entry (under pages/)", page.Kind)
	}
	if page.Metrics.Imports != 1 {
		t.Errorf("page imports = %d, want 1 (react dropped as non-local)", page.Metrics.Imports)
	}
	if page.Metrics.Dependents != 0 {
		t.Errorf("page dependents = %d, want 0", page.Metrics.Dependents)
	}
	if page.Location.Path != "apps/web/src/pages/index.tsx" {
		t.Errorf("page location.path = %q", page.Location.Path)
	}

	foo, ok := byID["apps/web/src/components/Foo.tsx"]
	if !ok {
		t.Fatalf("missing Foo node: %+v", g.Nodes)
	}
	if foo.Kind != "module" {
		t.Errorf("Foo kind = %q, want module", foo.Kind)
	}
	if foo.Metrics.Dependents != 1 || foo.Metrics.Imports != 1 {
		t.Errorf("Foo metrics = %+v, want dependents=1 imports=1", foo.Metrics)
	}

	util, ok := byID["apps/web/src/lib/util.ts"]
	if !ok {
		t.Fatalf("missing util node: %+v", g.Nodes)
	}
	if util.Metrics.Dependents != 1 || util.Metrics.Imports != 0 {
		t.Errorf("util metrics = %+v, want dependents=1 imports=0", util.Metrics)
	}

	// The npm "react" dependency must never appear as a node.
	for id := range byID {
		if id == "node_modules/react/index.js" || id == "apps/web/node_modules/react/index.js" {
			t.Fatalf("npm dependency leaked as a node: %q", id)
		}
	}

	wantEdges := map[[2]string]bool{
		{"apps/web/src/pages/index.tsx", "apps/web/src/components/Foo.tsx"}: true,
		{"apps/web/src/components/Foo.tsx", "apps/web/src/lib/util.ts"}:     true,
	}
	if len(g.Edges) != len(wantEdges) {
		t.Fatalf("edges = %+v, want %d", g.Edges, len(wantEdges))
	}
	for _, e := range g.Edges {
		if !wantEdges[[2]string{e.From, e.To}] {
			t.Errorf("unexpected edge %+v", e)
		}
	}
}

// TestParseFrontendGraphRepoRoot covers relModuleDir == "" / "." (a frontend
// package living at the repo root): node ids must be the bare depcruise
// source path, with no leading "./" or "/" join artifact.
func TestParseFrontendGraphRepoRoot(t *testing.T) {
	g, err := ParseFrontendGraph([]byte(fixtureDepcruiseJSON), ".")
	if err != nil {
		t.Fatalf("ParseFrontendGraph: %v", err)
	}
	found := false
	for _, n := range g.Nodes {
		if n.ID == "src/pages/index.tsx" {
			found = true
		}
		if n.ID[0] == '/' || len(n.ID) > 1 && n.ID[:2] == "./" {
			t.Errorf("node id has a join artifact: %q", n.ID)
		}
	}
	if !found {
		t.Fatalf("missing src/pages/index.tsx node: %+v", g.Nodes)
	}
}

// TestParseFrontendGraphExcludesNodeModules guards against depcruise's own
// behavior once it's followed a dependency chain into node_modules (e.g.
// --exclude node_modules wasn't honored, or a future caller omits it): even
// though a node_modules-internal require is tagged "local" by
// dependency-cruiser (true from its own perspective), it must never surface
// as a node or edge.
func TestParseFrontendGraphExcludesNodeModules(t *testing.T) {
	const withNodeModules = `{
  "modules": [
    {
      "source": "src/pages/index.tsx",
      "dependencies": [
        {"resolved": "node_modules/.pnpm/lucide-react@1.0.0/node_modules/lucide-react/dist/cjs/lucide-react.js", "dependencyTypes": ["npm", "import"]}
      ]
    },
    {
      "source": "node_modules/.pnpm/lucide-react@1.0.0/node_modules/lucide-react/dist/cjs/lucide-react.js",
      "dependencies": [
        {"resolved": "node_modules/.pnpm/lucide-react@1.0.0/node_modules/lucide-react/dist/cjs/chunk.js", "dependencyTypes": ["local"]}
      ]
    }
  ]
}`
	g, err := ParseFrontendGraph([]byte(withNodeModules), "apps/web")
	if err != nil {
		t.Fatalf("ParseFrontendGraph: %v", err)
	}
	for _, n := range g.Nodes {
		if strings.Contains(n.ID, "node_modules") {
			t.Errorf("node_modules path leaked as a node: %q", n.ID)
		}
	}
	for _, e := range g.Edges {
		if strings.Contains(e.From, "node_modules") || strings.Contains(e.To, "node_modules") {
			t.Errorf("node_modules path leaked into an edge: %+v", e)
		}
	}
	if len(g.Nodes) != 1 || g.Nodes[0].ID != "apps/web/src/pages/index.tsx" {
		t.Fatalf("nodes = %+v, want only the entry page", g.Nodes)
	}
}

func TestParseFrontendGraphInvalidJSON(t *testing.T) {
	if _, err := ParseFrontendGraph([]byte("not json"), "apps/web"); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}
