# Zoomies
#
# The Go binary is fully usable without Node. Node is a build-time dependency
# only, wrapped by `make ui`, and the built assets are embedded with go:embed.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# Without the leading v: the tag is v1.2.3, the version the binary reports is
# 1.2.3, and the release workflow stamps the images the same way.
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
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
# Package-wide budget, including race instrumentation and SQLite migrations.
# Sized for the fleet runner's one-CPU share with -coverpkg, where the API
# package alone has taken 38 minutes and the controller package is larger:
# a package that hits this limit is hung, not slow.
GO_TEST_TIMEOUT ?= 120m

# The packages a test target runs. CI splits the suite by package across
# runners -- the controller, API and store packages are each a large share of
# the whole -- and this is how a shard says which share it is. Local runs
# leave it alone and get everything.
TEST_PKGS ?= ./...

##@ Build

.PHONY: build
build: ui ## Build the binary with the UI embedded
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/zoomies

.PHONY: build-nogui
build-nogui: $(UI_OUT)/.placeholder ## Build without rebuilding the UI (fast inner loop)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/zoomies

# The placeholder alone, with nothing compiled: what CI needs before go vet,
# go test or a scanner can load internal/api. Building the binary to get it
# cost a cold compile of every dependency on an ephemeral runner, minutes of
# work in a step that only has to write one file.
.PHONY: ui-stub
ui-stub: $(UI_OUT)/.placeholder ## Write the placeholder UI that go:embed needs, and build nothing

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
	@for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		case $$os in windows) ext=.exe ;; *) ext= ;; esac; \
		echo "  building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
			-ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN)_$${os}_$${arch}$$ext ./cmd/zoomies; \
	done
	cd $(DIST) && sha256sum $(BIN)_* > checksums.txt
	@echo "  wrote $(DIST)/checksums.txt"

##@ Test

.PHONY: test
test: ## Run Go unit and integration tests
	$(GO) test -race -count=1 -timeout $(GO_TEST_TIMEOUT) $(TEST_PKGS)

.PHONY: test-short
test-short: ## Run only fast tests
	$(GO) test -short -count=1 ./...

.PHONY: cover
cover: ## Run tests with a coverage report
	# -coverpkg is what makes the number honest. Without it a package is only
	# credited for the lines its own _test.go files run, so code exercised
	# through the layer above it -- the controller's migration service, driven
	# entirely by internal/api's handler tests -- reports zero and sends
	# whoever reads the dashboard to write tests for the best-covered file in
	# the tree.
	$(GO) test -race -count=1 -timeout $(GO_TEST_TIMEOUT) -coverpkg=./... -coverprofile=coverage.out -covermode=atomic $(TEST_PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

# One shard of the coverage run, in the binary format `go tool covdata` merges.
# A text profile cannot be joined to another one line by line -- the same block
# appears in each with its own count -- so a shard leaves its counters in
# COVERDIR and `cover-merge` turns the set into the one coverage.out that
# `make cover` writes directly.
.PHONY: cover-shard
cover-shard: ## Run one shard of the coverage run into COVERDIR (see cover-merge)
	@test -n "$(COVERDIR)" || { echo "cover-shard needs COVERDIR=<directory>"; exit 2; }
	@mkdir -p $(COVERDIR)
	$(GO) test -race -count=1 -timeout $(GO_TEST_TIMEOUT) -cover -coverpkg=./... -covermode=atomic $(TEST_PKGS) -args -test.gocoverdir=$(abspath $(COVERDIR))

.PHONY: cover-merge
cover-merge: ## Merge every cover-shard directory under COVERDIRS into coverage.out
	@test -n "$(COVERDIRS)" || { echo "cover-merge needs COVERDIRS=<dir>,<dir>,..."; exit 2; }
	$(GO) tool covdata textfmt -i=$(COVERDIRS) -o coverage.out
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: test-ui
test-ui: ## Run the Playwright suite (builds the binary first)
	# Built as CI builds it, which is a plain `go build` with no version stamp
	# and so the "dev" default. A working tree is normally dirty while the UI
	# is being worked on, and `git describe --dirty` then stamps a version that
	# is neither a release nor a main build -- so the controller correctly
	# declines to pin new hosts to a channel that does not exist, and the hosts
	# spec, which asserts the pin CI produces, fails for no reason anyone can
	# see from the diff they are testing.
	$(MAKE) build VERSION=dev
	cd $(UI_DIR) && $(NPM) run test:e2e

.PHONY: test-e2e
test-e2e: ## Docker-based end-to-end test; needs GitHub credentials, skipped without
	$(GO) test -count=1 -tags e2e -timeout 40m ./test/e2e/...

.PHONY: test-e2e-proxmox
test-e2e-proxmox: ## Live Proxmox qualification: needs a cluster and ZOOMIES_PROXMOX_*, skipped without. Hours, not minutes -- not in CI.
	$(GO) test -count=1 -v -tags e2e -timeout 10h ./test/e2e/proxmox/...

.PHONY: test-drill
test-drill: build-nogui ## Runtime drills: the built binary as a real controller and agent, against a fake GitHub
	$(GO) test -count=1 -v -tags drill -timeout 10m ./test/drill/...

# Both binaries land under dist/, which is already ignored: a fixed directory
# rather than a mktemp -d so a failed run leaves the two binaries that produced
# it where they can be run again by hand.
UPGRADE_DIR := $(DIST)/upgrade

.PHONY: test-upgrade
test-upgrade: build-nogui ## Install the last published release, then upgrade it in place to this build
	@mkdir -p $(UPGRADE_DIR)/old
	$(GO) build -o $(UPGRADE_DIR)/new/zoomies ./cmd/zoomies
	sh install.sh --no-init --yes --prefix $(UPGRADE_DIR)/old
	sh test/upgrade/upgrade-check.sh $(UPGRADE_DIR)/old/zoomies $(UPGRADE_DIR)/new/zoomies

# The load measurement writes evidence, not an exit code: the figures are
# recorded and compared with the last run, never turned into a threshold a
# slower runner would fail a pull request on.
# Absolute, because `go test` runs in the package's own directory.
LOAD_RECORD ?= $(CURDIR)/roadmap/validation/load-$(shell git rev-parse --short HEAD).md

.PHONY: measure
measure: ## Build a fleet with a month of history and time the reads a page makes
	ZOOMIES_LOAD_RECORD=$(LOAD_RECORD) \
		$(GO) test -count=1 -v -tags load -timeout 20m ./test/load/...
	@echo "record: $(LOAD_RECORD)"

E2E_RESULTS ?= roadmap/validation/e2e

.PHONY: test-e2e-required
test-e2e-required: ## The same run, but a missing prerequisite is a failure and every scenario must pass
	rm -rf $(E2E_RESULTS)
	ZOOMIES_E2E_REQUIRED=1 ZOOMIES_E2E_RESULTS_DIR=$(E2E_RESULTS) \
	  $(GO) test -count=1 -v -tags e2e -timeout 40m ./test/e2e/... || true
	$(GO) run ./test/e2e/verify -dir $(E2E_RESULTS)

.PHONY: screenshots
screenshots: build ## Recapture docs/screenshots from the real UI (needs Pillow: pip install pillow)
	cd $(UI_DIR) && node tests/support/screenshots.mjs

# The UI's dependencies, as an order-only prerequisite of anything that checks
# the UI.
#
# Without them `prettier --check` fails with "Cannot find package
# 'prettier-plugin-svelte'", which reads like an npm hiccup and is easy to
# scroll past -- and the UI is then not checked at all. That is the one check
# that matters for a change to .svelte files, and it went unrun twice in a
# fresh worktree before anybody noticed. So a missing tree is installed rather
# than reported: the failure mode of this step should be slow, not silent.
#
# Order-only, so an install already there is never redone because something
# touched its mtime.
$(UI_DIR)/node_modules:
	cd $(UI_DIR) && $(NPM) ci --no-audit --no-fund

##@ Quality

.PHONY: lint
lint: | $(UI_DIR)/node_modules ## Vet, format check, and staticcheck when available
	$(GO) vet ./...
# The tagged suites too. An ordinary `go vet ./...` never compiles them, which
# is how test/e2e sat with a non-test file referring to a symbol only its
# _test.go half defined: every gate was green and the package did not build
# under its own tag. Vet rather than build, because vet compiles the tests as
# well and it is the test halves that carry most of this code.
	$(GO) vet -tags e2e ./...
	$(GO) vet -tags drill ./...
	$(GO) vet -tags load ./...
	@out=$$(gofmt -l $$(git ls-files '*.go')); \
	 if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; \
	 else echo "  staticcheck not installed, skipping"; fi
	cd $(UI_DIR) && $(NPM) run lint

.PHONY: fmt
fmt: | $(UI_DIR)/node_modules ## Format Go and UI sources
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
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) \
		-t ghcr.io/eyupio/zoomies:$(VERSION) -t ghcr.io/eyupio/zoomies:latest .

.PHONY: image-nixpacks
image-nixpacks: ## Build the controller image from source with Nixpacks (needs the nixpacks CLI)
	@command -v nixpacks >/dev/null 2>&1 || { \
	  echo "nixpacks is not installed: https://nixpacks.com/docs/install"; exit 1; }
	nixpacks build . --name ghcr.io/eyupio/zoomies:$(VERSION)-nixpacks

# The runner image's variants. Each one is a base image, the package family
# deploy/Dockerfile.runner installs with, and the platform it claims to be.
#
# The rows below are generated from internal/naming's catalogue, which is the
# one place an operating system is added or swapped. Do not edit them by hand:
# run `make generate` and commit what it writes.
RUNNER_IMAGE   ?= ghcr.io/eyupio/zoomies-runner
RUNNER_VARIANT ?= $(RUNNER_VARIANT_DEFAULT)

# zoomies:catalogue-begin
RUNNER_VARIANTS := ubuntu-2404 ubuntu-2604 ubuntu-2204 debian-13 debian-12 fedora-42 rocky-9
RUNNER_VARIANT_DEFAULT := ubuntu-2404

# base | family | os | version
variant.ubuntu-2404 := ubuntu:24.04@sha256:224a1869083a311ef3f13648a154ba79832fbef6364d31493642ca03082da254 apt ubuntu 24.04
variant.ubuntu-2604 := ubuntu:26.04@sha256:da6fc2be547864451aa253836dd926da33623312df4a9a243e35dc877c378a78 apt ubuntu 26.04
variant.ubuntu-2204 := ubuntu:22.04@sha256:829f6df217bcbae2b371026e81711d1a787c61b2967ad09d015063663ebafbf7 apt ubuntu 22.04
variant.debian-13   := debian:13-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a apt debian 13
variant.debian-12   := debian:12-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171 apt debian 12
variant.fedora-42   := fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814 dnf fedora 42
variant.rocky-9     := rockylinux/rockylinux:9@sha256:8101994123cf3d0a8fee517bee7f39e555c7d92bd2d9eb3303cc988a0eeed00f dnf rocky 9
# zoomies:catalogue-end

# variant-args renders one variant's row as docker build arguments.
variant-args = \
	--build-arg BASE=$(word 1,$(variant.$(1))) \
	--build-arg OS_FAMILY=$(word 2,$(variant.$(1))) \
	--build-arg OS_ID=$(word 3,$(variant.$(1))) \
	--build-arg OS_VERSION=$(word 4,$(variant.$(1)))

.PHONY: check-variant
check-variant:
	@if [ -z "$(variant.$(RUNNER_VARIANT))" ]; then \
		echo "unknown RUNNER_VARIANT '$(RUNNER_VARIANT)'; one of: $(RUNNER_VARIANTS)" >&2; \
		exit 1; \
	fi

.PHONY: image-runner
image-runner: check-variant ## Build one runner variant for the host architecture (RUNNER_VARIANT=debian-12, RUNNER_TARGET=runner-docker for the Docker one)
	docker build -f deploy/Dockerfile.runner --target $(or $(RUNNER_TARGET),runner) \
		--build-arg RUNNER_VERSION=$(RUNNER_VERSION) \
		$(call variant-args,$(RUNNER_VARIANT)) \
		-t $(RUNNER_IMAGE):$(RUNNER_VARIANT) .

.PHONY: images-runner
images-runner: ## Build every runner variant for the host architecture
	@for v in $(RUNNER_VARIANTS); do \
		echo "  building $(RUNNER_IMAGE):$$v"; \
		$(MAKE) --no-print-directory image-runner RUNNER_VARIANT=$$v; \
	done

.PHONY: check-image-runner
check-image-runner: check-variant ## Check a locally built runner variant has what Zoomies relies on (RUNNER_VARIANT=debian-12 RUNNER_TARGET=runner-docker)
	RUNNER_VERSION=$(RUNNER_VERSION) test/runner-image/check.sh $(RUNNER_IMAGE):$(RUNNER_VARIANT) \
		$(word 3,$(variant.$(RUNNER_VARIANT))) $(word 4,$(variant.$(RUNNER_VARIANT))) $(or $(RUNNER_TARGET),runner)

.PHONY: image-runner-multiarch
image-runner-multiarch: check-variant ## Build one runner variant for amd64 and arm64 (needs buildx)
	docker buildx build --platform linux/amd64,linux/arm64 \
		-f deploy/Dockerfile.runner --build-arg RUNNER_VERSION=$(RUNNER_VERSION) \
		$(call variant-args,$(RUNNER_VARIANT)) \
		-t $(RUNNER_IMAGE):$(RUNNER_VARIANT) .

.PHONY: images-multiarch
images-multiarch: ## Build the controller and every runner variant for amd64 and arm64 (needs buildx)
	docker buildx build --platform linux/amd64,linux/arm64 \
		-f deploy/Dockerfile --target controller \
		--build-arg VERSION=$(VERSION) -t ghcr.io/eyupio/zoomies:$(VERSION) .
	@for v in $(RUNNER_VARIANTS); do \
		echo "  building $(RUNNER_IMAGE):$$v for amd64 and arm64"; \
		$(MAKE) --no-print-directory image-runner-multiarch RUNNER_VARIANT=$$v; \
	done

##@ Housekeeping

.PHONY: openapi
openapi: ## Regenerate the TypeScript client from api/openapi.yaml
	cd $(UI_DIR) && $(NPM) run generate:api

.PHONY: generate
generate: ## Rewrite everything generated from a table in Go: the image catalogue, the problem and check catalog, the command reference
	go run internal/naming/gen_catalogue.go
	go run internal/catalog/gen_catalog.go
	mkdir -p skills/zoomies && go run ./cmd/zoomies commands --output markdown > skills/zoomies/reference.md

.PHONY: marketplace-lock
marketplace-lock: ## Re-resolve deploy/marketplace/images.lock against the registry (needs network)
	go run deploy/marketplace/gen_lock.go

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN) $(DIST) $(UI_OUT) coverage.out
	rm -rf $(UI_DIR)/node_modules $(UI_DIR)/dist

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nZoomies -- off the lead, on the job.\n\nUsage:\n  make \033[36m<target>\033[0m\n"} \
	 /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } \
	 /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
