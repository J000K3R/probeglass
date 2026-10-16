# ProbeGlass — developer tasks.
#
# The Go toolchain is the only hard requirement: the frontend is plain ES
# modules embedded with //go:embed, so there is nothing to install and no build
# step between editing a .js file and reloading the page.

BINARY      := probeglass
PKG         := github.com/j000k3r/probeglass
CMD         := ./cmd/probeglass
# `--always` is deliberately absent. It falls back to the commit hash when there is no
# tag to describe, which is the right answer only once this tree *has* tags. Before the
# first release there was nothing to describe, `git describe` failed, and the version
# stayed the string the project is developed under. With `--always` the day this directory
# became a repository would have silently renamed the build from 0.1.0-dev to a hash.
#
# Now that v0.1.0 exists, `git describe` answers again and a build from a commit past the
# tag reports `0.1.0-3-ga1b2c3d`: three commits past the release, and which three. The
# `dev` below is what is left for a build that stamps nothing at all — a plain
# `go build`, or a checkout with no tags.
#
# The leading `v` of the tag is stripped by the patsubst, so the tag `v0.1.0` yields the
# version `0.1.0`. That is what every document, the changelog and the fallback below spell,
# and a binary that answered `v0.1.0` to `probeglass version` would disagree with all of
# them. `patsubst` rather than a sed in a pipeline because the fallback has to survive: a
# failed `git describe` inside a pipe exits 0, so `|| echo` would never fire.
VERSION     ?= $(patsubst v%,%,$(shell git describe --tags --dirty 2>/dev/null || echo dev))
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
               -X $(PKG)/internal/model.Version=$(VERSION) \
               -X $(PKG)/internal/model.Commit=$(COMMIT) \
               -X $(PKG)/internal/model.BuildDate=$(BUILD_DATE)

IMAGE       ?= ghcr.io/j000k3r/probeglass
TAG         ?= dev
GO          ?= go

# Where the clickable demo is published from. The website repository holds it, and
# the two are expected to sit beside each other — which is how the project is laid
# out locally and how CI checks it out.
DEMO_DIR    ?= ../probeglass-website/site/demo

# The pictures the README shows. They are taken by the same program that fills the
# documentation's picture directory, and copied from there rather than taken twice:
# one set of pictures, two places they are read.
DOCS_IMG    ?= ../probeglass-docs/static/img
README_IMG  ?= docs/images

.DEFAULT_GOAL := help
.PHONY: help build run dev test test-race test-browser cover vet fmt lint tidy seed docker docker-run clean rename check demo demo-check readme-images

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary into bin/
	@mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(CMD)
	@echo "built bin/$(BINARY) $(VERSION)"

run: build ## Build and run on :8080
	./bin/$(BINARY) serve --listen :8080 --data ./data

dev: ## Run straight from source with debug logging
	$(GO) run $(CMD) serve --listen :8080 --data ./data --log-level debug

test: ## Run the test suite
	$(GO) test ./... -count=1

test-race: ## Run the test suite under the race detector
	$(GO) test ./... -count=1 -race

# Its own module, so it is not covered by `go test ./...` above and has to be
# named separately. It needs a Chromium-based browser and skips without one.
test-browser: ## Render the dashboard in a real browser (needs Chrome or Chromium)
	cd e2e && $(GO) test ./... -count=1 -v

cover: ## Write a coverage report to coverage.out and print the total
	$(GO) test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
	@$(GO) tool cover -func=coverage.out | tail -1

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format the Go sources
	$(GO) fmt ./...

lint: ## Run golangci-lint (must be installed)
	golangci-lint run

tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

check: fmt vet test ## What CI runs before building

seed: build ## Fill ./data with demo sensors and a week of history
	./bin/$(BINARY) seed --demo --days 7 --data ./data

demo: ## Regenerate the clickable demo the website serves ($(DEMO_DIR))
	$(GO) run ./cmd/demogen -out $(DEMO_DIR)

demo-check: ## Fail if the published demo is not the one this tree would write
	./scripts/check-demo.sh $(DEMO_DIR)

readme-images: ## Copy the README's screenshots out of the documentation's set
	@mkdir -p $(README_IMG)
	cp $(addprefix $(DOCS_IMG)/,dashboard.png detail.png dashboard-tree.png \
	   dashboard-wall.png dashboard-map.png dashboard-map-provinces.png) $(README_IMG)/
	@ls -l $(README_IMG)/*.png | awk '{printf "  %-28s %6d KB\n", $$9, $$5/1024}'

docker: ## Build the container image as $(IMAGE):$(TAG)
	docker build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(TAG) .
	@echo "built $(IMAGE):$(TAG) — the image is multi-arch capable, use buildx to publish"

docker-run: docker ## Run the built image on :8080 with a throwaway volume
	docker run --rm -p 8080:8080 --cap-add NET_RAW \
	  -v probeglass-dev:/data $(IMAGE):$(TAG)

clean: ## Remove build output and local data
	rm -rf bin coverage.out
	@echo "kept ./data — remove it by hand if you want the demo history gone"

rename: ## Rewrite the module path: make rename MODULE=github.com/you/probeglass
	@test -n "$(MODULE)" || { echo "usage: make rename MODULE=github.com/you/probeglass"; exit 1; }
	$(GO) mod edit -module $(MODULE)
	grep -rl '$(PKG)' --include='*.go' --include='*.md' --include='*.yml' . \
	  | xargs sed -i.bak 's|$(PKG)|$(MODULE)|g'
	find . -name '*.bak' -delete
	$(GO) mod tidy
	@echo "module path is now $(MODULE)"
