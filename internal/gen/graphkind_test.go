package gen

import "testing"

// TestClassifyKind pins the kind-derivation rules against servicePlus's
// own real package ids (design spec §5.1): entry/http, application,
// adapters (with the external override list), ports/domain.
func TestClassifyKind(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"api/http/handler", "entry"},
		{"api/http/handler/realtime", "entry"},
		{"api/http/middleware", "entry"},
		{"api/http/router", "entry"},
		{"cmd/server", "entry"},
		{"cmd/retention-sweeper", "entry"},

		{"internal/adapters/postgres", "adapter"},
		{"internal/adapters/postgres/migrationrunner", "adapter"},
		{"internal/adapters/circuitbreaker", "adapter"},
		{"internal/adapters/cloudflare", "adapter"},
		{"internal/adapters/wordpress", "adapter"},

		{"internal/adapters/stripe", "external"},
		{"internal/adapters/stalwart", "external"},
		{"internal/adapters/keycloak", "external"},
		{"internal/adapters/kansanhuuto", "external"},
		{"internal/adapters/brevo", "external"},
		{"internal/adapters/livekit", "external"},
		{"internal/adapters/s3", "external"},
		{"internal/adapters/redis", "external"},
		{"internal/adapters/ics", "external"},

		{"internal/application/provisioning", "service"},
		{"internal/application/provisioning/manifests", "service"},
		{"internal/ports", "service"},
		{"internal/domain/entity", "service"},
		{"internal/domain/valueobject", "service"},
		{"config", "service"},
		{"migrations", "service"},
		{"scripts/migrate-projects-to-tenant-db", "service"},
		{"internal/realtime", "service"},
		{"internal/tenantctx", "service"},
	}
	for _, c := range cases {
		if got := ClassifyKind(c.id); got != c.want {
			t.Errorf("ClassifyKind(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestLabelFor(t *testing.T) {
	cases := map[string]string{
		"internal/application/provisioning": "provisioning",
		"cmd/server":                        "server",
		"config":                            "config",
	}
	for id, want := range cases {
		if got := labelFor(id); got != want {
			t.Errorf("labelFor(%q) = %q, want %q", id, got, want)
		}
	}
}
