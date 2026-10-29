# Hilo CRM — developer & CI entry points. `make check` is the full quality and
# security gate; CI runs exactly the same targets.

SHELL := /bin/sh
GO    ?= go
TOOL  := $(GO) tool -modfile=tools/go.mod

TEST_PG_PORT ?= 54329
export HILO_TEST_ADMIN_URL ?= postgres://postgres:test-only-superuser@127.0.0.1:$(TEST_PG_PORT)/postgres?sslmode=disable
export HILO_TEST_APP_URL   ?= postgres://hilo_app:test-only-app@127.0.0.1:$(TEST_PG_PORT)/x?sslmode=disable
export HILO_TEST_REQUIRE_DB = 1

# Container images used by the harness, pinned.
TRIVY_IMAGE    := aquasec/trivy:0.67.2
HADOLINT_IMAGE := hadolint/hadolint:v2.14.0
GITLEAKS_IMAGE := zricethezav/gitleaks:v8.30.1
GITLEAKS       := docker run --rm -v "$(CURDIR)":/repo:ro -w /repo $(GITLEAKS_IMAGE)
ZAP_IMAGE      := ghcr.io/zaproxy/zaproxy:2.16.1

GIT_SHA ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

.PHONY: help check check-fast fmt fmt-check gen gen-check lint test test-db test-db-down cover vuln secrets secrets-staged \
        docker-lint trivy-fs trivy-images build images up down logs dast hooks fuzz

help: ## list targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

check: fmt-check gen-check lint test vuln secrets docker-lint trivy-fs ## full quality + security gate (CI)

check-fast: fmt-check secrets-staged lint ## pre-commit gate

fmt: ## format code
	gofmt -w cmd internal
	$(TOOL) golangci-lint fmt ./...

fmt-check: ## fail if code is not formatted
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then echo "not gofmt'ed:"; echo "$$out"; exit 1; fi

gen: ## regenerate sqlc code
	$(TOOL) sqlc generate

gen-check: ## fail if generated code is stale
	$(TOOL) sqlc generate
	@git diff --exit-code -- internal/store || (echo "internal/store is stale: run make gen and commit"; exit 1)

lint: ## golangci-lint (gosec, errcheck, dupl, gocognit, …)
	$(TOOL) golangci-lint run ./...

test-db: ## start the throw-away test Postgres and migrate it
	./scripts/test-db.sh

test-db-down: ## stop the test Postgres
	docker compose -f deploy/compose.test.yml down -v

test: test-db ## unit + integration tests with the race detector and coverage
	$(GO) test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...
	@$(GO) tool cover -func=coverage.out | tail -1

cover: test ## HTML coverage report
	$(GO) tool cover -html=coverage.out -o coverage.html

fuzz: ## run each fuzz target for 30s
	@for pkg in $$(grep -rl --include=*_test.go '^func Fuzz' internal | xargs -n1 dirname | sort -u); do \
	  for fn in $$(grep -h '^func Fuzz' $$pkg/*_test.go | sed 's/func \(Fuzz[A-Za-z0-9_]*\).*/\1/'); do \
	    echo "fuzz $$pkg $$fn"; $(GO) test ./$$pkg -run=^$$ -fuzz=^$$fn$$ -fuzztime=30s || exit 1; \
	  done; done

vuln: ## known vulnerabilities in dependencies and the Go toolchain
	$(TOOL) govulncheck ./...

secrets: ## scan the working tree and the whole git history for secrets
	$(GITLEAKS) dir /repo --config .gitleaks.toml --redact --no-banner
	@if git rev-parse HEAD >/dev/null 2>&1; then \
	  $(GITLEAKS) git /repo --config .gitleaks.toml --redact --no-banner; fi

secrets-staged: ## scan only what is staged (pre-commit)
	$(GITLEAKS) git /repo --staged --config .gitleaks.toml --redact --no-banner

docker-lint: ## lint the Dockerfile
	docker run --rm -i $(HADOLINT_IMAGE) < Dockerfile

trivy-fs: ## misconfiguration + vulnerability scan of the repo (Dockerfile, compose, deps)
	docker run --rm -v "$(CURDIR)":/src:ro $(TRIVY_IMAGE) fs --quiet --exit-code 1 \
	  --scanners vuln,misconfig,secret --severity HIGH,CRITICAL --skip-dirs /src/tools /src

trivy-images: images ## vulnerability scan of the built images
	@for svc in web mcp migrate; do \
	  docker run --rm -v /var/run/docker.sock:/var/run/docker.sock $(TRIVY_IMAGE) image --quiet --exit-code 1 \
	    --severity HIGH,CRITICAL hilo-$$svc:$(GIT_SHA) || exit 1; done

build: ## build binaries into ./bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/ ./cmd/...

images: ## build the container images
	@for svc in web mcp migrate; do \
	  docker build --build-arg GIT_SHA=$(GIT_SHA) --target $$svc -t hilo-$$svc:$(GIT_SHA) . || exit 1; done

up: ## run the full stack locally (docker compose)
	GIT_SHA=$(GIT_SHA) docker compose up -d --build --wait
	@docker compose logs web | head -20

down: ## stop the local stack
	docker compose down

logs: ## follow the local stack logs
	docker compose logs -f

dast: ## OWASP ZAP baseline scan against the local stack (make up first)
	mkdir -p reports
	docker run --rm --network host -v "$(CURDIR)/reports":/zap/wrk:rw -v "$(CURDIR)/deploy/zap":/zap/conf:ro $(ZAP_IMAGE) \
	  zap-baseline.py -t http://localhost:8080 -c /zap/conf/baseline.conf -r zap-report.html -I

hooks: ## install the git hooks
	git config core.hooksPath .githooks
	@echo "hooks installed (.githooks)"
