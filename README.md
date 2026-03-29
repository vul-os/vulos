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
- **Built-in Apps** — Terminal, file manager, web browser, activity monitor, notes, gallery
- **AI Assistant** — Pluggable AI backend (Ollama, OpenAI, etc.) with OS control via `<os-action>` blocks
- **App Sandbox** — Isolated Python app runtime with code validation, size limits, and execution timeouts
- **Browser Profiles** — Firefox-style profile isolation (Personal, Work, Private)
- **Auth & Security** — OAuth, session management, rate limiting, and enforced middleware on all API routes
- **Vault & Sync** — Encrypted storage with cross-device backup
- **Recall & Search** — ONNX-powered embeddings for full-system semantic search
- **Mobile Ready** — Alpine Linux and postmarketOS build scripts for mobile devices
- **Networking** — DNS, tunneling, TURN relay, web proxy, and WebSocket support

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Frontend | React 19, Tailwind CSS 4, Vite 8, xterm.js |
| Backend | Go (20 services, 110+ API endpoints) |
| Apps | Python plugin system with JSON manifests |
| Infrastructure | Alpine Linux, Docker, Chromium, GStreamer |

## Quick Start

### Prerequisites

- [Node.js](https://nodejs.org/) 22+
- [Go](https://go.dev/) 1.25+
- [Docker](https://www.docker.com/) (recommended — required for remote browser)
- [Ollama](https://ollama.ai/) running on `:11434` (optional, for AI features)

### Docker

Build and run a single container with everything included (backend, frontend, remote browser):

```bash
docker build -t vulos .
docker run -p 8080:8080 --shm-size=1g -v vulos-data:/root/.vulos vulos
```

Open **http://localhost:8080**.

> `--shm-size=1g` is required — Chromium needs shared memory for rendering.
> `-v vulos-data:/root/.vulos` persists user data across restarts.

### Docker Compose

Full dev environment with hot reload on the frontend and a persistent backend:

```bash
docker compose up
```

| Service | URL | Description |
|---------|-----|-------------|
| Frontend | http://localhost:5173 | Vite dev server with hot reload |
| Backend | http://localhost:8080 | Go server + remote browser |

To rebuild everything:

```bash
docker compose up --build
```

Data is persisted in Docker volumes (`vulos-data`, `node_modules`).

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
| `AI_PROVIDER` | `ollama` | AI backend (`ollama`, `openai`, `anthropic`) |
| `AI_ENDPOINT` | `http://host.docker.internal:11434` | AI backend URL |
| `DISPLAY` | `:99` | X11 display for remote browser (Docker only) |

## Project Structure

```
vulos/
├── src/                  # React frontend
│   ├── auth/             #   Authentication screens
│   ├── builtin/          #   Built-in apps (terminal, files, browser, activity)
│   ├── core/             #   App registry, settings, AI portal, telemetry
│   ├── layouts/          #   Desktop and mobile layouts
│   ├── providers/        #   Context providers
│   └── shell/            #   Dock, launchpad, window manager, toasts
├── backend/              # Go backend
│   ├── cmd/              #   Server and init entry points
│   ├── internal/         #   Config, storage, vector DB
│   └── services/         #   20 microservices (AI, auth, networking, etc.)
├── apps/                 # Plugin apps (browser, gallery, notes)
├── alpine/               # Alpine/postmarketOS build scripts
├── Dockerfile            # Production container
├── docker-compose.yml    # Dev environment
└── dev.sh                # Local dev startup script
```

## Roadmap

See [ROADMAP.md](ROADMAP.md) for the full roadmap. Key upcoming areas:

- Chromium-based browser improvements
- Bash terminal enhancements
- More default applications
- Improvements to existing apps
- Security verification and audit

## Releases

Vula OS uses [semantic versioning](https://semver.org/). Releases are automated via GitHub Actions — pushing a tag triggers a build that produces multi-architecture Docker images and a GitHub Release with changelogs.

```bash
# Create a release
git tag v0.1.0
git push origin v0.1.0
```

Pre-built Docker images are published to GitHub Container Registry:

```bash
docker pull ghcr.io/vul-os/vulos:latest
docker pull ghcr.io/vul-os/vulos:v0.1.0
```

Images are built for `linux/amd64` and `linux/arm64`.

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
