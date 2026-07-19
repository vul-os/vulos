# vulos-management — operational control plane (OSS)
#
# The self-host control plane binary. Runs free, with the no-op billing seam and
# bring-your-own-bucket storage. A commercial distributor wraps this module and
# injects real providers; see docs/ARCHITECTURE.md.

GO       ?= go
BIN_DIR  ?= ./bin
CP_BIN   ?= $(BIN_DIR)/cp
VERSION  ?= $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)

.PHONY: all build run dev test vet tidy fmt clean screenshots console

all: build

## console: build the management console SPA into web/dist (embedded by the binary)
console:
	cd web && npm ci && npm run build
	@echo "built web/dist (embedded via web/embed.go)"

## build: compile the control-plane binary to ./bin/cp
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(CP_BIN) ./cmd/server
	@echo "built $(CP_BIN) ($(VERSION))"

## run: build and run the control plane with PRODUCTION posture (fails closed
## without a real SESSION_SECRET — see `make dev` for a local quickstart).
run: build
	CP_VERSION=$(VERSION) $(CP_BIN)

## dev: build and run the control plane for local evaluation. Sets
## VULOS_ENV=local so the prod fail-closed guards (SESSION_SECRET, KEKs, push
## creds) step aside for a dev fallback instead of refusing to start — this is
## the command a first-time reader should run, NOT `make run` / bare `./bin/cp`.
dev: build
	VULOS_ENV=local CP_VERSION=$(VERSION) $(CP_BIN)

## test: run the full test suite
test:
	$(GO) test ./...

## vet: run go vet across the module
vet:
	$(GO) vet ./...

## tidy: reconcile go.mod / go.sum
tidy:
	$(GO) mod tidy

## fmt: gofmt the tree
fmt:
	$(GO) fmt ./...

## clean: remove build artifacts
clean:
	rm -rf $(BIN_DIR)

## screenshots: capture the React console SPA to docs/assets/screenshots/ (needs a built web/dist + Node + Playwright)
screenshots: console
	cd scripts/screenshots && npm install --silent && npx playwright install chromium && npm run screenshots
