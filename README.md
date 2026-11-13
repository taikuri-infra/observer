# Observer

> Local-first **code observability** for Go + Fiber + Postgres + Next.js repos.
> It reads a repo and renders a small, high-signal dashboard — a package
> service map, an ER diagram, API routes, what changed, TODOs and hotspots —
> and writes compact artifacts into that repo's `obs-data/` folder. You commit
> `obs-data/`, so your architecture gets a **plain-git-diffable history** you
> can travel across versions.

**Status:** v0.1.0 (early, actively developed). Local and private by design —
nothing leaves your machine. See [docs/DESIGN.md](docs/DESIGN.md) for the full
design.

## What you get

Point Observer at a repo and it detects the Go modules + migrations dirs and
serves a lean default set of tabs at `http://127.0.0.1:7979`:

- **Overview** — freshness + counts
- **AI context** — a paste-ready Markdown brief of the whole repo to drop into an LLM
- **Service map** — Go package dependency map, with a churn↔coverage color overlay
- **Changes** — the service map with packages that differ vs `main` highlighted
- **What changed** — a textual review digest
- **API routes** — the Fiber route table, per Go module
- **Database (ER)** — an entity-relationship canvas built from your SQL migrations
- **TODOs** — a marker scan
- **Hotspots** — git churn

Every map tab also has a **git-diff overlay** and an **atref "View / Compare
across git refs"** control — rebuild any tab's graph as it existed at an
arbitrary commit/tag ("what did this look like at v0.1.0?") without leaving the
dashboard.

The default tabs are **all native Go** — no external analyzers to install.
(Optional `observer gen callgraphmodel` / `gen frontendmodel` exist as CLI-only
generators and need go-callvis / dependency-cruiser.)

## Install & run (for your team)

Observer is a single Go binary. The fastest way to get the whole team running
today:

### Option A — `go install` (recommended today)

Requires Go 1.26+ and git on PATH.

```sh
go install github.com/taikuri-infra/observer/cmd/observer@latest

cd /path/to/your/repo
observer init          # scaffold obs-data/observer.yaml (auto-detected tabs)
observer serve         # opens http://127.0.0.1:7979
observer doctor        # report which (optional) analyzer tools are present
```

### Option B — clone & run from source

```sh
git clone https://github.com/taikuri-infra/observer.git
cd observer
make run                       # runs it against THIS repo on :7979
# …or against another repo:
go run ./cmd/observer serve --repo /path/to/your/repo
```

`make build` drops a static binary at `./bin/observer` you can copy to any
teammate on the same OS/arch.

### Option C — Docker

A one-container Docker image (`ghcr.io/taikuri-infra/observer`) and a
`curl … install.sh | sh` wrapper are the intended zero-host-tooling path, but
the **published image/release isn't live yet** (the v0.1.0 release pipeline
needs a fix — the from-source multi-arch build under QEMU). Until it's
published, build the image locally:

```sh
make docker-build                                    # builds ...:dev locally
OBSERVER_IMAGE=ghcr.io/taikuri-infra/observer:dev ./scripts/observer up
```

The wrapper publishes the dashboard on `127.0.0.1` only and never exposes it on
all interfaces (the dashboard's `POST /api/update` runs analyzer commands).

## The git-committed artifacts

Observer only ever writes inside **`<repo>/obs-data/`** — never your source.
Commit that folder like any other file; the artifacts are compact JSON / MD /
DBML, so diffs stay readable and the repo doesn't bloat. That committed history
is what powers the cross-ref time-travel.

## License

[Apache-2.0](LICENSE)
