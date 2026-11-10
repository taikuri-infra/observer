package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// testToken is the fixed API bearer token used across tests (production
// generates one randomly via crypto/rand — see cmd/observer).
const testToken = "test-token-deadbeef"

// req builds a test request with a loopback Host so it passes the allowedHost
// DNS-rebinding guard (httptest defaults Host to "example.com").
func req(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:7979"
	return r
}

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
	h := New(repo, "main", testToken)

	r := req(http.MethodGet, "/api/project")
	r.Header.Set("X-Observer-Token", testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("project status = %d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Changes") {
		t.Fatalf("project body = %s", rec.Body)
	}

	// R15: POST without the X-Observer header must be rejected, even with a
	// valid token.
	r = req(http.MethodPost, "/api/update?tab=Changes")
	r.Header.Set("X-Observer-Token", testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without X-Observer header, got %d body=%s", rec.Code, rec.Body)
	}

	// A valid CSRF header but no token still isn't enough — both are required.
	r = req(http.MethodPost, "/api/update?tab=Changes")
	r.Header.Set("X-Observer", "1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d body=%s", rec.Code, rec.Body)
	}

	r = req(http.MethodPost, "/api/update?tab=Changes")
	r.Header.Set("X-Observer", "1")
	r.Header.Set("X-Observer-Token", testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body)
	}

	r = req(http.MethodGet, "/api/artifact?tab=Changes")
	r.Header.Set("X-Observer-Token", testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
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
	h := New(repo, "main", testToken)

	r := req(http.MethodPost, "/api/update?tab=Nope")
	r.Header.Set("X-Observer", "1")
	r.Header.Set("X-Observer-Token", testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown tab, got %d", rec.Code)
	}
}

// TestAPIRequiresToken is the auth contract for every /api/* route: no token
// (or a wrong token) is 401; the correct token — via header or cookie — is 200.
func TestAPIRequiresToken(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main", testToken)

	r := req(http.MethodGet, "/api/project")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: expected 401, got %d body=%s", rec.Code, rec.Body)
	}

	r = req(http.MethodGet, "/api/project")
	r.Header.Set("X-Observer-Token", "wrong-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d body=%s", rec.Code, rec.Body)
	}

	// Correct token via header.
	r = req(http.MethodGet, "/api/project")
	r.Header.Set("X-Observer-Token", testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("header token: expected 200, got %d body=%s", rec.Code, rec.Body)
	}

	// Correct token via the browser cookie (what the dashboard uses).
	r = req(http.MethodGet, "/api/project")
	r.AddCookie(&http.Cookie{Name: "observer_token", Value: testToken})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie token: expected 200, got %d body=%s", rec.Code, rec.Body)
	}
}

func TestIndexRequiresTokenAndSetsCookie(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main", testToken)

	// Unauthenticated GET / must not disclose the dashboard.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET / without token: expected 401, got %d", rec.Code)
	}

	// GET /?token=<tok> authenticates, serves the dashboard, and sets the
	// HttpOnly session cookie — without ever putting the token in the DOM.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/?token="+testToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /?token: expected 200, got %d body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "Observer") {
		t.Fatalf("expected dashboard HTML marker, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testToken) {
		t.Fatalf("token leaked into served HTML")
	}
	var setCookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "observer_token" {
			setCookie = c.Value
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("cookie not HttpOnly/SameSite=Strict: %+v", c)
			}
		}
	}
	if setCookie != testToken {
		t.Fatalf("expected observer_token cookie = token, got %q", setCookie)
	}
}

// TestForbiddenHost blocks DNS-rebinding: a non-loopback Host is rejected.
func TestForbiddenHost(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main", testToken)

	r := httptest.NewRequest(http.MethodGet, "/api/project", nil) // Host defaults to example.com
	r.Header.Set("X-Observer-Token", testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host: expected 403, got %d", rec.Code)
	}
}

func TestWebAssetServesJS(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main", testToken)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/web/app.js"))
	if rec.Code != http.StatusOK {
		t.Fatalf("web asset status = %d body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("content-type = %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("expected non-empty app.js body")
	}
}

// TestGitRefsListsBranchesAndTags proves GET /api/gitrefs reflects the
// repo's real branches/tags/current branch, and is token-gated like every
// other /api/* route.
func TestGitRefsListsBranchesAndTags(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	run(t, repo, "tag", "v1.0.0")
	run(t, repo, "branch", "feature")
	h := New(repo, "main", testToken)

	r := req(http.MethodGet, "/api/gitrefs")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: expected 401, got %d", rec.Code)
	}

	r = req(http.MethodGet, "/api/gitrefs")
	r.Header.Set("X-Observer-Token", testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("gitrefs status = %d body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"current":"main"`) {
		t.Errorf("gitrefs body missing current branch: %s", body)
	}
	if !strings.Contains(body, "feature") {
		t.Errorf("gitrefs body missing feature branch: %s", body)
	}
	if !strings.Contains(body, "v1.0.0") {
		t.Errorf("gitrefs body missing v1.0.0 tag: %s", body)
	}
}

// TestGitDiffRejectsBogusRef proves a ?base= that doesn't resolve to a
// commit 400s rather than silently producing an empty/wrong diff.
func TestGitDiffRejectsBogusRef(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main", testToken)

	r := req(http.MethodGet, "/api/gitdiff?base=does-not-exist-anywhere")
	r.Header.Set("X-Observer-Token", testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus base ref: expected 400, got %d body=%s", rec.Code, rec.Body)
	}
}

// TestGitDiffReturnsFileStatuses exercises the real flow: a feature branch
// adds a file and modifies another relative to main; GET
// /api/gitdiff?base=main must report both with the right status.
func TestGitDiffReturnsFileStatuses(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "keep.go"), []byte("package x\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "add keep.go")

	run(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "new.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.go"), []byte("package x\n\nfunc A() {}\n\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "feature: add new.go, touch keep.go")

	h := New(repo, "main", testToken)
	r := req(http.MethodGet, "/api/gitdiff?base=main")
	r.Header.Set("X-Observer-Token", testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("gitdiff status = %d body=%s", rec.Code, rec.Body)
	}

	var resp struct {
		Base         string `json:"base"`
		BaseResolved string `json:"baseResolved"`
		Files        map[string]struct {
			Status string  `json:"status"`
			Hunks  [][]int `json:"hunks"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode gitdiff response: %v body=%s", err, rec.Body)
	}
	if resp.Base != "main" || resp.BaseResolved == "" {
		t.Fatalf("resp = %+v", resp)
	}
	newFile, ok := resp.Files["new.go"]
	if !ok || newFile.Status != "A" {
		t.Fatalf("new.go = %+v, want status A", newFile)
	}
	keepFile, ok := resp.Files["keep.go"]
	if !ok || keepFile.Status != "M" {
		t.Fatalf("keep.go = %+v, want status M", keepFile)
	}
}

func TestWebAssetUnknownPath404s(t *testing.T) {
	repo := initRepo(t)
	writeManifest(t, repo)
	h := New(repo, "main", testToken)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/web/does-not-exist.js"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
