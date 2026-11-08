# Observer Canvas Service Map — Design

- **Date:** 2026-11-08
- **Status:** Proposed (design review)
- **Author:** Babak + Claude
- **Repo:** taikuri-infra/observer

## 1. Problem

Observer's graph tabs (Go deps, Frontend deps, Go call graph) render with
graphviz/viz.js. graphviz does **both** jobs — layout *and* drawing — and its
drawing is a dense, unstyled "hairball": hard to read, impossible to extend, and
nothing you can click into. We cannot add product features (detail panels,
metrics, filters, links to work items) to an SVG image.

We want a presentation layer we own — a polished, interactive map in the spirit
of Datadog APM's service map — while keeping Observer's core promise intact:
**the analyzers grab the data; Observer only presents it, beautifully.**

## 2. The core idea — split data from presentation

```
  ANALYZERS (data plane, unchanged)        OBSERVER (presentation plane, new)
  go list / go-callvis ─┐
  dependency-cruiser  ──┼─►  graph.json  ─►  canvas map renderer  ─►  the map
  hotspots (churn)    ──┤   (normalized)       (dagre | force)
  godoc / JSDoc       ──┘        ▲
  future: Tempo / Grafana / Prometheus  ──────┘  (same graph.json)
```

Today graphviz is both engine and painter. We split them: the analyzers (plus a
layout step) produce a **normalized graph model**; Observer draws it. The
normalized model is the seam that makes every future capability — new data
sources, detail panels, work-item links — a matter of the model growing, not the
renderer changing.

## 3. Goals / Non-goals (v1)

**Goals**
- A new **`map` renderer**: an interactive, styled graph over the normalized model.
- **Two layout modes**, switchable: **dagre hierarchical** (default, top-down)
  and a **force-directed service map** (better for hub-and-spoke graphs).
- **Node detail side panel** on click: address, description, metrics, linked work.
- **Descriptions** from two sources: existing **godoc/JSDoc**, and a new
  **`//observer:` annotation convention** for everything else.
- **Package-level** granularity (Go packages, frontend modules).
- Interactions carried over: pan, zoom-to-cursor, drag, neighbour focus, filters.
- Self-contained: no CDN, no `eval`, offline, CSP-clean (Observer's existing model).

**Non-goals (deferred, but designed-for)**
- Function/block-level maps (needs the call graph + file:line — a later map).
- Live runtime sources (Tempo/Grafana/Prometheus) — only the *seam* is built now.
- Real ServicePlus/Jira integration — the panel shows link *slots*, not live data.
- The AI annotation-generator skill — a fast follow after the convention lands.

## 4. The normalized graph model (`graph.json`)

The contract between analyzers and renderer. One artifact per map tab.

```jsonc
{
  "meta": {
    "source": "go-deps",          // which adapter produced this
    "module": "services/api",
    "generatedAt": "2026-11-08T…",
    "sizeBy": "dependents",        // which metric drives node size
    "colorBy": "churn",            // which metric drives the status ring
    "defaultLayout": "hierarchical"
  },
  "nodes": [
    {
      "id": "internal/application/provisioning",
      "label": "provisioning",
      "kind": "service",           // entry | service | adapter | external | db
      "metrics": { "dependents": 7, "imports": 12, "churn": 44, "loc": 1900 },
      "location": {                // "address"
        "path": "services/api/internal/application/provisioning",
        "primaryFile": "pipeline.go",
        "files": 6
        // "lineStart"/"lineEnd" reserved for function-level maps
      },
      "description": {
        "text": "implements the tenant provisioning pipeline…",
        "source": "godoc"          // godoc | jsdoc | observer-annotation | none
      },
      "links": [                   // work-item slots (empty until integrated)
        { "type": "serviceplus", "id": null },
        { "type": "jira", "id": null }
      ]
    }
  ],
  "edges": [
    { "from": "api/http/handler", "to": "internal/application/provisioning", "weight": 1 }
  ]
}
```

Design notes:
- **`kind`** is derived from the path (entry/http, application, adapters, ports,
  domain) with an `external` override list (stripe, stalwart, keycloak, …).
- **`metrics`** is an open map. v1 fills `dependents`/`imports` (degree) and
  `churn` (from `hotspots.json`). A future Tempo adapter fills `reqPerSec`,
  `latencyMs`, `errorRate` under the *same* key — the renderer just reads
  `sizeBy`/`colorBy` from `meta`.
- **`description.source`** lets the panel badge where text came from and show the
  "Generate with Observer AI" empty state when `source == "none"`.
- **`links`** is forward-looking: a resolver turns `{type,id}` into a URL once an
  integration exists. v1 renders them as inert "connect later" rows.

## 5. Data sources (v1)

### 5.1 Structure → nodes/edges
A new generator, `observer gen graphmodel`, produces `graph.json` per module:
- **Go:** `go list -json -deps ./...` (import edges, package dirs, file lists) —
  more robust than scraping `godeps.dot`. One map per Go module.
- **Frontend:** reuse the dependency-cruiser output (already a dep graph) and
  fold to module/directory granularity. One map per frontend package.

### 5.2 Metrics
- `dependents` / `imports` = in/out degree computed from edges.
- `churn` = per-package sum from `hotspots.json` (map each hotspot file to its
  package dir). Packages with no hotspot entry get `churn: 0`.

### 5.3 Descriptions — two sources
1. **Existing docs (works today — 46/61 Go packages covered):**
   - Go: the package doc comment (the `//` block before `package X`, via
     `go/parser` `PackageClauseOnly` + `ast` doc, or `go doc`).
   - Frontend: the leading JSDoc/banner comment of the module entry file.
2. **`//observer:` annotation convention (new):** a greppable, language-neutral
   directive the analyzer reads and (later) an AI skill writes. It lives in a
   normal comment, so it never breaks a build and co-exists with godoc.

   ```go
   // Package provisioning implements the tenant provisioning pipeline.
   //
   //observer:summary Orchestrates tenant creation across Keycloak, DB and mail.
   //observer:owner platform
   //observer:task SP-142
   ```
   - Keys (v1): `summary` (free text), `owner`, `task` (work-item id).
   - Precedence for `description.text`: `//observer:summary` > godoc/JSDoc > none.
   - `//observer:task` populates a `links[]` entry.
   - The same `//observer:` line form works above a `func` for the future
     function-level map — the parser is granularity-agnostic.

## 6. Rendering (the presentation plane)

### 6.1 Tech
- **Layout — hierarchical:** vendor **dagre** (UMD, ~95 KB gzip-ish; no `eval`,
  CSP-safe). Proven on the full servicePlus graph (61 nodes / 170 edges) in the
  design mockup.
- **Layout — force:** vendor the **`d3-force`** module only (small, no `eval`),
  or a compact custom Barnes-Hut-free sim. Decision at build time; `d3-force`
  preferred for quality. Used for the hub-and-spoke "service map" view.
- **Drawing:** **SVG + CSS** (DOM nodes styled by tokens), matching Observer's
  existing dark theme. SVG keeps the detail panel/cards as ordinary styled DOM
  and handles Observer's graph sizes (tens–few-hundred nodes) comfortably. If a
  future graph is too large for SVG, the renderer swaps to `<canvas>` behind the
  same model — out of scope now, noted as the escape hatch.
- All assets vendored into `internal/server/web/vendor/` and embedded via the
  existing `//go:embed`, consistent with viz.js/mermaid today.

### 6.2 The map component (`internal/server/web/`)
A new renderer dispatched by `tab.renderer === "map"`:
- Loads `graph.json` (and its node metadata) for the tab.
- Runs the selected layout → node positions + edge points.
- Draws nodes (circle, type glyph, status ring colored by `colorBy`, size by
  `sizeBy`), edges (curved, width by weight), a filter rail (kind chips +
  search), and a legend.
- **Interactions:** wheel zoom-to-cursor, drag pan, click-to-focus neighbourhood
  (dim the rest), hover metric card. These mirror the pan/zoom/focus already
  shipped for the SVG renderers (reuse where PR #1 is merged).
- **Layout toggle:** hierarchical ⇄ force, re-running layout in place.

### 6.3 Detail side panel
Opens on node click; closes on ×, background click, or re-click.
Sections, in order:
1. **Header** — label, kind badge, type color.
2. **Address** — `location.path` + file count (+ `file:line` when present).
3. **Description** — `description.text` with a source badge (`from godoc` /
   `observer`); when `source == "none"`, an empty state with a **"✦ Generate
   with Observer AI"** affordance (wired to the generator skill later).
4. **Metrics** — dependents, imports, churn (open to runtime metrics later).
5. **Linked work** — rows per `links[]`; inert "connect later" until integrated.

## 7. Integration with Observer

- **Manifest:** a new tab per Go module / frontend package with
  `renderer: "map"`, `kind: "generator"`, `command: observer gen graphmodel …`,
  `output: map-<module>.json`. Detection reuses the existing module discovery.
- **Server:** `/api/artifact?tab=…` already streams the output file; the `map`
  renderer consumes JSON instead of SVG/DOT. No new endpoint needed.
- **Back-compat:** existing `graph`/`svg`/`mermaid` renderers stay. The map is
  added alongside Go deps (v1), not a replacement; we can retire the old graph
  tab later if the map supersedes it.
- **Self-contained/security:** new vendored libs are static files, no `eval`,
  same CSP. The annotation parser reads files already in scope; no new trust
  surface beyond what the analyzers already touch.

## 8. Extensibility (how the future asks drop in)

- **New data source (Tempo/Grafana/Prometheus):** write an adapter that emits the
  same `graph.json` (services as nodes, calls as edges, runtime `metrics`). The
  renderer and panel are unchanged; `meta.sizeBy`/`colorBy` point at the new
  metrics. "Sections grabbing from any source" = many adapters → one model → one
  map.
- **Function-level granularity:** a call-graph adapter emits function nodes with
  `location.lineStart/End`; the panel's Address shows `file:line`. Same renderer.
- **Work-item links:** a resolver maps `links[].{type,id}` → URL once ServicePlus
  or Jira integration exists. Panel rows become live.
- **AI annotation generator:** a skill that reads undocumented packages/functions
  and writes `//observer:summary …` lines; Observer picks them up on next run.

## 9. Testing

- **Go (unit):** `graphmodel` generator — kind classification, degree metrics,
  churn mapping from `hotspots.json`, godoc/JSDoc extraction, `//observer:`
  annotation parsing and precedence. Golden `graph.json` for a fixture repo and a
  servicePlus acceptance test (filesystem-only, no external tools).
- **JS (visual/manual):** the map renderer, layout toggle, and panel are
  exercised in-browser against servicePlus (Observer has no JS test harness
  today; follow the existing manual-verification practice).

## 10. Build phases (for the implementation plan)

1. **Model + generator** — `graph.json` schema, `observer gen graphmodel` (Go
   deps first), metrics, godoc extraction, churn mapping, tests.
2. **`//observer:` annotation** — parser + precedence + `task`→`links`, tests.
3. **Map renderer** — dagre layout, SVG/CSS nodes/edges, filters, pan/zoom/focus.
4. **Detail side panel** — address/description/metrics/links + empty state.
5. **Force layout** — second mode + toggle.
6. **Wire-up** — manifest tab, embed vendored libs, docs; Frontend-deps map.

## 11. Open questions

- `d3-force` vendored module vs. a compact custom force sim (quality vs. size).
- Whether the map **replaces** the Go deps `graph` tab in v1 or runs alongside it
  (lean: alongside, retire later).
- Exact `//observer:` key set beyond `summary`/`owner`/`task` (keep minimal; grow
  on demand).
