package gen

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFiberModule writes go.mod and the fiber router test fixture into dir, a
// tiny throwaway module (no real gofiber/fiber dependency needed — APIRoutes
// only parses source text, it never type-checks or builds the module).
func writeFiberModule(t *testing.T, dir string) {
	t.Helper()
	if err := writeFileMkdir(filepath.Join(dir, "go.mod"), "module example.com/fixture\n\ngo 1.22\n"); err != nil {
		t.Fatal(err)
	}
	routerSrc := `package router

import "github.com/gofiber/fiber/v2"

type Dependencies struct {
	AuthMiddleware fiber.Handler
	AdminOrOwner   fiber.Handler
	Handler        *Handler
}

type Handler struct{}

func (h *Handler) GetTenant(c *fiber.Ctx) error { return nil }
func (h *Handler) Invite(c *fiber.Ctx) error     { return nil }
func (h *Handler) ListAdmins(c *fiber.Ctx) error { return nil }

func Setup(app *fiber.App, deps Dependencies) {
	api := app.Group("/api/v1")
	api.Get("/health", healthHandler)

	auth := api.Group("", deps.AuthMiddleware)
	auth.Get("/tenant", deps.Handler.GetTenant)
	auth.Post("/users/invite", deps.AdminOrOwner, deps.Handler.Invite)

	admin := auth.Group("/admin")
	admin.Get("/users", deps.Handler.ListAdmins)

	app.Get("/health/realtime", healthHandler)
}

func healthHandler(c *fiber.Ctx) error { return nil }
`
	if err := writeFileMkdir(filepath.Join(dir, "router", "router.go"), routerSrc); err != nil {
		t.Fatal(err)
	}
}

// writeFileMkdir is writeFile plus MkdirAll — unlike the git-repo fixtures
// in changes_test.go/hotspots_test.go, APIRoutes' fixtures need nested
// directories (router/, vendor/decoy/, services/api/...) that don't exist
// yet.
func writeFileMkdir(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFile(path, content)
}

func TestAPIRoutes(t *testing.T) {
	dir := t.TempDir()
	writeFiberModule(t, dir)

	routes, err := APIRoutes(dir, dir)
	if err != nil {
		t.Fatalf("APIRoutes: %v", err)
	}

	byPath := map[string]Route{}
	for _, r := range routes {
		byPath[r.Method+" "+r.Path] = r
	}

	cases := []struct {
		key        string
		handler    string
		middleware string
	}{
		{"GET /api/v1/health", "healthHandler", ""},
		{"GET /api/v1/tenant", "deps.Handler.GetTenant", ""},
		{"POST /api/v1/users/invite", "deps.Handler.Invite", "deps.AdminOrOwner"},
		{"GET /api/v1/admin/users", "deps.Handler.ListAdmins", ""},
		{"GET /health/realtime", "healthHandler", ""},
	}
	for _, c := range cases {
		r, ok := byPath[c.key]
		if !ok {
			t.Errorf("missing route %s; got: %v", c.key, routeKeys(routes))
			continue
		}
		if r.Handler != c.handler {
			t.Errorf("%s: handler = %q, want %q", c.key, r.Handler, c.handler)
		}
		if r.Middleware != c.middleware {
			t.Errorf("%s: middleware = %q, want %q", c.key, r.Middleware, c.middleware)
		}
		if r.File == "" {
			t.Errorf("%s: File is empty", c.key)
		}
		if r.Line <= 0 {
			t.Errorf("%s: Line = %d, want > 0", c.key, r.Line)
		}
	}

	if len(routes) != len(cases) {
		t.Errorf("len(routes) = %d, want %d; got: %v", len(routes), len(cases), routeKeys(routes))
	}

	// Results must be sorted by (path, method).
	sorted := append([]Route(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		return sorted[i].Method < sorted[j].Method
	})
	for i := range routes {
		if routes[i].Path != sorted[i].Path || routes[i].Method != sorted[i].Method {
			t.Fatalf("routes not sorted by (path, method): %v", routeKeys(routes))
		}
	}
}

// TestAPIRoutesSkipsVendorAndTestdata asserts a route-shaped file under
// vendor/ or testdata/ is never scanned.
func TestAPIRoutesSkipsVendorAndTestdata(t *testing.T) {
	dir := t.TempDir()
	writeFiberModule(t, dir)
	decoy := `package decoy

import "github.com/gofiber/fiber/v2"

func Setup(app *fiber.App) {
	app.Get("/should-not-appear", nil)
}
`
	if err := writeFileMkdir(filepath.Join(dir, "vendor", "decoy", "decoy.go"), decoy); err != nil {
		t.Fatal(err)
	}
	if err := writeFileMkdir(filepath.Join(dir, "testdata", "decoy.go"), decoy); err != nil {
		t.Fatal(err)
	}

	routes, err := APIRoutes(dir, dir)
	if err != nil {
		t.Fatalf("APIRoutes: %v", err)
	}
	for _, r := range routes {
		if r.Path == "/should-not-appear" {
			t.Fatalf("route from vendor/testdata leaked into results: %+v", r)
		}
	}
}

// TestAPIRoutesFileIsRepoRelative asserts File is repo-relative when
// moduleDir is nested under repoRoot.
func TestAPIRoutesFileIsRepoRelative(t *testing.T) {
	repo := t.TempDir()
	moduleDir := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiberModule(t, moduleDir)

	routes, err := APIRoutes(repo, moduleDir)
	if err != nil {
		t.Fatalf("APIRoutes: %v", err)
	}
	for _, r := range routes {
		want := filepath.ToSlash(filepath.Join("services", "api", "router", "router.go"))
		if r.File != want {
			t.Errorf("File = %q, want %q", r.File, want)
		}
	}
}

func routeKeys(routes []Route) []string {
	var keys []string
	for _, r := range routes {
		keys = append(keys, r.Method+" "+r.Path)
	}
	return keys
}
