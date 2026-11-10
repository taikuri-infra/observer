package gen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"strings"
)

// testednessProxy returns a 0-100 STATIC proxy for a Go package's test
// coverage: the ratio of test functions (Test*/Benchmark*/Example*/Fuzz* in
// _test.go files) to exported declared functions and methods in the
// package's non-test source, capped at 100.
//
// It is NOT real line coverage — no tests are run, nothing is instrumented.
// It is a cheap, reliable, side-effect-free signal for "which packages have
// little or no test code", used to color the Service map's coverage overlay
// so an under-tested package (common in fast AI-assisted/"vibe" coding)
// stands out. A package with no exported functions has no meaningful
// testable surface, so it returns 100 rather than being flagged as a gap.
//
// dir is the package's directory; goFiles are its base filenames (source
// AND test files), exactly as `go list` reports GoFiles+TestGoFiles+
// XTestGoFiles. Unparsable files are skipped, never fatal.
func testednessProxy(dir string, goFiles []string) int {
	fset := token.NewFileSet()
	exported := 0
	testFuncs := 0
	for _, name := range goFiles {
		isTest := strings.HasSuffix(name, "_test.go")
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if isTest {
				if isTestFuncName(fd.Name.Name) {
					testFuncs++
				}
				continue
			}
			if ast.IsExported(fd.Name.Name) {
				exported++
			}
		}
	}
	if exported == 0 {
		return 100
	}
	pct := int(math.Round(100 * float64(testFuncs) / float64(exported)))
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

// isTestFuncName reports whether name is one of Go's recognized test entry
// points (go test only runs funcs with these prefixes).
func isTestFuncName(name string) bool {
	for _, p := range []string{"Test", "Benchmark", "Example", "Fuzz"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
