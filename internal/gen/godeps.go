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
)

// goListPkg is the subset of `go list -json` fields GoDeps needs.
type goListPkg struct {
	ImportPath string
	Imports    []string
}

// GoDeps builds a Graphviz DOT dependency graph of moduleDir's own packages
// (one node per package, one edge per direct import), scoped to the
// module itself — stdlib and third-party imports are dropped to keep the
// graph readable. Package names are rendered relative to the module path
// (e.g. "internal/domain" rather than the full import path).
func GoDeps(moduleDir string) (string, error) {
	modulePath, err := readModulePath(moduleDir)
	if err != nil {
		return "", err
	}

	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("go list -json ./... in %s: %w\n%s", moduleDir, err, ee.Stderr)
		}
		return "", fmt.Errorf("go list -json ./... in %s: %w", moduleDir, err)
	}

	type edge struct{ from, to string }
	var edges []edge
	nodes := map[string]bool{}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var pkg goListPkg
		if err := dec.Decode(&pkg); err != nil {
			return "", fmt.Errorf("decode go list output: %w", err)
		}
		from := trimModulePrefix(pkg.ImportPath, modulePath)
		nodes[from] = true
		for _, imp := range pkg.Imports {
			if !strings.HasPrefix(imp, modulePath) {
				continue // stdlib or third-party: dropped to stay readable
			}
			to := trimModulePrefix(imp, modulePath)
			if to == from {
				continue
			}
			edges = append(edges, edge{from: from, to: to})
			nodes[to] = true
		}
	}

	nodeNames := make([]string, 0, len(nodes))
	for n := range nodes {
		nodeNames = append(nodeNames, n)
	}
	sort.Strings(nodeNames)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
	})

	var b strings.Builder
	b.WriteString("digraph godeps {\n")
	b.WriteString("    rankdir=LR;\n")
	for _, n := range nodeNames {
		fmt.Fprintf(&b, "    %q;\n", n)
	}
	for _, e := range edges {
		fmt.Fprintf(&b, "    %q -> %q;\n", e.from, e.to)
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// readModulePath reads the "module <path>" directive from moduleDir/go.mod.
func readModulePath(moduleDir string) (string, error) {
	f, err := os.Open(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("open go.mod in %s: %w", moduleDir, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no module directive found in %s/go.mod", moduleDir)
}

// trimModulePrefix renders an import path relative to the module root: the
// module path itself becomes ".", and "<module>/internal/x" becomes
// "internal/x".
func trimModulePrefix(importPath, modulePath string) string {
	rel := strings.TrimPrefix(importPath, modulePath)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "."
	}
	return rel
}
