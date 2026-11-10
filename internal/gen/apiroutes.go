package gen

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Route is one Fiber route registration found by APIRoutes.
type Route struct {
	Method     string `json:"method"`
	Path       string `json:"path"`                 // full path incl. group prefixes, e.g. /api/v1/tenant
	Handler    string `json:"handler"`               // e.g. deps.TenantHandler.GetTenant (source text of last arg)
	Middleware string `json:"middleware,omitempty"` // comma-joined middle args, source text
	File       string `json:"file"`                 // repo-relative if possible, else module-relative
	Line       int    `json:"line"`
}

// fiberVerbs maps a Fiber route-registration method name (gofiber/fiber v2's
// *fiber.App / fiber.Router verb methods) to the HTTP method it emits.
var fiberVerbs = map[string]string{
	"Get":    "GET",
	"Post":   "POST",
	"Put":    "PUT",
	"Patch":  "PATCH",
	"Delete": "DELETE",
	"All":    "ALL",
}

// apirouteSkipDirs are directories APIRoutes never descends into.
var apirouteSkipDirs = map[string]bool{
	"vendor":   true,
	"testdata": true,
	".git":     true,
}

// APIRoutes statically parses every Go file under moduleDir with go/ast,
// resolves Fiber (gofiber/fiber v2) group-prefix chains, and returns all
// registered routes sorted by (path, method). repoRoot (may be "") is used
// only to make File paths repo-relative; pass moduleDir if you don't have a
// repo root.
//
// Approach: for each file, walk each top-level function body in source
// order (go/ast visits a block's statements in source order, so a single
// forward pass sees a Fiber group's `X := Y.Group("literal", ...)` /
// `X = Y.Group("literal", ...)` assignment before any route registered on
// X, as long as the assignment appears earlier in the source — which Fiber
// route-setup code always does). A per-function map tracks each group
// variable's resolved path prefix; unknown base identifiers (including the
// *fiber.App variable itself) default to prefix "". Route-registration
// calls (`base.Get("/path", ...mw, handler)` etc.) resolve the full path
// via that map, capture the last argument's source text as the handler and
// any middle arguments' source text as middleware (comma-joined), and
// record the call site's file:line.
func APIRoutes(repoRoot, moduleDir string) ([]Route, error) {
	var routes []Route
	fset := token.NewFileSet()

	err := filepath.WalkDir(moduleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != moduleDir && (apirouteSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			// Skip unparsable files rather than aborting the whole scan — one
			// broken file (e.g. a WIP branch) shouldn't hide every other route.
			return nil
		}
		relFile := relFilePath(path, repoRoot, moduleDir)
		routes = append(routes, parseFileRoutes(fset, file, relFile)...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	routes = dedupRoutes(routes)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

// parseFileRoutes walks every top-level function declaration in file,
// resolving Fiber group prefixes and emitting a Route per verb call. Each
// function gets its own fresh prefix map — group variable names aren't
// assumed to mean the same thing across two different functions in the
// same file.
func parseFileRoutes(fset *token.FileSet, file *ast.File, relFile string) []Route {
	var routes []Route
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		prefix := map[string]string{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				if len(stmt.Lhs) == 1 && len(stmt.Rhs) == 1 {
					if p, ok := groupPrefix(fset, stmt.Rhs[0], prefix); ok {
						prefix[exprText(fset, stmt.Lhs[0])] = p
					}
				}
			case *ast.CallExpr:
				if r, ok := routeFromCall(fset, stmt, prefix, relFile); ok {
					routes = append(routes, r)
				}
			}
			return true
		})
	}
	return routes
}

// groupPrefix reports whether rhs is a Fiber `<base>.Group("literal", ...)`
// call with a string-literal first argument, and if so returns the new
// group's resolved path prefix (base's own prefix, defaulting to "" when
// unknown, joined with the literal).
func groupPrefix(fset *token.FileSet, rhs ast.Expr, prefix map[string]string) (string, bool) {
	call, ok := rhs.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Group" {
		return "", false
	}
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := stringLiteral(call.Args[0])
	if !ok {
		return "", false
	}
	base := exprText(fset, sel.X)
	return cleanJoin(prefix[base], lit), true
}

// routeFromCall reports whether call is a Fiber verb registration
// (`<base>.Get/Post/Put/Patch/Delete/All("/path", ...middleware, handler)`)
// and if so returns the resolved Route.
func routeFromCall(fset *token.FileSet, call *ast.CallExpr, prefix map[string]string, file string) (Route, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return Route{}, false
	}
	method, ok := fiberVerbs[sel.Sel.Name]
	if !ok {
		return Route{}, false
	}
	// Need at least a path literal and a handler.
	if len(call.Args) < 2 {
		return Route{}, false
	}
	lit, ok := stringLiteral(call.Args[0])
	if !ok {
		return Route{}, false
	}
	// Fiber route paths always start with "/" (every literal in
	// servicePlus's router.go does, and Fiber's own router requires it).
	// This filters out same-named, same-shape calls that aren't route
	// registrations at all — most notably *fiber.Ctx.Get(headerName,
	// defaultValue), which shares the "Get" method name and a 2-arg,
	// string-literal-first-arg shape with the route-registration verb but
	// reads a request header (e.g. c.Get("X-Forwarded-Host", c.Hostname())).
	if !strings.HasPrefix(lit, "/") {
		return Route{}, false
	}
	base := exprText(fset, sel.X)
	fullPath := cleanJoin(prefix[base], lit)

	handlerArg := call.Args[len(call.Args)-1]
	handlerText := exprText(fset, handlerArg)

	var mw []string
	for _, a := range call.Args[1 : len(call.Args)-1] {
		mw = append(mw, exprText(fset, a))
	}

	pos := fset.Position(call.Pos())
	return Route{
		Method:     method,
		Path:       fullPath,
		Handler:    handlerText,
		Middleware: strings.Join(mw, ", "),
		File:       file,
		Line:       pos.Line,
	}, true
}

// stringLiteral reports whether e is a string literal (interpreted or raw)
// and, if so, returns its unquoted value.
func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// exprText renders e's exact source text via go/printer — used both as the
// map key for a group's base identifier/expression and as the captured
// source text for a route's handler/middleware arguments.
func exprText(fset *token.FileSet, e ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, e); err != nil {
		return ""
	}
	return buf.String()
}

// cleanJoin concatenates a group prefix and a route/group literal into a
// clean path: collapses repeated slashes, guarantees a single leading
// slash, and strips any trailing slash except for the bare root "/".
func cleanJoin(prefix, literal string) string {
	joined := prefix + literal
	if joined == "" {
		return "/"
	}
	for strings.Contains(joined, "//") {
		joined = strings.ReplaceAll(joined, "//", "/")
	}
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	if len(joined) > 1 && strings.HasSuffix(joined, "/") {
		joined = strings.TrimRight(joined, "/")
		if joined == "" {
			joined = "/"
		}
	}
	return joined
}

// relFilePath renders path relative to repoRoot when possible (so File
// matches the repo-relative paths every other generator emits), falling
// back to relative-to-moduleDir, then the raw path.
func relFilePath(path, repoRoot, moduleDir string) string {
	if repoRoot != "" {
		if rel, err := filepath.Rel(repoRoot, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if rel, err := filepath.Rel(moduleDir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

// dedupRoutes drops exact duplicate Route entries (same method, path,
// handler, file, and line), preserving first-seen order.
func dedupRoutes(routes []Route) []Route {
	seen := map[Route]bool{}
	out := make([]Route, 0, len(routes))
	for _, r := range routes {
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}
