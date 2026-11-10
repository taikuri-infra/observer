// Package server implements observer's HTTP API: the dashboard reads the
// repo's manifest and artifact freshness, triggers a tab's generator, and
// fetches a rendered artifact.
package server

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/taikuri-infra/observer/internal/atref"
	"github.com/taikuri-infra/observer/internal/gitmeta"
	"github.com/taikuri-infra/observer/internal/manifest"
	"github.com/taikuri-infra/observer/internal/runner"
)

// webFS embeds the entire dashboard UI (HTML/CSS/JS + vendored renderer
// libs) into the binary — no external assets, no build step (M2).
//
//go:embed web/*
var webFS embed.FS

const (
	obsDataDir  = "obs-data"
	manifestRel = "observer.yaml"
	indexRel    = "_index.json"
)

// index is the obs-data/_index.json schema (R7): freshness per tab, keyed by
// tab name.
type index struct {
	Tabs map[string]runner.Artifact `json:"tabs"`
}

// Server holds the per-repo state the API operates on. Safe for concurrent
// use: writes to _index.json are serialized by mu.
type Server struct {
	repo  string
	base  string
	token string
	mu    sync.Mutex
}

// New builds the observer HTTP handler for one repo. base is the branch
// tabs' "changes since" diffs are compared against (e.g. "main"). token is
// the per-session API bearer token (see cmd/observer's runServe): every
// /api/* request must carry it as X-Observer-Token, since network
// reachability of localhost must not be enough to trigger shell-exec
// endpoints.
func New(repo, base, token string) http.Handler {
	s := &Server{repo: repo, base: base, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project", s.requireToken(s.handleProject))
	mux.HandleFunc("/api/update", s.requireToken(s.handleUpdate))
	mux.HandleFunc("/api/artifact", s.requireToken(s.handleArtifact))
	mux.HandleFunc("/api/gitrefs", s.requireToken(s.handleGitRefs))
	mux.HandleFunc("/api/gitdiff", s.requireToken(s.handleGitDiff))
	mux.HandleFunc("/api/graphat", s.requireToken(s.handleGraphAt))
	mux.HandleFunc("/web/", s.handleWebAsset)
	mux.HandleFunc("/", s.handleIndex)
	// Wrap every route with a Host allowlist so a DNS-rebinding page (an
	// attacker domain resolving to 127.0.0.1) cannot reach the shell-exec API.
	return allowedHost(mux)
}

// allowedHost rejects requests whose Host header is not a loopback name,
// defeating DNS-rebinding attacks against the local server.
func allowedHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch host {
		case "localhost", "127.0.0.1", "::1", "[::1]":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
		}
	})
}

// tokenValid reports whether the request carries the session token, either as
// the HttpOnly observer_token cookie (browser, set on the authenticated index
// load) or the X-Observer-Token header (programmatic clients). Constant-time
// compared so the check can't leak the token via timing.
func (s *Server) tokenValid(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	if c, err := r.Cookie("observer_token"); err == nil &&
		subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1 {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Observer-Token")), []byte(s.token)) == 1
}

// requireToken wraps an /api/* handler so it 401s unless the request carries
// X-Observer-Token matching s.token, compared in constant time so the
// comparison itself can't leak the token via timing. s.token is always
// non-empty (cmd/observer generates one if OBSERVER_TOKEN is unset), so an
// empty header never matches.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenValid(r) {
			http.Error(w, "missing or invalid observer token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleIndex serves the dashboard shell (web/index.html) at the root path,
// with the __OBSERVER_TOKEN__ placeholder substituted for the real token so
// the browser-side JS can read it from the meta tag and send it on every
// /api/* request. Any other unmatched path is a 404 (the ServeMux routes
// /web/* and /api/* elsewhere first).
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// Require the token before disclosing the dashboard — otherwise an
	// unauthenticated GET / would hand out everything needed to drive the
	// shell-exec API. Accept it from the ?token= link `observer serve` prints,
	// or the cookie set on a prior authenticated load.
	q := r.URL.Query().Get("token")
	authed := s.tokenValid(r) ||
		(s.token != "" && subtle.ConstantTimeCompare([]byte(q), []byte(s.token)) == 1)
	if !authed {
		http.Error(w, "unauthorized — open the /?token=<token> URL printed by `observer serve`", http.StatusUnauthorized)
		return
	}
	// Set an HttpOnly, SameSite=Strict session cookie so subsequent page and
	// /api/* requests authenticate without the token in the URL or the DOM.
	http.SetCookie(w, &http.Cookie{
		Name:     "observer_token",
		Value:    s.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", webContentType("index.html"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleWebAsset serves everything under the embedded web/ tree (app.js,
// style.css, vendor/*) at /web/*.
func (s *Server) handleWebAsset(w http.ResponseWriter, r *http.Request) {
	// r.URL.Path is "/web/app.js"; the embedded FS root is "web", so strip
	// the leading "/web" before rejoining under "web/".
	rel := strings.TrimPrefix(r.URL.Path, "/web")
	serveEmbedded(w, r, "web"+rel)
}

// serveEmbedded reads name from the embedded web FS and writes it with a
// Content-Type derived from the file extension (R8-style explicit mapping —
// not left to the OS mime database, so behavior is identical across hosts).
func serveEmbedded(w http.ResponseWriter, r *http.Request, name string) {
	// path.Clean collapses any "../" so a request can never escape the
	// embedded web/ tree; fs.ReadFile also rejects ".." outright.
	clean := path.Clean(name)
	data, err := webFS.ReadFile(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", webContentType(clean))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// webContentType maps a file extension to the Content-Type the dashboard
// expects, independent of the host's mime database.
func webContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json; charset=utf-8"
	case ".map":
		return "application/json; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func (s *Server) loadManifest() (*manifest.Project, error) {
	b, err := os.ReadFile(filepath.Join(s.repo, obsDataDir, manifestRel))
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", obsDataDir, manifestRel, err)
	}
	return manifest.Parse(b)
}

func (s *Server) loadIndex() (index, error) {
	idx := index{Tabs: map[string]runner.Artifact{}}
	b, err := os.ReadFile(filepath.Join(s.repo, obsDataDir, indexRel))
	if errors.Is(err, os.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		return idx, err
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return idx, err
	}
	if idx.Tabs == nil {
		idx.Tabs = map[string]runner.Artifact{}
	}
	return idx, nil
}

// saveIndex writes _index.json atomically (write a temp file, then rename).
func (s *Server) saveIndex(idx index) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(s.repo, obsDataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	final := filepath.Join(dir, indexRel)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

type projectResponse struct {
	Project string                     `json:"project"`
	Tabs    []manifest.Tab             `json:"tabs"`
	Status  map[string]runner.Artifact `json:"status"` // per-tab last-run freshness, by tab name
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	proj, err := s.loadManifest()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	idx, err := s.loadIndex()
	s.mu.Unlock()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	resp := projectResponse{Project: proj.Project, Tabs: proj.Tabs, Status: idx.Tabs}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdate runs the named tab's generator command and records the
// resulting artifact in _index.json. R15: requires the X-Observer: 1 header
// so a page loaded from another origin can't drive-by POST a shell command
// into this repo.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Observer") != "1" {
		http.Error(w, "missing required X-Observer header", http.StatusForbidden)
		return
	}
	tabName := r.URL.Query().Get("tab")
	if tabName == "" {
		http.Error(w, "missing required ?tab= query param", http.StatusBadRequest)
		return
	}
	proj, err := s.loadManifest()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	tab, ok := findTab(proj, tabName)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown tab %q", tabName), http.StatusNotFound)
		return
	}

	artifact, runErr := runner.Run(r.Context(), s.repo, s.base, tab, time.Now())

	s.mu.Lock()
	idx, err := s.loadIndex()
	if err == nil {
		idx.Tabs[tab.Name] = artifact
		err = s.saveIndex(idx)
	}
	s.mu.Unlock()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}

	if runErr != nil {
		writeJSON(w, http.StatusInternalServerError, artifact)
		return
	}
	writeJSON(w, http.StatusOK, artifact)
}

// handleArtifact serves the raw artifact file for a tab, with Content-Type
// derived from the tab's renderer (R8).
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tabName := r.URL.Query().Get("tab")
	if tabName == "" {
		http.Error(w, "missing required ?tab= query param", http.StatusBadRequest)
		return
	}
	proj, err := s.loadManifest()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	tab, ok := findTab(proj, tabName)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown tab %q", tabName), http.StatusNotFound)
		return
	}
	path := filepath.Join(s.repo, obsDataDir, tab.Output)
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "artifact not generated yet — run Update first", http.StatusNotFound)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(tab.Renderer))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// gitRefsResponse is GET /api/gitrefs's body: the data the dashboard's
// "Compare vs" base-ref picker (every map tab, not just Changes) needs to
// populate itself — current branch (for defaulting) plus every local
// branch and tag.
type gitRefsResponse struct {
	Current  string   `json:"current"`
	Branches []string `json:"branches"`
	Tags     []string `json:"tags"`
}

// handleGitRefs serves GET /api/gitrefs: s.repo's current branch, local
// branches, and tags, straight from gitmeta.ListRefs. Read-only, so (like
// /api/project and /api/artifact) it doesn't require the X-Observer CSRF
// header — only R15's mutating POST /api/update does.
func (s *Server) handleGitRefs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	current, branches, tags, err := gitmeta.ListRefs(s.repo)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, gitRefsResponse{Current: current, Branches: branches, Tags: tags})
}

// gitDiffResponse is GET /api/gitdiff's body: base as given, the commit it
// actually resolved to (baseResolved), and every file that differs between
// HEAD and base's merge-base, keyed by repo-relative path — the data the
// git-aware overlay classifies every map node's added/changed/unchanged
// status from (see app.js's classifyForNode).
type gitDiffResponse struct {
	Base         string                      `json:"base"`
	BaseResolved string                      `json:"baseResolved"`
	Files        map[string]gitmeta.DiffFile `json:"files"`
}

// handleGitDiff serves GET /api/gitdiff?base=<ref>: validates base with
// gitmeta.VerifyRef FIRST, which also rejects (before any git call) a base
// that isn't a valid ref name or begins with "-" — so an option-looking
// ref like "--output=/tmp/x" or "-q" can never reach git as a flag — then
// --end-of-options + ^{commit}-verifies it resolves to a real commit.
// Everything downstream diffs against the RESOLVED hash, never the raw
// ref. A base that fails any of that is a 400; a base that's valid but has
// no merge-base with the working tree (doesn't exist in this history, or
// genuinely unrelated) degrades to an empty file set rather than an error,
// matching Changes/ChangedGraphModel's own tolerance for that case.
func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	base := r.URL.Query().Get("base")
	if base == "" {
		http.Error(w, "missing required ?base= query param", http.StatusBadRequest)
		return
	}
	baseResolved, err := gitmeta.VerifyRef(s.repo, base)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid base ref %q: does not resolve to a commit", base), http.StatusBadRequest)
		return
	}
	// Diff against the RESOLVED hash, never the raw user-supplied base —
	// VerifyRef already confirmed baseResolved is a real commit, so no git
	// call downstream re-parses attacker-controlled ref text.
	mergeBase, err := gitmeta.MergeBase(s.repo, baseResolved, "HEAD")
	if err != nil || mergeBase == "" {
		writeJSON(w, http.StatusOK, gitDiffResponse{Base: base, BaseResolved: baseResolved, Files: map[string]gitmeta.DiffFile{}})
		return
	}
	files, err := gitmeta.DiffAgainst(s.repo, mergeBase)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, gitDiffResponse{Base: base, BaseResolved: baseResolved, Files: files})
}

// handleGraphAt serves GET /api/graphat?tab=<name>&ref=<ref>: tab's
// graph.json AS IT EXISTED at ref (branch/tag/commit), not necessarily the
// current working tree — the data source for the "View a branch"/"Compare
// two refs" UI. ref is optional: "" or "working" means the current working
// tree (the fast path — no worktree, no cache, same generator run
// /api/update already does). Any other ref is resolved+validated and
// built/cached by internal/atref. Read-only like /api/artifact, so it
// doesn't require the X-Observer CSRF header — only R15's mutating POST
// /api/update does.
func (s *Server) handleGraphAt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tabName := r.URL.Query().Get("tab")
	if tabName == "" {
		http.Error(w, "missing required ?tab= query param", http.StatusBadRequest)
		return
	}
	ref := r.URL.Query().Get("ref")
	proj, err := s.loadManifest()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	tab, ok := findTab(proj, tabName)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown tab %q", tabName), http.StatusNotFound)
		return
	}
	if tab.Kind != manifest.KindGenerator {
		http.Error(w, fmt.Sprintf("tab %q has no generator to build at a ref", tabName), http.StatusBadRequest)
		return
	}
	body, err := atref.GraphAtRef(s.repo, tab, ref)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, atref.ErrInvalidRef) {
			status = http.StatusBadRequest
		}
		writeJSONError(w, status, err)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(tab.Renderer))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func contentTypeFor(renderer string) string {
	switch renderer {
	case "svg":
		return "image/svg+xml"
	case "markdown":
		return "text/markdown; charset=utf-8"
	case "json", "table", "map":
		return "application/json"
	default:
		return "text/plain; charset=utf-8"
	}
}

func findTab(proj *manifest.Project, name string) (manifest.Tab, bool) {
	for _, t := range proj.Tabs {
		if t.Name == name {
			return t, true
		}
	}
	return manifest.Tab{}, false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
