package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindMainPackageDirs covers the shape servicePlus's services/api
// module actually has (the reason this helper exists at all): more than
// one "package main" directory under a single Go module — cmd/server plus
// a one-off scripts/ tool — which is exactly the case go-callvis itself
// can't handle in one run ("found multiple packages with name: main").
func TestFindMainPackageDirs(t *testing.T) {
	mod := t.TempDir()
	writeFile(t, mod, "go.mod", "module example.com/api\n\ngo 1.22\n")
	writeFile(t, mod, "cmd/server/main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, mod, "scripts/migrate/main.go", "package main\n\nfunc main() {}\n")
	// Not a main package: an ordinary application package.
	writeFile(t, mod, "internal/app/app.go", "package app\n\nfunc Run() {}\n")
	// A test file naming "package main" must not count on its own.
	writeFile(t, mod, "internal/testonly/main_test.go", "package main\n\nfunc TestX() {}\n")
	// A nested module must not be descended into even if it has a main.
	writeFile(t, mod, "vendor-like/nested/go.mod", "module example.com/nested\n\ngo 1.22\n")
	writeFile(t, mod, "vendor-like/nested/main.go", "package main\n\nfunc main() {}\n")
	// node_modules/.git etc. must be skipped even if they somehow contain .go files.
	writeFile(t, mod, "node_modules/weird/main.go", "package main\n\nfunc main() {}\n")

	got, err := findMainPackageDirs(mod)
	if err != nil {
		t.Fatalf("findMainPackageDirs: %v", err)
	}
	want := []string{"cmd/server", "scripts/migrate"}
	if len(got) != len(want) {
		t.Fatalf("findMainPackageDirs = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("findMainPackageDirs[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// TestFindMainPackageDirsSkipsBuildIgnore proves a directory whose only
// "package main" file is build-constrained out of a normal build (the
// classic `//go:build ignore` tool file) is NOT returned — targeting it
// would spawn a go-callvis run that fails every time, leaving a
// permanently-red Call graph tab.
func TestFindMainPackageDirsSkipsBuildIgnore(t *testing.T) {
	mod := t.TempDir()
	writeFile(t, mod, "go.mod", "module example.com/api\n\ngo 1.22\n")
	// A real buildable main — must be found.
	writeFile(t, mod, "cmd/server/main.go", "package main\n\nfunc main() {}\n")
	// A //go:build ignore tool file as the ONLY .go in its dir — must be skipped.
	writeFile(t, mod, "tools/gen/gen.go", "//go:build ignore\n\npackage main\n\nfunc main() {}\n")

	got, err := findMainPackageDirs(mod)
	if err != nil {
		t.Fatalf("findMainPackageDirs: %v", err)
	}
	for _, d := range got {
		if d == "tools/gen" {
			t.Fatalf("//go:build ignore main was returned: %v", got)
		}
	}
	found := false
	for _, d := range got {
		if d == "cmd/server" {
			found = true
		}
	}
	if !found {
		t.Fatalf("buildable cmd/server main missing: %v", got)
	}
}

func TestFindMainPackageDirsNoMains(t *testing.T) {
	mod := t.TempDir()
	writeFile(t, mod, "go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeFile(t, mod, "pkg/thing.go", "package pkg\n\nfunc Thing() {}\n")

	got, err := findMainPackageDirs(mod)
	if err != nil {
		t.Fatalf("findMainPackageDirs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no main packages, got %v", got)
	}
}
