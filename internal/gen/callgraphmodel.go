package gen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// --- a minimal DOT tokenizer -----------------------------------------------
//
// go-callvis's "-format dot" output is Graphviz DOT run through `dot`'s own
// layout pass, which means every node/edge carries large "pos"/"tooltip"
// attribute strings (tooltip text even contains literal "[" / "]"
// characters, e.g. `tooltip="at main.go:12: calling [pkg.Func]"`). A
// line-oriented or bracket-counting parser would misfire on those
// characters, so this tokenizes the whole file properly instead: quoted
// strings are lexed atomically (whatever they contain, including stray
// brackets), and everything else is lexed as single-char punctuation or a
// bareword run. The grammar we actually need from there is tiny — "A" ->
// "B" edges and "A" [k=v, ...] node declarations, found by looking at each
// top-level STRING token's lookahead — so there's no need to track
// {}-nesting (subgraph/cluster bodies) at all: node ids in go-callvis DOT
// are already fully-qualified, so package membership is derived from the id
// text itself (splitFuncID), never from which cluster a node happened to be
// declared inside.

type dotTokKind int

const (
	dotString dotTokKind = iota
	dotBareword
	dotLBracket
	dotRBracket
	dotArrow
	dotEquals
	dotOther // '{', '}', ',', ';' — structurally irrelevant to us, skipped
)

type dotTok struct {
	kind dotTokKind
	val  string
}

// isDotBarewordChar covers every character go-callvis emits in an unquoted
// attribute key/value (identifiers, color names, numbers) — deliberately
// generous since misclassifying a rare character only widens a bareword
// token, it doesn't break parsing.
func isDotBarewordChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '_', '.', '/', '*', '(', ')', '$', ':', '-', '+', '@', '#':
		return true
	}
	return false
}

// lexDOT tokenizes raw DOT source. Backslash-newline continuations inside
// quoted strings (DOT's line-wrap convention — see go-callvis output) must
// already be joined by the caller (joinDOTContinuations) before lexing, so
// that a quoted string's content never contains a literal newline and
// every remaining newline is a safe statement separator.
func lexDOT(s string) []dotTok {
	var toks []dotTok
	n := len(s)
	i := 0
	for i < n {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"':
			j := i + 1
			var b strings.Builder
			for j < n && s[j] != '"' {
				if s[j] == '\\' && j+1 < n {
					b.WriteByte(s[j+1])
					j += 2
					continue
				}
				b.WriteByte(s[j])
				j++
			}
			toks = append(toks, dotTok{dotString, b.String()})
			i = j + 1
		case c == '[':
			toks = append(toks, dotTok{dotLBracket, "["})
			i++
		case c == ']':
			toks = append(toks, dotTok{dotRBracket, "]"})
			i++
		case c == '=':
			toks = append(toks, dotTok{dotEquals, "="})
			i++
		case c == '-' && i+1 < n && s[i+1] == '>':
			toks = append(toks, dotTok{dotArrow, "->"})
			i += 2
		case c == '{' || c == '}' || c == ',' || c == ';':
			toks = append(toks, dotTok{dotOther, string(c)})
			i++
		default:
			j := i
			for j < n && isDotBarewordChar(s[j]) {
				j++
			}
			if j == i {
				i++ // unrecognized punctuation: skip, never fatal
				continue
			}
			toks = append(toks, dotTok{dotBareword, s[i:j]})
			i = j
		}
	}
	return toks
}

// joinDOTContinuations removes every backslash-newline pair — DOT's
// line-wrap convention for long quoted strings (see package doc above) —
// so the lexer never has to special-case a quoted string spanning lines.
func joinDOTContinuations(s string) string {
	return strings.ReplaceAll(s, "\\\n", "")
}

// consumeAttrList expects tokens[i] to be the first token after an already-
// consumed "[", and returns every bareword/string "key=value" pair found up
// to (and past) the matching "]", plus the index just past it. Nested "["
// is tracked defensively, though attribute lists never actually nest in
// practice (a value's own "[" / "]", if any, is inside a dotString token and
// so never produces a dotLBracket/dotRBracket in the first place).
func consumeAttrList(tokens []dotTok, i int) (map[string]string, int) {
	attrs := map[string]string{}
	depth := 1
	for i < len(tokens) {
		t := tokens[i]
		switch t.kind {
		case dotLBracket:
			depth++
			i++
		case dotRBracket:
			depth--
			i++
			if depth == 0 {
				return attrs, i
			}
		case dotBareword:
			if i+2 < len(tokens) && tokens[i+1].kind == dotEquals &&
				(tokens[i+2].kind == dotString || tokens[i+2].kind == dotBareword) {
				attrs[t.val] = tokens[i+2].val
				i += 3
			} else {
				i++
			}
		default:
			i++
		}
	}
	return attrs, i
}

// callGraphEdgeKey dedupes edges — go-callvis already merges multiple call
// sites between the same two functions into one DOT edge (its tooltip lists
// every call site), but a defensive dedupe costs nothing.
type callGraphEdgeKey struct{ from, to string }

// ParseCallGraphDOT parses go-callvis's `-format dot` output into a
// normalized graph.json: one GraphNode per function (kind "func"), one
// GraphEdge per call. modulePath is the Go module's import path (from
// go.mod) — a node's location.path is its package's import path with
// modulePath trimmed off (e.g. "internal/adapters/postgres"), matching
// GraphModel's convention; third-party functions go-callvis also includes
// (since -nostd only drops stdlib, not other modules) keep their full
// import path as location.path, since they're outside this repo.
//
// File:line is not available here: go-callvis's DOT only gives a bare
// filename + line inside its tooltip text ("defined in main.go:93"), with
// no directory — not enough to build a repo-relative primaryFile, so
// location is package-only, same as the design note calls for.
func ParseCallGraphDOT(dot []byte, modulePath string) (*Graph, error) {
	tokens := lexDOT(joinDOTContinuations(string(dot)))

	nodeLabel := map[string]string{}
	nodesSeen := map[string]bool{}
	edgeSeen := map[callGraphEdgeKey]bool{}
	var edges []GraphEdge

	i := 0
	for i < len(tokens) {
		t := tokens[i]
		if t.kind != dotString {
			i++
			continue
		}

		// "A" -> "B" [attrs...];  — a call edge.
		if i+2 < len(tokens) && tokens[i+1].kind == dotArrow && tokens[i+2].kind == dotString {
			from, to := t.val, tokens[i+2].val
			nodesSeen[from] = true
			nodesSeen[to] = true
			i += 3
			if i < len(tokens) && tokens[i].kind == dotLBracket {
				_, ni := consumeAttrList(tokens, i+1)
				i = ni
			}
			key := callGraphEdgeKey{from, to}
			if from != to && !edgeSeen[key] {
				edgeSeen[key] = true
				edges = append(edges, GraphEdge{From: from, To: to, Weight: 1})
			}
			continue
		}

		// "A" [attrs...];  — a node declaration (function or type).
		if i+1 < len(tokens) && tokens[i+1].kind == dotLBracket {
			id := t.val
			nodesSeen[id] = true
			attrs, ni := consumeAttrList(tokens, i+2)
			i = ni
			if lbl, ok := attrs["label"]; ok && lbl != "" {
				nodeLabel[id] = lbl
			}
			continue
		}

		i++
	}

	if len(nodesSeen) == 0 {
		return nil, fmt.Errorf("parse call graph dot: no function nodes found")
	}

	ids := make([]string, 0, len(nodesSeen))
	for id := range nodesSeen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	callers := map[string]int{} // in-degree: who calls this function
	callees := map[string]int{} // out-degree: what this function calls
	for _, e := range edges {
		callees[e.From]++
		callers[e.To]++
	}

	nodes := make([]GraphNode, 0, len(ids))
	for _, id := range ids {
		label := nodeLabel[id]
		if label == "" {
			label = callGraphFallbackLabel(id)
		}
		pkgPath := splitFuncPkgPath(id)
		locPath := pkgPath
		if modulePath != "" && underModule(pkgPath, modulePath) {
			locPath = trimModulePrefix(pkgPath, modulePath)
		}
		nodes = append(nodes, GraphNode{
			ID:    id,
			Label: label,
			Kind:  "func",
			Metrics: GraphMetrics{
				Callers: callers[id],
				Callees: callees[id],
			},
			Location:    GraphLocation{Path: locPath},
			Description: GraphDescription{Source: "none"},
			Links:       []GraphLink{},
		})
	}

	return &Graph{
		Meta: GraphMeta{
			Source:        "go-callvis",
			Module:        modulePath,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			SizeBy:        "callers",
			ColorBy:       "callers",
			DefaultLayout: "hierarchical",
		},
		Nodes: nodes,
		Edges: edges,
	}, nil
}

// splitFuncPkgPath derives a go-callvis function id's package import path:
//   - "pkg/path.FuncName"            -> "pkg/path"
//   - "pkg/path.FuncName$1"          -> "pkg/path" (closures: "$N" suffix)
//   - "(*pkg/path.TypeName).Method"  -> "pkg/path" (pointer receiver)
//   - "(pkg/path.TypeName).Method"   -> "pkg/path" (value receiver)
//
// Best-effort: Go import paths never contain "(", ")", or a bare trailing
// "." before an identifier, so splitting on the last "." (after unwrapping
// an optional receiver parenthesis) reliably recovers the package even
// though this is working from go-callvis's string form, not an AST.
func splitFuncPkgPath(id string) string {
	inner := id
	if strings.HasPrefix(id, "(") {
		if close := strings.Index(id, ")"); close > 0 {
			inner = strings.TrimPrefix(id[1:close], "*")
		}
	}
	if dot := strings.LastIndex(inner, "."); dot >= 0 {
		return inner[:dot]
	}
	return inner
}

// callGraphFallbackLabel derives a short display name when go-callvis's DOT
// didn't give this node a "label" attribute (defensive — observed go-callvis
// output always sets one, but a hand-built test fixture might not).
func callGraphFallbackLabel(id string) string {
	if idx := strings.LastIndex(id, ")."); idx >= 0 && idx+2 <= len(id) {
		return id[idx+2:]
	}
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

// CallGraphModel runs `go-callvis -format dot -nostd <mainPkg>` with cwd
// moduleDir and parses its DOT output into graph.json (ParseCallGraphDOT).
// mainPkg must resolve to exactly ONE `package main` — go-callvis's own
// limitation is that running it over a pattern that matches more than one
// main errors with "found multiple packages with name: main", which is
// exactly the error the graphviz-SVG "Go call graph" tab used to hit on
// multi-binary repos (see plugins.findMainPackageDirs, which is how callers
// avoid that: one CallGraphModel call per detected main package).
//
// go-callvis has no stdout/file-content mode for DOT ("-file" is required —
// omitting it starts an HTTP server and never returns), so this runs it
// against a throwaway temp file and reads that back.
func CallGraphModel(moduleDir, mainPkg string) (*Graph, error) {
	modulePath, err := readModulePath(moduleDir)
	if err != nil {
		return nil, err
	}

	pkgArg := mainPkg
	if !strings.HasPrefix(pkgArg, ".") && !strings.HasPrefix(pkgArg, "/") {
		pkgArg = "./" + strings.TrimPrefix(pkgArg, "./")
	}

	tmpDir, err := os.MkdirTemp("", "observer-callgraph-")
	if err != nil {
		return nil, fmt.Errorf("gen callgraphmodel: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	stem := filepath.Join(tmpDir, "graph")

	cmd := exec.Command("go-callvis", "-format", "dot", "-file", stem, "-nostd", pkgArg)
	cmd.Dir = moduleDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go-callvis -format dot -nostd %s in %s: %w\n%s", pkgArg, moduleDir, err, out)
	}

	dotBytes, err := os.ReadFile(stem + ".dot")
	if err != nil {
		return nil, fmt.Errorf("gen callgraphmodel: read go-callvis dot output: %w", err)
	}
	g, err := ParseCallGraphDOT(dotBytes, modulePath)
	if err != nil {
		return nil, err
	}
	// Enrich every "func" node with its real file + line range (go/ast,
	// not go-callvis's own DOT — see funcloc.go's doc comment for why DOT
	// alone can't give us this). Best-effort per node (unresolved nodes
	// just keep their package-only location, same as before this
	// enrichment existed); only a hard `go list` failure propagates.
	if _, err := enrichCallGraphLocations(g, moduleDir); err != nil {
		return nil, fmt.Errorf("enrich call graph locations: %w", err)
	}
	return g, nil
}
