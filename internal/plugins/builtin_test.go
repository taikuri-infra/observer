package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/taikuri-infra/observer/internal/manifest"
)

func findTabByName(tabs []manifest.Tab, name string) (manifest.Tab, bool) {
	for _, t := range tabs {
		if t.Name == name {
			return t, true
		}
	}
	return manifest.Tab{}, false
}

// TestDefaultTabsFixture exercises DefaultTabs against a small synthetic
// repo (not servicePlus) so the detection logic itself is pinned down:
// a nested Go module with a migrations dir, and a nested frontend package.
func TestDefaultTabsFixture(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services/api/go.mod", "module example.com/api\n\ngo 1.22\n")
	write("services/api/internal/adapters/postgres/tenant_migrations/001_x.sql",
		"CREATE TABLE x (\n    id UUID PRIMARY KEY\n);\n")
	write("services/api/internal/adapters/postgres/tenant_migrations/001_x_down.sql",
		"DROP TABLE x;\n")
	write("services/api/cmd/server/main.go", "package main\n\nfunc main() {}\n")
	write("services/api/cmd/worker/main.go", "package main\n\nfunc main() {}\n")
	write("apps/web/package.json", `{"name":"web"}`)
	write("apps/web/src/index.ts", "export const x = 1;\n")
	write("tests/e2e/package.json", `{"name":"e2e"}`) // should be excluded (looks like test tooling)

	tabs, err := DefaultTabs(repo)
	if err != nil {
		t.Fatalf("DefaultTabs: %v", err)
	}

	if _, ok := findTabByName(tabs, "Overview"); !ok {
		t.Fatalf("missing Overview tab: %+v", tabs)
	}
	if _, ok := findTabByName(tabs, "Changes"); !ok {
		t.Fatalf("missing Changes tab: %+v", tabs)
	}
	if _, ok := findTabByName(tabs, "Hotspots"); !ok {
		t.Fatalf("missing Hotspots tab: %+v", tabs)
	}
	if _, ok := findTabByName(tabs, "Go deps"); ok {
		t.Fatalf("Go deps tab must no longer be wired (Service map replaces it): %+v", tabs)
	}

	if tabs[0].Name != "Overview" {
		t.Fatalf("tabs[0] = %q, want Overview", tabs[0].Name)
	}
	if tabs[1].Name != "Service map" {
		t.Fatalf("tabs[1] = %q, want Service map (2nd tab, right after Overview)", tabs[1].Name)
	}
	if tabs[2].Name != "Changes" {
		t.Fatalf("tabs[2] = %q, want Changes (right after Service map)", tabs[2].Name)
	}

	changes, ok := findTabByName(tabs, "Changes")
	if !ok {
		t.Fatalf("missing Changes tab: %+v", tabs)
	}
	if changes.Renderer != "map" {
		t.Fatalf("Changes renderer = %q, want map (highlighted service map)", changes.Renderer)
	}
	if !strings.Contains(changes.Command, "observer gen changedgraph") || !strings.Contains(changes.Command, "--dir 'services/api'") {
		t.Fatalf("Changes command wrong: %q", changes.Command)
	}

	// Two main packages (cmd/server, cmd/worker) under one module -> two
	// disambiguated Call graph tabs, each targeting exactly one main (the
	// whole point: go-callvis errors on a pattern matching more than one).
	callGraphServer, ok := findTabByName(tabs, "Call graph (cmd/server)")
	if !ok {
		t.Fatalf("missing Call graph (cmd/server) tab: %+v", tabs)
	}
	if callGraphServer.Renderer != "map" || callGraphServer.Kind != manifest.KindGenerator {
		t.Fatalf("Call graph (cmd/server) tab wrong: %+v", callGraphServer)
	}
	if callGraphServer.Dir != "" {
		t.Fatalf("Call graph tab must run from the repo root (no Dir), got %q", callGraphServer.Dir)
	}
	if len(callGraphServer.Requires) == 0 || callGraphServer.Requires[0] != "go-callvis" {
		t.Fatalf("Call graph (cmd/server) tab missing go-callvis requirement: %+v", callGraphServer)
	}
	if !strings.Contains(callGraphServer.Command, "observer gen callgraphmodel") ||
		!strings.Contains(callGraphServer.Command, "--dir 'services/api'") ||
		!strings.Contains(callGraphServer.Command, "--main 'cmd/server'") {
		t.Fatalf("Call graph (cmd/server) command wrong: %q", callGraphServer.Command)
	}

	callGraphWorker, ok := findTabByName(tabs, "Call graph (cmd/worker)")
	if !ok {
		t.Fatalf("missing Call graph (cmd/worker) tab: %+v", tabs)
	}
	if !strings.Contains(callGraphWorker.Command, "--main 'cmd/worker'") {
		t.Fatalf("Call graph (cmd/worker) command wrong: %q", callGraphWorker.Command)
	}
	if callGraphServer.Output == callGraphWorker.Output {
		t.Fatalf("Call graph tabs must have disambiguated output files, both got %q", callGraphServer.Output)
	}

	svcMap, ok := findTabByName(tabs, "Service map")
	if !ok {
		t.Fatalf("missing Service map tab: %+v", tabs)
	}
	if svcMap.Renderer != "map" || svcMap.Kind != manifest.KindGenerator {
		t.Fatalf("Service map tab wrong: %+v", svcMap)
	}
	if svcMap.Dir != "" {
		t.Fatalf("Service map tab must run from the repo root (no Dir), got %q", svcMap.Dir)
	}
	if !strings.Contains(svcMap.Command, "--dir 'services/api'") || !strings.Contains(svcMap.Command, "obs-data/map.json") {
		t.Fatalf("Service map command wrong: %q", svcMap.Command)
	}

	db, ok := findTabByName(tabs, "Database")
	if !ok {
		t.Fatalf("missing Database tab: %+v", tabs)
	}
	if !strings.Contains(db.Command, "services/api/internal/adapters/postgres/tenant_migrations") {
		t.Fatalf("Database command missing detected migrations dir: %q", db.Command)
	}
	if db.Renderer != "map" {
		t.Fatalf("Database renderer = %q, want map (canvas ER)", db.Renderer)
	}
	if !strings.Contains(db.Command, "--json-out") {
		t.Fatalf("Database command missing --json-out: %q", db.Command)
	}

	fe, ok := findTabByName(tabs, "Frontend deps")
	if !ok {
		t.Fatalf("missing Frontend deps tab: %+v", tabs)
	}
	if fe.Dir != "" {
		t.Fatalf("Frontend deps tab must run from the repo root (no Dir), got %q", fe.Dir)
	}
	if fe.Renderer != "map" {
		t.Fatalf("Frontend deps renderer = %q, want map (canvas)", fe.Renderer)
	}
	if !strings.Contains(fe.Command, "--dir 'apps/web'") {
		t.Fatalf("Frontend deps command missing --dir 'apps/web': %q", fe.Command)
	}
	if len(fe.Requires) == 0 || fe.Requires[0] != "depcruise" {
		t.Fatalf("Frontend deps tab wrong requires: %+v", fe)
	}

	// Manifest-level validation must accept everything DefaultTabs produces.
	proj := manifest.Project{Project: "fixture", Tabs: tabs}
	for _, tab := range proj.Tabs {
		if strings.Contains(tab.Dir, "..") {
			t.Fatalf("tab %q has an escaping Dir: %q", tab.Name, tab.Dir)
		}
		if strings.Contains(tab.Output, "..") {
			t.Fatalf("tab %q has an escaping Output: %q", tab.Name, tab.Output)
		}
	}
}

// TestDefaultTabsServicePlus is M3's acceptance check: run DefaultTabs
// against the real servicePlus monorepo and assert it finds the Go module
// at services/api (Service map + Go call graph tabs) and a canvas-rendered
// Database tab backed by the tenant_migrations dir (which has chat_channels
// et al). It must NOT invoke any external tool — only inspect the
// filesystem and build command strings.
func TestDefaultTabsServicePlus(t *testing.T) {
	const repo = "/Users/babak/codes/DoTech/servicePlus"
	if _, err := os.Stat(repo); err != nil {
		t.Skipf("servicePlus checkout not available: %v", err)
	}

	tabs, err := DefaultTabs(repo)
	if err != nil {
		t.Fatalf("DefaultTabs: %v", err)
	}

	svcMap, ok := findTabByName(tabs, "Service map")
	if !ok || svcMap.Renderer != "map" {
		t.Fatalf("missing/incorrect Service map tab for servicePlus: %+v (ok=%v)", svcMap, ok)
	}

	// servicePlus's services/api module has four "package main" dirs
	// (cmd/server, cmd/retention-sweeper, scripts/bootstrap_billing,
	// scripts/migrate-projects-to-tenant-db) — DefaultTabs must emit one
	// disambiguated Call graph tab per main rather than erroring the way
	// go-callvis itself does on "./..." with more than one main.
	var callGraphTabs []manifest.Tab
	for _, tab := range tabs {
		if strings.HasPrefix(tab.Name, "Call graph") {
			callGraphTabs = append(callGraphTabs, tab)
		}
	}
	if len(callGraphTabs) < 2 {
		t.Fatalf("expected multiple Call graph tabs for servicePlus's multi-main services/api module, got %+v", callGraphTabs)
	}
	callGraphServer, ok := findTabByName(tabs, "Call graph (cmd/server)")
	if !ok || callGraphServer.Renderer != "map" {
		t.Fatalf("missing/incorrect Call graph (cmd/server) tab: %+v (ok=%v)", callGraphServer, ok)
	}
	if !strings.Contains(callGraphServer.Command, "--dir 'services/api'") {
		t.Fatalf("Call graph (cmd/server) command missing --dir 'services/api': %q", callGraphServer.Command)
	}

	var dbTabs []manifest.Tab
	for _, tab := range tabs {
		if strings.HasPrefix(tab.Name, "Database") {
			dbTabs = append(dbTabs, tab)
		}
	}
	if len(dbTabs) == 0 {
		t.Fatalf("expected at least one Database tab for servicePlus: %+v", tabs)
	}
	foundTenantMigrations := false
	for _, tab := range dbTabs {
		if strings.Contains(tab.Command, "tenant_migrations") {
			foundTenantMigrations = true
			if tab.Renderer != "map" {
				t.Fatalf("Database tab renderer = %q, want map", tab.Renderer)
			}
		}
	}
	if !foundTenantMigrations {
		t.Fatalf("no Database tab references tenant_migrations: %+v", dbTabs)
	}
}

// TestGoCallGraphTabsMultiModuleNoCollision guards the call-graph
// disambiguation fix: two Go modules that each have a `cmd/server` main
// must NOT collapse to one duplicate tab name (which breaks
// /api/artifact?tab= lookup) or one shared output file (last-write-wins).
// Both must be unique, keyed on the main's full repo-relative path.
func TestGoCallGraphTabsMultiModuleNoCollision(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services/api/go.mod", "module example.com/api\n\ngo 1.22\n")
	write("services/api/cmd/server/main.go", "package main\n\nfunc main() {}\n")
	write("services/worker/go.mod", "module example.com/worker\n\ngo 1.22\n")
	write("services/worker/cmd/server/main.go", "package main\n\nfunc main() {}\n")

	tabs, err := goCallGraphTabs(repo, []string{"services/api", "services/worker"})
	if err != nil {
		t.Fatalf("goCallGraphTabs: %v", err)
	}
	if len(tabs) != 2 {
		t.Fatalf("expected 2 call-graph tabs, got %d: %+v", len(tabs), tabs)
	}
	names := map[string]bool{}
	outputs := map[string]bool{}
	for _, tb := range tabs {
		if names[tb.Name] {
			t.Fatalf("duplicate call-graph tab name %q: %+v", tb.Name, tabs)
		}
		if outputs[tb.Output] {
			t.Fatalf("duplicate call-graph output %q: %+v", tb.Output, tabs)
		}
		names[tb.Name] = true
		outputs[tb.Output] = true
	}
	// The display name must carry the module so the two are tellable apart.
	if !names["Call graph (services/api/cmd/server)"] || !names["Call graph (services/worker/cmd/server)"] {
		t.Fatalf("tab names should include the full main path: %v", names)
	}
}

func TestFilterSafeRelRejectsInjection(t *testing.T) {
	in := []string{"services/api", "apps/web", "foo;rm -rf ~", "$(evil)", "a b", "../escape", "ok-1.2", "`x`"}
	got := filterSafeRel(in)
	want := map[string]bool{"services/api": true, "apps/web": true, "ok-1.2": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d safe paths, got %v", len(want), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected kept path %q in %v", g, got)
		}
	}
}
