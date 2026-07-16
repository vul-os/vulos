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

.PHONY: all build run test vet tidy fmt clean screenshots

all: build

## build: compile the control-plane binary to ./bin/cp
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(CP_BIN) ./cmd/server
	@echo "built $(CP_BIN) ($(VERSION))"

## run: build and run the control plane (self-host defaults; in-memory SQLite)
run: build
	CP_VERSION=$(VERSION) $(CP_BIN)

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

## screenshots: render the admin console to docs/assets/screenshots/ (needs Node + Playwright)
screenshots:
	cd scripts/screenshots && npm install --silent && npx playwright install chromium && npm run screenshots
