<p align="center">
  <img src="public/icon-128.png" width="80" alt="Vula OS" />
</p>

<h1 align="center">Vula OS</h1>

<p align="center">
  <strong>A web-native operating system built on Debian Linux.</strong><br/>
  <em>"Vula" is isiZulu for "open".</em>
</p>

<p align="center">
  <a href="https://github.com/vul-os/vulos/actions/workflows/ci.yml"><img src="https://github.com/vul-os/vulos/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/vul-os/vulos/actions/workflows/release.yml"><img src="https://github.com/vul-os/vulos/actions/workflows/release.yml/badge.svg" alt="Release" /></a>
  <a href="https://github.com/vul-os/vulos/releases"><img src="https://img.shields.io/github/v/release/vul-os/vulos?include_prereleases&label=version" alt="Version" /></a>
  <a href="https://github.com/vul-os/vulos/blob/main/LICENSE"><img src="https://img.shields.io/github/license/vul-os/vulos" alt="License" /></a>
</p>

<p align="center">
  <a href="#install">Install</a> &middot;
  <a href="#features">Features</a> &middot;
  <a href="#development">Development</a> &middot;
  <a href="#contributing">Contributing</a> &middot;
  <a href="#license">License</a>
</p>

> **Alpha Software** — Under active development.

<p align="center">
  <img src="landing/docs/desktop.png" width="720" alt="Vula OS Desktop" />
</p>

---

## What is Vula OS?

Vula OS is a **web-native window manager and operating system** built on Debian Linux. Instead of streaming an entire remote desktop, Vula streams individual application windows on demand — web apps run as first-class citizens in the browser, and native Linux GUI apps (GIMP, LibreOffice, Blender, games via Wine/Lutris) stream via WebRTC only when you open them.

**Key ideas:**

- **Web apps are first-class** — install from apt or Flatpak, they run in isolated network namespaces and load in their own subdomain. No streaming overhead, just proxied HTTP.
- **Desktop apps stream on demand** — open Audacity and it launches in its own virtual display, streams via WebRTC. Close the window, the stream stops. No always-on VNC session.
- **Cloud gaming built in** — Wine/Lutris games stream with GPU-accelerated encoding (NVENC, VA-API, AV1). Gamepad, keyboard, and mouse input injected via uinput at kernel level.
- **Full OS underneath** — real Debian Linux with terminal, file manager, package management. Multi-user with per-user isolation.
- **Runs anywhere** — flash to bare metal (boots into a WebKit kiosk), deploy to a cloud server, or run in Docker for development.

---

## Install

### Bare Metal (flash to USB)

Download, flash, boot — like Ubuntu. On bare metal, Vula OS boots into a WebKit browser that renders the desktop shell. Game windows and native apps render alongside the browser as real compositor windows.

| Platform | File | Devices |
|----------|------|---------|
| **x86_64** | `vulos-vX.X.X-x86_64.img.gz` | PC, laptop, server |
| **ARM64** | `vulos-vX.X.X-arm64.img.gz` | Raspberry Pi, Pine64, Rock64 |

```bash
# Flash to USB drive
gunzip -c vulos-vX.X.X-x86_64.img.gz | sudo dd of=/dev/sdX bs=4M status=progress
```

Or use [Balena Etcher](https://etcher.balena.io/) — drag and drop the `.img.gz` file.

### Cloud Server

Deploy to any Debian server with one command:

```bash
./build.sh --deploy YOUR_SERVER_IP --domain os.yourdomain.com
```

Web apps available at `https://{app}.os.yourdomain.com`. Wildcard TLS via Caddy + Namecheap/Cloudflare DNS.

### Docker (development)

```bash
docker run -p 8080:8080 --shm-size=1g --privileged -v vulos-data:/root/.vulos ghcr.io/vul-os/vulos:latest
```

Open **https://lvh.me:8080** (requires [mkcert](https://github.com/FiloSottile/mkcert) for local TLS).

---

### GPU-Accelerated Streaming

Vula OS auto-detects GPU hardware and selects the best encoder for streaming desktop apps and games.

| Tier | GPU | Encoder | FPS | Latency | Setup |
|------|-----|---------|-----|---------|-------|
| 0 | None | VP8 (CPU) | 30 | ~15ms | Default |
| 1 | Intel/AMD | H.264/AV1 (VA-API) | 60 | <2ms | `--device /dev/dri` |
| 2 | NVIDIA | H.264/AV1 (NVENC) | 120 | <1ms | `--gpus all` + [NVIDIA Container Toolkit](DEVELOPMENT.md#nvidia-container-toolkit-setup-host) |

---

## Features

### Window Manager
- Multiple windows with drag, resize, snap (half/quarter screen like Ubuntu)
- Mission Control (F3) — overview of all windows and desktops
- Multiple desktops with drag-to-move between them
- Dock with running app indicators

### Applications
- **Terminal** — persistent PTY sessions with bash, accessible from anywhere
- **Browser** — Chromium instances streamed via WebRTC, multiple independent windows
- **File Manager** — browse, upload, download, manage files
- **App Store** — install web apps and desktop apps from apt/Flatpak
- **Activity Monitor** — processes, CPU, memory, network connections
- **Settings** — theme, display, WiFi, Bluetooth, audio, energy, backups

### App Platform
- **Web apps** run in isolated network namespaces with auth-gated subdomain routing
- **Desktop apps** (apt/Flatpak) stream via WebRTC with GPU encoding
- **Games** via Wine/Lutris with gamepad support and low-latency input
- **AI Assistant** with pluggable backend (Ollama, OpenAI, Anthropic) and sandboxed code execution

### Infrastructure
- Multi-user with per-user Linux accounts, sudo, and profile isolation
- Built-in tunnel for remote access from any device
- S3/Restic backup and restore
- 110+ API endpoints across 24 Go backend services

### Tech Stack

| Layer | Technology |
|-------|-----------|
| Shell | React 19, Tailwind CSS 4, Vite |
| Backend | Go (single binary, 24 services) |
| Streaming | GStreamer, WebRTC (pion), Xvfb |
| Apps | apt, Flatpak, isolated network namespaces |
| Base | Debian 13 (Trixie), Caddy |

---

## Development

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos

./dev.sh                # Local dev — Go + Vite HMR (localhost:5173)
./dev.sh deploy         # Full Docker build (localhost:8080)
./dev.sh deploy quick   # Quick rebuild into running container
./dev.sh deploy layer   # Docker rebuild, reuses cached apt layer
```

### Deploy to production

```bash
./build.sh --deploy SERVER_IP --domain os.yourdomain.com --dns-namecheap USER APIKEY
```

See [DEVELOPMENT.md](DEVELOPMENT.md) for detailed setup, GPU configuration, and environment variables.

### Project Structure

```
vulos/
├── src/                  # React frontend (shell, apps, auth)
├── backend/              # Go backend (24 services, 110+ endpoints)
├── apps/                 # Bundled app manifests
├── registry.json         # App store registry (apt + web apps)
├── landing/              # Landing page
├── build.sh              # Bare-metal image builder + deployer
└── dev.sh                # Dev and Docker deploy script
```

---

## Releases

Each release produces:

- **System images** — `.img.gz` for bare metal (flash to USB)
- **Docker images** — `ghcr.io/vul-os/vulos:latest` for `linux/amd64` and `linux/arm64`

```bash
git tag v0.1.0 && git push origin v0.1.0
```

Download from the [Releases](https://github.com/vul-os/vulos/releases) page.

---

## Contributing

1. Fork and clone
2. `./dev.sh` to run locally
3. Create a branch (`feat/`, `fix/`, `docs/`, `refactor/`)
4. Open a PR

See [DEVELOPMENT.md](DEVELOPMENT.md) for detailed setup.

---

## License

MIT

<p align="center">
  <br/>
  <img src="public/icon-48.png" width="24" alt="" /><br/>
  <em>Built with purpose. Open by design.</em>
</p>
