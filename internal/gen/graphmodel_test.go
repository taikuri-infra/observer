package gen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureModule lays out a tiny 3-package Go module (entry -> service
// -> external adapter) inside a real git repo, so GraphModel's kind
// classification, degree metrics, git-derived churn, and description
// extraction can all be pinned down against one small, known graph.
//
// Churn is seeded by committing every file once, then touching
// foo/service.go a second time, so that package's churn is 2 and the rest
// are 1 — proving churn comes from git history with no hotspots.json.
func writeFixtureModule(t *testing.T) (repo, moduleDir string) {
	t.Helper()
	repo = t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(path, content); err != nil {
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

	run(t, repo, "init", "-q", "-b", "main")
	run(t, repo, "config", "user.email", "t@t")
	run(t, repo, "config", "user.name", "t")
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "initial")

	// Second commit touching only foo/service.go -> its churn becomes 2.
	write("services/api/internal/application/foo/service.go", `// Package foo implements the foo use cases.
//
//observer:summary Orchestrates foo end to end.
//observer:owner platform
//observer:task SP-142
package foo

import "example.com/fixture/internal/adapters/stripe"

func Run() { stripe.Charge() /* touched */ }
`)
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "touch foo")

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
	if server.Metrics.Churn != 1 {
		t.Errorf("cmd/server churn = %d, want 1", server.Metrics.Churn)
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
	if foo.Metrics.Churn != 2 {
		t.Errorf("foo churn = %d, want 2 (committed twice)", foo.Metrics.Churn)
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
	if stripe.Metrics.Churn != 1 {
		t.Errorf("stripe churn = %d, want 1", stripe.Metrics.Churn)
	}
	if stripe.Description.Source != "godoc" || stripe.Description.Text != "provides a Stripe payment adapter." {
		t.Errorf("stripe description = %+v", stripe.Description)
	}
	// Links is always an array, never null — even when there's no task.
	if stripe.Links == nil {
		t.Errorf("stripe.Links is nil, want empty slice")
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

// TestGraphModelLinksMarshalArray asserts the serialized JSON never has a
// null links field — every node's links must be [] (or a populated array).
func TestGraphModelLinksMarshalArray(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)
	g, err := GraphModel(repo, moduleDir)
	if err != nil {
		t.Fatalf("GraphModel: %v", err)
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"links":null`) {
		t.Fatalf(`JSON contains "links":null; want always an array: %s`, b)
	}
	if !strings.Contains(string(b), `"links":[]`) {
		t.Fatalf(`expected at least one "links":[] in JSON: %s`, b)
	}
}

// TestGraphModelSiblingModule guards the module-prefix bug: a sibling
// module "example.com/fixturehelper" (string-prefixed by, but not under,
// "example.com/fixture") must never appear as a node or an edge target.
func TestGraphModelSiblingModule(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)

	// Add a package that imports a package in a sibling module whose path
	// shares the "example.com/fixture" prefix. We fake the sibling as a
	// vendored-style dependency via a replace, so `go list -deps` resolves
	// it without network access.
	write := func(rel, content string) {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(path, content); err != nil {
			t.Fatal(err)
		}
	}
	// Sibling module living inside the repo but as its own module.
	write("services/sibling/go.mod", "module example.com/fixturehelper\n\ngo 1.22\n")
	write("services/sibling/help/help.go", "package help\n\nfunc H() {}\n")
	// Wire the main module to depend on it via a local replace.
	write("services/api/go.mod", `module example.com/fixture

go 1.22

require example.com/fixturehelper v0.0.0

replace example.com/fixturehelper => ../sibling
`)
	write("services/api/internal/application/foo/service.go", `// Package foo implements the foo use cases.
package foo

import (
	"example.com/fixture/internal/adapters/stripe"
	"example.com/fixturehelper/help"
)

func Run() { stripe.Charge(); help.H() }
`)

	g, err := GraphModel(repo, moduleDir)
	if err != nil {
		t.Fatalf("GraphModel: %v", err)
	}
	for _, n := range g.Nodes {
		if strings.HasPrefix(n.ID, "../") || strings.Contains(n.ID, "fixturehelper") || n.ID == "help" {
			t.Errorf("sibling-module package leaked as node: %q", n.ID)
		}
	}
	for _, e := range g.Edges {
		if strings.Contains(e.To, "fixturehelper") || e.To == "help" {
			t.Errorf("edge targets sibling-module package: %+v", e)
		}
	}
}

func TestGraphModelMissingGoMod(t *testing.T) {
	dir := t.TempDir()
	if _, err := GraphModel(dir, dir); err == nil {
		t.Fatal("expected error for a directory without go.mod")
	}
}

// TestGraphModelChurnStandalone proves the map shows real churn with no
// obs-data/hotspots.json present — churn is computed from git history
// directly (the fixture never writes that artifact).
func TestGraphModelChurnStandalone(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)
	if _, err := os.Stat(filepath.Join(repo, "obs-data", "hotspots.json")); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has obs-data/hotspots.json (err=%v)", err)
	}
	g, err := GraphModel(repo, moduleDir)
	if err != nil {
		t.Fatalf("GraphModel: %v", err)
	}
	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	if byID["internal/application/foo"].Metrics.Churn != 2 {
		t.Errorf("foo churn = %d, want 2 from git with no hotspots.json", byID["internal/application/foo"].Metrics.Churn)
	}
	if byID["internal/adapters/stripe"].Metrics.Churn != 1 {
		t.Errorf("stripe churn = %d, want 1 from git", byID["internal/adapters/stripe"].Metrics.Churn)
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
		if n.Links == nil {
			t.Errorf("node %s has nil Links (must marshal as [])", n.ID)
		}
	}
	if server, ok := byID["cmd/server"]; !ok || server.Kind != "entry" {
		t.Errorf("cmd/server = %+v, ok=%v", server, ok)
	}
	if _, ok := byID["internal/ports"]; !ok {
		t.Error("missing internal/ports node")
	}

	described := 0
	churned := 0
	for _, n := range g.Nodes {
		if n.Description.Source != "none" {
			described++
		}
		if n.Metrics.Churn > 0 {
			churned++
		}
	}
	if described == 0 {
		t.Error("expected at least one package with a godoc/annotation description")
	}
	if churned == 0 {
		t.Error("expected at least one package with non-zero git-derived churn")
	}
}
