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

	goDeps, ok := findTabByName(tabs, "Go deps")
	if !ok {
		t.Fatalf("missing Go deps tab: %+v", tabs)
	}
	if goDeps.Dir != "services/api" {
		t.Fatalf("Go deps Dir = %q, want services/api", goDeps.Dir)
	}
	if !strings.Contains(goDeps.Command, "../../obs-data/godeps.dot") {
		t.Fatalf("Go deps command should write back up to top-level obs-data: %q", goDeps.Command)
	}

	callGraph, ok := findTabByName(tabs, "Go call graph")
	if !ok {
		t.Fatalf("missing Go call graph tab: %+v", tabs)
	}
	if callGraph.Dir != "services/api" || len(callGraph.Requires) == 0 || callGraph.Requires[0] != "go-callvis" {
		t.Fatalf("Go call graph tab wrong: %+v", callGraph)
	}
	if strings.Contains(callGraph.Command, "-focus") {
		t.Fatalf("default go-callvis command must omit -focus (R13): %q", callGraph.Command)
	}

	db, ok := findTabByName(tabs, "Database")
	if !ok {
		t.Fatalf("missing Database tab: %+v", tabs)
	}
	if !strings.Contains(db.Command, "services/api/internal/adapters/postgres/tenant_migrations") {
		t.Fatalf("Database command missing detected migrations dir: %q", db.Command)
	}

	fe, ok := findTabByName(tabs, "Frontend deps")
	if !ok {
		t.Fatalf("missing Frontend deps tab: %+v", tabs)
	}
	if fe.Dir != "apps/web" {
		t.Fatalf("Frontend deps Dir = %q, want apps/web (tests/e2e must be excluded)", fe.Dir)
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
// at services/api (with Dir set correctly) and a Database tab backed by
// the tenant_migrations dir (which has chat_channels et al). It must NOT
// invoke any external tool — only inspect the filesystem and build command
// strings.
func TestDefaultTabsServicePlus(t *testing.T) {
	const repo = "/Users/babak/codes/DoTech/servicePlus"
	if _, err := os.Stat(repo); err != nil {
		t.Skipf("servicePlus checkout not available: %v", err)
	}

	tabs, err := DefaultTabs(repo)
	if err != nil {
		t.Fatalf("DefaultTabs: %v", err)
	}

	goDeps, ok := findTabByName(tabs, "Go deps")
	if !ok {
		t.Fatalf("missing Go deps tab for servicePlus: %+v", tabs)
	}
	if goDeps.Dir != "services/api" {
		t.Fatalf("Go deps Dir = %q, want services/api", goDeps.Dir)
	}

	callGraph, ok := findTabByName(tabs, "Go call graph")
	if !ok || callGraph.Dir != "services/api" {
		t.Fatalf("missing/incorrect Go call graph tab: %+v (ok=%v)", callGraph, ok)
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
			if tab.Renderer != "mermaid" {
				t.Fatalf("Database tab renderer = %q, want mermaid", tab.Renderer)
			}
		}
	}
	if !foundTenantMigrations {
		t.Fatalf("no Database tab references tenant_migrations: %+v", dbTabs)
	}
}
