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
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	mux.HandleFunc("/web/", s.handleWebAsset)
	mux.HandleFunc("/", s.handleIndex)
	return mux
}

// requireToken wraps an /api/* handler so it 401s unless the request carries
// X-Observer-Token matching s.token, compared in constant time so the
// comparison itself can't leak the token via timing. s.token is always
// non-empty (cmd/observer generates one if OBSERVER_TOKEN is unset), so an
// empty header never matches.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Observer-Token")
		if s.token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "missing or invalid X-Observer-Token", http.StatusUnauthorized)
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
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	html := strings.ReplaceAll(string(data), "__OBSERVER_TOKEN__", s.token)
	w.Header().Set("Content-Type", webContentType("index.html"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
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

func contentTypeFor(renderer string) string {
	switch renderer {
	case "svg":
		return "image/svg+xml"
	case "markdown":
		return "text/markdown; charset=utf-8"
	case "json", "table":
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
