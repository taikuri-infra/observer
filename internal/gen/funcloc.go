package gen

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// funcLoc is a resolved function/method declaration's location: a file
// path relative to the module root ("/"-slashed) and its 1-based
// [lineStart, lineEnd] span — the `func` keyword through the closing brace.
type funcLoc struct {
	file      string
	lineStart int
	lineEnd   int
}

// funcIndex is the result of parsing a module's own packages with go/ast:
// byID matches go-callvis's exact DOT node-id convention (see
// ParseCallGraphDOT's doc comment: "pkg/path.Func" / "(*pkg/path.Type).Method"
// / "(pkg/path.Type).Method"); byPkgName is a looser "importPath#Name"
// fallback for when the exact id doesn't line up with the AST (e.g. a
// generic receiver spelled differently, or a receiver type go-callvis
// renders in a way funcDeclID doesn't reproduce exactly). An ambiguous
// byPkgName key — two declarations sharing one, e.g. two types in the same
// package both with a method named "Do" — is dropped entirely rather than
// risk pointing at the wrong one.
type funcIndex struct {
	byID      map[string]funcLoc
	byPkgName map[string]funcLoc
}

// listModulePackages runs `go list -json -deps ./...` in moduleDir and
// returns every package *within the module itself* (stdlib/third-party
// deps are dropped — their source isn't something buildFuncIndex needs to
// walk, and go-callvis's own location comment already notes third-party
// call-graph nodes keep their full import path as location.path, never
// file:line), keyed by full import path.
func listModulePackages(moduleDir string) (map[string]goListDepsPkg, error) {
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
	pkgs := map[string]goListDepsPkg{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p goListDepsPkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if !underModule(p.ImportPath, modulePath) {
			continue
		}
		pkgs[p.ImportPath] = p
	}
	return pkgs, nil
}

// funcDeclID renders fd's go-callvis-style DOT node id: "importPath.Name"
// for a plain function, "(*importPath.Type).Name" / "(importPath.Type).Name"
// for a method (pointer-ness taken from the receiver's own type
// expression). "" is returned when the receiver's base type name can't be
// determined syntactically — defensive; every receiver this repo's own
// fixtures (and go-callvis's expected input, ordinary Go source) use is an
// identifier, optionally wrapped in a pointer and/or generic
// type-parameter brackets.
func funcDeclID(importPath string, fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return importPath + "." + fd.Name.Name
	}
	texpr := fd.Recv.List[0].Type
	ptr := false
	if se, ok := texpr.(*ast.StarExpr); ok {
		ptr = true
		texpr = se.X
	}
	typeName := recvBaseName(texpr)
	if typeName == "" {
		return ""
	}
	if ptr {
		return "(*" + importPath + "." + typeName + ")." + fd.Name.Name
	}
	return "(" + importPath + "." + typeName + ")." + fd.Name.Name
}

// recvBaseName extracts a receiver type expression's base identifier,
// unwrapping a generic receiver's type-parameter brackets ("T[P]" /
// "T[P1, P2]") since go-callvis's node ids only ever carry the bare type
// name, never its instantiation.
func recvBaseName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvBaseName(t.X)
	case *ast.IndexListExpr:
		return recvBaseName(t.X)
	default:
		return ""
	}
}

// buildFuncIndex parses every non-test *.go file in moduleDir's own
// packages and indexes each top-level FuncDecl (plain function or method)
// by both the exact go-callvis id (funcDeclID) and the looser
// "importPath#Name" fallback key. Test files are never passed to it in the
// first place (goListDepsPkg.GoFiles excludes them) — go-callvis builds its
// call graph from a `package main` entrypoint, so a _test.go function is
// never a reachable call-graph node.
func buildFuncIndex(moduleDir string) (*funcIndex, error) {
	// modulePath + trimModulePrefix (not filepath.Rel against moduleDir)
	// derives each file's module-relative path: `go list`'s own Dir field
	// resolves symlinks (e.g. macOS's /tmp -> /private/var/folders), so a
	// caller-supplied moduleDir under a symlinked path would otherwise
	// make filepath.Rel produce a path full of "../" instead of a clean
	// module-relative one. Import-path trimming sidesteps the mismatch
	// entirely, the same way GraphModel derives location.path.
	modulePath, err := readModulePath(moduleDir)
	if err != nil {
		return nil, err
	}
	pkgs, err := listModulePackages(moduleDir)
	if err != nil {
		return nil, err
	}
	idx := &funcIndex{byID: map[string]funcLoc{}, byPkgName: map[string]funcLoc{}}
	ambiguous := map[string]bool{}
	fset := token.NewFileSet()
	for importPath, pkg := range pkgs {
		pkgRel := trimModulePrefix(importPath, modulePath)
		for _, name := range pkg.GoFiles {
			full := filepath.Join(pkg.Dir, name)
			src, err := os.ReadFile(full)
			if err != nil {
				continue // best-effort: an unreadable file just stays unresolved
			}
			f, err := parser.ParseFile(fset, full, src, 0)
			if err != nil {
				continue // best-effort: a file that fails to parse stays unresolved
			}
			rel := name
			if pkgRel != "." {
				rel = pkgRel + "/" + name
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				loc := funcLoc{
					file:      rel,
					lineStart: fset.Position(fd.Pos()).Line,
					lineEnd:   fset.Position(fd.End()).Line,
				}
				if id := funcDeclID(importPath, fd); id != "" {
					idx.byID[id] = loc
				}
				key := importPath + "#" + fd.Name.Name
				if _, exists := idx.byPkgName[key]; exists {
					ambiguous[key] = true
				} else {
					idx.byPkgName[key] = loc
				}
			}
		}
	}
	for key := range ambiguous {
		delete(idx.byPkgName, key)
	}
	return idx, nil
}

// enrichCallGraphLocations resolves each of g's "func" nodes to a real
// file + line range by parsing moduleDir's own packages with go/ast
// (buildFuncIndex): matching first by go-callvis's exact id convention,
// falling back to a package+name match when the exact id doesn't line up
// (e.g. a generic receiver spelled differently). Closures (ids ending
// "$N" — see ParseCallGraphDOT's doc comment) have no FuncDecl to match
// against and are deliberately left unresolved: their location stays
// package-only, same as every func node before this enrichment existed.
//
// It returns the ids it could NOT resolve (callers may want to report the
// gap); an individual unresolved node is never an error, only a `go list`
// failure (e.g. a broken go.mod) is.
func enrichCallGraphLocations(g *Graph, moduleDir string) ([]string, error) {
	idx, err := buildFuncIndex(moduleDir)
	if err != nil {
		return nil, err
	}
	var unresolved []string
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if loc, ok := idx.byID[n.ID]; ok {
			n.Location.PrimaryFile = loc.file
			n.Location.LineStart = loc.lineStart
			n.Location.LineEnd = loc.lineEnd
			continue
		}
		name := callGraphFallbackLabel(n.ID)
		if strings.Contains(name, "$") {
			// A closure: there is no FuncDecl for it at all (it's an
			// ast.FuncLit nested inside one), so even the loose fallback
			// can't help — stay unresolved.
			unresolved = append(unresolved, n.ID)
			continue
		}
		key := splitFuncPkgPath(n.ID) + "#" + name
		if loc, ok := idx.byPkgName[key]; ok {
			n.Location.PrimaryFile = loc.file
			n.Location.LineStart = loc.lineStart
			n.Location.LineEnd = loc.lineEnd
			continue
		}
		unresolved = append(unresolved, n.ID)
	}
	return unresolved, nil
}
