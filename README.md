# Observer

> Local-first, Docker-packaged **code observability** for polyglot repos.
> One container, many projects. Generated diagrams are committed to each repo,
> so your architecture gets a **git history** you can diff across versions.

**Status:** early development (v1). See [docs/DESIGN.md](docs/DESIGN.md).

Observer unifies best-in-class code-analysis tools (go-callvis, dependency-cruiser,
code-maat, graphify, DBML, …) into one local dashboard:

- runs as **one Docker image** — zero host tooling beyond Docker
- analyzes any repo you mount (Go + Next.js now; Python later)
- writes compact artifacts into that repo's `obs-data/` folder
- which you commit to git — architecture history for free

Local and private by design: nothing leaves your machine.

## License

[Apache-2.0](LICENSE)
