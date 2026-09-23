# MangaVeyra build targets. See docs/ARCHITECTURE.md → "Build and test".

SHELL          := /bin/bash
GO             ?= go
BIN            ?= bin/mangaveyra
PKG            := github.com/MaramHarsha/MangaVeyra
VERSION        ?= $(or $(shell git describe --tags --abbrev=0 2>/dev/null | sed "s/^v//"),1.0.0)
COMMIT         ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS        := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.BuildDate=$(BUILD_DATE)
GOBIN          ?= $(shell $(GO) env GOPATH)/bin
TEMPL          ?= $(shell command -v templ 2>/dev/null || echo $(GOBIN)/templ)
PG_TEST_PORT   ?= 55433
PG_TEST_NAME   ?= mangaveyra-test-pg

.PHONY: all tools web generate build run test test-go test-pg test-web test-worker e2e vet lint fmt tidy docker docker-worker clean help

all: build

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

tools: ## Install templ and golangci-lint (optional)
	$(GO) install github.com/a-h/templ/cmd/templ@latest
	-$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

web: ## Build frontend assets into internal/web/static/dist (skipped when web/ is absent)
	@if [ -f web/package.json ]; then \
		if command -v npm >/dev/null; then cd web && npm ci && npm run build; \
		else echo "npm not found: skipping web build"; fi; \
	else echo "web/ not present: skipping web build"; fi

generate: ## Run templ generate (skipped when templ is missing or there are no .templ files)
	@if [ -n "$$(find internal -name '*.templ' -print -quit 2>/dev/null)" ]; then \
		if [ -x "$(TEMPL)" ] || command -v templ >/dev/null; then $(TEMPL) generate; \
		else echo "templ not installed (make tools): skipping generate"; fi; \
	else echo "no .templ files: skipping generate"; fi

build: web generate ## Build ./bin/mangaveyra (static, CGO disabled)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/mangaveyra

run: build ## Run the server (SQLite in ./data, :8080)
	./$(BIN) serve --dev

test: test-go test-web test-worker ## Run every test suite that exists

test-go: ## Go tests on SQLite (set MV_TEST_POSTGRES_URL to also run Postgres)
	$(GO) test ./...

test-pg: ## Go tests on SQLite and a throwaway Postgres 18 container
	@docker rm -f $(PG_TEST_NAME) >/dev/null 2>&1 || true
	docker run -d --name $(PG_TEST_NAME) -e POSTGRES_PASSWORD=mv -p $(PG_TEST_PORT):5432 postgres:18-alpine >/dev/null
	@for i in $$(seq 1 30); do docker exec $(PG_TEST_NAME) pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
	MV_TEST_POSTGRES_URL="postgres://postgres:mv@127.0.0.1:$(PG_TEST_PORT)/postgres?sslmode=disable" $(GO) test ./... ; \
		status=$$?; docker rm -f $(PG_TEST_NAME) >/dev/null; exit $$status

test-web: ## Frontend unit tests (vitest) when web/ exists
	@if [ -f web/package.json ] && command -v npm >/dev/null; then cd web && npm test --if-present; \
	else echo "web tests skipped"; fi

test-worker: ## Python worker tests when worker/ exists
	@if [ -f worker/pyproject.toml ] && command -v python3 >/dev/null; then cd worker && python3 -m pytest -q; \
	else echo "worker tests skipped"; fi

e2e: build ## Playwright end-to-end flows against ./bin/mangaveyra
	@if [ -f e2e/package.json ]; then cd e2e && npm ci && npx playwright test; \
	else echo "e2e/ not present: skipping"; fi

vet: ## go vet
	$(GO) vet ./...

lint: vet ## golangci-lint (when installed) and gofmt check
	@test -z "$$(gofmt -l cmd internal)" || (echo "gofmt needed:"; gofmt -l cmd internal; exit 1)
	@if command -v golangci-lint >/dev/null; then golangci-lint run; else echo "golangci-lint not installed"; fi

fmt: ## gofmt all Go files
	gofmt -w cmd internal

tidy: ## go mod tidy
	$(GO) mod tidy

docker: ## Build the server image
	docker build -f deploy/Dockerfile -t mangaveyra:$(VERSION) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) .

docker-worker: ## Build the GPU worker image (needs worker/)
	docker build -f deploy/Dockerfile.worker -t mangaveyra-worker:$(VERSION) .

clean: ## Remove build output
	rm -rf bin internal/web/static/dist coverage.*
