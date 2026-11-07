# syntax=docker/dockerfile:1

# ── Builder ──────────────────────────────────────────────────────────
# Builds the observer binary itself.
FROM golang:1.26 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/observer ./cmd/observer

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

# R13: pin go-callvis to a tagged release, never @latest, so the image is
# reproducible. NOTE: github.com/ondrajz/go-callvis was renamed on GitHub;
# `git clone` still redirects, but `go install` validates the module's
# declared path in go.mod, which is github.com/ofabry/go-callvis — use that.
RUN go install github.com/ofabry/go-callvis@v0.7.1 \
    && cp /go/bin/go-callvis /usr/local/bin/go-callvis

# R9: dependency-cruiser for the Frontend deps tab.
RUN npm i -g dependency-cruiser

# R5: a repo mounted at /work is owned by the host UID (not root, not this
# image's build-time UID); `git` refuses "dubious ownership" otherwise.
RUN git config --system --add safe.directory '*'

COPY --from=builder /out/observer /usr/local/bin/observer

WORKDIR /work
EXPOSE 7979

ENTRYPOINT ["observer"]
# Inside the container we bind 0.0.0.0 (it's the only interface in the
# network namespace); the host-side publish in scripts/observer restricts
# exposure to 127.0.0.1 (R4).
CMD ["serve", "--repo", "/work", "--addr", "0.0.0.0", "--port", "7979"]
