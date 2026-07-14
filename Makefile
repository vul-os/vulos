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

# Signing (docs/KEY-CEREMONY.md).
#   ANCHOR / CERT — the PUBLIC trust material shipped in the image. Overridable
#   so a release build can point at real ceremony output instead of the dev keys.
#   RELEASE_PRIV  — the RELEASE private key. Never in CI, never committed.
KEYS         := keys
ANCHOR       := $(KEYS)/trust-anchor.pub
CERT         := $(KEYS)/release-cert.json
RELEASE_PRIV := $(KEYS)/release.priv.json
REGISTRY     := registry.json

.PHONY: build test-local test-dev test-all coverage help \
        dev-keys sign-registry verify-registry verify-registry-prod

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

## dev-keys: regenerate the repo's DEVELOPMENT signing keys (not secret; refused in prod).
dev-keys:
	$(SCRIPTS)/signing/dev-keys.sh

## sign-registry: sign every registry.json entry with the RELEASE key, then verify.
## Defaults to the dev release key. For a real release, run the ceremony first and
## point RELEASE_PRIV at the key on your offline signing machine:
##   make sign-registry RELEASE_PRIV=/media/signing/release.priv.json
## This is a HUMAN operation — CI never holds a private key (docs/KEY-CEREMONY.md).
sign-registry:
	@if [ ! -f "$(RELEASE_PRIV)" ]; then \
	  if [ "$(RELEASE_PRIV)" = "keys/release.priv.json" ]; then \
	    echo "▸ dev release key missing — regenerating (make dev-keys)"; \
	    $(SCRIPTS)/signing/dev-keys.sh >/dev/null; \
	  else \
	    echo "ERROR: release key not found: $(RELEASE_PRIV)"; exit 1; \
	  fi; \
	fi
	cd $(BACKEND) && go run ./cmd/sign sign-registry \
	  -release-priv ../$(RELEASE_PRIV) -registry ../$(REGISTRY)
	@$(MAKE) --no-print-directory verify-registry

## verify-registry: verify every registry.json entry against the shipped anchor.
## Public keys only — no private key required. This is what CI runs.
verify-registry:
	cd $(BACKEND) && go run ./cmd/sign verify-registry \
	  -anchor ../$(ANCHOR) -cert ../$(CERT) -registry ../$(REGISTRY)

## verify-registry-prod: as above, but REFUSES the dev keypair. Run by the release
## workflow so a tag cannot ship an image whose registry is signed by a key whose
## private half is derived from a published seed. Halts the release until the
## founder runs the ceremony — the same contract as netboot's os-core.roothash.sig.
verify-registry-prod:
	cd $(BACKEND) && go run ./cmd/sign verify-registry -require-prod-keys \
	  -anchor ../$(ANCHOR) -cert ../$(CERT) -registry ../$(REGISTRY)

## coverage: run tests with coverage and print a per-package summary.
coverage:
	cd $(BACKEND) && go test -cover ./... 2>&1 | \
	  awk '/coverage:/ { pkg=$$2; cov=$$NF; printf "%-60s %s\n", pkg, cov }' | sort

## help: print available targets.
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
