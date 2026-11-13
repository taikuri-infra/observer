# Observer — Design

> Local-first, Docker-packaged **code observability** for polyglot repos.
> One container, many projects. Generated artifacts are committed to each repo
> so your architecture has a **git history** you can diff across versions.

Status: **design approved** (2026-11-08), **synced to as-built** (2026-11-13).
This document is the source of truth for v1 and tracks what actually shipped —
the tab set (§4), data flow (§5) and roadmap (§9) reflect the current `main`.
See [Roadmap](#roadmap) for what's next.

---

## 1. Why

Understanding a fast-moving ("vibe-coded") codebase is hard: how do the modules
fit together, how is the database shaped, what changed in this version, where are
the risky hotspots? Best-in-class tools exist (go-callvis, dependency-cruiser,
code-maat, graphify, DBML, Sourcegraph, CodeScene) but they are scattered, some
are hosted/paid, and each has its own install footprint.

**Observer unifies them into one local dashboard** that:

- runs as **one Docker image** (zero host tooling beyond Docker),
- analyzes **any repo you mount** (Go + Next.js now; Python later),
- writes compact artifacts into that repo's **`obs-data/`** folder,
- which you **commit to git** — giving you architecture history for free.

It is explicitly **local and private** (no SaaS, nothing leaves the machine) to
fit an EU-hosted / no-vendor posture.

## 2. Non-goals (YAGNI)

- Not a hosted SaaS, not a team portal (that's Backstage's job).
- Not a replacement for the underlying tools — it **orchestrates** them.
- v1 does not include the heavy "service" tools (Sourcegraph, Azimutt) — those
  are v2 (see Roadmap). (The cross-version diff view was originally scoped to v2
  but shipped early in v1: every map tab has a git-diff overlay and an atref
  "View / Compare across git refs" time-travel control — see §4 and §9.)

## 3. Architecture

```
┌─ Docker image: taikuri-infra/observer ─────────────────────────────┐
│  observer (Go binary): HTTP server + plugin orchestrator + web UI   │
│  bundled toolchains: go · node+dependency-cruiser · java+code-maat  │
│    · graphviz · graphify · (python later)                           │
└─────────────────────────────────────────────────────────────────────┘
        │  bind mounts (scoped to the project roots you choose)
        ▼
  <repo>/            → code (read)        … e.g. /work/servicePlus
  <repo>/obs-data/   → artifacts (write)  … committed to that repo's git

Browser → http://localhost:7979 → tabs → [Update] → run a plugin command
        → write <repo>/obs-data/<plugin>.{svg|json|md|dbml} → you `git add`
```

### Components (each one purpose, interface, dependency)

| Component | Does | Interface | Depends on |
|---|---|---|---|
| `cmd/observer` | CLI entrypoint (`serve`, `update`, `doctor`, `init`) | flags/args | internal/* |
| `internal/server` | HTTP server + REST API + serves embedded web UI | `GET /` UI, `GET /api/projects`, `POST /api/update` | registry, runner |
| `internal/registry` | Loads project config (`obs-data/observer.yaml`), lists tabs/plugins | Go structs | config files |
| `internal/runner` | Executes a plugin command with timeout, writes artifact, records freshness | `Run(plugin, repo) → artifact` | os/exec, git |
| `internal/plugins` | Built-in plugin definitions (command + renderer + required tools) | manifest entries | bundled tools |
| `internal/gitmeta` | Branch, merge-base, dirty state, commit of last update (freshness) | Go funcs | git |
| `web/` | Embedded static dashboard (no build step) — tabs, Update buttons, renderers (mermaid.js, viz.js, marked bundled) | served by server | — |
| `Dockerfile` | Polyglot image bundling observer + all tools | — | base images |
| `install.sh` + `observer` wrapper | One-line install; runs the container with correct mounts + host UID | shell | docker |

**Design rule:** the Go binary **orchestrates**; the actual analysis is done by
the **real tools bundled in the image** (best-in-class output, no reimplementation).
Only lightweight analyses (git changes, ER-from-SQL parsing) are native Go.

## 4. Plugin model

A plugin is one tab. Three kinds (v1 ships `generator` only):

```yaml
# <repo>/obs-data/observer.yaml  — per-project, committed.
# `observer init` auto-detects Go modules + migrations dirs and writes this
# whole file for you; it's shown here only to illustrate the shape.
project: servicePlus
tabs:
  - name: AI context
    kind: generator
    command: "observer gen contextpack --dir services/api --out obs-data/context.md"
    output: context.md
    renderer: markdown
  - name: Service map
    kind: generator
    command: "observer gen graphmodel --dir services/api --out obs-data/map.json"
    output: map.json
    renderer: canvas
  - name: Database
    kind: generator
    command: "observer gen er --migrations services/api/internal/adapters/postgres/tenant_migrations --dbml-out obs-data/er.dbml --json-out obs-data/er-map.json"
    output: er-map.json
    renderer: canvas
  # v2:
  # - name: Code Search ; kind: service ; start: "docker ..." ; url: http://localhost:7080
  # - name: Hotspots (CodeScene) ; kind: link ; url: https://codescene.io/...
```

- **generator**: `command` → `output` file in `obs-data/` → rendered inline by `renderer`.
- **service** (v2): `start` a sidecar → tab is an iframe to `url`.
- **link** (v2): tab is a deep link to an external tool.

`requires:` lists host/image tools; `observer doctor` reports what's missing.

### v1 tabs (the built-in, deliberately LEAN default set)

`observer init` emits this set, highest-signal first. All are **native Go
generators** (no external analyzer needed), detected per Go module / migrations
dir. Every map tab also carries a **git-diff overlay** (changed nodes
highlighted) and an **atref "View / Compare across git refs"** time-travel
control (§9).

| Tab | Generator | Output | Renderer |
|---|---|---|---|
| Overview | `observer gen overview` | index.md (freshness, counts) | markdown |
| AI context | `observer gen contextpack` | context.md (paste-ready repo brief) | markdown |
| Service map | `observer gen graphmodel` | map.json (Go pkg deps + churn↔coverage overlay) | canvas |
| Changes | `observer gen changedgraph` | changes-map.json (map, changed pkgs highlighted) | canvas |
| What changed | `observer gen whatchanged` | whatchanged.md (textual review digest) | markdown |
| API routes | `observer gen apiroutes` | api-routes.json (Fiber route table, per module) | canvas |
| Database (ER) | `observer gen er` | er.dbml + er-map.json (ER canvas) | canvas/dbml |
| TODOs | `observer gen todos` | todos.json (marker scan) | table |
| Hotspots | `observer gen hotspots` | hotspots.json (git churn) | table |

**Removed 2026-11** (now CLI-only, not default tabs): the per-main-package **Go
call graph** (`observer gen callgraphmodel`) and per-frontend-package **Frontend
deps** (`observer gen frontendmodel`) — a whole-binary function call graph is a
dense hairball the package Service map already answers at a readable altitude,
and both fanned out to one tab per app and errored without go-callvis /
dependency-cruiser installed. The generators remain for anyone who wants to run
one by hand. (Knowledge-graph / graphify is likewise not a default tab.)

## 5. Data & git flow

- One folder per repo: **`<repo>/obs-data/`** — `observer.yaml` (config) + one
  artifact per tab + a small `index.json` (freshness: branch, commit, generated-at).
- Artifacts are **compact** (SVG/JSON/MD/DBML), so diffs stay readable and the
  repo doesn't bloat. This is what enables the v2 **version diff**.
- The user commits `obs-data/` like any other file. Observer only ever **writes
  inside `obs-data/`** — never touches source.

## 6. Install & run (the "one line, just works" bar)

- **Primary (Docker):** `curl -sSL https://raw.githubusercontent.com/taikuri-infra/observer/main/install.sh | sh`
  installs a tiny `observer` wrapper. Then `observer up` runs the container
  (mounts the current repo + `~/codes` configurable, maps host UID, opens
  `localhost:7979`). No host toolchains.
- **Alt (Go devs):** `go install github.com/taikuri-infra/observer/cmd/observer@latest`
  (uses host tools; `observer doctor` guides installs).
- **doctor:** `observer doctor` checks Docker (primary) or the per-plugin tools (alt).

## 7. Multi-language

Go + Next.js in v1. Python backend later = add the toolchain to the image + a
Python plugin (e.g. `pydeps`/`import-linter` for deps, native for ER). The plugin
manifest makes new languages additive.

## 8. SDLC / CI

Mirror ServicePlus: **GitHub Actions runnable locally with `act`**. Workflows:
`ci.yml` (go vet/test/build + `golangci-lint`), `docker.yml` (build+push image to
GHCR on tag), `release.yml` (**goreleaser** → cross-platform wrapper + GHCR image +
`install.sh`). Everything green under `act` before tagging.

## 9. Roadmap

- **v1 (now):** engine + the lean native generator tabs above; snapshots
  committed. **Cross-version diff shipped early** (pulled forward from v2): every
  map tab has a git-diff overlay plus an **atref "View / Compare across git
  refs"** control that rebuilds any tab's graph as it existed at an arbitrary
  ref (via a throwaway git worktree) — "what did this look like at v0.1.0?"
  without leaving the dashboard. This is the differentiator, and it's why v1
  commits snapshots from day one.
- **v2:** `service` tabs (Sourcegraph, Azimutt via Docker iframe); `link` tabs
  (CodeScene cloud); `observer refresh` (headless regen of every tab) + a
  stale-vs-HEAD badge + a git post-commit hook, so committed artifacts never
  rot; `observer diff --base main` as a semantic artifact diff with a CI exit
  code; architecture `rules.yaml` (forbidden-edge / layer gates).
- **v3:** Python toolchain; `observer init --detect` auto-wiring; multi-repo
  overview; agent-facing delivery (MCP server / documented `--stdout` contract);
  optional publish to a static site.

## 10. Open-source launch checklist (post-build)

License **Apache-2.0**. Then: repo About/description/topics, README with GIF,
`install.sh`, tagged release via goreleaser, and submit to: GitHub topics,
awesome-devtools / awesome-code-visualization lists, alternativeto.net (as a
CodeSee alternative), a Show HN, and r/golang.
