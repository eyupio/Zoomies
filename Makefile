# Zoomies
#
# The Go binary is fully usable without Node. Node is a build-time dependency
# only, wrapped by `make ui`, and the built assets are embedded with go:embed.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG      := github.com/eyupio/zoomies
LDFLAGS  := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)

BIN      := zoomies
DIST     := dist
UI_DIR   := web
UI_OUT   := internal/api/webdist
RUNNER_VERSION ?= 2.337.0

GO       ?= go
NPM      ?= npm

##@ Build

.PHONY: build
build: ui ## Build the binary with the UI embedded
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/zoomies

.PHONY: build-nogui
build-nogui: $(UI_OUT)/.placeholder ## Build without rebuilding the UI (fast inner loop)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/zoomies

$(UI_OUT)/.placeholder:
	@mkdir -p $(UI_OUT)
	@# go:embed fails on an empty directory, so keep a placeholder for builds
	@# that skip the UI. `make ui` overwrites the directory wholesale.
	@printf '<!doctype html><title>Zoomies</title><p>The UI was not built. Run <code>make ui</code>.' > $(UI_OUT)/index.html
	@touch $@

.PHONY: ui
ui: ## Build the Svelte UI into the embed directory
	cd $(UI_DIR) && $(NPM) ci --no-audit --no-fund
	cd $(UI_DIR) && $(NPM) run build

.PHONY: ui-dev
ui-dev: ## Run the Vite dev server against a controller on :8080
	cd $(UI_DIR) && $(NPM) run dev

.PHONY: dist
dist: ui ## Cross-compile release binaries into dist/
	@mkdir -p $(DIST)
	@for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "  building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
			-ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN)_$${os}_$${arch} ./cmd/zoomies; \
	done
	cd $(DIST) && sha256sum $(BIN)_* > checksums.txt
	@echo "  wrote $(DIST)/checksums.txt"

##@ Test

.PHONY: test
test: ## Run Go unit and integration tests
	$(GO) test -race -count=1 ./...

.PHONY: test-short
test-short: ## Run only fast tests
	$(GO) test -short -count=1 ./...

.PHONY: cover
cover: ## Run tests with a coverage report
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: test-ui
test-ui: ## Run the Playwright suite (builds the binary first)
	$(MAKE) build
	cd $(UI_DIR) && $(NPM) run test:e2e

.PHONY: test-e2e
test-e2e: ## Docker-based end-to-end test; needs GitHub credentials, skipped without
	$(GO) test -count=1 -tags e2e -timeout 20m ./test/e2e/...

##@ Quality

.PHONY: lint
lint: ## Vet, format check, and staticcheck when available
	$(GO) vet ./...
	@out=$$(gofmt -l . | grep -v '^web/' || true); \
	 if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; \
	 else echo "  staticcheck not installed, skipping"; fi
	cd $(UI_DIR) && $(NPM) run lint

.PHONY: fmt
fmt: ## Format Go and UI sources
	gofmt -w $$(git ls-files '*.go')
	cd $(UI_DIR) && $(NPM) run format

.PHONY: tidy
tidy: ## Tidy go.mod
	$(GO) mod tidy

##@ Run

.PHONY: run
run: build-nogui ## Run a controller against a local database
	./$(BIN) controller --config ./zoomies.yaml

.PHONY: dev
dev: build-nogui ## Run a controller with auth off, for local UI development
	ZOOMIES_DISABLE_AUTH=true ZOOMIES_LOG_FORMAT=text ZOOMIES_LOG_LEVEL=debug \
	ZOOMIES_DB_PATH=./zoomies.db ./$(BIN) controller

##@ Images

.PHONY: image
image: ## Build the controller/agent image
	docker build -f deploy/Dockerfile --target controller \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t ghcr.io/eyupio/zoomies:$(VERSION) -t ghcr.io/eyupio/zoomies:latest .

.PHONY: image-runner
image-runner: ## Build the runner image for the host architecture
	docker build -f deploy/Dockerfile.runner \
		--build-arg RUNNER_VERSION=$(RUNNER_VERSION) \
		-t ghcr.io/eyupio/zoomies-runner:$(RUNNER_VERSION) \
		-t ghcr.io/eyupio/zoomies-runner:latest .

.PHONY: images-multiarch
images-multiarch: ## Build both images for amd64 and arm64 (needs buildx)
	docker buildx build --platform linux/amd64,linux/arm64 \
		-f deploy/Dockerfile --target controller \
		--build-arg VERSION=$(VERSION) -t ghcr.io/eyupio/zoomies:$(VERSION) .
	docker buildx build --platform linux/amd64,linux/arm64 \
		-f deploy/Dockerfile.runner --build-arg RUNNER_VERSION=$(RUNNER_VERSION) \
		-t ghcr.io/eyupio/zoomies-runner:$(RUNNER_VERSION) .

##@ Housekeeping

.PHONY: openapi
openapi: build-nogui ## Regenerate the TypeScript client from api/openapi.yaml
	cd $(UI_DIR) && $(NPM) run generate:api

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN) $(DIST) $(UI_OUT) coverage.out
	rm -rf $(UI_DIR)/node_modules $(UI_DIR)/dist

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nZoomies -- off the lead, on the job.\n\nUsage:\n  make \033[36m<target>\033[0m\n"} \
	 /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } \
	 /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
