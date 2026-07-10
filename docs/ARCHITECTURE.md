# Vulos OS — Architecture

## Overview

Vulos is a **sovereign personal server** with a browser-native desktop that runs on a single machine (bare-metal or VM/VPS) and exposes itself over WebSocket/WebRTC. The shell is a React SPA; the backend is a single Go binary. At its center is an on-box **sovereign assistant** — an AI agent aware of your calendar, contacts, files, and reminders that acts on your behalf under a confirmation-gated, egress-fenced security contract. Native Linux apps stream into browser windows on demand — no always-on VNC, no remote desktop protocol.

---

## System diagram

```mermaid
flowchart TD
    Browser["Browser (React SPA)<br/>src/ — shell, AI Home, ⌘K, served from /"]
    Browser -->|"WebSocket / HTTP / SSE"| Backend

    subgraph Backend["Go HTTP backend (backend/cmd/server/)"]
        Assistant["Sovereign assistant<br/>agent · ledger · egress Guard"]
        Auth["Auth: passkeys · PIN · TOTP · QR · recovery"]
        Files["Files service<br/>viewer/editor/owner ACL · sealed shares"]
        Notify["Notifications"]
        AppNet["AppNet launcher + Stream pool"]
        Peering["Peering / fabric (Ed25519, CRDT sync, VulaID)"]
        Obs["Observability: /metrics + OTel<br/>backend/internal/obs/"]
    end

    Assistant -->|"loopback by default"| LLMux["llmux gateway (on-box)<br/>internal/llmuxclient/"]
    Assistant -->|"/v1 mail · calendar · contacts"| Lilmail["lilmail server (separate repo)"]
    Assistant --> Vec["on-instance embeddings<br/>internal/vecdb/"]
    Backend --> DB["SQLite DB<br/>~/.vulos/db/"]
    Backend --> NS["Namespace isolation (appnet)"]
```

---

## Key design decisions

**Single binary.** The Go backend embeds the entire frontend SPA at build time via `go:embed`. One binary to deploy, one process to supervise.

**Local-first storage.** SQLite for auth/config; S3 (optional, via Restic) for encrypted backup. No external database required for a basic install.

**App sandboxing.** Each user app runs in its own Linux network namespace with a unique port. Traffic is proxied through the app gateway at `{app}--{profile}.{ulid}.vulos.org`. Web apps get no streaming overhead — just proxied HTTP.

**Sovereign assistant.** An on-box AI agent (`backend/services/assistant/`) with a curated toolset. Read-only tools (mail search, calendar/agenda, contacts, files, reminders) run inside the turn; anything with side effects becomes a *proposal* recorded in a single-use server-side ledger. Approval posts only the opaque proposal id to `/api/assistant/execute` — never client args. A tier-aware egress `Guard` fences model egress (local / sovereign / brokered / external), and tool results are framed as untrusted data to blunt prompt injection. The LLM runs through the on-box `llmux` gateway by default. See [THREAT-MODEL.md](../THREAT-MODEL.md) Component 5.

**Authentication.** Email + password + optional WebAuthn/TOTP. No third-party identity providers. Passkeys are the primary login for new accounts (with sign-counter clone/replay detection). Device PIN and QR/phone-approval cover kiosk and shared clients. A per-user master key is wrapped by both the password and a 24-word recovery phrase, so account recovery never needs a server-held plaintext key.

**Streaming on demand.** Native Linux apps (GIMP, LibreOffice, games) launch in their own Xvfb virtual display and stream via WebRTC. Close the window, stream stops. No persistent VNC session.

**CRDT sync.** Multi-instance data sync uses cr-sqlite (leaderless CRDTs). Every instance holds a full mergeable copy; concurrent writes converge without a leader. Sync state travels over the peering mesh (live) and S3 (cold checkpoint).

---

## Component map

### Frontend (`src/`)

| Directory | Purpose |
|-----------|---------|
| `src/shell/` | Window manager, dock, Mission Control, launchpad, AI Home, ⌘K palette, notification center |
| `src/auth/` | Login, passkey enrollment, QR login, setup wizard |
| `src/core/` | App registry, settings panel, system pulse |
| `src/builtin/` | Built-in apps: assistant, terminal, files/drive, app hub, dashboard, peering, notes |
| `src/apps/` | Heavier app integrations: vault, authenticator |
| `src/providers/` | React context providers |
| `src/layouts/` | Desktop and mobile layout shells |

### Backend (`backend/`)

| Path | Purpose |
|------|---------|
| `backend/cmd/server/` | HTTP server, all route handlers, middleware |
| `backend/services/assistant/` | Sovereign AI agent: tool catalog, proposal ledger, egress Guard, on-instance mail/RAG index |
| `backend/services/ai/` | LLM/embeddings service seam (the assistant's `Completer`) |
| `backend/services/auth/` | Email/password auth, sessions, device PIN, fingerprint |
| `backend/services/passkeys/` | WebAuthn/FIDO2 passkey registration/login, QR/phone approval |
| `backend/services/files/` | Files service: viewer/editor/owner ACL, sealed (content-blind) shares, share-by-email, resumable (tus-style) chunked upload promote |
| `backend/services/upload/` | Resumable upload manager (tus core: Create/Head/Patch/Delete/Sweep), SQLite-persisted offset for resume-across-restart, per-chunk checksum, abandoned-partial sweep |
| `backend/services/notify/` | Notifications (types, priority, TTL, DND, WebSocket delivery) + cell-side **Web Push** send-path (VAPID, per-owner subscription store, RFC 8291) |
| `backend/services/joincode/`, `joinsync/`, `cloudenroll/` | Device/box join tokens, join ceremony, RFC 8628 enrollment |
| `backend/services/peering/` | Ed25519 peering, VulaID key lifecycle, single reachability seam (`resolvePeerBaseURL`) over the relay, Drop |
| `backend/services/stream/` | WebRTC stream pool, bitrate control |
| `backend/services/gpu/` | GPU capability detection (NVENC, VA-API, software) |
| `backend/services/credvault/` | Server-side encrypted credential store (the OS's own, not third-party OAuth) |
| `backend/services/sync/` | CRDT rehydration and compaction |
| `backend/services/telemetry/` | GPU usage metering |
| `backend/internal/llmuxclient/` | Client for the on-box `llmux` LLM gateway (`LLMUX_URL`) |
| `backend/internal/multiinstance/` | Multi-instance quorum, signed change propagation |
| `backend/internal/safedial/` | SSRF-safe dialer (pre-dial validation + connect-time IP check) |
| `backend/internal/gpuhost/` | GPU host capability detection |
| `backend/internal/meethost/` | Self-host Meet SFU registration + heartbeat (opt-in `VULOS_SFU_HOST`); big calls escalate to media on the operator's own box |
| `backend/internal/fabric/` | Fabric mesh identity and key management |
| `backend/internal/vecdb/` | Local vector store for on-instance retrieval |
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

## Sovereign assistant flow

```mermaid
sequenceDiagram
    participant Client
    participant Agent as Assistant agent
    participant Guard as Egress Guard
    participant Model as llmux (on-box)
    participant Ledger as Proposal ledger

    Client->>Agent: POST /api/assistant/agent {message}
    Agent->>Guard: Guard(cfg, allowExternal)
    Note right of Guard: classify tier; block if external & not opted-in
    Agent->>Model: prompt (mail content framed as UNTRUSTED DATA)
    Model-->>Agent: read-only tool calls (run server-side)
    alt side-effecting action
        Agent->>Ledger: Put(userID, proposal) — opaque id, single-use, TTL
        Agent-->>Client: {id, tool, summary, args(display only)}
        Client->>Agent: POST /api/assistant/execute {id}
        Note right of Agent: id ONLY — never client args
        Agent->>Ledger: Take(userID, id) → server-stored args
        Agent->>Agent: ExecuteProposal(server args)
    else answer
        Agent-->>Client: SSE token stream
    end
```

The streaming endpoint (`POST /api/assistant/agent/stream`) emits `status`,
`token`, `proposal`, and `done`/`error` events; the `Guard` runs once up front,
so a blocked tier makes zero model calls and streams nothing. A leaked proposal
id is not indefinitely replayable (single-use + 10-minute TTL) and is bound to
the calling user's session. See [THREAT-MODEL.md](../THREAT-MODEL.md) Component 5.

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

## Cell / edge: reachability, resumable upload, push

The box is always the authority for its own data; a handful of seams let it work behind NAT and across instances without a central data plane.

- **Single reachability seam.** All box→box delivery resolves its target through one primitive (`resolvePeerBaseURL(toVulaID, server)` in `peering/resolve.go`). Today it's a pass-through to `https://<server>`; it's the single place to later flip to *verified-direct → relay-tunnel → contact.Server* without touching call sites. A durable outbox retries failed sends.
- **Server-to-server peering auth boundary.** A remote peer has no OS session on the receiving box, so the box's server-to-server surface authenticates the peer *itself*, not an OS login: the whole `/api/peering/inbound/*` subtree runs behind `peering.InboundMiddleware` (fail-closed Ed25519 envelope signature → revocation gate → approved-contacts allow-list, with `/inbound/request` the one exception so an unknown peer can send a first contact request), the relay store-and-forward routes verify an Ed25519-signed request, prekey publish/claim are gated by the signed-prekey signature, and `/.well-known/vula-id` exposes public fields only. These S2S routes are therefore exempt from the OS-session gate (like `/api/files/peer/serve`), while every *client*-facing peering route (contacts, conversations, media upload, identity export/revoke, collab WebSocket) stays OS-session-authed. Per-resource authorization is capability-scoped: per-contact permissions (message/media/call/video), per-document share ACL (`ShareStore.PeerPerm`, edit vs view), and group membership — no ambient "any approved peer can touch anything" authority.
- **Resumable upload through the relay.** Large files (≥16 MiB by default) upload in bounded chunks so each `PATCH` rides the relay as an ordinary ≤-cap HTTP request — the relay needs no changes. The box (`services/upload`) reassembles into the owner's own storage with an offset persisted in SQLite (so a dropped connection resumes via `HEAD` + `PATCH` from the committed offset), per-chunk + whole-file SHA-256 integrity, and an hourly sweep of abandoned partials. Additive: `nil` manager ⇒ `503`, and the UI falls back to the single-shot grant→PUT→commit path on an older box.
- **Sovereign Web Push (PUSH-CELL-01).** When VAPID keys are configured, the box sends Web Push **directly** to browser-vendor push services (FCM/Apple/Mozilla) — outbound-only, works behind NAT, no central dependency. Payloads are RFC-8291 encrypted (vendor routes but can't read). Only owner-targeted notifications are pushed, DND/prefs are honoured by the box first, and gone (404/410) subscriptions are pruned. Flag-gated: no keys ⇒ push off, the in-app WebSocket stream is unchanged.
- **Self-host Meet SFU (`VULOS_SFU_HOST`).** `internal/meethost` registers the box's SFU with the relay's host registry so a call escalated past the mesh cap resolves to media on the operator's own infra. E2EE is off for BYO-SFU (operator-owned media node); recording/transcription stay on the box. Inert by default — no relay / no host / relay error all degrade to the co-located Phase-1 SFU and never block the token mint.

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
