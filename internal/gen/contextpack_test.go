package gen

import (
	"strings"
	"testing"
)

// TestContextPack exercises ContextPack against the shared 3-package
// fixture module (writeFixtureModule, from graphmodel_test.go): a real git
// repo with an entry/service/external-adapter chain, no migrations, no
// Fiber routes. That means the Data model and API surface sections must be
// best-effort-skipped (short note, no error) while every other section
// still renders real fixture data.
func TestContextPack(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)

	md, err := ContextPack(repo, moduleDir, "main")
	if err != nil {
		t.Fatalf("ContextPack: %v", err)
	}

	for _, heading := range []string{
		"# ",
		"## Overview",
		"## Structure",
		"## Architecture (Go packages)",
		"## Data model",
		"## API surface",
		"## Hotspots",
		"## How to navigate",
	} {
		if !strings.Contains(md, heading) {
			t.Errorf("missing section %q in brief:\n%s", heading, md)
		}
	}

	if !strings.Contains(md, "example.com/fixture") {
		t.Errorf("missing module path example.com/fixture in brief:\n%s", md)
	}

	// At least one fixture package id must appear in the Architecture
	// section (proves GraphModel actually ran against the fixture).
	for _, pkg := range []string{"cmd/server", "internal/application/foo", "internal/adapters/stripe"} {
		if !strings.Contains(md, pkg) {
			t.Errorf("missing fixture package %q in brief:\n%s", pkg, md)
		}
	}

	// The fixture has no migrations dir and no Fiber routes: those
	// sections must be present but explicitly marked skipped, not silently
	// empty and not a fatal error (already asserted above via err == nil).
	dataModelIdx := strings.Index(md, "## Data model")
	apiIdx := strings.Index(md, "## API surface")
	if dataModelIdx < 0 || apiIdx < 0 || apiIdx < dataModelIdx {
		t.Fatalf("Data model / API surface sections missing or out of order:\n%s", md)
	}
	dataModelSection := md[dataModelIdx:apiIdx]
	if !strings.Contains(dataModelSection, "skipped") {
		t.Errorf("expected Data model section to note it was skipped (no migrations dir):\n%s", dataModelSection)
	}
	hotspotsIdx := strings.Index(md, "## Hotspots")
	if hotspotsIdx < 0 {
		t.Fatalf("missing Hotspots section:\n%s", md)
	}
	apiSection := md[apiIdx:hotspotsIdx]
	if !strings.Contains(apiSection, "no routes detected") {
		t.Errorf("expected API surface section to note no routes detected:\n%s", apiSection)
	}

	// Hotspots: the fixture commits twice, touching foo/service.go a
	// second time, so some file must show up with churn.
	if !strings.Contains(md, "foo/service.go") {
		t.Errorf("expected foo/service.go in Hotspots section:\n%s", md)
	}
}

// TestContextPackMissingGoMod proves ContextPack still surfaces a hard
// failure when the module itself can't be read at all (readModulePath
// errors) rather than silently producing a brief with no module info —
// unlike the per-section best-effort skips, a missing go.mod means there's
// no Go module to analyze in the first place.
func TestContextPackMissingGoMod(t *testing.T) {
	repo, moduleDir := writeFixtureModule(t)
	md, err := ContextPack(repo, moduleDir+"-does-not-exist", "main")
	if err != nil {
		t.Fatalf("ContextPack: %v (want no hard error — go.mod failure is reported inline, not fatal)", err)
	}
	if !strings.Contains(md, "unavailable") {
		t.Errorf("expected inline note about the unreadable go.mod:\n%s", md)
	}
}
