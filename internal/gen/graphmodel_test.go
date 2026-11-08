package gen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeFixtureModule lays out a tiny 3-package Go module (entry -> service
// -> external adapter) plus a repo-root hotspots.json, so GraphModel's
// kind classification, degree metrics, churn mapping, and description
// extraction can all be pinned down against one small, known graph.
func writeFixtureModule(t *testing.T) (repo, moduleDir string) {
	t.Helper()
	repo = t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("services/api/go.mod", "module example.com/fixture\n\ngo 1.22\n")
	write("services/api/cmd/server/main.go", `package main

import "example.com/fixture/internal/application/foo"

func main() { foo.Run() }
`)
	write("services/api/internal/application/foo/service.go", `// Package foo implements the foo use cases.
//
//observer:summary Orchestrates foo end to end.
//observer:owner platform
//observer:task SP-142
package foo

import "example.com/fixture/internal/adapters/stripe"

func Run() { stripe.Charge() }
`)
	write("services/api/internal/adapters/stripe/adapter.go", `// Package stripe provides a Stripe payment adapter.
package stripe

func Charge() {}
`)

	stats := []FileStat{
		{Path: "services/api/internal/application/foo/service.go", Commits: 5, Churn: 5},
		{Path: "services/api/cmd/server/main.go", Commits: 2, Churn: 2},
	}
	b, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	write("obs-data/hotspots.json", string(b))

	return repo, filepath.Join(repo, "services/api")
}

func TestGraphModelFixture(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)

	g, err := GraphModel(repo, moduleDir)
	if err != nil {
		t.Fatalf("GraphModel: %v", err)
	}

	if g.Meta.Source != "go-deps" || g.Meta.Module != "services/api" {
		t.Errorf("meta = %+v", g.Meta)
	}

	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	server, ok := byID["cmd/server"]
	if !ok {
		t.Fatalf("missing cmd/server node: %+v", g.Nodes)
	}
	if server.Kind != "entry" {
		t.Errorf("cmd/server kind = %q, want entry", server.Kind)
	}
	if server.Metrics.Dependents != 0 || server.Metrics.Imports != 1 {
		t.Errorf("cmd/server metrics = %+v", server.Metrics)
	}
	if server.Metrics.Churn != 2 {
		t.Errorf("cmd/server churn = %d, want 2", server.Metrics.Churn)
	}
	if server.Location.Path != "services/api/cmd/server" {
		t.Errorf("cmd/server location.path = %q", server.Location.Path)
	}

	foo, ok := byID["internal/application/foo"]
	if !ok {
		t.Fatalf("missing internal/application/foo node: %+v", g.Nodes)
	}
	if foo.Kind != "service" {
		t.Errorf("foo kind = %q, want service", foo.Kind)
	}
	if foo.Metrics.Dependents != 1 || foo.Metrics.Imports != 1 {
		t.Errorf("foo metrics = %+v", foo.Metrics)
	}
	if foo.Metrics.Churn != 5 {
		t.Errorf("foo churn = %d, want 5", foo.Metrics.Churn)
	}
	if foo.Description.Source != "observer-annotation" || foo.Description.Text != "Orchestrates foo end to end." {
		t.Errorf("foo description = %+v", foo.Description)
	}
	if len(foo.Links) != 1 || foo.Links[0].Type != "serviceplus" || foo.Links[0].ID != "SP-142" {
		t.Errorf("foo links = %+v", foo.Links)
	}

	stripe, ok := byID["internal/adapters/stripe"]
	if !ok {
		t.Fatalf("missing internal/adapters/stripe node: %+v", g.Nodes)
	}
	if stripe.Kind != "external" {
		t.Errorf("stripe kind = %q, want external", stripe.Kind)
	}
	if stripe.Metrics.Dependents != 1 || stripe.Metrics.Imports != 0 {
		t.Errorf("stripe metrics = %+v", stripe.Metrics)
	}
	if stripe.Metrics.Churn != 0 {
		t.Errorf("stripe churn = %d, want 0 (no hotspot entry)", stripe.Metrics.Churn)
	}
	if stripe.Description.Source != "godoc" || stripe.Description.Text != "provides a Stripe payment adapter." {
		t.Errorf("stripe description = %+v", stripe.Description)
	}
	if len(stripe.Links) != 0 {
		t.Errorf("stripe should have no links, got %+v", stripe.Links)
	}

	wantEdges := map[[2]string]bool{
		{"cmd/server", "internal/application/foo"}:               true,
		{"internal/application/foo", "internal/adapters/stripe"}: true,
	}
	if len(g.Edges) != len(wantEdges) {
		t.Fatalf("edges = %+v, want %d edges", g.Edges, len(wantEdges))
	}
	for _, e := range g.Edges {
		if !wantEdges[[2]string{e.From, e.To}] {
			t.Errorf("unexpected edge %+v", e)
		}
		if e.Weight != 1 {
			t.Errorf("edge %+v has weight %d, want 1", e, e.Weight)
		}
	}
}

func TestGraphModelMissingGoMod(t *testing.T) {
	dir := t.TempDir()
	if _, err := GraphModel(dir, dir); err == nil {
		t.Fatal("expected error for a directory without go.mod")
	}
}

func TestGraphModelNoHotspots(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)
	if err := os.Remove(filepath.Join(repo, "obs-data", "hotspots.json")); err != nil {
		t.Fatal(err)
	}
	g, err := GraphModel(repo, moduleDir)
	if err != nil {
		t.Fatalf("GraphModel without hotspots.json: %v", err)
	}
	for _, n := range g.Nodes {
		if n.Metrics.Churn != 0 {
			t.Errorf("node %s churn = %d, want 0 with no hotspots.json", n.ID, n.Metrics.Churn)
		}
	}
}

// TestGraphModelServicePlus is a filesystem-only acceptance check against
// the real servicePlus monorepo (skipped if that checkout isn't present on
// this machine) — mirrors TestDefaultTabsServicePlus in internal/plugins.
func TestGraphModelServicePlus(t *testing.T) {
	const repo = "/Users/babak/codes/DoTech/servicePlus"
	if _, err := os.Stat(repo); err != nil {
		t.Skipf("servicePlus checkout not available: %v", err)
	}
	moduleDir := filepath.Join(repo, "services", "api")
	if _, err := os.Stat(filepath.Join(moduleDir, "go.mod")); err != nil {
		t.Skipf("services/api go.mod not found: %v", err)
	}

	g, err := GraphModel(repo, moduleDir)
	if err != nil {
		t.Fatalf("GraphModel: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Fatal("expected at least one node")
	}
	if len(g.Edges) == 0 {
		t.Fatal("expected at least one edge")
	}

	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
		if n.Location.Path == "" {
			t.Errorf("node %s has empty location.path", n.ID)
		}
	}
	if server, ok := byID["cmd/server"]; !ok || server.Kind != "entry" {
		t.Errorf("cmd/server = %+v, ok=%v", server, ok)
	}
	if _, ok := byID["internal/ports"]; !ok {
		t.Error("missing internal/ports node")
	}

	described := 0
	for _, n := range g.Nodes {
		if n.Description.Source != "none" {
			described++
		}
	}
	if described == 0 {
		t.Error("expected at least one package with a godoc/annotation description")
	}
}
