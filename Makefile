# Makefile — Vulos OSS build + test targets.
#
# Targets:
#   make build          — build backend + frontend
#   make test-local     — unit tests (no race, fast)
#   make test-dev       — unit + e2e tests with race detector
#   make test-all       — full suite: race + e2e + npm build
#   make coverage       — generate per-package coverage report

SHELL := /bin/bash
BACKEND := backend
SCRIPTS := scripts

.PHONY: build test-local test-dev test-all coverage help

## build: compile backend and build frontend assets.
build:
	cd $(BACKEND) && go build ./...
	npm run build

## test-local: run backend unit tests without the race detector (fast iteration).
## Does not require Node.js.
test-local:
	cd $(BACKEND) && go test -timeout 5m ./...

## test-dev: run backend unit tests with the race detector and the seeded e2e suite.
## Suitable for pre-push validation on a dev machine.
test-dev:
	cd $(BACKEND) && go test -race -timeout 5m ./...
	cd $(BACKEND) && go test -tags=e2e -timeout 2m -v ./firstboot/e2e/...

## test-all: run the full test suite (race + e2e + npm build).
## Equivalent to running scripts/test-all.sh directly.
test-all:
	$(SCRIPTS)/test-all.sh

## coverage: run tests with coverage and print a per-package summary.
coverage:
	cd $(BACKEND) && go test -cover ./... 2>&1 | \
	  awk '/coverage:/ { pkg=$$2; cov=$$NF; printf "%-60s %s\n", pkg, cov }' | sort

## help: print available targets.
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
