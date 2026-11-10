package gen

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFuncLocFixture lays out a tiny module with a plain function and a
// pointer-receiver method, both on known source lines (counted by hand
// below, from the literal content written here — not borrowed from
// another test's fixture, so a future edit to that fixture can't silently
// desync these line numbers).
func writeFuncLocFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/locfix\n\ngo 1.22\n")
	// Line:      1              2  3                     4  5                   6  7  8              9  10                 11
	write("internal/widget/widget.go", `package widget

type Widget struct{}

func New() *Widget {
	return &Widget{}
}

func (w *Widget) Render() string {
	return "widget"
}
`)
	return dir
}

func TestEnrichCallGraphLocations(t *testing.T) {
	moduleDir := writeFuncLocFixture(t)
	g := &Graph{Nodes: []GraphNode{
		{ID: "example.com/locfix/internal/widget.New", Location: GraphLocation{Path: "internal/widget"}},
		{ID: "(*example.com/locfix/internal/widget.Widget).Render", Location: GraphLocation{Path: "internal/widget"}},
		// Mismatched receiver spelling (value, not pointer) — must still
		// resolve via the byPkgName fallback, not the exact id match.
		{ID: "(example.com/locfix/internal/widget.Widget).Render", Location: GraphLocation{Path: "internal/widget"}},
		// A closure: no FuncDecl exists for it; must stay unresolved.
		{ID: "example.com/locfix/internal/widget.New$1", Location: GraphLocation{Path: "internal/widget"}},
	}}

	unresolved, err := enrichCallGraphLocations(g, moduleDir)
	if err != nil {
		t.Fatalf("enrichCallGraphLocations: %v", err)
	}

	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	newFn := byID["example.com/locfix/internal/widget.New"]
	if newFn.Location.PrimaryFile != "internal/widget/widget.go" {
		t.Errorf("New PrimaryFile = %q, want internal/widget/widget.go", newFn.Location.PrimaryFile)
	}
	if newFn.Location.LineStart != 5 || newFn.Location.LineEnd != 7 {
		t.Errorf("New lines = %d-%d, want 5-7", newFn.Location.LineStart, newFn.Location.LineEnd)
	}

	method := byID["(*example.com/locfix/internal/widget.Widget).Render"]
	if method.Location.PrimaryFile != "internal/widget/widget.go" {
		t.Errorf("Render PrimaryFile = %q, want internal/widget/widget.go", method.Location.PrimaryFile)
	}
	if method.Location.LineStart != 9 || method.Location.LineEnd != 11 {
		t.Errorf("Render lines = %d-%d, want 9-11", method.Location.LineStart, method.Location.LineEnd)
	}

	fallback := byID["(example.com/locfix/internal/widget.Widget).Render"]
	if fallback.Location.PrimaryFile != "internal/widget/widget.go" {
		t.Errorf("fallback-matched Render PrimaryFile = %q, want internal/widget/widget.go", fallback.Location.PrimaryFile)
	}
	if fallback.Location.LineStart != 9 || fallback.Location.LineEnd != 11 {
		t.Errorf("fallback-matched Render lines = %d-%d, want 9-11", fallback.Location.LineStart, fallback.Location.LineEnd)
	}

	closure := byID["example.com/locfix/internal/widget.New$1"]
	if closure.Location.PrimaryFile != "" || closure.Location.LineStart != 0 {
		t.Errorf("closure should stay unresolved, got %+v", closure.Location)
	}
	foundClosure := false
	for _, id := range unresolved {
		if id == closure.ID {
			foundClosure = true
		}
	}
	if !foundClosure {
		t.Errorf("expected closure id in unresolved list, got %v", unresolved)
	}
	// The mismatched-receiver node must NOT be reported unresolved: it was
	// resolved, just via the fallback path rather than the exact id.
	for _, id := range unresolved {
		if id == fallback.ID {
			t.Errorf("fallback-resolved node %q reported as unresolved: %v", id, unresolved)
		}
	}
}

// TestEnrichCallGraphLocationsAmbiguousFallbackStaysUnresolved proves an
// ambiguous byPkgName key (two methods named "Do" on different types in
// the same package) is dropped rather than resolving to whichever
// declaration happened to be parsed first.
func TestEnrichCallGraphLocationsAmbiguousFallbackStaysUnresolved(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/ambig\n\ngo 1.22\n")
	write("internal/x/x.go", `package x

type A struct{}
type B struct{}

func (a *A) Do() {}

func (b *B) Do() {}
`)

	// An id whose receiver type ("C") matches neither A nor B — the exact
	// id lookup must miss, and the byPkgName fallback key
	// ("example.com/ambig/internal/x#Do") must have been dropped as
	// ambiguous rather than pointing at A's or B's Do.
	g := &Graph{Nodes: []GraphNode{
		{ID: "(*example.com/ambig/internal/x.C).Do", Location: GraphLocation{Path: "internal/x"}},
	}}

	unresolved, err := enrichCallGraphLocations(g, dir)
	if err != nil {
		t.Fatalf("enrichCallGraphLocations: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0] != g.Nodes[0].ID {
		t.Fatalf("unresolved = %v, want [%q]", unresolved, g.Nodes[0].ID)
	}
	if g.Nodes[0].Location.PrimaryFile != "" {
		t.Errorf("ambiguous fallback resolved to %q, want unresolved", g.Nodes[0].Location.PrimaryFile)
	}
}
