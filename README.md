<p align="center">
  <img src="public/icon-128.png" width="80" alt="Vula OS" />
</p>

<h1 align="center">Vula OS</h1>

<p align="center">
  <strong>A web-first operating system built on Alpine Linux.</strong><br/>
  <em>"Vula" is Zulu for "open".</em>
</p>

<p align="center">
  <a href="https://github.com/vul-os/vulos/actions/workflows/ci.yml"><img src="https://github.com/vul-os/vulos/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/vul-os/vulos/actions/workflows/release.yml"><img src="https://github.com/vul-os/vulos/actions/workflows/release.yml/badge.svg" alt="Release" /></a>
  <a href="https://github.com/vul-os/vulos/releases"><img src="https://img.shields.io/github/v/release/vul-os/vulos?include_prereleases&label=version" alt="Version" /></a>
  <a href="https://github.com/vul-os/vulos/blob/main/LICENSE"><img src="https://img.shields.io/github/license/vul-os/vulos" alt="License" /></a>
  <a href="https://github.com/vul-os/vulos/issues"><img src="https://img.shields.io/github/issues/vul-os/vulos" alt="Issues" /></a>
  <a href="https://github.com/vul-os/vulos/pulls"><img src="https://img.shields.io/github/issues-pr/vul-os/vulos" alt="Pull Requests" /></a>
</p>

<p align="center">
  <a href="#features">Features</a> &middot;
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#deployment">Deployment</a> &middot;
  <a href="#roadmap">Roadmap</a> &middot;
  <a href="#contributing">Contributing</a> &middot;
  <a href="#license">License</a>
</p>

> **Alpha Software** — Vula OS is under active development. APIs, features, and data formats may change between releases. Not recommended for production use yet.

---

## Features

- **Desktop Shell** — Window manager, dock, launchpad, screensaver, and toast notifications
- **Built-in Apps** — Terminal, file manager, web browser, activity monitor, app hub, packages, drivers, disk usage
- **AI Assistant** — Pluggable AI backend (Ollama, OpenAI, Anthropic, or any OpenAI-compatible endpoint) with OS control via `<os-action>` blocks and semantic context from Recall
- **App Ecosystem** — Python/HTML apps with JSON manifests, sandboxed runtime, and easy for LLMs to generate
- **Browser Profiles** — Firefox-style profile isolation (Personal, Work, Private)
- **Auth & Security** — Session-based auth with optional OAuth (Google, GitHub), rate limiting, and middleware on protected routes
- **Vault & Sync** — Encrypted storage with cross-device backup
- **Recall & Search** — ONNX-powered embeddings for full-system semantic search
- **Mobile Ready** — Alpine Linux and postmarketOS build scripts for mobile devices
- **Networking** — DNS, tunneling, TURN relay, web proxy, and WebSocket support

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Frontend | React 19, Tailwind CSS 4, Vite 8, xterm.js |
| Backend | Go (24 services, 110+ API endpoints) |
| Apps | Python/HTML apps with JSON manifests |
| Infrastructure | Alpine Linux, Docker, Chromium, GStreamer |

## Install

### Bare metal (flash to USB/SD card)

Download the latest image for your architecture from [Releases](https://github.com/vul-os/vulos/releases):

| Platform | File | Devices |
|----------|------|---------|
| **x86_64** | `vulos-vX.X.X-x86_64.img.gz` | PC, laptop, server |
| **ARM64** | `vulos-vX.X.X-arm64.img.gz` | Raspberry Pi, Pine64, Rock64 |
| **postmarketOS** | See `alpine/` build scripts | PinePhone, Librem 5, OnePlus 6 |

Flash to a USB drive or SD card:

```bash
# Linux/macOS
gunzip -c vulos-vX.X.X-x86_64.img.gz | sudo dd of=/dev/sdX bs=4M status=progress

# Or use Balena Etcher — drag and drop the .img.gz file
```

Boot from the drive and Vula OS starts automatically. Open a browser on another device and navigate to the device's IP on port 8080.

### Docker (try without installing)

```bash
docker run -p 8080:8080 --shm-size=1g -v vulos-data:/root/.vulos ghcr.io/vul-os/vulos:latest
```

Open **http://localhost:8080**.

> `--shm-size=1g` is required for Chromium. `-v vulos-data:/root/.vulos` persists data across restarts.

### Docker Compose

```bash
docker compose up
```

| Service | URL | Description |
|---------|-----|-------------|
| Frontend | http://localhost:5173 | Vite dev server with hot reload |
| Backend | http://localhost:8080 | Go server + remote browser |
| Landing | http://localhost:3000 | Landing page & docs |

### Development

All development workflows use `dev.sh`:

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos

# Local dev — Go backend + Vite HMR (no Docker)
./dev.sh

# Full Docker build + deploy
./dev.sh deploy

# Quick rebuild — recompile backend + frontend into running container
./dev.sh deploy quick
```

| Command | What it does | URL |
|---------|-------------|-----|
| `./dev.sh` | Runs Go backend + Vite dev server locally | http://localhost:5173 |
| `./dev.sh deploy` | Full Docker image build + container start | http://localhost:8080 |
| `./dev.sh deploy quick` | Rebuilds backend + frontend, copies into running container, restarts | http://localhost:8080 |

The Vite config proxies `/api` and `/app` requests to the backend on `:8080`.

> **Note:** The remote browser (Xvfb + Chromium + GStreamer + WebRTC) only runs inside Docker. In local dev mode the backend will log a warning and skip it.

#### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `APP_URL` | `http://localhost:8080` | Public URL of the OS |
| `LANDING_PORT` | _(empty)_ | Port for landing page (empty = disabled) |
| `LANDING_URL` | `http://localhost:3000` | Public URL of the landing page |
| `DISPLAY` | `:99` | X11 display for remote browser (Docker only) |

## Project Structure

```
vulos/
├── src/                  # React frontend
│   ├── auth/             #   Authentication screens
│   ├── builtin/          #   Built-in apps (terminal, files, browser, activity, app hub, etc.)
│   ├── core/             #   App registry, settings, AI portal, telemetry
│   ├── layouts/          #   Desktop and mobile layouts
│   ├── providers/        #   Context providers
│   └── shell/            #   Dock, launchpad, window manager, toasts
├── backend/              # Go backend
│   ├── cmd/              #   Server and init entry points
│   ├── internal/         #   Config, storage, vector DB
│   └── services/         #   24 services (AI, auth, networking, sandbox, etc.)
├── apps/                 # Plugin apps (browser, gallery, notes)
├── alpine/               # Alpine/postmarketOS build scripts
├── Dockerfile            # Production container
├── docker-compose.yml    # Dev environment
└── dev.sh                # Dev, build, and deploy script
```

## Roadmap

See [ROADMAP.md](ROADMAP.md) for the full roadmap. Key upcoming areas:

- Chromium-based browser improvements
- Bash terminal enhancements
- More default applications
- Improvements to existing apps
- Security verification and audit

## Releases

Each release produces:

- **Flashable system images** — `vulos-vX.X.X-x86_64.img.gz` and `vulos-vX.X.X-arm64.img.gz` for bare metal install
- **Docker images** — `ghcr.io/vul-os/vulos:latest` and versioned tags for `linux/amd64` and `linux/arm64`

Releases are automated via GitHub Actions — pushing a tag triggers the build:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Download images from the [Releases](https://github.com/vul-os/vulos/releases) page.

## Contributing

Contributions are welcome! Here's how to get started:

### Getting Started

1. **Fork** the repository
2. **Clone** your fork:
   ```bash
   git clone https://github.com/<your-username>/vulos.git
   cd vulos
   ```
3. **Create a branch** for your feature or fix:
   ```bash
   git checkout -b feat/my-feature
   ```
4. **Run the dev environment** to test your changes:
   ```bash
   ./dev.sh
   ```
5. **Commit** your changes with a clear message
6. **Push** and open a Pull Request

### Branch Naming

| Prefix | Purpose |
|--------|---------|
| `feat/` | New feature |
| `fix/` | Bug fix |
| `docs/` | Documentation |
| `refactor/` | Code refactoring |

### Guidelines

- Keep PRs focused — one feature or fix per PR
- Test your changes locally before submitting
- Follow the existing code style
- Update documentation if your change affects usage
- Be respectful in discussions and reviews

### Areas to Contribute

- **Apps** — Build new apps using the Python plugin system in `apps/`
- **Frontend** — Improve the shell, window manager, or built-in apps in `src/`
- **Backend Services** — Extend or improve the Go services in `backend/services/`
- **Mobile** — Help with Alpine/postmarketOS builds in `alpine/`
- **Docs** — Improve documentation, guides, and examples

## License

MIT

---

<p align="center">
  Built with purpose. Open by design.
</p>
