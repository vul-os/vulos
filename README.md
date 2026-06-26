<p align="center">
  <img src="docs/assets/vulos-logo.png" width="120" alt="Vulos" />
</p>

<h1 align="center">Vulos</h1>

<p align="center">
  <strong>A self-hostable, web-native operating system. Your cloud, your hardware, your rules.</strong>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT" /></a>
  <img src="https://img.shields.io/badge/frontend-React%2019%20%2B%20Vite-61dafb.svg" alt="React 19 + Vite" />
  <img src="https://img.shields.io/badge/backend-Go%201.25-00ADD8.svg" alt="Go 1.25" />
</p>

<p align="center">
  <em>This is the core OS repo. The Vulos suite spans companion repos:<br/>
  <a href="https://github.com/vul-os/vulos-office">vulos-office</a> &middot;
  <a href="https://github.com/vul-os/vulos-relay">vulos-relay</a> &middot;
  <a href="https://github.com/vul-os/vulos-cloud">vulos-cloud</a></em>
</p>

![Vulos desktop](docs/screenshots/hero.png)

---

## What is Vulos?

Vulos is a **web-native desktop operating system you run on your own hardware.** The shell is a React single-page app — a real window manager with virtual desktops, a dock, and bundled apps — that runs in any browser. Open it from a laptop, a phone, or a shared screen and you get the same full desktop, backed by a single self-contained Go binary that embeds the entire frontend.

No Electron, no VNC, no always-on remote-desktop session, no third-party login. Web apps run natively in the shell; native Linux GUI apps stream over WebRTC only while their window is open. The whole thing flashes to a USB stick, deploys to a cloud server, or runs in Docker.

*"Vula" is isiZulu for "open."*

---

## Features

- **Window-manager shell** — drag, resize, and snap windows; virtual desktops; Mission Control overview; a dock with running-app indicators. Pure JSX React 19 + Vite + Tailwind.
- **Bundled apps** — Terminal (persistent PTY over xterm.js), File Manager, App Hub, Activity Monitor, Settings, plus a full suite under `apps/`: Browser, Calendar, Notes, Mail, Office, Gallery, Music, Maps, Camera, and more.
- **Passwordless auth, no third parties** — WebAuthn/FIDO2 passkeys as the primary factor, QR / phone-approval login for shared clients, TOTP 2FA fallback. No Google SSO, no OAuth login flows.
- **On-demand app streaming** — native Linux apps stream via WebRTC with GPU-accelerated encoding (NVENC / VA-API / VP8 fallback). Close the window and the stream stops.
- **AI router** — a built-in LLM gateway (`airouter`) that brokers chat and embeddings, with a local vector store for retrieval. You choose the provider.
- **Peering & sync** — every instance has its own Ed25519 identity; leaderless CRDT sync across your nodes; AirDrop-style local Drop; real-time collaboration over Yjs.
- **Local-first storage** — SQLite on the box, S3/Restic for encrypted backup. Your data lives on your machine first.
- **One binary, immutable image** — the Go server embeds the SPA. Ship it as a signed, immutable image with A/B slots and rollback, or just run the binary.

---

## Architecture

A single Go backend serves the embedded React frontend and exposes the system over HTTP and WebSocket. The backend is organized into focused services under `backend/services/` and domain packages under `backend/internal/`:

- **gateway** — request routing, auth enforcement, and the API surface
- **airouter** — LLM/embeddings router and proxy, with a local vector DB (`vecdb`)
- **fabric** — peer discovery and the leaderless sync mesh between your instances
- **storage** — local-first file storage, app filesystems, and backup
- **auth** — WebAuthn passkeys, TOTP, QR/phone approval, credential vault
- **apps** — bundled app manifests, per-app network namespaces, GPU host, and streaming

```
vulos/
├── src/            # React frontend: shell, window manager, auth, providers
│   ├── shell/      #   desktop, dock, menu bar, window chrome
│   └── apps/       #   bundled app UIs
├── apps/           # App manifests + per-app frontends (browser, office, mail, …)
├── backend/        # Go backend
│   ├── cmd/        #   entrypoints: server, installer, sign, verify, init
│   ├── services/   #   gateway, ai, storage, gpu, network, identity, …
│   └── internal/   #   airouter, auth, fabric, storage, vecdb, obs, …
├── scripts/        # Build, signing, and utility scripts
├── docs/           # Architecture, configuration, deploy, self-host docs
├── build.sh        # Bare-metal image builder + deployer
└── dev.sh          # Local dev + Docker deploy helper
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full component map and design decisions.

---

## Quickstart

### Run with Docker (fastest)

```bash
docker run -d \
  --name vulos \
  -p 8080:8080 \
  --shm-size=1g \
  -v vulos-data:/root/.vulos \
  ghcr.io/vul-os/vulos:latest
```

Open **http://localhost:8080** and complete first-boot setup.

### Develop with hot reload

Prerequisites: **Node.js 22+** and **Go 1.25+**.

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
Or run both together with `./dev.sh`.

### Build for production

```bash
npm run build          # frontend → dist/ (embedded into the Go binary)
go build ./backend/... # backend
```

---

## Configuration

Vulos runs locally with zero configuration via `--env=local`. Common knobs:

| Setting | Purpose |
|---------|---------|
| `--env=local` | Run without a cloud account; data under `~/.vulos` |
| `VULOS_DATA_DIR` | Override the data directory (default `~/.vulos`) |
| Port `8080` | Backend HTTP/WebSocket server |
| `.env` | Local dev overrides (frontend + dev scripts) |

The full list of environment variables, config files, and installer flags lives in [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

---

## Development & testing

```bash
npm run dev          # Vite dev server (localhost:5173)
npm run build        # Production frontend build → dist/
npm run lint         # ESLint
npm run test         # Vitest unit tests

go build ./backend/...                       # Compile the backend
go test ./backend/...                        # Go tests
go run ./backend/cmd/server --env=local      # Run the backend locally

./dev.sh             # Go + Vite together
./dev.sh deploy      # Full Docker build on localhost:8080
```

**Frozen invariants** (enforced in review): no CGO in OSS Go code; frontend is JSX only (no `.tsx`); no Google SSO/OAuth login; billing lives in `vulos-cloud`, not here.

---

## Self-hosting

Vulos is built to be owned end to end. Deploy it to your own server:

```bash
./build.sh --deploy YOUR_SERVER_IP --domain os.yourdomain.com --dns-namecheap USER APIKEY
```

Or flash a signed image to bare metal:

```bash
gunzip -c vulos-vX.X.X-x86_64.img.gz | sudo dd of=/dev/sdX bs=4M status=progress
```

The image is forkable: supply your own trust-anchor key and bucket URL for a fully independent build. See [docs/DEPLOY.md](docs/DEPLOY.md) and [docs/SELF-HOST-BUNDLE.md](docs/SELF-HOST-BUNDLE.md).

### Entry points: desktop shell vs. Workspace

A self-hosted box exposes two clients for the **same** apps and data:

- **Desktop shell (primary, local)** — `http://YOUR_BOX:8080/` serves the React
  window-manager shell. This is the full local experience.
- **Vulos Workspace (browser / remote front door)** — `http://YOUR_BOX:8080/workspace`
  redirects to the gateway-served Workspace app (`/app/vulos-workspace/`). It is
  the lightweight browser/remote client of the box: a remote browser hitting the
  box lands on the Workspace front door to that box's apps.

Workspace is served through the auth-enforcing gateway, which injects the box's
identity headers and rewrites the app's `<base href>` so its assets resolve under
`/app/vulos-workspace/`. Workspace's absolute `/api/*` calls bypass that base tag
and resolve to the box's control-plane (this server), so the browser client always
talks to the box it was opened from.

---

## Security

We take security seriously and welcome good-faith research under a documented safe-harbor policy. Report vulnerabilities via GitHub Security Advisories or `security@vulos.org`. See [SECURITY.md](SECURITY.md) and the [THREAT-MODEL.md](THREAT-MODEL.md).

---

## Contributing

Contributions are welcome. Pick a task, branch as `task/<ID>` or `feat/`/`fix/`/`docs/`, and run `go build ./backend/...`, `npm run build`, and `go test ./backend/...` before opening a PR. The full guide — task format, decision log, and disclosure process — is in [CONTRIBUTING.md](CONTRIBUTING.md).

---

## License

MIT — see [LICENSE](LICENSE).

<p align="center">
  <br/>
  <img src="public/icon-48.png" width="24" alt="" /><br/>
  <em>Built with purpose. Open by design.</em>
</p>
