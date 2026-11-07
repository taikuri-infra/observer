package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeModule(t *testing.T, dir string) {
	t.Helper()
	mustWrite := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("go.mod", "module example.com/tm\n\ngo 1.22\n")
	mustWrite("a/a.go", "package a\n\nimport \"example.com/tm/b\"\n\nfunc Use() { b.Hello() }\n")
	mustWrite("b/b.go", "package b\n\nfunc Hello() {}\n")
}

func TestGoDeps(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir)

	dot, err := GoDeps(dir)
	if err != nil {
		t.Fatalf("GoDeps: %v", err)
	}
	if !strings.HasPrefix(dot, "digraph godeps {") {
		t.Fatalf("missing digraph header: %s", dot)
	}
	if !strings.Contains(dot, `"a" -> "b";`) {
		t.Fatalf("missing a -> b edge: %s", dot)
	}
}

func TestGoDepsMissingGoMod(t *testing.T) {
	dir := t.TempDir()
	if _, err := GoDeps(dir); err == nil {
		t.Fatal("expected error for a directory without go.mod")
	}
}
