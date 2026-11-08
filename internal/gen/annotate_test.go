package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExtractPackageDocGodocOnly(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "service.go", `// Package provisioning implements the tenant provisioning pipeline.
//
// When a new tenant registers, the pipeline creates the entire tenant
// infrastructure.
package provisioning
`)
	res, err := ExtractPackageDoc(dir, []string{"service.go"})
	if err != nil {
		t.Fatalf("ExtractPackageDoc: %v", err)
	}
	if res.PrimaryFile != "service.go" {
		t.Errorf("PrimaryFile = %q, want service.go", res.PrimaryFile)
	}
	want := "implements the tenant provisioning pipeline.\n\nWhen a new tenant registers, the pipeline creates the entire tenant infrastructure."
	if res.GodocText != want {
		t.Errorf("GodocText = %q, want %q", res.GodocText, want)
	}
	if res.Summary != "" || res.Owner != "" || res.Task != "" {
		t.Errorf("unexpected annotations: %+v", res)
	}
}

func TestExtractPackageDocWithAnnotations(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "pipeline.go", `// Package provisioning implements the tenant provisioning pipeline.
//
//observer:summary Orchestrates tenant creation across Keycloak, DB and mail.
//observer:owner platform
//observer:task SP-142
package provisioning
`)
	res, err := ExtractPackageDoc(dir, []string{"pipeline.go"})
	if err != nil {
		t.Fatalf("ExtractPackageDoc: %v", err)
	}
	if res.Summary != "Orchestrates tenant creation across Keycloak, DB and mail." {
		t.Errorf("Summary = %q", res.Summary)
	}
	if res.Owner != "platform" {
		t.Errorf("Owner = %q", res.Owner)
	}
	if res.Task != "SP-142" {
		t.Errorf("Task = %q", res.Task)
	}
	// The //observer: lines must not leak into the godoc prose.
	if res.GodocText != "implements the tenant provisioning pipeline." {
		t.Errorf("GodocText = %q", res.GodocText)
	}
}

func TestExtractPackageDocNoComment(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "z.go", "package nodoc\n")
	writeGoFile(t, dir, "a.go", "package nodoc\n\nfunc A() {}\n")
	res, err := ExtractPackageDoc(dir, []string{"z.go", "a.go"})
	if err != nil {
		t.Fatalf("ExtractPackageDoc: %v", err)
	}
	if res.GodocText != "" || res.Summary != "" {
		t.Errorf("expected no doc/annotations, got %+v", res)
	}
	// Fallback primary file is the first non-test file in sorted order.
	if res.PrimaryFile != "a.go" {
		t.Errorf("PrimaryFile = %q, want a.go (sorted fallback)", res.PrimaryFile)
	}
}

func TestExtractPackageDocSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "x_test.go", `// Package x is a decoy doc comment that must never be used — test files
// are never where real package docs live.
package x
`)
	res, err := ExtractPackageDoc(dir, []string{"x_test.go"})
	if err != nil {
		t.Fatalf("ExtractPackageDoc: %v", err)
	}
	if res.GodocText != "" || res.PrimaryFile != "" {
		t.Errorf("test-only package should yield no doc/primary file, got %+v", res)
	}
}

func TestInferLinkType(t *testing.T) {
	cases := map[string]string{
		"SP-142":  "serviceplus", // SP prefix stays serviceplus even though it matches the Jira shape
		"OBS-42":  "jira",
		"PLAT-7":  "jira",
		"ticket1": "serviceplus", // doesn't match [A-Z]+-\d+ at all
		"":        "serviceplus",
	}
	for task, want := range cases {
		if got := InferLinkType(task); got != want {
			t.Errorf("InferLinkType(%q) = %q, want %q", task, got, want)
		}
	}
}
