# Development

## Prerequisites

- Node.js 22+
- Go 1.25+
- Docker (with OrbStack recommended on macOS)

## Quick Start

### Docker (full stack)

```sh
docker build -t vulos .
docker run -p 8080:8080 --shm-size=1g -v vulos-data:/root/.vulos vulos
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

> The remote browser (Xvfb + Chromium + GStreamer) only runs inside Docker. In dev mode the backend will log a warning and skip it.

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
Dockerfile         Production container image
```

## Rebuilding

```sh
# Full rebuild
docker build -t vulos .

# Backend only
cd backend && go build ./cmd/server

# Frontend only
npm run build
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `AI_PROVIDER` | `ollama` | AI backend (ollama, openai, anthropic) |
| `AI_ENDPOINT` | `http://host.docker.internal:11434` | AI API endpoint |
| `DISPLAY` | `:99` | X11 display for remote browser |
