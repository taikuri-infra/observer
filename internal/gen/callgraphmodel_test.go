package gen

import (
	"sort"
	"testing"
)

// fixtureCallGraphDOT mimics the structural quirks of real go-callvis DOT
// output that break naive parsers: a "cluster_focus" subgraph with a nested
// per-package subgraph, a tooltip attribute whose *value* contains literal
// "[" / "]" characters (so bracket-counting without quote-awareness would
// misparse it), a closure node ("main$1"), and a pointer-receiver method
// node/edge.
const fixtureCallGraphDOT = `digraph gocallvis {
	graph [bgcolor=lightgray];
	subgraph cluster_focus {
		graph [label=main];
		subgraph "cluster_example.com/mod/internal/util" {
			graph [label=util];
			"example.com/mod/internal/util.Helper" [fillcolor=moccasin,
				label=Helper,
				tooltip="example.com/mod/internal/util.Helper | defined in util.go:10"];
		}
		"example.com/mod/cmd/app.main" [fillcolor=lightblue,
			label=main,
			tooltip="example.com/mod/cmd/app.main | defined in main.go:5"];
		"example.com/mod/cmd/app.main" -> "example.com/mod/internal/util.Helper" [color=saddlebrown,
			tooltip="at main.go:7: calling [example.com/mod/internal/util.Helper]"];
		"example.com/mod/cmd/app.main" -> "example.com/mod/cmd/app.main$1" [arrowhead=normalnoneodot];
		"example.com/mod/cmd/app.main$1" [fillcolor=lightblue,
			label="main$1"];
		"example.com/mod/cmd/app.main" -> "(*example.com/mod/internal/util.Client).Do" [color=saddlebrown,
			tooltip="at main.go:8: calling [(*example.com/mod/internal/util.Client).Do]"];
		"(*example.com/mod/internal/util.Client).Do" [fillcolor=moccasin,
			label="(*Client).Do"];
	}
}
`

func TestParseCallGraphDOT(t *testing.T) {
	g, err := ParseCallGraphDOT([]byte(fixtureCallGraphDOT), "example.com/mod")
	if err != nil {
		t.Fatalf("ParseCallGraphDOT: %v", err)
	}

	if len(g.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d: %+v", len(g.Nodes), g.Nodes)
	}
	if len(g.Edges) != 3 {
		t.Fatalf("expected 3 edges, got %d: %+v", len(g.Edges), g.Edges)
	}

	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	main, ok := byID["example.com/mod/cmd/app.main"]
	if !ok {
		t.Fatalf("missing main node; nodes: %+v", g.Nodes)
	}
	if main.Label != "main" {
		t.Errorf("main.Label = %q, want %q", main.Label, "main")
	}
	if main.Kind != "func" {
		t.Errorf("main.Kind = %q, want %q", main.Kind, "func")
	}
	if main.Location.Path != "cmd/app" {
		t.Errorf("main.Location.Path = %q, want %q", main.Location.Path, "cmd/app")
	}
	if main.Metrics.Callees != 3 {
		t.Errorf("main.Metrics.Callees = %d, want 3", main.Metrics.Callees)
	}
	if main.Metrics.Callers != 0 {
		t.Errorf("main.Metrics.Callers = %d, want 0", main.Metrics.Callers)
	}

	helper, ok := byID["example.com/mod/internal/util.Helper"]
	if !ok {
		t.Fatalf("missing Helper node")
	}
	if helper.Label != "Helper" {
		t.Errorf("helper.Label = %q, want %q", helper.Label, "Helper")
	}
	if helper.Location.Path != "internal/util" {
		t.Errorf("helper.Location.Path = %q, want %q", helper.Location.Path, "internal/util")
	}
	if helper.Metrics.Callers != 1 {
		t.Errorf("helper.Metrics.Callers = %d, want 1", helper.Metrics.Callers)
	}

	closure, ok := byID["example.com/mod/cmd/app.main$1"]
	if !ok {
		t.Fatalf("missing closure node")
	}
	if closure.Label != "main$1" {
		t.Errorf("closure.Label = %q, want %q", closure.Label, "main$1")
	}

	method, ok := byID["(*example.com/mod/internal/util.Client).Do"]
	if !ok {
		t.Fatalf("missing method node")
	}
	if method.Label != "(*Client).Do" {
		t.Errorf("method.Label = %q, want %q", method.Label, "(*Client).Do")
	}
	if method.Location.Path != "internal/util" {
		t.Errorf("method.Location.Path = %q, want %q", method.Location.Path, "internal/util")
	}
	if method.Metrics.Callers != 1 {
		t.Errorf("method.Metrics.Callers = %d, want 1", method.Metrics.Callers)
	}

	if g.Meta.SizeBy != "callers" || g.Meta.ColorBy != "callers" {
		t.Errorf("meta sizeBy/colorBy = %q/%q, want callers/callers", g.Meta.SizeBy, g.Meta.ColorBy)
	}
	if g.Meta.Source != "go-callvis" {
		t.Errorf("meta.Source = %q, want go-callvis", g.Meta.Source)
	}

	// Nodes must be sorted by id (same convention as GraphModel/
	// ParseFrontendGraph) — a stable graph.json diffs cleanly in git.
	ids := make([]string, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[i] = n.ID
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("nodes not sorted by id: %v", ids)
	}
}

func TestParseCallGraphDOT_NoNodes(t *testing.T) {
	_, err := ParseCallGraphDOT([]byte("digraph gocallvis {}\n"), "example.com/mod")
	if err == nil {
		t.Fatal("expected an error for a DOT file with no function nodes")
	}
}

func TestSplitFuncPkgPath(t *testing.T) {
	cases := map[string]string{
		"example.com/mod/cmd/app.main":               "example.com/mod/cmd/app",
		"example.com/mod/cmd/app.main$1":             "example.com/mod/cmd/app",
		"(*example.com/mod/internal/util.Client).Do": "example.com/mod/internal/util",
		"(example.com/mod/internal/util.Client).Do":  "example.com/mod/internal/util",
		"github.com/rs/zerolog/log.Output":           "github.com/rs/zerolog/log",
		"noDotAtAll":                                 "noDotAtAll",
	}
	for id, want := range cases {
		if got := splitFuncPkgPath(id); got != want {
			t.Errorf("splitFuncPkgPath(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestCallGraphFallbackLabel(t *testing.T) {
	cases := map[string]string{
		"example.com/mod/cmd/app.main":               "main",
		"(*example.com/mod/internal/util.Client).Do": "Do",
	}
	for id, want := range cases {
		if got := callGraphFallbackLabel(id); got != want {
			t.Errorf("callGraphFallbackLabel(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestJoinDOTContinuations(t *testing.T) {
	in := "tooltip=\"abc\\\ndef\""
	want := "tooltip=\"abcdef\""
	if got := joinDOTContinuations(in); got != want {
		t.Errorf("joinDOTContinuations(%q) = %q, want %q", in, got, want)
	}
}
