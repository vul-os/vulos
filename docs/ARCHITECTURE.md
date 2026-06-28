# Vulos OS — Architecture

## Overview

Vulos is a self-hosted personal operating system that runs on a single machine (bare-metal or VM/VPS) and exposes a browser-native desktop via WebSocket/WebRTC. The shell is a React SPA; the backend is a single Go binary. Native Linux apps stream into browser windows on demand — no always-on VNC, no remote desktop protocol.

---

## System diagram

```mermaid
flowchart TD
    Browser["Browser (WebApp UI)<br/>src/ — React SPA, served from /"]
    Browser -->|"WebSocket / HTTP"| Backend

    subgraph Backend["Go HTTP backend (backend/cmd/server/)"]
        Auth["Auth services"]
        AI["AI/Chat router"]
        AppNet["AppNet launcher"]
        Vault["Vault (Restic)"]
        Recall["Recall (vector)"]
        Stream["Stream pool"]
        Obs["Observability: /metrics + OTel<br/>backend/internal/obs/"]
    end

    Backend --> DB["SQLite DB<br/>~/.vulos/db/"]
    Backend --> NS["Namespace isolation (appnet)"]
```

---

## Key design decisions

**Single binary.** The Go backend embeds the entire frontend SPA at build time via `go:embed`. One binary to deploy, one process to supervise.

**Local-first storage.** SQLite for auth/config; S3 (optional, via Restic) for encrypted backup. No external database required for a basic install.

**App sandboxing.** Each user app runs in its own Linux network namespace with a unique port. Traffic is proxied through the app gateway at `{app}--{profile}.{ulid}.vulos.org`. Web apps get no streaming overhead — just proxied HTTP.

**Authentication.** Email + password + optional WebAuthn/TOTP. No third-party identity providers. Passkeys are the primary login for new accounts. QR/phone-approval for kiosk clients.

**Streaming on demand.** Native Linux apps (GIMP, LibreOffice, games) launch in their own Xvfb virtual display and stream via WebRTC. Close the window, stream stops. No persistent VNC session.

**CRDT sync.** Multi-instance data sync uses cr-sqlite (leaderless CRDTs). Every instance holds a full mergeable copy; concurrent writes converge without a leader. Sync state travels over the peering mesh (live) and S3 (cold checkpoint).

---

## Component map

### Frontend (`src/`)

| Directory | Purpose |
|-----------|---------|
| `src/shell/` | Window manager, dock, Mission Control, launchpad |
| `src/auth/` | Login, passkey enrollment, QR login, setup wizard |
| `src/core/` | App registry, settings panel, system pulse |
| `src/builtin/` | Built-in apps: terminal, file manager, app hub, dashboard |
| `src/apps/` | Heavier app integrations: mail, vault, authenticator |
| `src/providers/` | React context providers |
| `src/layouts/` | Desktop and mobile layout shells |

### Backend (`backend/`)

| Path | Purpose |
|------|---------|
| `backend/cmd/server/` | HTTP server, all route handlers, middleware |
| `backend/services/auth/` | Email/password auth, session management |
| `backend/services/passkeys/` | WebAuthn/FIDO2 passkey registration and login |
| `backend/services/stream/` | WebRTC stream pool, bitrate control |
| `backend/services/gpu/` | GPU capability detection (NVENC, VA-API, software) |
| `backend/services/credvault/` | OAuth token vault, credential storage |
| `backend/services/openrouter/` | AI routing (Open Router abstraction) |
| `backend/services/sync/` | CRDT rehydration and compaction |
| `backend/services/telemetry/` | GPU usage metering |
| `backend/internal/multiinstance/` | Multi-instance quorum, signed change propagation |
| `backend/internal/gpuhost/` | GPU host capability detection |
| `backend/internal/fabric/` | Fabric mesh identity and key management |
| `backend/internal/obs/` | Prometheus metrics + OTel tracing |

---

## Browser architecture (BROWSER-01/02)

Browsing is **host-browser-native**: `POST /api/open` returns a `{"action":"open_in_host_browser","url":"..."}` instruction. The frontend shell opens the URL in the kiosk Chromium (bare-metal) or the user's desktop browser (remote). No server-side Chromium session is created.

The `services/webbrowser` package (server-side Chromium streaming) was removed in decision BROWSER-02. The `xvfb`, `chromium`, and `xdotool` streaming-only packages are no longer installed. The kiosk Chromium and its enterprise-policy files (`/etc/chromium/policies/managed/vulos.json`) remain intact — the kiosk Chromium *is* the host browser on bare metal.

Isolated/Disposable Browsing (RBI) is not implemented; the stub and its flag (`VULOS_ENABLE_ISOLATED_BROWSER`) have been removed.

---

## Auth flow

```mermaid
sequenceDiagram
    participant Client
    participant Backend
    Client->>Backend: POST /api/auth/login
    Note right of Backend: validate credentials
    Backend-->>Client: Set-Cookie: session (issue session)
    Client->>Backend: POST /api/auth/passkey/begin
    Note right of Backend: generate WebAuthn challenge
    Backend-->>Client: PublicKeyCredentialRequestOptions
    Client->>Backend: POST /api/auth/passkey/finish
    Note right of Backend: verify assertion
    Backend-->>Client: Set-Cookie: session (issue session)
```

Session cookies are `HttpOnly`, `Secure`, `SameSite=Strict` in `prod` mode. In `local` mode cookie flags are relaxed for development without TLS.

---

## Streaming pipeline

```mermaid
flowchart TD
    A["Xvfb virtual display"] --> B["GStreamer capture"]
    B --> C["GPU encode (NVENC / VA-API / VP8-software)"]
    C --> D["RTP over WebRTC (pion)"]
    D --> E["Browser MediaStream"]
```

Stream pool (`backend/services/stream/pool.go`) manages the lifecycle: one stream per open native app window, ref-counted. When the last viewer closes the browser window the stream is torn down and the virtual display released.

GPU tier auto-detection (`backend/services/gpu/gpu.go`):
1. NVIDIA (NVENC) — `nvidia-smi` + GStreamer `nvh264enc`/`nvav1enc`
2. Intel/AMD (VA-API) — `/dev/dri` + `vainfo` + GStreamer `vaapih264enc`
3. Software (VP8) — always available fallback

---

## Multi-instance CRDT sync

```mermaid
flowchart LR
    A["Instance A"] -->|"peering mesh (WebSocket/Ziti)"| B["Instance B"]
    A --> S3["S3 bucket (checkpoint + compaction)"]
    B --> S3
```

- **Hot path**: live instances stream `crsql_changes` directly over the peering mesh (relay fallback for NAT/cross-location)
- **Cold path**: periodic durable checkpoint to the shared S3 bucket; offline instances catch up from the bucket
- **Snapshot/compaction**: periodic compacted snapshot so new instances bootstrap from `snapshot + short tail`, not unbounded replay
- **Coordination**: bucket-backed leases with fencing tokens (`If-Match` CAS) prevent concurrent compaction

---

## OS distribution (bare metal)

```mermaid
flowchart TD
    A["Signed squashfs"] --> B["dm-verity Merkle tree"]
    B --> C["A/B slots"]
    C --> D["bootloader (boot counter)"]
    D --> E["auto-rollback if services don't come up"]
```

- OS ships as a signed, immutable squashfs pulled from `os.vulos.org`
- dm-verity enforces block-level integrity at runtime via the initramfs
- A/B slot auto-rollback: new image staged to inactive slot; if it doesn't come up clean the bootloader flips back
- Trust anchor: Ed25519 public key baked into the seed at flash time; forks supply their own key + bucket URL

---

## Observability

| Endpoint | Description |
|----------|-------------|
| `GET /metrics` | Prometheus textfile (`vulos_*` namespace) |
| OTel traces | Active when `OTEL_EXPORTER_OTLP_ENDPOINT` set; `backend/internal/obs.Start(ctx, op)` |

---

## See also

- [GETTING-STARTED.md](GETTING-STARTED.md) — install and first boot
- [CONFIGURATION.md](CONFIGURATION.md) — all environment variables and config files
- [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md) — deterministic build + dm-verity signing
- [THREAT-MODEL.md](../THREAT-MODEL.md) — STRIDE threat model
- [ROADMAP.md](../ROADMAP.md) — design roadmap
