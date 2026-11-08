package gen

import "strings"

// externalAdapters is the override set of adapter package names (the path
// segment immediately after "adapters/") that represent a genuinely
// external system (SaaS/managed service) rather than an in-cluster
// component — per the design spec's kind derivation rules.
var externalAdapters = map[string]bool{
	"stripe":      true,
	"stalwart":    true,
	"keycloak":    true,
	"kansanhuuto": true,
	"brevo":       true,
	"livekit":     true,
	"s3":          true,
	"redis":       true,
	"ics":         true,
}

// ClassifyKind derives a graph node's `kind` from its module-relative
// package id (e.g. "internal/adapters/stripe"), per the design spec §4/§5.1:
//
//   - any package under "cmd/" or with an "http" path segment (entry/HTTP
//     surface, e.g. "api/http/handler") -> "entry"
//   - "adapters/<name>/..." where <name> is in externalAdapters -> "external"
//   - any other "adapters/<name>/..." -> "adapter"
//   - everything else (application, ports, domain, and anything
//     unclassified) -> "service"
func ClassifyKind(id string) string {
	segs := strings.Split(id, "/")
	if len(segs) > 0 && segs[0] == "cmd" {
		return "entry"
	}
	for _, s := range segs {
		if s == "http" {
			return "entry"
		}
	}
	for i, s := range segs {
		if s != "adapters" {
			continue
		}
		if i+1 < len(segs) && externalAdapters[segs[i+1]] {
			return "external"
		}
		return "adapter"
	}
	return "service"
}

// labelFor returns a node's display label: the last path segment of its id
// (e.g. "internal/application/provisioning" -> "provisioning").
func labelFor(id string) string {
	segs := strings.Split(id, "/")
	return segs[len(segs)-1]
}
