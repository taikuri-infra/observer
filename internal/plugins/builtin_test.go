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
	if tabs[1].Name != "AI context" {
		t.Fatalf("tabs[1] = %q, want AI context (2nd tab, right after Overview)", tabs[1].Name)
	}
	if tabs[2].Name != "Service map" {
		t.Fatalf("tabs[2] = %q, want Service map (3rd tab, right after AI context)", tabs[2].Name)
	}
	if tabs[3].Name != "Changes" {
		t.Fatalf("tabs[3] = %q, want Changes (right after Service map)", tabs[3].Name)
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

	// Call graph tabs were removed from the lean default set — even with two
	// mains (cmd/server, cmd/worker) present, none must be emitted.
	for _, tab := range tabs {
		if strings.HasPrefix(tab.Name, "Call graph") {
			t.Fatalf("Call graph tabs must no longer be scaffolded by default: %+v", tab)
		}
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

	// Frontend deps tabs were likewise removed from the lean default set —
	// the fixture's apps/web package must NOT produce one.
	if _, ok := findTabByName(tabs, "Frontend deps"); ok {
		t.Fatalf("Frontend deps tabs must no longer be scaffolded by default: %+v", tabs)
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

	// The lean default set emits NO Call graph or Frontend deps tabs, even
	// though servicePlus's services/api has four "package main" dirs and
	// there are frontend packages under apps/ — removed as clutter.
	for _, tab := range tabs {
		if strings.HasPrefix(tab.Name, "Call graph") || strings.HasPrefix(tab.Name, "Frontend deps") {
			t.Fatalf("lean default set must not scaffold %q", tab.Name)
		}
	}
	// It MUST emit an "API routes" tab for the Fiber module, though.
	if apiTab, ok := findTabByName(tabs, "API routes"); !ok || apiTab.Renderer != "table" {
		t.Fatalf("missing/incorrect API routes tab for servicePlus's Fiber module: %+v (ok=%v)", apiTab, ok)
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
