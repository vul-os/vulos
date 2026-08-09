# Development

## Prerequisites

- Node.js 22+
- Go 1.25+
- Docker (with OrbStack recommended on macOS)

Vulos builds **standalone** — no sibling repos. `npm install` resolves
everything from this repo (the endpoint/offline-boot layer lives natively in
`frontend/src/lib/net/`, aliased in `frontend/vite.config.js`).

> **There is no `package.json` at the repo root.** Every `npm` command in this
> repo runs from `frontend/`. The `make` targets and `scripts/dev.sh` already
> `cd` there for you; typing `npm run build` at the root will not work.

## Quick Start

### Docker (full stack)

`dist/` and `bin/` are gitignored, and the Dockerfile defaults to
`BINARY_SOURCE=prebuilt` (it COPYs binaries built on the host — the CI path).
From a fresh clone, either build the frontend first and let Docker compile Go
from source:

```sh
cd frontend && npm install && npm run build && cd ..   # produces frontend/dist/
docker build --build-arg BINARY_SOURCE=built -t vulos .
docker run -p 8080:8080 --shm-size=1g -v vulos-data:/root/.vulos vulos
```

…or reproduce the CI path exactly by pre-building both halves on the host:

```sh
cd frontend && npm run build && cd ..
mkdir -p bin
cd backend && go build -o ../bin/vulos-server ./cmd/server \
           && go build -o ../bin/vulos-init ./cmd/init && cd ..
docker build -t vulos .                          # BINARY_SOURCE=prebuilt (default)
```

Open http://localhost:8080

### Dev Mode (hot reload)

```sh
# Terminal 1 — backend
cd backend
go run ./cmd/server

# Terminal 2 — frontend
cd frontend
npm install
npm run dev
```

(Or `make dev` / `./scripts/dev.sh`, which starts both.)

Open http://localhost:5173

Vite proxies `/api` and `/app` requests to the backend on `:8080`.

## Project Structure

```
backend/           Go backend (the Go module root — run `go` commands here)
  cmd/server/      Entry point
  cmd/init/        Bare-metal init process
  services/        Service packages (auth, webbrowser, pty, gateway, ...)
  internal/        Shared internal packages
  e2e/             Real-server end-to-end tests (build tag `e2e`)
  firstboot/e2e/   Seeded first-boot e2e tests (build tag `e2e`)
frontend/          React + TypeScript frontend (the npm root — run `npm` here)
  src/shell/       Window manager, dock, menu bar, Spotlight
  src/builtin/     Built-in apps (browser, terminal, files, ...)
  src/providers/   Context providers
  src/auth/        Login, registration, auth provider
  src/core/        App registry, portal, system pulse
  src/layouts/     Desktop and mobile layouts
  src/lib/net/     Endpoint / offline-boot layer
  apps/            Installable app manifests
  public/          Static assets
  dist/            Build output (gitignored)
scripts/           Build, signing, smoke-test and CI scripts
Dockerfile         Production container image (Debian)
```

The frontend is **TypeScript** (`.ts`/`.tsx`); `npm run typecheck` (`tsc --noEmit`)
is a separate step from `npm run build`, because Vite strips types without
checking them.

## Rebuilding

```sh
# Full rebuild (see the Docker note above — frontend/dist/ must exist first)
(cd frontend && npm run build) && docker build --build-arg BINARY_SOURCE=built -t vulos .

# Backend only
cd backend && go build ./cmd/server

# Frontend only
cd frontend && npm run build

# Both, via the Makefile
make build
```

## Testing

The Go module root is `backend/`, and the npm root is `frontend/`.

| Command | What it runs |
|---------|--------------|
| `make test-local` | Backend unit tests, no race detector (fast; no Node needed) |
| `make test-dev` | Backend unit tests with `-race`, plus the seeded first-boot e2e suite |
| `make test-all` | The full suite — `scripts/test-all.sh` (below) |
| `make coverage` | Per-package coverage summary |
| `make smoke` | Peering-route smoke test (`scripts/smoke-peering.sh`) — builds and starts the server, then fails if any registered peering route returns 501 |

`scripts/test-all.sh` runs these steps in order, and prints a PASS/FAIL summary
rather than stopping at the first failure:

1. `go test -race ./...` — the full backend unit suite.
2. `go test -tags=e2e ./firstboot/e2e/...` — seeded first-boot e2e.
3. `go test -tags=e2e ./e2e/...` — **the real server binary, driven over HTTP.**
   This is not an in-process `httptest` mux: the suite builds and starts
   `cmd/server` and talks to it as a client would.
4. `scripts/smoke-relay.sh` — **two real relay processes and real box agents**
   on loopback, as separate processes.
5. `scripts/test-storage-mode.sh` — the storage-mode selection gate.
6. `scripts/test-vulos-live-cmdline.sh` — kernel-cmdline parsing gate.
7. `scripts/netboot-install-smoke.sh --skip-if-unavailable` — **installs to a
   real disk image and boots it in QEMU.** It needs qemu + OVMF; when those are
   missing it skips *loudly and itemised*, naming every claim the run did not
   verify, so a skip can never read as a green.
8. `npm run build` (skip with `SKIP_NPM=1`).

Frontend commands, all from `frontend/`:

| Command | What it runs |
|---------|--------------|
| `npm test` | Vitest unit tests, including the frontend security contract |
| `npm run typecheck` | `tsc --noEmit` — types are **not** checked by `npm run build` |
| `npm run lint` | ESLint |
| `npm run test:e2e` | Playwright |

The live-USB QEMU smoke test (`scripts/smoke-liveusb.sh`) is **not** part of any
`make` target — run it directly.

## GPU Acceleration

GPU detection runs once at startup (`services/gpu/gpu.go`). The detection order:

1. **NVIDIA (NVENC)** — `nvidia-smi` + GStreamer `nvh264enc`/`nvav1enc`
2. **Intel/AMD (VA-API)** — `/dev/dri` + `vainfo` + GStreamer `vaapih264enc`/`vaav1enc`
3. **Software (VP8)** — always available, no GPU needed

AV1 is preferred over H.264 when the hardware supports it (RTX 4000+, Intel Arc, AMD RX 7000+).

### Testing GPU tiers locally

```sh
# Tier 0 — software (default Docker, no GPU)
docker build -t vulos . && docker run -p 8080:8080 --shm-size=1g vulos

# Tier 1 — VA-API (Intel/AMD, pass /dev/dri)
docker run --device /dev/dri -p 8080:8080 --shm-size=1g vulos

# Tier 2 — NVENC (requires nvidia-container-toolkit on host)
docker run --gpus all -p 8080:8080 --shm-size=1g vulos
```

Check detected tier: `curl localhost:8080/api/browser/status | jq .gpu_tier`

### NVIDIA Container Toolkit Setup (Host)

NVENC requires the NVIDIA Container Toolkit on the Docker host. This gives containers access to the GPU without installing drivers inside the image.

**Ubuntu/Debian:**

```sh
# Add NVIDIA container toolkit repo
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
  sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
  sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list

# Install
sudo apt-get update && sudo apt-get install -y nvidia-container-toolkit

# Configure Docker runtime
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

**Fedora/RHEL:**

```sh
curl -s -L https://nvidia.github.io/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo | \
  sudo tee /etc/yum.repos.d/nvidia-container-toolkit.repo
sudo dnf install -y nvidia-container-toolkit
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

**Verify:**

```sh
docker run --rm --gpus all nvidia/cuda:12.3.1-base-ubuntu22.04 nvidia-smi
```

Then run Vulos with GPU:

```sh
docker run --gpus all -p 8080:8080 --shm-size=1g vulos
```

The backend auto-detects NVENC and selects `nvh264enc` or `nvav1enc` (RTX 4000+). No configuration needed inside the container.

### DMA-BUF zero-copy path

When a GPU is detected, the GStreamer pipeline uses zero-copy frame upload:
- **VA-API**: `vaapipostproc` uploads X11 frames to VA surfaces
- **NVENC**: `cudaupload ! cudaconvert` uploads to CUDA memory
- **Software**: plain `videoconvert` (CPU)

This is handled by `gpu.Info.ConvertArgs()` and used by the stream pool.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `AI_PROVIDER` | `ollama` | AI backend: `ollama`, `openai`, `claude`, or `custom`. The value for Anthropic is `claude`, **not** `anthropic` |
| `AI_ENDPOINT` | `http://localhost:11434` | AI API endpoint. Inside Docker, point this at `http://host.docker.internal:11434` to reach an Ollama on the host — that is not the default |
| `DISPLAY` | _(inherited from the environment)_ | X11 display used by the browser/streaming path. There is no built-in `:99` default; the container image sets it |

This is the dev-relevant subset. The full reference is
[CONFIGURATION.md](CONFIGURATION.md), and the security-relevant variables are in
[SECURITY.md](SECURITY.md).

> **Precedence:** `internal/config.Load` reads the **process environment first**
> and falls back to the tracked `.env` file only for keys the environment does
> not set. This order was inverted at one point — a stale `.env` baked into the
> compiled binary's source path silently outranked an explicit
> `PORT=… vulos-server` — and is now pinned by
> `backend/internal/config/config_precedence_test.go`.
