.DEFAULT_GOAL := help

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS         := -X main.vVersion=$(VERSION) -X main.vCommit=$(COMMIT) -X main.vBuildDate=$(BUILD_DATE)
LDFLAGS_RELEASE := $(LDFLAGS) -s -w

BIN_DIR := bin
BINARY  := $(BIN_DIR)/fotobank

.PHONY: build build-release install dev test test-short test-e2e vet lint nilaway \
        testify-helper-check tidy api-generate \
        install-hooks clean help docs-build docs-check docs-serve \
        ensure-embed-dir frontend frontend-dev frontend-check air-install

$(BIN_DIR):
	@mkdir -p $(BIN_DIR)

build: frontend | $(BIN_DIR) ## Build debug binary with version ldflags
	go build -tags sqlite_fts5 -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/fotobank

build-release: frontend | $(BIN_DIR) ## Build release binary (trimpath + stripped)
	go build -tags sqlite_fts5 -ldflags="$(LDFLAGS_RELEASE)" -trimpath -o $(BINARY) ./cmd/fotobank

install: build-release ## Install to ~/.local/bin or $GOBIN
	@if [ -d "$(HOME)/.local/bin" ]; then \
		echo "Installing to ~/.local/bin/fotobank"; \
		cp $(BINARY) "$(HOME)/.local/bin/fotobank"; \
	else \
		INSTALL_DIR="$${GOBIN:-$$(go env GOBIN)}"; \
		[ -z "$$INSTALL_DIR" ] && INSTALL_DIR="$$(go env GOPATH)/bin"; \
		mkdir -p "$$INSTALL_DIR"; \
		echo "Installing to $$INSTALL_DIR/fotobank"; \
		cp $(BINARY) "$$INSTALL_DIR/fotobank"; \
	fi

# Ensure go:embed has at least one file (no-op if frontend is built).
ensure-embed-dir: ## Ensure internal/web/dist has at least a stub
	@mkdir -p internal/web/dist
	@test -n "$$(ls internal/web/dist/ 2>/dev/null)" \
		|| echo ok > internal/web/dist/stub.html

# Build the frontend SPA into internal/web/dist for embedding.
# Preserves tracked placeholders (.gitignore, .gitkeep, stub.html) so a
# fresh checkout still has them after a build.
frontend: ## Build the SPA into internal/web/dist
	cd frontend && bun install --frozen-lockfile && bun run build
	mkdir -p internal/web/dist
	find internal/web/dist -mindepth 1 \
		! -name .gitignore ! -name .gitkeep ! -name stub.html \
		-exec rm -rf {} +
	cp -r frontend/dist/. internal/web/dist/

# Run vite with /api proxy. Use alongside `make dev`.
frontend-dev: ## Run vite dev server (use with `make dev`)
	./scripts/frontend-dev.sh $(ARGS)

# Lint + typecheck + unit-test the frontend.
frontend-check: ## Lint + typecheck + unit-test the frontend
	cd frontend && bun install --frozen-lockfile && bun run lint && bun run check:kit-ui && bun run typecheck && bun run test

# Install air for backend live reload.
air-install: ## go install github.com/air-verse/air@latest
	go install github.com/air-verse/air@latest

dev: ensure-embed-dir ## Live-reload backend via air
	@if ! command -v air >/dev/null 2>&1; then \
		echo "air not found. Install with: make air-install" >&2; exit 1; \
	fi
	air -c .air.toml -- $(ARGS)

test: ensure-embed-dir ## Run full test suite
	go test -tags sqlite_fts5 ./... -shuffle=on

test-short: ensure-embed-dir ## Run short tests only
	go test -tags sqlite_fts5 ./... -short -shuffle=on

test-e2e: frontend ## Run Playwright e2e suite against built backend
	mkdir -p tmp
	go build -tags sqlite_fts5 -o tmp/e2e-server ./cmd/e2e-server
	cd frontend && bun run test:e2e

scale-e2e: frontend ## Run Playwright /library scale spec (100k rows, no thumbs)
	mkdir -p tmp
	go build -tags sqlite_fts5 -o tmp/e2e-server ./cmd/e2e-server
	cd frontend && bun run test:e2e:scale

bench-scale: ## Run scale benchmarks (100k-row repo / facets / embedding / hybrid, 10k reconcile)
	go test -tags sqlite_fts5 -run '^$$' -bench . -benchmem -benchtime 3s \
		./internal/media \
		./internal/service/facets \
		./internal/ai/embedding \
		./internal/reconcile \
		./internal/search/hybrid

vet: ## Run go vet
	go vet -tags sqlite_fts5 ./...

lint: ## Run golangci-lint + testify-helper-check
	mise exec -- golangci-lint run --fix
	$(MAKE) testify-helper-check

testify-helper-check: ## Enforce testify helper usage
	go run go.kenn.io/kit/cmd/testify-helper-check ./...

nilaway: ## Run nilaway (pre-push tier)
	go run go.uber.org/nilaway/cmd/nilaway -tags sqlite_fts5 -include-pkgs=go.kenn.io/fotobank ./...

tidy: ## go mod tidy
	go mod tidy

api-generate: ## Regenerate OpenAPI spec + TypeScript schema
	go run ./cmd/fotobank-openapi -out openapi.json
	cd frontend && bun install && bunx openapi-typescript ../openapi.json -o src/lib/api/generated/schema.ts

docs-build: ## Build the marketing site, guide, and Zensical docs
	mise exec -- node scripts/docs/build.mjs

docs-check: docs-build ## Validate the generated documentation site
	mise exec -- node scripts/docs/verify-site.mjs site

docs-serve: docs-build ## Serve the generated documentation site locally
	mise exec -- node scripts/docs/serve.mjs site

install-hooks: ## Install prek git hooks
	prek install -f

clean: ## Remove built artefacts
	rm -rf $(BIN_DIR) openapi.json

help: ## Print available targets
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*?##/ {printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
