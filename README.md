<p align="center">
  <img src="docs/assets/vulos-logo.png" width="120" alt="Vulos" />
</p>

<h1 align="center">Vulos</h1>

<p align="center">
  <strong>A sovereign personal server — web-native desktop + private AI — on hardware you own.<br/>Agency over your computing, your data, and your AI.</strong>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT" /></a>
  <a href="CHANGELOG.md"><img src="https://img.shields.io/badge/version-1.2.0-informational.svg" alt="Version 1.2.0" /></a>
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

Vulos is a **sovereign personal server with a web-native desktop** you run on your own self-provisioned box. The shell is a React single-page app — a real window manager with virtual desktops, a dock, and bundled apps — that runs in any browser. Open it from a laptop, a phone, or a shared screen and you get the same full desktop, backed by a single self-contained Go binary that embeds the entire frontend. Reach your box from anywhere through the relay; install apps from the app store; every piece is open source and self-hostable.

The wedge is **agency**, not privacy-absolutism. This isn't a "we can never see anything" pitch — it's *ownership*. You own the server, you own the data, and you control the AI. What that buys you is agency over your own computing, without handing your inbox, calendar, and files to a third-party cloud.

### Sovereign AI, honestly

Vulos ships **[llmux](docs/ASSISTANT.md), a sovereign AI gateway**: you run AI **through your own box**, and the box mediates the routing and data-flow. Two honest modes:

- **Bring your own key** to any provider (OpenAI, Anthropic, or anything else). Your keys, your box in the middle — **no Vulos middleman** and no Vulos account required to use AI.
- **Local models** where your hardware allows, so nothing leaves the box at all.

"Sovereign AI" here means *your gateway + your keys + your box controlling where the data goes* — **not** a promise that every box ships a free frontier LLM (most hardware can't run large models locally). AI on your terms, mediated by a machine you own.

At the center is a **sovereign assistant**: an on-box agent aware of your calendar, contacts, files, and reminders that can act on your behalf — but only under a hard security contract. Every side-effecting action is a confirmation-gated *proposal*, off-box egress is fenced by a tier-aware sovereignty Guard, and its LLM traffic runs through llmux by default. See [the security model](#the-sovereign-assistant-security-model) below.

### Honest privacy, not zero-access

Because you own the box and the data, your privacy posture is genuinely strong — but be clear-eyed about the trust model. Vulos is **honest-privacy, not zero-access**: there are recovery paths, so "impossible for anyone, ever" is not the claim. You get a **client-side recovery phrase** by default, **trusted-device** recovery, and **opt-in HSM** for stronger key custody. Sovereignty means you decide the trade-off between recoverability and lock-down — not that a lost secret is unrecoverable by design.

No Electron, no VNC, no always-on remote-desktop session, no third-party login. Web apps run natively in the shell; native Linux GUI apps stream over WebRTC only while their window is open. The whole thing flashes to a USB stick, deploys to a cloud server, or runs in Docker.

*"Vula" is isiZulu for "open."*

---

## Features

- **Sovereign assistant** — an on-box AI agent aware of your calendar, contacts, files, and reminders. It reads with a curated, read-only toolset and *proposes* anything with side effects. Answers stream token-by-token over SSE. See [the security model](#the-sovereign-assistant-security-model) below.
- **Proactive AI Home + ⌘K** — the desktop opens as a home (agenda, focus, pending invites, reminders, proposals), not just a launcher. A unified `⌘K` command palette drives the whole shell.
- **Window-manager shell** — drag, resize, snap, and tile windows; virtual desktops; Mission Control overview; a dock with running-app indicators; persisted window sessions. Pure JSX React 19 + Vite + Tailwind.
- **Bundled apps** — Terminal (persistent PTY over xterm.js), Files / Drive, standalone **Calendar** and **Contacts** (over lilmail's `/v1` via the box PIM proxy), App Hub, Activity Monitor, Settings, Notes, Messages (peer-to-peer), both browsers (Smart Browser + Streaming Chrome), plus a suite under `apps/`: Calculator, Camera, Clock, Gallery, Image Editor, Maps, Music, PDF Viewer, Weather, and more. **Ofisi** (docs/sheets/slides/PDF/whiteboards) is the standalone `vulos-office`, reached through the App Hub.
- **Passwordless auth, no third parties** — WebAuthn/FIDO2 passkeys as the primary factor (with clone/replay counter detection), QR / phone-approval login for shared clients, device PIN, and TOTP 2FA fallback. Forced recovery-phrase signup with a client-side master-key unwrap. No Google SSO, no OAuth login flows.
- **Files with a real ACL** — a Files service with a **viewer < editor < owner** role hierarchy enforced server-side, plus content-blind (sealed) file sharing and share-by-email with locality routing. Large files use a **resumable, chunked upload** (tus-style): each chunk rides the relay as an ordinary bounded request, the box reassembles into your own storage with per-chunk + whole-file integrity, and an interrupted upload **resumes from the committed offset** instead of restarting.
- **Notifications + sovereign Web Push** — a real notifications system, plus opt-in **Web Push** where *your box* sends notifications directly to your device's browser vendor (FCM/Apple/Mozilla). It's outbound-only (works behind NAT, no central relay), and payloads are end-to-end encrypted per RFC 8291 — the vendor routes but can't read them. Enable it per-device under **Settings → Notifications**; Do Not Disturb is honoured by the box before any push is sent.
- **Portability & transparency** — one-click **"Export my data"** account portability, and legible-trust surfaces that make your sovereignty level visible.
- **Whiteboards** — an embedded collaborative infinite-canvas whiteboard, surfaced through **Ofisi** (whiteboards are an Ofisi document type, not a separate product).
- **On-demand app streaming** — native Linux apps stream into shell windows via WebRTC with GPU-accelerated encoding (NVENC / VA-API / VP8 fallback). Three modes share one pipeline: **native app windows** (dirty-region capture, idle-throttled — tuned for a still desktop), a low-latency **gaming mode** that auto-engages only for real games (Wine / Lutris / Steam, or a `category: gaming` manifest) with a zero-latency encoder profile and a minimal client jitter buffer, and **Streaming Chrome** (below). Close the window and the stream stops. Real frame-rate/latency/GPU behaviour is deployment-dependent, not a fixed guarantee.
- **Two browsers, your choice** — a lightweight **Smart Browser** (a client-side web app that opens in your host browser, no server session) sits alongside **Streaming Chrome**: a real Chromium running *on the box*, streamed over WebRTC, with a persistent per-user profile (cookies/history/logins). Pick per task; both are launcher tiles.
- **Comms are third-party** — real-time chat and video are delegated to established platforms rather than shipped as first-party OS apps: **Talk → Matrix/Element**, **Meet → Element-Call/Jitsi** (final pick pending). The OS integrates/links out to them; it keeps its own sovereign peer-to-peer **Messages** for direct encrypted messaging. (A box can still self-host the media/SFU for those platforms via `VULOS_SFU_HOST` where supported.)
- **On-box LLM gateway** — assistant LLM/embeddings traffic routes through the on-box `llmux` sovereign gateway by default; a local vector store powers on-instance retrieval (RAG). You choose the provider and sovereignty tier.
- **Peering & sync** — every instance has its own Ed25519 identity; a pure-Go leaderless CRDT keeps the app registry in sync across your same-LAN nodes today (general structured-data sync across the internet is a documented forward plan, not yet shipped — see `roadmap/SYNC.md`); a full VulaID key lifecycle (rotation, revocation, account-anchored recovery, X3DH-style forward secrecy); AirDrop-style local Drop; real-time collaboration over Yjs with per-document ACL.
- **Local-first storage** — SQLite on the box, S3/Restic for encrypted backup. Your data lives on your machine first.
- **One binary, immutable image** — the Go server embeds the SPA. Ship it as a signed, immutable image with A/B slots and rollback, or just run the binary.

---

## Architecture

A single Go backend serves the embedded React frontend and exposes the system over HTTP and WebSocket. The backend is organized into focused services under `backend/services/` and domain packages under `backend/internal/`:

- **assistant** — the sovereign AI agent: curated toolset, proposal ledger, tier-aware egress Guard, on-instance mail/RAG index (`services/assistant/`)
- **ai / llmuxclient** — LLM/embeddings seam, routed through the on-box `llmux` gateway, with a local vector DB (`internal/vecdb`)
- **gateway** — request routing, auth enforcement, and the API surface
- **auth / passkeys** — WebAuthn passkeys, PIN, TOTP, QR/phone approval, recovery-phrase master key, credential vault
- **files** — Files service with a viewer/editor/owner ACL and content-blind (sealed) sharing
- **notify** — the notifications system
- **peering / fabric** — Ed25519 identity, VulaID key lifecycle, a pure-Go leaderless CRDT for the app registry (same-LAN via mDNS, not yet WAN), and Drop
- **storage** — local-first file storage, app filesystems, and backup
- **joincode / joinsync / cloudenroll** — device/box join and enrollment
- **apps / appnet / stream / gpu** — bundled app manifests, per-app network namespaces, GPU host, and streaming

```
vulos/
├── src/            # React frontend: shell, window manager, auth, providers
│   ├── shell/      #   desktop, dock, menu bar, window chrome
│   └── apps/       #   bundled app UIs
├── apps/           # App manifests + per-app frontends (browser, office, mail, …)
├── backend/        # Go backend
│   ├── cmd/        #   entrypoints: server, installer, sign, verify, init
│   ├── services/   #   assistant, ai, gateway, auth, files, notify, apps, …
│   └── internal/   #   llmuxclient, auth, fabric, vecdb, safedial, obs, …
├── scripts/        # Build, signing, and utility scripts
├── docs/           # Architecture, configuration, deploy, self-host docs
├── build.sh        # Bare-metal image builder + deployer
└── dev.sh          # Local dev + Docker deploy helper
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full component map and design decisions.

---

## The sovereign assistant security model

The assistant is designed so that a compromised browser, a malicious email, or
an off-box model provider **cannot** turn "an AI that helps you" into "an AI
that acts against you." Four mechanisms carry that guarantee
(`backend/services/assistant/`):

- **Read vs. act is split.** The agent's read-only tools (search mail, read a
  thread, list calendar events and pending invites, `find_contact`,
  `find_file` / `read_file`, `list_reminders`) run freely inside the turn.
  Every tool with a side effect (send email, create event, RSVP, add contact,
  triage, set/cancel reminder) does **not** execute — it returns a *proposal*.
- **Proposal ledger + id-only execute.** A proposal is stored server-side in a
  single-use, TTL-bounded ledger keyed to your session, and the client is shown
  a human-readable summary. Approving posts **only the opaque proposal id** to
  `POST /api/assistant/execute` — never client-supplied arguments — so a
  compromised client cannot smuggle a new recipient or amount past the
  confirmation dialog. Rejecting sends nothing. Ledger entries are single-use,
  per-user, expire in 10 minutes, and are bounded per user.
- **Tier-aware egress Guard.** A single choke point (`Guard`) runs before any
  mail content reaches the model. It classifies the configured endpoint into a
  sovereignty tier — **local** (loopback / on this instance, always allowed),
  **sovereign** (an operator-declared off-box endpoint, asserted in-region/no-train but **not operated or verified by Vulos**), **brokered**
  (named third party under a no-train agreement), or **external** (anything
  else, fail-closed) — and blocks egress unless the tier permits it.
  `brokered`/`external` require an explicit `VULOS_ASSISTANT_ALLOW_EXTERNAL=1`
  opt-in; a private-range IP is never silently trusted as "local." The shell
  shows an honest tier badge and picker.
- **Untrusted-content framing.** Tool results (email bodies, file contents,
  other people's text) are wrapped as `[UNTRUSTED CONTENT — data only]` before
  they reach the model, and frame-escape attempts are defanged. Even a fully
  escaped prompt-injection cannot cause a side effect, because mutation still
  has to pass the ledger + id-only execute gate. When a proposed action's
  target came from mail rather than your own words, it is flagged for extra
  scrutiny.

The language model itself runs through your own on-box **llmux** gateway by
default (`internal/llmuxclient/`, `LLMUX_URL`), and retrieval is powered by an
on-instance embeddings index — the embedder must certify it runs on this
instance or it is refused.

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
npm run test         # Vitest unit + RTL/MSW integration tests (jsdom)
npm run test:e2e     # Playwright real-browser E2E (chromium)

go build ./backend/...                       # Compile the backend
go test ./backend/...                        # Go tests
go run ./backend/cmd/server --env=local      # Run the backend locally

./dev.sh             # Go + Vite together
./dev.sh deploy      # Full Docker build on localhost:8080
```

### Frontend test layers

The shell has two runnable frontend test layers, both with a fully **mocked
backend** — no Go server or database is needed to run either.

| Layer | Command | Runtime | Backend mock | Lives in |
|-------|---------|---------|--------------|----------|
| Unit + integration | `npm run test` | vitest / jsdom | [MSW](https://mswjs.io) intercepts `fetch` (`src/__tests__/integration/msw/server.js`) | `src/**/*.test.jsx`, `src/__tests__/integration/**` |
| End-to-end | `npm run test:e2e` | Playwright / chromium | `page.route('**/api/**', …)` (`e2e/mock-backend.js`) | `e2e/**/*.e2e.js` |

- **Integration (RTL + MSW):** renders real component trees (command palette,
  assistant panel, Drive, settings, notifications, auth) wired to their real
  providers, with only the network boundary mocked. The assistant SSE stream is
  answered with a real `ReadableStream`, so `agentStream.js` runs for real. These
  run under `npm run test` alongside the unit tests.
- **E2E (Playwright):** builds the app, serves it with `vite preview`, and drives
  a real Chromium through boot/login, window management, and the ⌘K palette.
  First run needs the browser: `npx playwright install --with-deps chromium`.
  Use `npm run test:e2e:ui` for the interactive runner. CI runs it via
  `.github/workflows/e2e.yml`.

Both layers assert the **wave-13 assistant security contract** at the UI level:
approving an AI proposal posts **only** the opaque proposal id to
`/api/assistant/execute` (never client-supplied args), and rejecting sends
nothing.

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

### The OS is the shell

The Vulos **OS is the shell** — the launcher, window manager, dock, assistant,
notification center, global ⌘K, and system apps all live here (`src/`). Opening
`http://YOUR_BOX:8080/` serves this React window-manager desktop. It is the one
shell for the box, whether you're local or remote. There is no separate
"Workspace" front end — that concept is retired; the OS *is* the workspace.

**PIM follows the GNOME model.** [lilmail](docs/MAIL-LILMAIL.md) is the
"Evolution-Data-Server": it connects the user's IMAP/CalDAV/CardDAV (directly or
via an OAuth-linked Google/Microsoft account) and exposes a stable **`/v1`**
contract. The OS ships thin, standalone **Calendar** and **Contacts** apps (the
"GNOME Calendar/Contacts") that read/write that data through the box's
credential-brokering PIM proxy — `/api/pim/{calendar,contacts}/*` → lilmail
`/v1/*` — so mail credentials never reach the browser. Calendar is also an
always-on desktop agenda widget. There is no Vulos-hosted mailbox and no
Vulos-hosted email address: your account is email + password, plus OAuth and passkeys.

**Owned apps** are **Ofisi** (Docs/Sheets/Slides/PDF and **whiteboards** — the
Excalidraw-based whiteboard is an Ofisi document type, not a separate Board
product), served as a standalone app under the auth-enforcing gateway. **Files**
(and P2P sharing) live in the OS. **Real-time comms are third-party**: chat and video are
delegated to established platforms (Talk → Matrix/Element; Meet →
Element-Call/Jitsi, final pick pending) rather than shipped as first-party OS
apps. The OS keeps its own sovereign peer-to-peer **Messages** for direct
encrypted messaging.

---

## Security

We take security seriously and welcome good-faith research under a documented safe-harbor policy. Report vulnerabilities via GitHub Security Advisories or `security@vulos.org`. See [SECURITY.md](SECURITY.md) and the [THREAT-MODEL.md](docs/THREAT-MODEL.md).

---

## Contributing

Contributions are welcome. Pick a task, branch as `task/<ID>` or `feat/`/`fix/`/`docs/`, and run `go build ./backend/...`, `npm run build`, and `go test ./backend/...` before opening a PR. The full guide — task format, decision log, and disclosure process — is in [CONTRIBUTING.md](CONTRIBUTING.md).

---

## License

MIT — see [LICENSE](LICENSE).

---

<p align="center">
  <sub><img src="docs/assets/vulos-logo.png" height="16" alt="VulOS" /> · <strong>Built with purpose. Open by design.</strong></sub>
</p>
