# ============================================================
# Observer — Makefile
# ============================================================
# Usage: make <target>
# Run `make help` to see all available targets.
# ============================================================

.DEFAULT_GOAL := help
SHELL := /bin/sh

IMAGE   ?= ghcr.io/taikuri-infra/observer:dev
VERSION := $(shell cat version.txt 2>/dev/null || echo "dev")

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "%-20s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the observer binary (./bin/observer)
	go build -o bin/observer ./cmd/observer

.PHONY: test
test: ## Run all tests (race detector)
	go test ./... -race

.PHONY: lint
lint: ## Lint (golangci-lint if installed, else go vet)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found, falling back to go vet"; \
		go vet ./...; \
	fi

.PHONY: run
run: ## Run observer against the current repo (host mode, no Docker)
	go run ./cmd/observer serve --repo . --addr 127.0.0.1 --port 7979

.PHONY: docker-build
docker-build: ## Build the polyglot Docker image
	docker build -t $(IMAGE) .

.PHONY: act-ci
act-ci: ## Run the CI workflow locally via act
	act -j ci

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin
