package gen

import (
	"testing"
)

// TestChangedGraphModel reuses writeFixtureModule's 3-package fixture
// (cmd/server -> internal/application/foo -> internal/adapters/stripe),
// then branches off it and touches only foo/service.go — mirroring
// TestChanges' pattern of a real feature branch so the merge-base diff is
// non-trivial. Only the foo package should come back Changed.
func TestChangedGraphModel(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)

	run(t, repo, "checkout", "-q", "-b", "feature")
	if err := writeFile(repo+"/services/api/internal/application/foo/service.go", `// Package foo implements the foo use cases.
//
//observer:summary Orchestrates foo end to end.
package foo

import "example.com/fixture/internal/adapters/stripe"

func Run() { stripe.Charge() /* feature-branch tweak */ }
`); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "feature: tweak foo")

	g, err := ChangedGraphModel(repo, moduleDir, "main")
	if err != nil {
		t.Fatalf("ChangedGraphModel: %v", err)
	}

	if g.Meta.Source != "changes" || g.Meta.Base != "main" {
		t.Fatalf("meta = %+v", g.Meta)
	}
	if !g.Meta.AnyChanged {
		t.Fatalf("meta.AnyChanged = false, want true")
	}

	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	foo := byID["internal/application/foo"]
	if !foo.Changed {
		t.Fatalf("foo.Changed = false, want true: %+v", foo)
	}
	wantFile := "services/api/internal/application/foo/service.go"
	if len(foo.ChangedFiles) != 1 || foo.ChangedFiles[0] != wantFile {
		t.Fatalf("foo.ChangedFiles = %+v, want [%q]", foo.ChangedFiles, wantFile)
	}

	for _, id := range []string{"cmd/server", "internal/adapters/stripe"} {
		if n := byID[id]; n.Changed {
			t.Errorf("%s.Changed = true, want false (untouched on the feature branch): %+v", id, n)
		}
	}
}

// TestChangedGraphModelNoChanges proves a branch with no diff vs base comes
// back with every node unflagged and meta.AnyChanged false, so the renderer
// knows to fall back to its "no changes vs base" note instead of dimming
// everything.
func TestChangedGraphModelNoChanges(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)

	g, err := ChangedGraphModel(repo, moduleDir, "main")
	if err != nil {
		t.Fatalf("ChangedGraphModel: %v", err)
	}
	if g.Meta.AnyChanged {
		t.Fatalf("meta.AnyChanged = true, want false (HEAD is main itself)")
	}
	for _, n := range g.Nodes {
		if n.Changed {
			t.Errorf("node %s.Changed = true, want false: %+v", n.ID, n)
		}
	}
}

// TestChangedGraphModelNoMergeBase mirrors TestChangesNoMergeBase: a base
// branch that doesn't exist locally must degrade to "nothing changed"
// rather than failing the generator.
func TestChangedGraphModelNoMergeBase(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)
	g, err := ChangedGraphModel(repo, moduleDir, "does-not-exist")
	if err != nil {
		t.Fatalf("ChangedGraphModel: %v", err)
	}
	if g.Meta.AnyChanged {
		t.Fatalf("meta.AnyChanged = true, want false (no merge-base)")
	}
}
