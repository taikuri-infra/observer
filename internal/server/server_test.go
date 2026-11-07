package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func writeManifest(t *testing.T, repo string) {
	t.Helper()
	dir := filepath.Join(repo, "obs-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `project: demo
tabs:
  - name: Changes
    kind: generator
    command: "echo '# hi' > obs-data/changes.md"
    output: changes.md
    renderer: markdown
`
	if err := os.WriteFile(filepath.Join(dir, "observer.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectUpdateArtifactFlow(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main")

	req := httptest.NewRequest(http.MethodGet, "/api/project", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("project status = %d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Changes") {
		t.Fatalf("project body = %s", rec.Body)
	}

	// R15: POST without the X-Observer header must be rejected.
	req = httptest.NewRequest(http.MethodPost, "/api/update?tab=Changes", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without X-Observer header, got %d body=%s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/update?tab=Changes", nil)
	req.Header.Set("X-Observer", "1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/artifact?tab=Changes", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("artifact status = %d body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "hi") {
		t.Fatalf("artifact body = %s", rec.Body.String())
	}

	if _, err := os.Stat(filepath.Join(repo, "obs-data", "_index.json")); err != nil {
		t.Fatalf("expected _index.json to be written: %v", err)
	}
}

func TestUpdateUnknownTab(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main")

	req := httptest.NewRequest(http.MethodPost, "/api/update?tab=Nope", nil)
	req.Header.Set("X-Observer", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown tab, got %d", rec.Code)
	}
}

func TestIndexPlaceholder(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("expected non-empty placeholder body")
	}
}
