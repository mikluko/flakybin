.DEFAULT_GOAL := help

.PHONY: help build image package test test-coverage lint clean install run

# Version derived from git tags (e.g. v0.1.0 -> 0.1.0). Without a tag, fall back
# to a SemVer-valid pre-release (0.0.0-<sha>) so `helm package --version` accepts
# it. Note: --always is intentionally omitted, else a bare SHA would shadow the
# fallback and break the chart version.
GIT_VERSION := $(shell git describe --tags --dirty 2>/dev/null || echo "0.0.0-$(shell git rev-parse --short HEAD)")
VERSION := $(GIT_VERSION:v%=%)
KO_DOCKER_REPO := ghcr.io/mikluko/flakybin
# helm push appends the chart name, landing it at $(CHART_REPO)/flakybin.
CHART_REPO := ghcr.io/mikluko/flakybin/charts
export KO_DOCKER_REPO

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Release targets
build: image package ## Build and push container image and Helm chart

image: ## Build and push container image with ko
	@echo "Building and pushing image: $(KO_DOCKER_REPO):$(VERSION)"
	VERSION=$(VERSION) ko build --bare --tags $(VERSION)

package: ## Package and push Helm chart to the OCI registry
	@echo "Packaging and pushing chart: $(CHART_REPO)/flakybin:$(VERSION)"
	@helm package charts/flakybin --version $(VERSION) --app-version $(VERSION) --destination .build/
	@helm push .build/flakybin-$(VERSION).tgz oci://$(CHART_REPO)
	@rm .build/flakybin-$(VERSION).tgz
	@echo "Chart pushed successfully"

# Test targets
test: ## Run tests
	go test ./...

test-coverage: ## Run tests with coverage report
	go test ./... -cover -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

lint: ## Run linters
	golangci-lint run ./...

# Local development
clean: ## Clean build artifacts
	rm -f flakybin coverage.out coverage.html
	rm -rf .build/ dist/

install: ## Install the binary with the version stamped in
	go install -ldflags="-X main.version=$(VERSION)"

run: ## Run locally on :8080
	go run -ldflags="-X main.version=$(VERSION)" .
