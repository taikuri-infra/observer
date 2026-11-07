# syntax=docker/dockerfile:1

# ── Builder ──────────────────────────────────────────────────────────
# Builds the observer binary itself.
FROM golang:1.26 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/observer ./cmd/observer

# ── Tool builder ─────────────────────────────────────────────────────
# Builds go-callvis from the committed tools/go.sum so its checksum (and all
# its transitive deps) is enforced from OUR pinned sum file — immune to a
# re-registered / repo-jacked upstream namespace serving a different artifact.
FROM golang:1.26 AS toolbuilder
WORKDIR /tools
COPY tools/go.mod tools/go.sum ./
RUN go mod download
RUN go build -o /out/go-callvis github.com/ofabry/go-callvis

# ── Runtime ──────────────────────────────────────────────────────────
# Slim Debian base, polyglot: bundles every analyzer tab's external tool so
# the host needs only Docker (plan M4). The Go toolchain is kept at runtime
# (not just the compiled binary) because go-callvis and `observer gen
# godeps` run `go list`/build-tooling against whatever repo gets mounted
# at /work, not against this image's own source.
FROM debian:12-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive

# git               — gitmeta, changes/hotspots generators, go-callvis, go mod
# graphviz          — renders .dot output (go deps, dependency-cruiser) to SVG
# fontconfig,
# fonts-dejavu-core — R5: without a font, graphviz SVG text renders as tofu
# ca-certificates   — TLS for `go install`/npm against module/registry proxies
# curl              — fetch helpers during image build
# nodejs, npm       — runtime for dependency-cruiser (R9)
RUN apt-get update && apt-get install -y --no-install-recommends \
        git \
        graphviz \
        fontconfig \
        fonts-dejavu-core \
        ca-certificates \
        curl \
        nodejs \
        npm \
    && rm -rf /var/lib/apt/lists/*

# Go toolchain (runtime, not just the build stage) — go-callvis and
# `observer gen godeps` need `go list`/`go build` against the mounted repo.
COPY --from=builder /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:/go/bin:${PATH}"

# R5 (Docker UID/cache/fonts): the wrapper remaps the container to the
# host UID (`-u "$(id -u):$(id -g)"`), so any Go cache/HOME-relative
# dotfile path must be writable by an arbitrary UID. Pre-create /go
# world-writable and park every such path under it or /tmp.
RUN mkdir -p /go && chmod 777 /go
ENV GOPATH=/go \
    GOCACHE=/go/cache \
    GOMODCACHE=/go/pkg/mod \
    HOME=/tmp

# go-callvis: built from the pinned tools/ module (see toolbuilder stage) and
# copied in as a binary — integrity enforced by our committed tools/go.sum,
# not by trusting the upstream namespace at install time. (ofabry is the
# canonical module path; ondrajz is the old owner handle and fails the
# module-path check.) Pinned to v0.7.1 (R13 — never @latest).
COPY --from=toolbuilder /out/go-callvis /usr/local/bin/go-callvis

# R9: dependency-cruiser for the Frontend deps tab.
RUN npm i -g dependency-cruiser

# R5: a repo mounted at /work is owned by the host UID (not root, not this
# image's build-time UID); `git` refuses "dubious ownership" otherwise.
# Scoped to the mount point only (not '*') — a wildcard would also mark any
# other untrusted repo path reachable inside the container as safe.
RUN git config --system --add safe.directory /work

COPY --from=builder /out/observer /usr/local/bin/observer

WORKDIR /work
EXPOSE 7979

ENTRYPOINT ["observer"]
# Inside the container we bind 0.0.0.0 (it's the only interface in the
# network namespace); the host-side publish in scripts/observer restricts
# exposure to 127.0.0.1 (R4).
CMD ["serve", "--repo", "/work", "--addr", "0.0.0.0", "--port", "7979"]
