.DEFAULT_GOAL := help

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS         := -X main.vVersion=$(VERSION) -X main.vCommit=$(COMMIT) -X main.vBuildDate=$(BUILD_DATE)
LDFLAGS_RELEASE := $(LDFLAGS) -s -w

BINARY := fotobank

.PHONY: build build-release install dev test test-short vet lint nilaway \
        testify-helper-check migration-history-check tidy api-generate \
        install-hooks clean help

build: ## Build debug binary with version ldflags
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/fotobank

build-release: ## Build release binary (trimpath + stripped)
	go build -ldflags="$(LDFLAGS_RELEASE)" -trimpath -o $(BINARY) ./cmd/fotobank

install: build-release ## Install to ~/.local/bin or $GOBIN
	@if [ -d "$(HOME)/.local/bin" ]; then \
		echo "Installing to ~/.local/bin/$(BINARY)"; \
		cp $(BINARY) "$(HOME)/.local/bin/$(BINARY)"; \
	else \
		INSTALL_DIR="$${GOBIN:-$$(go env GOBIN)}"; \
		[ -z "$$INSTALL_DIR" ] && INSTALL_DIR="$$(go env GOPATH)/bin"; \
		mkdir -p "$$INSTALL_DIR"; \
		echo "Installing to $$INSTALL_DIR/$(BINARY)"; \
		cp $(BINARY) "$$INSTALL_DIR/$(BINARY)"; \
	fi

dev: ## Live-reload via air
	air

test: ## Run full test suite
	go test ./... -shuffle=on

test-short: ## Run short tests only
	go test ./... -short -shuffle=on

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint + testify-helper-check
	mise exec -- golangci-lint run --fix
	$(MAKE) testify-helper-check

testify-helper-check: ## Enforce testify helper usage
	go run ./tools/testifyhelpercheck/cmd ./...

migration-history-check: ## Check no edits to main-branch migrations
	go run ./tools/migrationhistorycheck

nilaway: ## Run nilaway (pre-push tier)
	go run go.uber.org/nilaway/cmd/nilaway -include-pkgs=github.com/wesm/fotobank ./...

tidy: ## go mod tidy
	go mod tidy

api-generate: ## Regenerate OpenAPI spec
	go run ./cmd/fotobank-openapi > openapi.json

install-hooks: ## Install prek git hooks
	prek install -f

clean: ## Remove built artefacts
	rm -f $(BINARY) openapi.json

help: ## Print available targets
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*?##/ {printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
