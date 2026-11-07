# Observer — Design

> Local-first, Docker-packaged **code observability** for polyglot repos.
> One container, many projects. Generated artifacts are committed to each repo
> so your architecture has a **git history** you can diff across versions.

Status: **design approved** (2026-11-08). This document is the source of truth
for v1. See [Roadmap](#roadmap) for v2+.

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
- v1 does not include the heavy "service" tools (Sourcegraph, Azimutt) or the
  cross-version diff view — those are v2 (see Roadmap), but v1 commits the
  snapshots they'll diff.

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
# <repo>/obs-data/observer.yaml  — per-project, committed
project: servicePlus
tabs:
  - name: Changes
    kind: generator
    command: "observer-gen changes --base main"   # built-in generators
    output: changes.md
    renderer: markdown
  - name: Call graph
    kind: generator
    command: "go-callvis -focus chat -format svg -file obs-data/callgraph ./services/api/cmd/api"
    output: callgraph.svg
    renderer: svg
    requires: [go-callvis]
  - name: Database
    kind: generator
    command: "observer-gen er --migrations services/api/internal/adapters/postgres/tenant_migrations"
    output: er.dbml
    renderer: dbml
  # v2:
  # - name: Code Search ; kind: service ; start: "docker ..." ; url: http://localhost:7080
  # - name: Hotspots (CodeScene) ; kind: link ; url: https://codescene.io/...
```

- **generator**: `command` → `output` file in `obs-data/` → rendered inline by `renderer`.
- **service** (v2): `start` a sidecar → tab is an iframe to `url`.
- **link** (v2): tab is a deep link to an external tool.

`requires:` lists host/image tools; `observer doctor` reports what's missing.

### v1 tabs (all local generators)

| Tab | Tool | Output | Renderer |
|---|---|---|---|
| Overview | native | index.md (freshness, counts) | markdown |
| Changes since main | native git | changes.md | markdown |
| Database (ER) | native SQL parser → DBML | er.dbml | dbml→svg |
| Go deps | native (`go list`) / goda | deps.svg | svg |
| Go call graph | go-callvis | callgraph.svg | svg |
| Frontend deps | dependency-cruiser | web-deps.json/svg | svg |
| Evolution / hotspots | code-maat (git log) | hotspots.json | table/chart |
| Knowledge graph | graphify | graph.json | graph |

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

- **v1 (now):** engine + local generator tabs above; snapshots committed.
- **v2:** `service` tabs (Sourcegraph, Azimutt via Docker iframe); `link` tabs
  (CodeScene cloud); **version diff** — compare committed `obs-data/` snapshots
  across tags/branches ("what changed between v0.1.0 and v0.2.0"). This is the
  differentiator and is why v1 commits snapshots from day one.
- **v3:** Python toolchain; `observer init --detect` auto-wiring; multi-repo
  overview; optional publish to a static site.

## 10. Open-source launch checklist (post-build)

License **Apache-2.0**. Then: repo About/description/topics, README with GIF,
`install.sh`, tagged release via goreleaser, and submit to: GitHub topics,
awesome-devtools / awesome-code-visualization lists, alternativeto.net (as a
CodeSee alternative), a Show HN, and r/golang.
