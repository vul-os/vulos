<p align="center">
  <img src="public/icon-128.png" width="80" alt="Vulos" />
</p>

# Vulos

<p align="center">
  <strong>A web-native operating system built on Debian Linux.</strong>
</p>

<p align="center">
  <a href="https://github.com/vul-os/vulos/blob/main/LICENSE"><img src="https://img.shields.io/github/license/vul-os/vulos" alt="License: MIT" /></a>
  <a href="https://github.com/vul-os/vulos/releases"><img src="https://img.shields.io/github/v/release/vul-os/vulos?include_prereleases&label=version" alt="Version" /></a>
  <a href="https://github.com/vul-os/vulos/actions/workflows/ci.yml"><img src="https://github.com/vul-os/vulos/actions/workflows/ci.yml/badge.svg" alt="Build" /></a>
</p>

![Vulos](docs/screenshots/hero.png)

---

## Overview

Vulos is a **web-native window manager and operating system** built on Debian Linux. The shell is a React SPA that runs in any browser — open it from a laptop, phone, or shared screen and get the same full desktop. Web apps run as first-class citizens with no streaming overhead; native Linux GUI apps (GIMP, LibreOffice, games via Wine/Lutris) stream via WebRTC only when you open them.

The OS ships as a **signed, immutable squashfs** you can flash to USB and boot, deploy to a cloud server, or run in Docker. A single Go binary embeds the entire frontend. No Electron, no VNC, no always-on remote desktop session.

*"Vula" is isiZulu for "open".*

---

## Screenshots

![Login screen](docs/screenshots/login.png)

**Login — email/password or passkey (WebAuthn/FIDO2). QR login for kiosk/shared clients.**

![Launchpad — app grid](docs/screenshots/launchpad.png)

**Launchpad — full-screen app grid with search across all installed apps.**

![Settings panel](docs/screenshots/settings.png)

**Settings — display, WiFi, audio, Bluetooth, energy, backup, and identity.**

![Terminal](docs/screenshots/terminal.png)

**Terminal — persistent PTY sessions (xterm.js) that survive browser reloads.**

![File Manager](docs/screenshots/files.png)

**File Manager — browse, upload, download, drag-and-drop.**

![App Hub](docs/screenshots/apphub.png)

**App Hub — install web apps and desktop apps. Each app runs in its own isolated network namespace.**

See [docs/SCREENSHOTS.md](docs/SCREENSHOTS.md) for the full gallery and how to regenerate.

---

## Features

### Window manager
- Multiple windows with drag, resize, and snap (half/quarter screen)
- Mission Control (F3) — overview of all windows and virtual desktops
- Dock with running-app indicators

### Applications
- **Terminal** — persistent PTY with bash, accessible from any browser
- **Mail** — LilMail, the bundled IMAP/SMTP webmail client
- **Office** — vulos-office (docs, sheets, slides, spaces, calendar) via `@vulos/office-client`
- **File Manager** — browse, upload, download, manage files
- **App Hub** — install web apps and desktop apps from apt/Flatpak
- **Activity Monitor** — processes, CPU, memory, network connections
- **Settings** — display, WiFi, Bluetooth, audio, energy, backups, identity

### Authentication
- **Passkeys (primary)** — WebAuthn/FIDO2; private key never leaves the authenticator
- **QR / phone-approval login** — for kiosk/shared clients; no reusable secrets
- **Password + 2FA** — TOTP fallback via any authenticator app
- No Google OAuth or third-party identity providers

### Streaming
- Web apps run natively (no streaming overhead) in isolated network namespaces
- Native Linux apps stream via WebRTC on demand — close the window, stream stops
- GPU-accelerated encoding: NVENC (NVIDIA), VA-API (Intel/AMD), VP8 software fallback
- Cloud gaming: Wine/Lutris with gamepad support via `uinput`

### Peering & sync
- Every instance has an Ed25519 identity (`vula:<id>` URI)
- Leaderless CRDT sync (cr-sqlite) across your own nodes — no leader, no split-brain
- AirDrop-style Drop: LAN mDNS, BLE on bare metal, 6-digit proximity code fallback
- Real-time collaboration via Yjs CRDT over the peering mesh

### Image-based OS distribution
- Signed, immutable squashfs pulled from `os.vulos.org`
- A/B slots with automatic rollback if the new image does not come up clean
- dm-verity enforces block-level integrity at runtime
- Forkable: supply your own trust anchor key + bucket URL for a fully independent fork

### Infrastructure
- Single Go binary embeds the full frontend SPA
- SQLite local-first storage; S3/Restic for encrypted backup
- 110+ API endpoints across 24+ backend services
- Multi-user with per-user Linux accounts and profile isolation

---

## Quick start

### Docker (fastest)

```bash
docker run -d \
  --name vulos \
  -p 8080:8080 \
  --shm-size=1g \
  -v vulos-data:/root/.vulos \
  ghcr.io/vul-os/vulos:latest
```

Open **http://localhost:8080** and complete first-boot setup.

### Dev mode (hot reload)

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos
npm install

# Terminal 1 — backend (no cloud account needed)
go run ./backend/cmd/server --env=local

# Terminal 2 — frontend
npm run dev
```

Open **http://localhost:5173** — Vite proxies `/api` to the backend on `:8080`.

### Deploy to a server

```bash
./build.sh --deploy YOUR_SERVER_IP --domain os.yourdomain.com --dns-namecheap USER APIKEY
```

### Bare metal (flash to USB)

```bash
gunzip -c vulos-vX.X.X-x86_64.img.gz | sudo dd of=/dev/sdX bs=4M status=progress
```

Or use [Balena Etcher](https://etcher.balena.io/).

| Platform | Image |
|----------|-------|
| x86_64 | `vulos-vX.X.X-x86_64.img.gz` |
| ARM64 | `vulos-vX.X.X-arm64.img.gz` |

---

## Documentation

- [docs/GETTING-STARTED.md](docs/GETTING-STARTED.md) — install, first boot, troubleshooting
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — system diagram, component map, design decisions
- [docs/CONFIGURATION.md](docs/CONFIGURATION.md) — all env vars, config files, installer flags
- [docs/SCREENSHOTS.md](docs/SCREENSHOTS.md) — screenshot gallery + how to regenerate
- [docs/DEPLOY.md](docs/DEPLOY.md) — self-hosting and environment variables
- [docs/SELF-HOST-BUNDLE.md](docs/SELF-HOST-BUNDLE.md) — one-line install of OS + mail + office
- [docs/REPRODUCIBLE-BUILDS.md](docs/REPRODUCIBLE-BUILDS.md) — deterministic builds + dm-verity signing
- [docs/RELEASING.md](docs/RELEASING.md) — versioning and release workflow
- [ROADMAP.md](ROADMAP.md) — design roadmap across all system areas
- [CHANGELOG.md](CHANGELOG.md) — release history
- [THREAT-MODEL.md](THREAT-MODEL.md) — STRIDE threat model

---

## Development

### Prerequisites

- Node.js 22+, Go 1.25+
- Docker 24+ (OrbStack recommended on macOS)

### Build commands

```bash
npm run dev          # Vite dev server (localhost:5173)
npm run build        # Production frontend build → dist/
npm run test         # Vitest unit tests
npm run lint         # ESLint

go build ./...                                  # Compile all Go packages
go test ./backend/...                           # Go tests
go run ./backend/cmd/server --env=local         # Run backend locally

./dev.sh                # Go + Vite together
./dev.sh deploy         # Full Docker build on localhost:8080
./dev.sh deploy quick   # Quick rebuild into running container
```

### Project structure

```
vulos/
├── src/                  # React frontend (shell, apps, auth)
├── backend/              # Go backend (24+ services, 110+ endpoints)
│   ├── internal/         # Domain packages: auth, fabric, multiinstance, …
│   └── cmd/server/       # HTTP server + all route handlers
├── scripts/              # Build, signing, and utility scripts
├── docs/                 # Project documentation
├── apps/                 # Bundled app manifests
├── registry.json         # App store registry (apt + web apps)
├── roadmap/              # Design documents (one per system area)
├── build.sh              # Bare-metal image builder + deployer
└── dev.sh                # Dev and Docker deploy script
```

### Regenerate screenshots

```bash
# Install Playwright Chromium
npx playwright install chromium

# Boot the app (see docs/SCREENSHOTS.md for full instructions)
go run ./backend/cmd/server --env=local &
npm run dev &

# Capture
npm run screenshots
```

Screenshots are saved to `docs/screenshots/`. See [docs/SCREENSHOTS.md](docs/SCREENSHOTS.md) for the full list of captured routes and how to target a remote instance.

---

## Contributing

1. Skim [`tasks.md`](tasks.md) → "At-a-glance" table → pick a `todo` task whose dependencies are `done`.
2. Branch as `task/<ID>` (e.g. `task/AUTH-10`) or `feat/`, `fix/`, `docs/` for off-roadmap work.
3. Run `go build ./...` + `npm run build` + `go test ./backend/...` before opening a PR.
4. Open a PR against `main` with the acceptance-criteria checkboxes ticked.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full contribution guide, task format, design decisions log, and security disclosure process.

**Frozen invariants** (PRs violating these will not be merged):
- No CGO in any OSS Go code
- No `.tsx` files — frontend is JSX only
- No Google SSO / OAuth login flows
- No Stripe billing — billing lives in `vulos-cloud` only
- No Rust — Go throughout

---

## License

MIT — see [LICENSE](LICENSE).

<p align="center">
  <br/>
  <img src="public/icon-48.png" width="24" alt="" /><br/>
  <em>Built with purpose. Open by design.</em>
</p>
