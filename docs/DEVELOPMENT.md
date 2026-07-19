# Development

## Prerequisites

- Node.js 22+
- Go 1.25+
- Docker (with OrbStack recommended on macOS)
- **The two sibling repos, cloned beside `vulos/`**: `npm install` resolves
  `@vulos/relay-client` from `../vulos-relay/client` and `@vulos/office-client`
  from `../vulos-office`. Clone both, then build the relay client library once
  (its `dist-lib/` is not committed but its subpath exports point at it):

  ```sh
  git clone https://github.com/vul-os/vulos-relay.git
  git clone https://github.com/vul-os/vulos-office.git
  cd vulos-relay/client && npm install && npm run build:lib && cd ../..
  ```

## Quick Start

### Docker (full stack)

`dist/` and `bin/` are gitignored, and the Dockerfile defaults to
`BINARY_SOURCE=prebuilt` (it COPYs binaries built on the host — the CI path).
From a fresh clone, either build the frontend first and let Docker compile Go
from source:

```sh
npm run build                                    # produces dist/
docker build --build-arg BINARY_SOURCE=built -t vulos .
docker run -p 8080:8080 --shm-size=1g -v vulos-data:/root/.vulos vulos
```

…or reproduce the CI path exactly by pre-building both halves on the host:

```sh
npm run build
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
npm install
npm run dev
```

Open http://localhost:5173

Vite proxies `/api` and `/app` requests to the backend on `:8080`.

## Project Structure

```
backend/           Go backend
  cmd/server/      Entry point
  services/        Service packages (auth, webbrowser, pty, gateway, ...)
  internal/        Shared internal packages
src/               React frontend
  shell/           Window manager, dock, launchpad
  builtin/         Built-in apps (browser, terminal, files, ...)
  providers/       Context providers
  auth/            Login, registration, auth provider
  core/            App registry, portal, system pulse
  layouts/         Desktop and mobile layouts
apps/              Installable app manifests
public/            Static assets
Dockerfile         Production container image (Debian)
```

## Rebuilding

```sh
# Full rebuild (see the Docker note above — dist/ must exist first)
npm run build && docker build --build-arg BINARY_SOURCE=built -t vulos .

# Backend only
cd backend && go build ./cmd/server

# Frontend only
npm run build
```

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

Then run Vula with GPU:

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
| `AI_PROVIDER` | `ollama` | AI backend (ollama, openai, anthropic) |
| `AI_ENDPOINT` | `http://host.docker.internal:11434` | AI API endpoint |
| `DISPLAY` | `:99` | X11 display for remote browser |
