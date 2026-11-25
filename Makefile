SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

CORE_PKGS := ./internal/domain ./internal/auth ./internal/config ./internal/service ./internal/httpapi
COVER_FILE := /tmp/gym-booking-coverage.out
TEST_DEPS := testify|uber\.org/mock|testcontainers

.PHONY: help up down down-v build generate generate-check fmt vet vuln check-prod-deps test cover test-flake test-integration check clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

up: ## Start PostgreSQL and API with Docker Compose
	docker compose up --build

down: ## Stop containers
	docker compose down

down-v: ## Stop containers and delete volumes
	docker compose down -v

build: ## Compile all packages
	go build ./...

generate: ## Regenerate generated code and mocks
	go generate ./...

generate-check: ## Fail if generated mocks drift
	go generate ./...
	git diff --exit-code

fmt: ## Fail if Go files need formatting
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		printf 'Unformatted files:\n%s\n' "$$files"; \
		exit 1; \
	fi

vet: ## Run go vet
	go vet ./...

vuln: ## Run govulncheck
	govulncheck ./...

check-prod-deps: ## Verify test-only packages are absent from production
	@deps="$$(go list -deps ./cmd/api)"; \
	if printf '%s\n' "$$deps" | grep -Eq "$(TEST_DEPS)"; then \
		echo "test-only dependency leaked into the production binary" >&2; \
		exit 1; \
	fi; \
	echo "OK: prod binary is clean"

test: ## Run unit and HTTP tests without Docker
	go test -short -race ./...

cover: ## Enforce 100.0% unit coverage on core packages
	@trap 'rm -f $(COVER_FILE)' EXIT; \
	go test -short -race -coverprofile=$(COVER_FILE) $(CORE_PKGS); \
	go tool cover -func=$(COVER_FILE); \
	total="$$(go tool cover -func=$(COVER_FILE) | awk '/^total:/ {print $$3}')"; \
	if [ "$$total" != "100.0%" ]; then \
		echo "coverage below 100.0%: $$total" >&2; \
		exit 1; \
	fi

test-flake: ## Run shuffled repeated unit tests
	go test -short -race -shuffle=on -count=5 $(CORE_PKGS)

test-integration: ## Run full suite including Testcontainers
	go test -race -shuffle=on -count=1 -v ./...

check: ## Run fast verification gate
	$(MAKE) generate-check
	$(MAKE) fmt
	$(MAKE) vet
	$(MAKE) check-prod-deps
	$(MAKE) cover
	$(MAKE) test-flake

clean: ## Remove temporary build and coverage artifacts
	rm -f $(COVER_FILE) coverage.out coverage.html api
	rm -rf bin
