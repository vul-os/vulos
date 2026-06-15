# Vulos OS — Task Backlog

**Status: native-first re-architecture (v8 — 2026-05-26) is the ACTIVE track.** All legacy
tasks (AIROT, IDENTITY, PUBWEB, MINST, STORE, OFFLINE, MEET, audit waves) are `done` — see the
lower sections. The new work is in **§ Native-first re-architecture** below.

> **Stack invariants (FROZEN):** Go backend; pure-Go `modernc.org/sqlite` (never CGO);
> JSX-only frontend (NEVER `.tsx`); cage v1 / labwc v2 (D93); no Rust. Cloud control-plane lives
> in the separate vulos-cloud repo; all OSS tracks below must work without it (cloud is an
> optional accelerator).
>
> **Native-first invariants (v8):** browsing is native (host browser), never streamed — no
> server-side streamed browser. Every launch routes through the Open Router into one of five
> lanes (web app / CPU stream / GPU route / compute worker / local-only). GPU is peer-based BYO
> now (cloud later, metered) — no code path depends on Fly GPUs. Streaming throttling never
> applies under `opts.Gaming`. Login isolates the credential (passkeys + token vault), never the
> browsing. Tenant isolation = one Firecracker microVM per tenant on Fly Machines (orchestration
> in vulos-cloud). See ROADMAP.md §§ 0, 11, 12, 18.

---

## Area: Native-first re-architecture (v8 — 2026-05-26)

_Roadmap: ROADMAP.md §§ 0, 10, 11, 12, 18_ · _Prefixes: `ROUTER-`, `BROWSER-`, `WEBAPP-`, `LOGINISO-`, `GPU-`, `STREAMWIN-`, `TOPO-`, `PENTEST-`_

> Sequencing (ROADMAP.md §0/§11): Wins 1+2 → Open Router + browser change → web-app curation →
> login isolation → GPU route → Wins 3–5 → topology. Each task lists concrete file targets so an
> autonomous agent can pick it up. `parallel: no` = touches a hot shared file (stream/pool.go,
> stream/stream.go, gpu/gpu.go, registry.json) — serialize within a wave.

### [STREAMWIN-01] Win 1 — stop encoding when no peer is connected
`todo` · P0 · S · dep: none · parallel: no — backend/services/stream/pool.go, backend/services/stream/stream.go
Scope: The video pipeline starts at `Launch` (`pool.go` ~L451) and runs until `Stop()`, independent of viewers. Add a connected-peer refcount on `Session`; on 0→1 start `gstVideo`, on 1→0 SIGSTOP/kill it. Hook the refcount to `HandleSignaling` connect/disconnect. Never applies when no peers but session still launched — pipeline must be idle.
AC: [ ] launched-but-unconnected session runs no gst video / ~no CPU [ ] connecting a peer starts encode within ~1s [ ] last peer disconnect stops encode [ ] `go build ./backend/... && go test ./backend/services/stream/...`

### [STREAMWIN-02] Win 2 — dirty-region capture for non-gaming
`todo` · P0 · S · dep: none · parallel: no — backend/services/gpu/gpu.go
Scope: `gpu.CaptureArgs` hardcodes `use-damage=false` (~L76). Make it conditional: `use-damage=true` for non-gaming, `false` when `opts.Gaming`. Thread the gaming flag through to `CaptureArgs` if not already available.
AC: [ ] a static native-app window produces ~0 encoded frames [ ] gaming keeps constant framerate (`use-damage=false`) [ ] `go build ./backend/... && go test ./backend/services/gpu/...`

### [ROUTER-01] Open Router package — Classify(intent) → Lane
`todo` · P0 · M · dep: none · parallel: yes — backend/services/openrouter/router.go, backend/services/openrouter/router_test.go
Scope: New package `backend/services/openrouter/`. `Classify(intent) → Lane` where Lane ∈ {WebApp, CPUStream, GPURoute, ComputeWorker, LocalOnly}. Inputs: registry entry type, URL/MIME, flags `web`/`needs_gpu`/`game`/`local_only`/`compute_job`. Rules: URL or `web` → WebApp; native GUI no-flags → CPUStream; `needs_gpu`/`game` → GPURoute (prefer GPU peer, CPU fallback per app; games unavailable w/o GPU); `local_only` → LocalOnly; `compute_job` → ComputeWorker.
AC: [ ] every `registry.json` entry classifies to its expected lane (table-driven test) [ ] any URL → WebApp [ ] `go build && go test ./backend/services/openrouter/...`

### [ROUTER-02] Shell "launch app" routes through Open Router
`todo` · P0 · M · dep: ROUTER-01 · parallel: yes — src/lib/launch.js (or shell app launcher), backend/cmd/server/routes_router.go
Scope: Expose the classifier over HTTP (`GET /api/router/classify?app=<id>` → `{lane}`) and make the shell launcher consult it: WebApp → host-browser window/tab (in-shell web view via subdomain proxy), CPUStream/GPURoute → streamed window, ComputeWorker → background job, LocalOnly → local. Launching a web app must spawn **zero** `stream.Session`.
AC: [ ] launcher calls the router and dispatches per lane [ ] launching a web app spawns zero stream.Session (test/assert) [ ] `go test ./backend/cmd/server/...` [ ] `npm run build`

### [BROWSER-01] Route all browsing to the host browser; retire streamed Browser app
`todo` · P0 · M · dep: ROUTER-01 · parallel: no — registry.json, src/ (dock/launcher), apps/browser/
Scope: All web content (web apps + arbitrary URLs) opens in the host browser — window/tab or in-shell web view. Remove the streamed "Browser" app from `registry.json` and the dock. In-shell web windows render as host-browser iframes/web views inside the React shell. Bare metal: a browser window is a local compositor window (WPE WebKit / Chromium-kiosk), not a streamed session.
AC: [ ] default build exposes no streamed browser [ ] opening any web app/URL creates zero stream.Session [ ] dock no longer shows the streamed browser [ ] `npm run build`

### [BROWSER-02] Audit + remove server-side Chromium streaming path
`todo` · P1 · M · dep: BROWSER-01 · parallel: no — backend/services/webbrowser/, build.sh, Dockerfile
Scope: Remove the server-side Chromium streaming service (`backend/services/webbrowser/chrome.go`) and the `xvfb chromium xdotool` streaming install in `build.sh` (~L245, ~L540). KEEP the bare-metal kiosk Chromium + policies (~L305, ~L787) — that is the host browser. Document the removal. Confirm no retained streamed app or the GPU route depends on the removed service.
AC: [ ] webbrowser streaming service removed/retired [ ] streaming-only xvfb-chromium install dropped; kiosk Chromium kept [ ] image builds [ ] retained streamed apps still launch [ ] removal documented [ ] `go build ./backend/...`

### [BROWSER-03] Isolated/Disposable Browsing (RBI) stub behind flag
`removed` · P3 · S · dep: BROWSER-02
Scope: The flag-gated stub (`backend/services/isolatedbrowser/`) and its doc (`docs/ISOLATED-BROWSING.md`) have been removed as dead code — the stub was never mounted on any mux and VULOS_ENABLE_ISOLATED_BROWSER was unreachable. Revisit as a new task if a concrete use case arises.

### [WEBAPP-01] Curate first-class web apps in registry.json (+ lane flags)
`todo` · P1 · M · dep: ROUTER-01 · parallel: no — registry.json
Scope: Add web apps tagged `web`: kerf (CAD — default CAD intents here; see roadmap/CAD-KERF.md), miniPaint, code-server, Immich/PhotoPrism, diagrams.net, AudioMass, SVG-Edit, Jellyfin. Add lane flags (`web`/`needs_gpu`/`game`/`local_only`/`compute_job`) to existing entries: Blender/Kdenlive → `needs_gpu`; Steam/Lutris/Wine → `game`; ensure office/mail/terminal/notes are recognized web-native. Priority order: image-edit → IDE → photos → diagrams → media → kerf → audio/vector.
AC: [ ] new web apps present + valid against the manifest schema [ ] each carries correct lane flags [ ] ROUTER-01 classifies all entries with no "unknown" [ ] `npm run build`

### [LOGINISO-01] Passkey register + login flow (promote from re-auth gate)
`done` · P1 · L · dep: none · parallel: yes — backend/services/passkeys/, backend/services/stream/webauthn.go, apps/setup-wizard/ or login UI
Scope: Promote WebAuthn from the AUTH-13 re-auth gate to a full registration + assertion **login** flow for Vulos accounts. Private key never leaves the authenticator. Keep password+2FA as fallback; default new accounts to passkeys. Wire backend ceremony endpoints + the login UI.
AC: [x] passkey register works end-to-end [x] passkey login works end-to-end [x] password+2FA still works as fallback [x] `go test ./backend/services/passkeys/...` [x] `npm run build`

### [LOGINISO-02] QR / phone-approval login for kiosk/streamed clients
`done` · P2 · M · dep: LOGINISO-01 · parallel: yes — backend/services/passkeys/qrlogin.go, src/auth/QRLogin.jsx
Scope: Add QR-code / phone-approval login so a reusable secret is never typed on an untrusted (shared/streamed/kiosk) client. The kiosk shows a QR; an already-authenticated phone approves; the kiosk receives a scoped session.
AC: [x] kiosk shows QR + polls for approval [x] phone approval grants a scoped session [x] expiry + single-use enforced [x] `go test ./backend/services/passkeys/...`

### [LOGINISO-03] Token vault / BFF for connected services (OAuth refresh server-side)
`wontfix` · P2 · L — **WON'T-DO.** OAuth / connected-services was de-scoped: Vulos auth is email/password (+2FA/passkey/QR) only, no Google OAuth. The Connected-Accounts OAuth BFF (routes_oauth.go, credvault/oauth_provider.go, tokenvault.go, ConnectedAccountsPanel.jsx) was deleted. This task will not be built.
Scope: Run OAuth/OIDC for connected services (e.g. Google). Store the refresh token **server-side, encrypted** (reuse fabric key-at-rest encryption). Client gets only a session cookie; the backend makes credentialed outbound calls. No cookie-injection MITM. The connected app browses in the host browser (no stream.Session).
AC: [ ] OAuth connect stores refresh token in server-side vault only [ ] network test asserts the refresh token is NEVER sent to the client [ ] backend makes credentialed outbound call on the client's behalf [ ] `go test ./backend/services/...`

### [LOGINISO-04] THREAT-MODEL.md — login-isolation analysis
`done` · P2 · S · dep: LOGINISO-01 · parallel: yes — THREAT-MODEL.md
Scope: Document: passkeys / out-of-band auth are the only things that make the credential un-capturable by an untrusted client; pixel-streaming a login does NOT protect a secret typed on a compromised client.
AC: [x] THREAT-MODEL.md contains the login-isolation section (Component 4) [x] explicitly states streaming-login is not a credential protection [x] no code change

### [GPU-01] GPUProvider seam (BYOPeerProvider now; cloud impls as stubs)
`removed` · P1 · L · dep: none
Scope: The `GPUProvider` interface, `BYOPeerProvider`, `OnDemandCloudProvider`, `WarmPoolProvider`, and `InMemoryCapabilityStore` implementations (`backend/services/gpu/provider.go`) have been removed as dead code — they had zero non-test callers and the stream path uses `gpu.Detect()` from `gpu.go` directly. Re-implement against a concrete wiring when the GPU session path is built end-to-end.

### [GPU-02] GPU capability advertisement over the fabric
`removed` · P1 · M · dep: GPU-01
Scope: `GPUCapabilityStore`, `DescriptorFromInfo`, and `PollRelayForGPUHosts` (`backend/internal/gpuhost/capability.go`) have been removed as dead code — they depended on the now-deleted GPU-01 provider types and had zero non-test callers. Re-implement when GPU-01 is rebuilt.

### [GPU-03] Direct media plane (relay = NAT fallback only) + GPU-second metering
`removed` · P1 · L · dep: GPU-01, GPU-02
Scope: `GPUMeter`, `MeteringRecord`, `MeteringHandler`, `LogMeteringHandler`, `MediaPathFromICE`, and `FormatGPUSeconds` (`backend/services/telemetry/gpu_meter.go`) have been removed as dead code — no `stream.Session` ever called them. Re-implement when the direct media path (GPU-01/02) is built end-to-end.

### [STREAMWIN-03] Win 3 — idle FPS + idle suspend
`todo` · P2 · M · dep: STREAMWIN-01 · parallel: no — backend/services/stream/pool.go, backend/services/stream/stream.go
Scope: `SetFPS` exists (`stream.go` ~L143) but nothing calls it automatically. Add an idle lifecycle: static content for N s → drop to ~1–5 fps, ramp on activity; after X min no input AND no peer → suspend (free Xvfb/app RAM) or kill. Configurable thresholds. Skip all of this when `opts.Gaming`.
AC: [ ] idle+watched+static → low fps [ ] idle+unwatched → reclaimed after timeout [ ] gaming unaffected [ ] `go test ./backend/services/stream/...`

### [STREAMWIN-04] Win 4 — resolution adaptation (extend ABR)
`todo` · P2 · M · dep: STREAMWIN-01 · parallel: no — backend/services/stream/bitrate.go, backend/services/stream/stream.go
Scope: `bitrateController` adjusts only bitrate. On sustained loss/RTT, also step resolution via `Session.Resize()` (1080→720→480) alongside bitrate; recover when the link improves. Skip when `opts.Gaming`.
AC: [ ] sustained loss steps resolution down [ ] recovery steps it back up [ ] gaming unaffected [ ] `go test ./backend/services/stream/...`

### [STREAMWIN-05] Win 5 — live bitrate/FPS change (no full pipeline restart)
`todo` · P2 · L · dep: STREAMWIN-01 · parallel: no — backend/services/stream/pool.go, backend/services/stream/bitrate.go
Scope: ABR currently kills+respawns the whole gst process (`pool.go` ~L456–483) → re-warm + black blip. Encoders are named (`name=venc`); set bitrate live on the element and add a `videorate` element for live FPS. Keep process restart only as a fallback.
AC: [ ] a bitrate change produces no pipeline-restart log / no black frame [ ] FPS change is live [ ] fallback restart still possible [ ] `go test ./backend/services/stream/...`

### [TOPO-01] Durable-state-survives-host-loss (OS-side rehydration)
`todo` · P2 · L · dep: none · parallel: yes — backend/services/sync/, backend/services/cluster/
Scope: OS-side of the uniform-microVM topology. Treat Fly Volumes as cache, not truth: ensure an instance rehydrates fully from S3/Tigris + cr-sqlite CRDT after a host/Machine kill. Verify a wiped local cache reconstructs from the durable bucket + changeset tail.
AC: [ ] instance with wiped local cache rehydrates from bucket + CRDT [ ] no data loss on simulated host kill [ ] `go test ./backend/services/sync/... ./backend/services/cluster/...`

### [TOPO-02] Dedicated-instance migration (keep identity + synced data, peer back)
`todo` · P3 · M · dep: TOPO-01 · parallel: yes — backend/internal/multiinstance/, backend/services/peering/
Scope: "Move to your own instance" = spin up a new instance with the SAME Ed25519 identity, sync the CRDT, optionally retire the shared-pool presence, peer back via the fabric. Leaderless → no split-brain, no hard cutover.
AC: [ ] new instance adopts existing identity [ ] CRDT syncs to the new instance [ ] new instance peers back into the mesh [ ] `go test ./backend/internal/multiinstance/...`

### [PENTEST-01] Extend attacker-style pentest suite to app-level multi-tenancy
`todo` · P1 · M · dep: ROUTER-02 · parallel: yes — backend/security/, backend/services/*/security_test.go
Scope: Extend the existing pentest suites to cover the app-level multi-tenancy layer: tenant isolation, IDOR across tenants, auth bypass, open-relay, quorum (CRDT-QUORUM-class). Add cases for the new surfaces: Open Router lane confusion, GPU-peer brokering auth. (LOGINISO-03 OAuth BFF dep is moot — no OAuth in Vulos.)
AC: [ ] new tenant-isolation/IDOR cases added and green [ ] router lane-confusion case [ ] `go test ./backend/...`

> **Cross-repo (NOT this repo):** the Fly Machines per-tenant microVM fleet orchestration,
> scale-to-zero autostop/autostart, and the `ComputeProvider` abstraction live in **vulos-cloud**.
> The billing model (`vulos-cloud/billingmodel/model.py`, v8) already covers GPU metering (BYO
> credited / specialist pass-through +50%), scale-to-zero free tier, and Wave-B add-ons; a
> dedicated CPU compute-worker meter line (kerf FEA/regen) is a future line item, bounded by
> existing conservative buffers.

---

---

## At-a-glance

**Active track — Native-first re-architecture (v8): ALL WAVES IMPLEMENTED + VERIFIED (2026-05-26).**
Full backend suite green (`CGO_ENABLED=0 go test ./...`), frontend builds, pentest suite found no real vulnerabilities.

| Wave | Tasks | Status |
|---|---|---|
| 1 — Streaming wins (surgical) | STREAMWIN-01, STREAMWIN-02 | ✓ done |
| 2 — Open Router + browser change | ROUTER-01, ROUTER-02, BROWSER-01, BROWSER-02, BROWSER-03 | ✓ done |
| 3 — Web-app curation | WEBAPP-01 | ✓ done (kerf/jellyfin/minipaint/audiomass enabled; code-server/immich/diagrams/svg-edit `_disabled` pending real upstream artifacts) |
| 4 — Login isolation | LOGINISO-01, 02, 04 | ✓ done · LOGINISO-03 (OAuth BFF) **won't-do** — no OAuth in Vulos |
| 5 — GPU route | GPU-01, GPU-02, GPU-03 | ✓ done |
| 6 — Streaming wins 3–5 | STREAMWIN-03, STREAMWIN-04, STREAMWIN-05 | ✓ done |
| 7 — Topology (OS-side) + pentest | TOPO-01, TOPO-02, PENTEST-01 | **todo** — not implemented (TOPO-01/02 OS-side rehydration + dedicated-instance migration; PENTEST-01 dep on removed LOGINISO-03 is moot, but broad multi-tenant pentest coverage was added across cloud/office this session) |

**Legacy tracks (all `done`):**

| Area | Roadmap section | Done / Total |
|---|---|---:|
| BMINIT legacy | § Boot, Init & Bare Metal | 1 / 1 |
| AI Router | § AI Router | 8 / 8 |
| Vulos Mail Identity | § Identity | 7 / 7 |
| Public Webapps | § Public Webapps | 8 / 8 |
| Multi-Instance Routing | § Multi-Instance | 7 / 7 |
| Storage / Offline / MEET / audit waves | §§ Storage, Offline, Video meetings | all `done` |

---

## How to read a task

```
### [ID] short title
`todo` · P0|P1|P2|P3 · S|M|L · dep: <IDs or none> · parallel: yes|no — owned file path(s)
Scope: one paragraph; enough for an autonomous agent.
AC: [ ] verifiable outcome 1 [ ] outcome 2 [ ] go build / go test / npm run build as appropriate
```

**Status token** — `` `todo` `` or `` `done` ``.
**Priority** — `P0` highest → `P3` lowest.
**Effort** — `S` / `M` / `L` rough size.
**`parallel: no`** — touches a hot shared file; rebase before opening PR.
**Picking a task** — any `todo` whose `dep:` entries are all `done` is fair game.

---

## Area: Legacy re-open

_Prefix: `BMINIT-*`_

### [BMINIT-14] Fix live-USB installer: write bootable ESP to the USB target
`done` · P0 · M · dep: none · parallel: no — backend/cmd/installer/live.go, backend/internal/installer/esp.go
Scope: The `--live` flag currently produces a raw squashfs image that cannot boot from USB because no EFI System Partition is written. The installer must partition the target block device (GPT: 512 MiB FAT32 ESP + remainder ext4 or squashfs data), write the signed `bootx64.efi` into `EFI/BOOT/`, embed the GRUB/systemd-boot stub that loads the live squashfs via `toram` or direct mount, and sign the ESP contents with the offline key so dm-verity chains through. Mirror the netboot iPXE path (NETB-*) but writing to a physical block device instead of serving over HTTP.
AC: [ ] `vulos-install --live /dev/sdX` writes a GPT with a valid ESP [ ] the resulting USB device boots in a QEMU OVMF VM and reaches the Vulos desktop [ ] sha256sum of squashfs matches the published manifest [ ] `go build ./cmd/installer/...` passes

---

## Area: AI Router

_Roadmap: ROADMAP.md § AI Router (OS-Level)_  ·  _Prefix: `AIROT-*`_

> OS-level model-API router. Two supply modes: (a) BYO provider keys stored in
> OS settings; (b) cloud zero-setup — OS authenticates with the Vulos account
> session, keys held server-side, usage billed through the Vulos account. LiteLLM
> as provider-abstraction layer; Vercel AI SDK for UI streaming. Model choice is
> preserved in both modes. All AI features in the OS (Notes indexing, Smart Browser
> summaries, assistant, inline suggestions) call the router — never individual
> provider SDKs directly.

### [AIROT-01] AI Router package: provider abstraction + config store
`done` · P0 · M · dep: none · parallel: no — backend/internal/airouter/router.go, backend/internal/airouter/config.go, backend/internal/airouter/migrations/0001_airouter.sql
Scope: Create `internal/airouter` package. `Config` struct: mode (`byo | cloud`), active model slug, list of configured providers (OpenAI-compatible base URL, API key encrypted at rest with OS keyring, display name). SQLite migration for `airouter_config` and `airouter_providers` tables. In `byo` mode the router selects the right provider for the requested model from the local table. In `cloud` mode it calls `POST /api/ai/proxy` on the Vulos cloud control plane, forwarding the OS device cert for auth. Expose `airouter.Route(ctx, req ChatRequest) (stream io.ReadCloser, err error)` — callers receive a Server-Sent Events stream regardless of mode.
AC: [ ] `byo` mode round-trips to a stubbed OpenAI-compatible endpoint [ ] `cloud` mode sends device-cert header and streams the proxy response [ ] config survives restart (SQLite round-trip) [ ] `go build ./internal/airouter/...` passes

### [AIROT-02] AI Router HTTP handler + SSE streaming endpoint
`done` · P0 · M · dep: AIROT-01 · parallel: yes — backend/cmd/server/routes_airouter.go
Scope: Register `POST /api/ai/chat` on the OS local server. Handler validates the JSON body (`{model, messages[], stream:bool}`), calls `airouter.Route`, and pipes the SSE stream back to the caller. Include `POST /api/ai/models` (list available models from current config), `GET /api/ai/status` (current mode + active model), `PUT /api/ai/config` (update mode/model/provider, requires local auth session). Rate-limit: 10 concurrent in-flight requests; queue remainder.
AC: [ ] `/api/ai/chat` returns `text/event-stream` with `data: {...}` chunks [ ] unknown model → 422 with `{"error":"model_not_found"}` [ ] `/api/ai/models` lists models from config [ ] `go test ./cmd/server/...` covers handler

### [AIROT-03] Settings UI: AI Router config panel
`done` · P0 · M · dep: AIROT-02 · parallel: yes — apps/settings/src/components/AIRouterPanel.jsx
Scope: Add an "AI" section to the OS Settings app. Panel shows current mode (`BYO Keys` / `Vulos Cloud`). BYO mode: form to add/edit/delete providers (name, base URL, API key — key shown as masked on save). Cloud mode: shows logged-in Vulos account, estimated usage for current billing cycle (fetched from `/api/ai/status`), link to cloud dashboard. Model selector dropdown populated from `/api/ai/models`. All state via existing settings React store. JSX only (no `.tsx`).
AC: [ ] mode toggle persists via `PUT /api/ai/config` [ ] provider add/edit/delete round-trips correctly [ ] model selector updates active model [ ] `npm run build` from apps/settings passes

### [AIROT-04] Notes app: AI indexing + semantic search via router
`done` · P1 · M · dep: AIROT-01 · parallel: yes — apps/notes/src/lib/aiIndex.js, apps/notes/src/components/SearchBar.jsx
Scope: When a note is saved, enqueue an embedding request to `/api/ai/embed` (new endpoint on the router: single string → float32 vector). Store embeddings in a `note_embeddings` SQLite table (note_id, model_slug, vector BLOB). Semantic search: on query, embed the query string, compute cosine similarity over stored vectors, surface top-5 results above threshold 0.75 alongside the existing full-text results. Show an "AI search" badge on semantic hits. Gracefully degrade (fall back to full-text only) when AI Router is unconfigured.
AC: [ ] saving a note triggers background embedding (non-blocking) [ ] semantic search surfaces correct notes [ ] graceful fallback when AIROT unconfigured [ ] `npm run build` passes for notes app

### [AIROT-05] Browser app: Smart Summarise via router
`done` · P1 · M · dep: AIROT-02 · parallel: yes — apps/browser/src/components/SmartBar.jsx
Scope: Add a "Summarise" button to the browser address/toolbar. On click: extract the visible page text (via `window.getSelection()` or `document.body.innerText` from the WebKit content script), POST to `/api/ai/chat` with a system prompt instructing summarisation, stream the response into a slide-up panel below the toolbar. "Copy", "Share", and "Save to Notes" actions in the panel footer. Cancel button aborts the stream. Disable button when AI Router not configured; show config nudge instead.
AC: [ ] button fires summarisation and streams result [ ] panel shows streaming text incrementally [ ] cancel aborts the fetch [ ] "Save to Notes" creates a note [ ] `npm run build` passes for browser app

### [AIROT-06] AI assistant app: wire to router, replace direct provider calls
`done` · P1 · S · dep: AIROT-02 · parallel: yes — apps/assistant/src/lib/api.js
Scope: The existing AI assistant app currently calls provider APIs directly (hardcoded keys). Replace all direct provider calls with calls to `POST /api/ai/chat` on the local OS server. Remove all provider-specific SDK imports. Ensure the conversation history format is preserved. The assistant must work in both `byo` and `cloud` router modes transparently.
AC: [ ] assistant app has zero direct provider API calls [ ] conversations continue normally in both router modes [ ] `npm run build` passes

### [AIROT-07] Cloud AI proxy endpoint (OS-side client)
`done` · P1 · M · dep: AIROT-01 · parallel: yes — backend/internal/airouter/cloudproxy.go
Scope: Implement the `cloud` mode transport in the router. When `mode=cloud`, requests to `/api/ai/chat` or `/api/ai/embed` are proxied to `POST https://api.vulos.org/ai/proxy` using the OS device cert (mTLS or `Authorization: VulosDevice <cert>` header). The cloud proxy authenticates the device cert against the enrolled account and bills usage to that account. Implement exponential back-off on 429/503 from the proxy. Expose `VULOS_AI_PROXY_URL` env override for dev/self-hosted.
AC: [ ] cloud mode sends device cert and receives streamed response [ ] 429 triggers back-off with retry [ ] env override redirects to local stub [ ] `go test ./internal/airouter/...`

### [AIROT-08] AI Router: embed endpoint + vector store
`done` · P2 · M · dep: AIROT-01 · parallel: yes — backend/cmd/server/routes_airouter.go, backend/internal/airouter/embed.go
Scope: Add `POST /api/ai/embed` endpoint: body `{input: string, model?: string}`, returns `{embedding: [float32...], model: string}`. In `byo` mode, route to the provider's embeddings API (OpenAI `/v1/embeddings` or compatible). In `cloud` mode, proxy to `POST https://api.vulos.org/ai/embed`. Cache embeddings in SQLite keyed by `SHA256(model+input)` with a 30-day TTL to avoid redundant calls. Expose `GET /api/ai/embed/stats` (cache hit rate, total stored).
AC: [ ] embed endpoint returns valid float32 vector [ ] cache hit skips provider call [ ] cache eviction honours TTL [ ] `go test ./internal/airouter/...`

---

## Area: Vulos Mail Identity

_Roadmap: ROADMAP.md § Identity_  ·  _Prefix: `IDENTITY-*`_

> Every Vulos instance has a mandatory mail identity created at install/first-boot.
> The identity is a `user@vulos.org` address (or `user@custom-domain` for self-hosted).
> The mail server is vulos-mail (OSS, separate repo). Delivery relay is vulos-relay.
> No external email provider. Vulos Mail is the identity backbone for account recovery,
> inter-instance peering contact cards, and notification delivery.

### [IDENTITY-01] First-boot wizard: Vulos account creation step
`done` · P0 · M · dep: none · parallel: no — apps/setup-wizard/src/steps/VulosAccountStep.jsx, backend/internal/identity/identity.go
Scope: Add a mandatory "Create your mail identity" step to the first-boot wizard (after the user-account step, before cluster-join). The user picks a username; the wizard checks availability against `https://vulos.org/api/check?user=<name>` (GET, returns `{available: bool}`). On confirm, `POST /api/identity/claim` on the local OS server: stores the chosen identity locally (`identity` table: address, ed25519_public_key, ed25519_private_key_encrypted), and registers the keypair with vulos-relay for delivery routing. The step is skippable only if `VULOS_CLOUD_SKIP=1` is set in the boot environment (for dev/testing). JSX only.
AC: [ ] wizard shows cloudAccount step [ ] availability check calls the check endpoint [ ] confirmed identity is stored in SQLite and survives reboot [ ] skip flag works in dev [ ] `npm run build` passes for setup-wizard

### [IDENTITY-02] identity package: keypair, identity store, send/receive primitives
`done` · P0 · L · dep: IDENTITY-01 · parallel: yes — backend/internal/identity/identity.go, backend/internal/identity/store.go, backend/internal/identity/migrations/0001_identity.sql
Scope: Create `internal/identity` package. `Identity` struct: address, Ed25519 keypair (private key encrypted under OS keyring). SQLite migration: `identity_store`, `identity_mailbox` (id, from_address, subject, body_encrypted, received_at, read), `identity_outbox` (id, to_address, subject, body_encrypted, queued_at, sent_at, status). `Send(ctx, to, subject, body string) error` — encrypts body to recipient's published public key (fetched from vulos-relay key directory), signs with sender key, POSTs to relay `/api/mail/deliver`. `Receive` — relay pushes inbound mail via WebSocket; decrypt + store in mailbox. Wire `go build ./internal/identity/...`.
AC: [ ] keypair generation, encryption round-trip [ ] send constructs signed+encrypted envelope [ ] receive decrypts and stores in mailbox [ ] `go test ./internal/identity/...` passes

### [IDENTITY-03] Mail app: inbox, compose, send, thread view
`done` · P0 · L · dep: IDENTITY-02 · parallel: yes — apps/mail/src/App.jsx, apps/mail/src/components/
Scope: Default Mail app (replaces placeholder). Inbox list (subject, from, date, read/unread). Thread view: expand chain of messages. Compose: to (address picker backed by contacts + Vulos identity directory lookup), subject, body. Send via `POST /api/identity/send`. Mark-read, archive, delete. Unread badge on the launcher icon via the OS notification count API. The app must load and be useful with zero mail (empty state). JSX only; style consistent with existing apps.
AC: [ ] inbox renders messages from local mailbox [ ] compose → send round-trips through identity package [ ] thread view groups by subject+participants [ ] unread count propagates to launcher [ ] `npm run build` passes

### [IDENTITY-04] identity HTTP handlers: send, mailbox, identity
`done` · P0 · M · dep: IDENTITY-02 · parallel: yes — backend/cmd/server/routes_identity.go
Scope: Register: `POST /api/identity/send` (body, envelope validated, calls identity.Send), `GET /api/identity/mailbox` (paginated, returns messages with decrypted subjects, encrypted body IDs), `GET /api/identity/mailbox/:id` (fetch + decrypt single message body), `PATCH /api/identity/mailbox/:id` (mark read/archived/deleted), `GET /api/identity/identity` (returns current address + public key), `POST /api/identity/identity/rotate` (re-generate keypair, publish new key to relay, old key kept for 30 days for pending decryption). All routes require local OS session.
AC: [ ] send → delivery to stub relay works end-to-end [ ] mailbox pagination tested [ ] identity rotate publishes new key [ ] `go test ./cmd/server/...` covers handlers

### [IDENTITY-05] Relay integration: delivery routing, key directory
`done` · P1 · M · dep: IDENTITY-02 · parallel: yes — backend/internal/identity/relay.go
Scope: Implement the relay client used by identity.Send and the inbound WebSocket subscriber. `RelayClient.PublishKey(ctx, address, pubKey)` — registers/updates the Ed25519 public key for this instance's address in the vulos-relay key directory (`PUT https://relay.vulos.org/keys/<address>`). `RelayClient.LookupKey(ctx, address) (ed25519.PublicKey, error)` — resolves a recipient's public key (cached locally for 1 h). `RelayClient.Subscribe(ctx)` — WebSocket subscription to `wss://relay.vulos.org/ws/mail/<address>`; dispatches inbound encrypted messages to identity.Receive. VULOS_RELAY_URL env override for self-hosted relay.
AC: [ ] PublishKey round-trip against a local stub relay [ ] LookupKey returns cached key on second call [ ] Subscribe receives and dispatches test message [ ] `go test ./internal/identity/...`

### [IDENTITY-06] Account recovery via Vulos account address
`done` · P1 · M · dep: IDENTITY-01 · parallel: yes — backend/internal/auth/recovery.go, apps/setup-wizard/src/steps/RecoveryStep.jsx
Scope: During first-boot, after Vulos account identity is created, generate a recovery kit: a 24-word BIP39 mnemonic that can re-derive the Ed25519 keypair + OS keyring root key. Store the mnemonic encrypted under the user's login password. On the Recovery screen (accessible from boot menu): user enters mnemonic → keyring is re-derived → local data is decryptable without cloud. Cloud-assisted recovery (optional): user can escrow an encrypted copy of the recovery kit to their Vulos cloud account — cloud never holds the plaintext. Update the first-boot wizard recovery step to reference the Vulos account address as the human-readable identifier.
AC: [ ] mnemonic generated and displayed once at setup [ ] re-entry of mnemonic restores keyring [ ] cloud escrow stores encrypted-only blob [ ] `go test ./internal/auth/...` passes

### [IDENTITY-07] Contact cards: Vulos account address as peering contact field
`done` · P2 · S · dep: IDENTITY-01 · parallel: yes — apps/contacts/src/components/ContactCard.jsx, backend/internal/peering/contact.go
Scope: Add `vulos_address` field to the `contacts` SQLite table (migration). Display the Vulos account address on the contact card. "Send mail" button opens the Mail compose view pre-populated with the contact's address. Peering invitation exchange includes the Vulos account address in the signed contact card JSON so peers automatically populate each other's mail address field. Lookup via relay key directory is triggered when a contact card arrives without a locally-cached key.
AC: [ ] contacts table migration adds vulos_address [ ] contact card shows mail address + send button [ ] peering exchange populates vulos_address field [ ] `go test ./internal/peering/...` passes

---

## Area: Public Webapps & Resource Governance

_Roadmap: ROADMAP.md § Public Webapps & Resource Governance_  ·  _Prefix: `PUBWEB-*`_

> Any installed Vulos webapp can be published to a public subdomain.
> Subdomain scheme: `{app}--{profile}.{ulid}.vulos.net` (or custom domain).
> cgroup v2 reservation: OS system services (sync, mail) get a protected CPU+RAM
> slice that no published webapp can starve. Edge cache (Nginx micro-cache or CDN
> push) for static assets. Dashboard publish toggle with resource usage.

### [PUBWEB-01] App manifest: `visibility` field + publish toggle
`done` · P0 · S · dep: none · parallel: yes — backend/services/appnet/manifest.go, apps/launcher/src/components/AppMenu.jsx
Scope: Add `"visibility": "private" | "public"` to `app.json` schema (default `"private"`). Validate on app install. In the launcher context menu, add a "Publish to web" toggle that calls `PATCH /api/apps/:id/visibility`. Published apps get a public subdomain provisioned via PUBWEB-02. Show a "Public" badge on published apps in the launcher. The toggle must be disabled for system apps (settings, files, terminal).
AC: [ ] `app.json` with `visibility` field passes schema validation [ ] toggle calls API and updates badge [ ] system apps cannot be published [ ] `go test ./internal/apps/...`

### [PUBWEB-02] Subdomain provisioning for published apps
`done` · P0 · M · dep: PUBWEB-01 · parallel: yes — backend/services/appnet/subdomain.go, backend/cmd/server/routes_apps.go
Scope: When an app's visibility is set to `public`, provision a subdomain `{app}--{profile}.{ulid}.vulos.net` via the Vulos cloud DNS API (`POST https://api.vulos.org/dns/provision`). Store the provisioned FQDN in `app_deployments` SQLite table. The OS reverse proxy (Caddy or Nginx config generator) adds a virtual host entry for the subdomain, TLS via ACME (Let's Encrypt). PUBWEB-04 (cgroup) must be applied before the subdomain goes live. Self-hosted: emit a Caddy Caddyfile snippet so users can point their own domain.
AC: [ ] `PATCH /api/apps/:id/visibility` provisions subdomain on publish [ ] reverse proxy config is regenerated and reloaded [ ] TLS certificate is obtained [ ] FQDN stored in DB and returned in API response [ ] `go test ./cmd/server/...`

### [PUBWEB-04] cgroup v2 resource governance: system reservation + per-app limits
`done` · P0 · L · dep: none · parallel: no — backend/internal/cgroups/governor.go, backend/internal/cgroups/migrations/0001_cgroups.sql
Scope: Create `internal/cgroups` package. At OS startup, write cgroup v2 hierarchy under `/sys/fs/cgroup/vulos/`. System slice `vulos-system.slice`: 30% CPU weight + 512 MiB memory.min guarantee (no webapp can take this). Per-app slice `vulos-apps-{ulid}.slice`: default 10% CPU weight, 256 MiB memory.high (soft limit), 512 MiB memory.max (hard OOM). Published apps (public visibility) get a tighter default: 15% CPU weight, 128 MiB memory.high. Expose `GET /api/cgroups/status` (current usage per app + system slice). On app crash/OOM, emit a structured notification. All Go, no shell scripts.
AC: [ ] system slice has memory.min >= 512 MiB written [ ] published app slice has correct limits [ ] OOM kills app process (not OS services) [ ] status endpoint returns live cgroup stats [ ] `go test ./internal/cgroups/...`

### [PUBWEB-03] Edge cache: Nginx micro-cache for published app static assets
`done` · P1 · M · dep: PUBWEB-02 · parallel: yes — backend/internal/network/edgecache.go, config/nginx/pubweb.conf.tmpl
Scope: Generate an Nginx `proxy_cache` config for each published app subdomain: cache `GET` responses for static assets (JS/CSS/images — `Cache-Control: public`) for up to 5 minutes. Pass-through for API routes and authenticated requests. Include `X-Cache: HIT|MISS` header. Provide `POST /api/apps/:id/cache/purge` endpoint to flush the Nginx cache for an app (calls `nginx -s reload` or uses the Nginx Purge module if available). Dashboard shows cache hit rate from Nginx stub_status.
AC: [ ] static asset request served from cache on second hit [ ] `X-Cache: HIT` header present [ ] cache purge reloads Nginx config [ ] `go build ./internal/network/...`

### [PUBWEB-05] Dashboard: publish toggle + resource usage per app
`done` · P1 · M · dep: PUBWEB-01, PUBWEB-04 · parallel: yes — apps/dashboard/src/components/AppPublishCard.jsx
Scope: In the OS Dashboard app, add a "Web" section listing all installed apps with a publish toggle, their current public URL (if published), and a live resource usage bar (CPU %, RAM MiB, from `/api/cgroups/status`). Show a warning banner when an app is approaching its memory.high limit. Published apps show a "copy link" button for the public URL and a "purge cache" button (PUBWEB-03). JSX only.
AC: [ ] publish toggle works from dashboard [ ] resource bars update every 5 s via polling [ ] memory warning appears at >=80% of memory.high [ ] `npm run build` passes for dashboard app

### [PUBWEB-06] Top-bar public-app warning banner
`done` · P1 · S · dep: PUBWEB-01 · parallel: yes — apps/shell/src/components/TopBar.jsx
Scope: When the user is currently viewing a published (public) app, show a persistent amber banner at the top of the OS shell: "This app is publicly accessible — anyone on the internet can view it." Include a "Make private" quick-action that calls `PATCH /api/apps/:id/visibility` with `private`. The banner must not appear for private apps or system apps. Align with the existing topbar warning design pattern if one exists.
AC: [ ] banner appears when current app has `visibility: public` [ ] "Make private" hides banner and updates app [ ] banner absent for private/system apps [ ] `npm run build` passes for shell app

### [PUBWEB-07] Custom domain support for published apps
`done` · P2 · M · dep: PUBWEB-02 · parallel: yes — backend/internal/network/customdomain.go, backend/cmd/server/routes_apps.go
Scope: Allow users to attach a custom domain to a published app. `POST /api/apps/:id/domain` body `{domain: "mysite.example.com"}`: verify ownership via a DNS TXT record `_vulos-verify.mysite.example.com = <challenge-token>`, provision TLS via ACME for the custom domain, add virtual host to Nginx config, store in `app_deployments`. `DELETE /api/apps/:id/domain` removes the custom domain (reverts to default subdomain). Show domain verification status in the dashboard.
AC: [ ] TXT challenge issued and checked [ ] verified domain added to Nginx config + ACME [ ] unverified domain shows pending status [ ] `go test ./cmd/server/...`

### [PUBWEB-08] Resource alerts: notification + auto-throttle on sustained overuse
`done` · P2 · M · dep: PUBWEB-04 · parallel: yes — backend/internal/cgroups/alerter.go
Scope: Background goroutine polls cgroup stats every 10 s. If a published app sustains >90% of its CPU weight for >60 s, emit a structured OS notification (priority: warning, action: "Review in Dashboard"). If memory.current > memory.high for >30 s, throttle the app by reducing its CPU weight by 50% and notify. Auto-restore to normal limits after the app drops below 70% usage for 120 s. Log all throttle events to `cgroup_events` SQLite table for dashboard history view.
AC: [ ] alert fires after 60 s sustained CPU overuse [ ] CPU throttle is applied and logged [ ] auto-restore after cooldown [ ] `go test ./internal/cgroups/...`

---

## Area: Multi-Instance Routing

_Roadmap: ROADMAP.md § Multi-Instance & Account Routing_  ·  _Prefix: `MINST-*`_

> One Vulos account routes and federates many instances: BYO physical devices +
> optionally Vulos-provisioned cloud instances (fly.io). The cloud control plane
> knows which instances belong to an account and routes the `{app}--{profile}.{ulid}`
> subdomain to the right live instance. Vulos's core OS-side value is the networking
> and data-routing between instances — leaderless cr-sqlite CRDT sync, shared
> app-registry, coordinated leases.

### [MINST-01] Instance registry: local manifest of all account instances
`done` · P0 · M · dep: none · parallel: no — backend/internal/multiinstance/registry.go, backend/internal/multiinstance/migrations/0001_instances.sql
Scope: Create `internal/multiinstance` package. SQLite migration: `instances` table (ulid, display_name, last_seen_at, endpoint_url, ed25519_public_key, role enum(`owner|peer`), status enum(`online|offline|unknown`)). `Registry.Upsert`, `Registry.List`, `Registry.Get`, `Registry.MarkSeen`. Instances are discovered from: (a) the vulos-relay presence feed, (b) manual `POST /api/instances/add` with peer exchange QR code / link, (c) cloud account sync on login. The registry is the source of truth for routing decisions and the CRDT sync peer list.
AC: [ ] registry CRUD survives restart [ ] Upsert deduplicates by ULID [ ] `go test ./internal/multiinstance/...` passes

### [MINST-02] Cloud-sync: pull instance list from Vulos account on login
`done` · P0 · M · dep: MINST-01 · parallel: yes — backend/internal/multiinstance/cloudsync.go
Scope: After the OS device authenticates with the Vulos cloud control plane (CLOGIN-* done), pull the list of instances enrolled under the same account: `GET https://api.vulos.org/api/instances` (auth: device cert). Upsert each instance into the local registry. Subscribe to `wss://api.vulos.org/ws/instances` for real-time presence updates (instance comes online / goes offline). Re-sync on cloud reconnect. Expose `GET /api/instances` on the local OS server returning the merged list.
AC: [ ] instance list pulled on login and stored in registry [ ] WebSocket presence updates are applied [ ] `GET /api/instances` returns current list [ ] offline mode: uses last-known registry [ ] `go test ./internal/multiinstance/...`

### [MINST-03] App routing: `{app}--{profile}.{ulid}.vulos.net` per instance
`done` · P0 · M · dep: MINST-01, PUBWEB-02 · parallel: yes — backend/internal/multiinstance/router.go
Scope: Each instance has a unique ULID. The OS reverse proxy config generator (PUBWEB-02) already handles the current instance's subdomains. Extend it to generate `{app}--{profile}.{ulid}` subdomains for every instance in the registry that has the given app published. The cloud DNS plane routes these to the correct instance's WireGuard/relay endpoint. Locally, `GET /api/routing/apps` returns a table of `{app, ulid, fqdn, instance_display_name}` for all reachable published apps across all account instances.
AC: [ ] routing table lists apps across all account instances [ ] subdomains resolve to correct instance endpoints (tested with stub DNS) [ ] `go test ./internal/multiinstance/...`

### [MINST-04] App registry sync: cr-sqlite CRDT replication across instances
`done` · P1 · L · dep: MINST-01 · parallel: no — backend/internal/multiinstance/appsync.go
Scope: Extend the existing cr-sqlite cluster sync (CLUSTER.md / SYNC.md — already `done`) to include the `app_registry` table in the replicated set. When an app is installed, updated, or uninstalled on any instance, the change propagates via cr-sqlite changesets over the existing sync channel. Conflict resolution: last-write-wins on `app_version` field; `installed` flag merges as boolean OR (install wins over uninstall — uninstall requires quorum of 2 if more than 2 instances). Expose `GET /api/instances/:ulid/apps` for per-instance app inventory.
AC: [ ] app install on instance A appears in instance B's registry within 5 s (test with two in-process stores) [ ] uninstall conflict resolved by quorum [ ] `go test ./internal/multiinstance/...`

### [MINST-05] Vulos-provisioned cloud instance: fly.io launch from OS dashboard
`done` · P1 · L · dep: MINST-01, MINST-02 · parallel: yes — apps/dashboard/src/components/NewInstancePanel.jsx, backend/internal/multiinstance/provision.go
Scope: "New Instance" panel in the Dashboard lets the user spin up a Vulos-provisioned instance on fly.io, billed to their Vulos account. The OS calls `POST https://api.vulos.org/api/instances/provision` (auth: device cert, body: `{region, plan}`). The cloud side launches the fly.io machine, enrolls it under the account, and returns the new instance ULID + endpoint. The new instance appears in the local registry (MINST-01) and is immediately available for app routing (MINST-03). Show provisioning progress (polling `GET /api/instances/:ulid/status`).
AC: [ ] panel shows region + plan selector [ ] provision call returns ULID within 30 s (stub) [ ] new instance appears in registry [ ] `npm run build` passes for dashboard app

### [MINST-06] Multi-instance notifications: fan-out + dedup
`done` · P2 · M · dep: MINST-02 · parallel: yes — backend/internal/multiinstance/notifyfanout.go
Scope: When the OS emits a notification (OS notification system — NOTIFICATIONS.md `done`), fan it out to all online instances via the vulos-relay S2S messaging channel so the user sees it on all their devices. Dedup by `notification_id` (stored in `seen_notifications` SQLite table with 7-day TTL) — each instance delivers the notification to the local UI at most once. Priority mapping: OS P0/P1 notifications fan out immediately; P2/P3 are batched (30-second window, deduplicated before send).
AC: [ ] notification sent on instance A appears on instance B within 2 s [ ] duplicate delivery prevented by seen table [ ] P2/P3 batching tested [ ] `go test ./internal/multiinstance/...`

### [MINST-07] Instance dashboard: unified view of all account instances
`done` · P2 · M · dep: MINST-02, MINST-03 · parallel: yes — apps/dashboard/src/components/InstancesPanel.jsx

Scope: Add an "Instances" section to the OS Dashboard. Shows a card per instance: display name, ULID (truncated), online/offline badge, list of published apps, resource summary (CPU %, RAM — fetched lazily when instance is online). Actions: "Open app on this instance" (navigates to the FQDN), "Rename", "Remove from account" (with confirmation). "Add existing device" flow: generates a QR code / link containing a signed invite token; scanning on the other device adds it to the registry. JSX only.
AC: [ ] instances list populated from `/api/instances` [ ] online/offline badges update on presence change [ ] "open app" link navigates to correct FQDN [ ] `npm run build` passes for dashboard

---

## Area: Storage backend, multi-location & co-location

_Roadmap: ROADMAP.md §Storage Backend, Multi-Location, Co-location & Identity_  ·  _Prefix: `STORE-*`, `BUNDLE-*`_

> Storage-backend selector (Tigris vs MinIO), multi-location enrollment, cross-location mail
> routing, co-located installer bundle, and anchor-inbox provisioning.
> See decisions A–E in ROADMAP.md.

### [STORE-BYO-01] Per-account storage-backend selector (Tigris vs MinIO)
`todo` · P1 · M · dep: none · parallel: yes — backend/internal/storage/config.go, apps/settings/src/components/StoragePanel.jsx
Scope: Add a per-account `storage_backend` field (`tigris | minio_local`) to the account settings
store. Tigris is the default. MinIO-local requires the customer to provide endpoint + credentials.
The bucket interface is unchanged — only the connection bootstrapping differs. Expose a "Storage"
settings panel (JSX only) that lets the account owner view and update their storage backend.
Propagate the chosen endpoint/creds to the CRDT sync, mail, and file store layers via a shared
`StorageConfig` struct. Document the anchor-inbox exception: anchor inbox always uses Tigris
regardless of the selected backend.
AC: [ ] `storage_backend` persists in account settings [ ] StoragePanel renders backend selector [ ] MinIO endpoint + creds injected into all S3 clients [ ] Tigris remains the default [ ] anchor inbox is always Tigris regardless of backend choice [ ] `go build ./... && npm run build`

### [STORE-MULTLOC-02] Multi-location enrollment: join a second or third box to an org
`todo` · P2 · L · dep: STORE-BYO-01 · parallel: yes — backend/internal/multiinstance/, apps/dashboard/src/components/LocationsPanel.jsx
Scope: Extend the instance registry and the Vulos cloud enrollment API to support multiple compute
locations for a single org, all sharing one bucket (v1 central-bucket model). A new "Join this
box to an existing org" flow: the new box authenticates with the org's enrollment token, registers
in the instance registry with a location label, and receives the bucket endpoint/credentials from
the cloud control plane. The existing bucket-lease coordinator handles concurrent writes. The
dashboard shows all enrolled locations with online/offline status.
AC: [ ] second box joins org via enrollment token [ ] both boxes share the same bucket endpoint [ ] instance registry shows multi-location topology [ ] bucket-lease coordinator tested with two concurrent instances [ ] `go build ./... && npm run build`

### [STORE-MULTLOC-01] Multi-location replicated peer-sync — document as future work, out of v1 scope
`todo` · P3 · S · dep: none · parallel: yes — docs/ (markdown only)
Scope: Document the deferred v2 multi-location topology (each location runs its own MinIO with
peer-to-peer CRDT sync over the Vulos fabric). This is for strict per-site data sovereignty or
offline-must-work requirements only. Write a short design note in `docs/MULTI-LOCATION-FUTURE.md`
marking it explicitly out of v1 scope. No code changes.
AC: [ ] `docs/MULTI-LOCATION-FUTURE.md` created [ ] document states v1 default is central bucket [ ] replicated-peer-sync labelled "future work, not v1" [ ] `npm run build` unaffected

### [BUNDLE-01] Co-located meta-installer: OS + vulos-mail + vulos-office on one box
`todo` · P2 · L · dep: none · parallel: yes — scripts/bundle-install.sh (new), apps/firstboot/src/steps/BundleStep.jsx (new)
Scope: Create a `vulos` meta-bundle bash installer and a first-boot wizard step that installs
and supervises all three co-located services — OS, vulos-mail, and vulos-office — on a single
instance. They share one bucket endpoint, one CRDT/peering fabric, and one identity. The wizard
step presents: "Install everything on this box (recommended for BYO single-box)" vs "Mail only"
vs "OS only". Service supervision: systemd units for vulos-mail and vulos-office alongside the
existing OS services. Today's installer is mail-only; this extends it. JSX wizard step only; bash
script is the install glue. No changes to vulos-mail or vulos-office source code.
AC: [ ] bash installer provisions OS + mail + office on a fresh VM [ ] wizard step presents bundle/mail-only/OS-only choice [ ] all three services start under systemd [ ] single bucket endpoint shared by all three [ ] `npm run build` passes for firstboot app

### [ANCHOR-01] Anchor inbox provisioning: always-on ~1 GB Tigris inbox per account
`todo` · P1 · M · dep: STORE-BYO-01 · parallel: yes — backend/internal/identity/anchor.go (new)
Scope: Ensure every account (including complete-BYO/MinIO accounts) has a small (~1 GB) always-on
anchor inbox on Vulos Tigris. This inbox is provisioned at account creation and is independent of
the account's main storage-backend choice. It serves as the account's always-reachable fallback
inbox so the user can never be locked out even when their local MinIO or compute is offline.
Surface the anchor inbox status and usage in the OS Dashboard mail card and in the cloud billing
dashboard. Document the anchor inbox in the recovery ladder.
AC: [ ] anchor inbox bucket prefix provisioned on Tigris at account creation [ ] MinIO-backend accounts also have an anchor inbox on Tigris [ ] anchor inbox capped at ~1 GB (configurable; overage → soft warning) [ ] inbox accessible via standard JMAP/IMAP endpoints [ ] `go build ./...`

---

## Area: Future

### OS app wrappers for office / spaces / calendar / meet
`todo` · P2 · M · dep: none · parallel: yes — apps/ (new dirs)
Add installable OS app wrappers for `vulos-office` surfaces (docs, sheets, slides, spaces, calendar, meet) following the `apps/mail/` pattern. Each wrapper registers with the OS launcher, gets its own subdomain, and integrates with OS notifications and session management. Wrappers are thin JSX shells only — do not touch `vulos-office/src/apps/*/lib.jsx`.

### airouter mail-specific endpoints — verify shipped
`todo` · P3 · S · dep: none · parallel: yes — apps/mail/, backend/internal/airouter/
Verify that smart-compose, summarize, reply-suggestions, and extract-actions endpoints are wired in `airouter` and called by the mail app. Update ROADMAP.md §3 status if confirmed shipped. No new implementation unless a gap is found.

### LLM phishing classifier endpoint via airouter
`todo` · P2 · M · dep: none · parallel: yes — backend/internal/airouter/
Add `POST /api/ai/classify/phishing` to `airouter`. Input: URL or attachment metadata. Output: risk score, confidence, action (show/warn/block). Uses local Ollama model (small) with cloud-billed fallback. Called by `vulos-mail` inbound filter and OS browser before rendering external links.

### identity package rename (completed 2026-05)
`todo` · P3 · S · dep: none · parallel: yes — apps/mail/, backend/internal/identity/
Completed: renamed the `vumail` package to `identity`; all `@vulos.org` user-visible strings updated. Coordinated across vulos, vulos-cloud, vulos-mail, vulos-relay, vulos-office.

---

## Area: BYO Mail — OS integration

_Spec: [`ROADMAP.md §BYO Mail support`](ROADMAP.md)_  ·  _Prefix: `OS-BYO-*`_
_Cross-repo: [`vulos-mail`](https://github.com/vul-os/vulos-mail) (MAIL-BYO-04) · [`vulos-cloud`](https://github.com/vul-os/vulos-cloud) (BYO-CP-*)_

> The OS first-boot wizard gains a "Mail service" step that installs vulos-mail as a built-in
> service. These tasks cover the OS-side integration: the wizard step, dashboard status surface,
> and notification routing for BYO offline alerts.

### [OS-BYO-01] First-boot wizard "Mail service" optional step
`in-progress` · P2 · M · dep: none · parallel: yes — apps/firstboot/src/steps/MailService.jsx (new), backend/internal/installer/
Scope: Add an optional "Mail service" step to the first-boot wizard (after the "Networking" step).
Presents two choices: (a) set up vulos-mail on this instance (BYO mode — calls MAIL-BYO-04 bash
installer integration), or (b) use hosted Vulos Mail (redirects to signup). If (a) is selected,
calls `vulos-mail byo setup` as a sub-process and waits for pubkey upload confirmation.
JSX only — no new .tsx; no changes to existing wizard step components.
AC: [ ] "Mail service" step renders in wizard (skippable) [ ] BYO choice triggers installer integration [ ] hosted choice routes to signup [ ] wizard advances on success [ ] npm run build

### [OS-BYO-02] Dashboard: vulos-mail service status card
`in-progress` · P3 · S · dep: OS-BYO-01 · parallel: yes — apps/dashboard/src/components/MailServiceCard.jsx (new)
Scope: Add a "Mail service" card to the OS Dashboard showing vulos-mail service status
(running / stopped / offline — fetched from local service health endpoint and the cloud "last
seen" signal). Shows: service state, last seen timestamp, key fingerprint (from cloud identity
store), and an "Alerts" toggle for the offline >30-min notification. JSX only.
AC: [ ] card renders service state [ ] "last seen" shown when cloud signal available [ ] key fingerprint displayed [ ] alerts toggle works [ ] npm run build

### [OS-BYO-03] BYO offline alert routing to OS notification system
`in-progress` · P3 · S · dep: OS-BYO-02 · parallel: yes — backend/internal/notify/byo_alert.go (new)
Scope: When the cloud BYO health-check (BYO-CP-04) emits an offline >30-min alert, route it to
the OS notification system on any online instance in the account (via MINST-06 fan-out). Priority:
`high`. Body: "Your Vulos Mail instance has been offline for 30+ minutes. Inbound mail is queued
for up to 5 days." Action button: "View mail dashboard."
AC: [ ] alert delivered to OS notification system on online instance [ ] not repeated more than once per 4h [ ] action button links to mail dashboard [ ] go build ./...

### [OS-BYO-04] "Install vulos-mail" entry in OS App Store
`in-progress` · P3 · S · dep: none · parallel: yes — registry.json
Scope: Add a `vulos-mail` entry to `registry.json` so the OS App Store shows "Mail Server" as an
installable service. Recipe type: `bash-installer` with the `curl ... | bash` URL. Tier gate: visible
to all paid tiers (Vulos Mail+); greyed out on Free with "Requires Vulos Mail tier" note.
AC: [ ] registry.json entry added for vulos-mail [ ] App Store renders it [ ] tier gate applied [ ] no .go or .jsx changes beyond registry.json [ ] npm run build

---

## Area: Offline LAN access + local-first storage (Decisions F+G, v6 — 2026-05-24)

_Roadmap: ROADMAP.md §Offline LAN access & local-first storage_ · _Prefix: `OFFLINE-` / `STORE-LOCAL-` / `STREAM-BYO-`_
> Opt-in (NOT default). When the internet/cloud is down but the client is on the box's LAN, OS + office +
> mail keep working by talking to the box directly. Local MinIO + CRDT sync is an extra config option;
> default stays central Tigris. Identity + anchor inbox stay central regardless.

### [OFFLINE-01] Box LAN reachability: mDNS + local DNS responder + LAN TLS cert
`done` · P1 · L · dep: none · parallel: yes — backend/internal/lan/ (new), firstboot/
Scope: OS instance advertises on the LAN via mDNS (`vulos.local`) and runs a tiny DNS responder that
answers `box.<id>.lan.vulos.org` → its LAN IP so the name resolves with the internet down. Install +
serve the DNS-01-issued cert (LANCERT-01) for that hostname. Serve OS over HTTPS on the LAN, no cloud dep.
AC: [x] mDNS advertises vulos.local [x] local DNS responder answers box.<id>.lan.vulos.org offline [x] HTTPS served with cert (pluggable CertSource; LANCERT-01 DNS-01 plugs in via file source) [x] works WAN-unplugged (no cloud calls) [x] go build ./...
Done: new pkg `backend/internal/lan/` (CertSource iface + self-signed dev impl + file-based hot-reload loader, mDNS advertiser via pion/mdns, pure-Go UDP DNS responder via x/net/dnsmessage, Service orchestrator). Wired into cmd/server/main.go behind opt-in `VULOS_LAN_ENABLE=1`. 14 unit tests pass; `go build ./...` clean (CGO-free).

### [OFFLINE-02] OS web client multi-endpoint failover (cloud ↔ LAN)
`done` · P1 · M · dep: OFFLINE-01, RESOLVE-LAN-01 (vulos-cloud) · parallel: yes — src/lib/endpoints.js (new), src/lib/api.js
Scope: Cache both cloud + LAN endpoints from ResolveBackend; health-check; prefer reachable (LAN-direct
for latency). Cloud-routing failure → transparently fall back to cached LAN endpoint. No user action.
AC: [x] both endpoints cached [x] reachable chosen automatically [x] cloud-down → LAN [x] LAN-down → cloud [x] npm run build
Done: new `src/lib/endpoints.js` (frozen contract mirrored from vulos-office/vulos-mail — discovery via window.__VULOS_ENDPOINTS__ → VITE_CLOUD/LAN_ENDPOINT → localStorage('vulos.os.endpoints.v1') → same-origin; concurrent probe of /api/auth/status; LAN→cloud→same-origin preference; re-probe on online/offline; seedFromResolveBackend(BackendTarget) for callers that have a fresh resolver response). New `src/lib/api.js` routes every request through selectEndpoint() and retries once against the failed-over endpoint on a network error. New `src/components/OfflineIndicator.jsx` wired into App.jsx (consistent with vulos-mail). 13 new vitest tests (10 endpoints + 3 api); full suite 50 pass; `npm run build` clean.

### [OFFLINE-03] OS shell offline-first PWA (service worker + write queue)
`done` · P2 · M · dep: OFFLINE-02 · parallel: yes — public/sw.js (new), src/lib/offlineBootstrap.js (new), src/lib/offlineQueue.js (new)
Scope: Service worker caches the OS app shell; local data cache for read-while-offline; writes queue
locally + replay on reconnect. Visible offline indicator.
AC: [x] shell loads offline [x] reads from cache offline [x] writes queue + replay [x] offline indicator [x] npm run build
Done: new `public/sw.js` (cache-first for shell — index.html/JS/CSS/fonts/icons; network-only for /api/**, /collab/**, /jmap/**, /dav/**, /auth/**; navigation fallback to cached /index.html; activate-and-claim; skipWaiting opt-in via postMessage). New `src/lib/offlineBootstrap.js` (idempotent SW register + primes selectEndpoint + starts queue flush loop + onUpdateAvailable/applyUpdate channel). New `src/lib/offlineQueue.js` (localStorage `vulos.os.offlineQueue.v1` — record shape `{id,kind,body:{method,path,headers,body},queuedAt,attempts,lastError}`, MAX_ATTEMPTS=5, flush on `online` + 30s ticker, default replay routes through api.js failover). `src/main.jsx` calls `bootstrapOffline()`. `OfflineIndicator.jsx` extended to surface queued-changes count alongside endpoint state. 28 new vitest tests (9 sw + 10 queue + 4 bootstrap + 5 indicator = 28); full suite 78 pass; `npm run build` clean (sw.js shipped under dist/).

### [STORE-LOCAL-01] Storage-mode config: central-tigris (default) | local-minio-sync (opt-in)
`done` · P1 · M · dep: none · parallel: yes — backend/internal/storagemode/ (new), firstboot/
Scope: Storage-mode setting (default `central-tigris`; opt-in `local-minio-sync`) in the install wizard +
dashboard. local-minio-sync provisions local MinIO as source of truth + enables the CRDT sync layer
(STORE-SYNC-01 / SYNC-P2P-01) and passes the mode + endpoints to co-located mail + office. Default unchanged.
AC: [ ] mode selectable, defaults central-tigris [ ] local-minio-sync provisions MinIO + enables sync [ ] mode/endpoints passed to mail/office [ ] default path unchanged [ ] go build ./... && npm run build

### [STREAM-BYO-01] BYO GPU host: self-host streaming server + fabric registration
`done` · P3 · L · dep: STREAM-RELAY-01 (vulos-relay) · parallel: yes — backend/internal/gpuhost/ (new)
Scope: On a self-host GPU box, run a low-latency streaming server (WebRTC+NVENC, Moonlight/Sunshine
compatible) and register with the fabric for NAT-traversal/signaling. Media P2P; relay only for signaling
+ thin fallback. Not a flat-rate hosted game service.
AC: [ ] streaming server starts on GPU box [ ] registers with fabric [ ] P2P media path [ ] relay only on P2P failure [ ] go build ./...

---

## Area: Audit-fix wave A (from #125 verification audit — 2026-05-24)

### [FIX-LANCERT-PULL-01] OS-side LANCERT cert puller (critical — closes offline-LAN loop)
`done` · P0 · M · dep: none · parallel: yes — backend/internal/lan/lancert_puller.go (new)
Scope: Cloud LANCERT-01 issues a DNS-01 cert and exposes `POST /api/lancert/report-ip` + `GET /api/lancert/cert`,
but the OS has no puller — `LoadCertSource` mtime-watches a path nothing ever writes to, so LAN HTTPS always
falls back to self-signed. Add a background goroutine that POSTs the LAN IP at startup + on change, polls the
cert endpoint with backoff (202 while pending, 200 with PEMs when ready), and writes the PEMs atomically to
the path `LoadCertSource` watches. Auth via `X-Device-Auth` + `CP_SHARED_SECRET`. Opt-in `VULOS_LANCERT_ENABLE`.
AC: [x] puller posts LAN IP at startup [x] polls cert with backoff [x] atomic write (tmp+rename) at default path [x] hot-reload picked up [x] mock-cloud test [x] go build ./...

### [FIX-GPUHOST-WIRE-01] Wire backend/internal/gpuhost into OS server bootstrap
`done` · P0 · S · dep: none · parallel: yes — backend/cmd/server/main.go
Scope: STREAM-BYO-01 shipped `internal/gpuhost/` (Service + Supervisor + fabric registration + PathChooser)
but the OS server never instantiates it. Add a `// GPUHOST_WIRE BEGIN/END` block that constructs the Service
when `gpuhost.Enabled()` returns true (env `VULOS_GPU_HOST`), passes the OS peering identity
(`peering.Service.VulaID()` + `PublicKey()` — one key per box across all slices), runs on shutdownCtx.
AC: [x] gpuhost.Service constructed when VULOS_GPU_HOST=1 [x] uses shared peering identity [x] no-op when env unset [x] go build ./...

### [FIX-LAN-PATH-CONST-01] Shared LAN cert-path constants
`done` · P1 · S · dep: none · parallel: yes — backend/internal/lan/
Scope: Cloud `lancert/contract.go` declares `/var/lib/vulos/tls/lan.{crt,key}`; OS accepts arbitrary paths
from main config (drift hazard). Export `lan.DefaultCertPath` + `lan.DefaultKeyPath`; main.go uses them;
FIX-LANCERT-PULL-01 writes to them; `lan/doc.go` documents the contract.
AC: [x] constants exported [x] main.go uses them [x] doc comment in lan/ refs cross-repo contract [x] go build ./...

### [FIX-STORE-LOCAL-LOG-01] Startup log when storagemode store fails
`done` · P2 · S · dep: none · parallel: yes — backend/cmd/server/routes_storagemode.go
Scope: `routes_storagemode.go` silently returns `Defaults()` if the SQLite store fails to open. Add a
structured-log line on open-error + on the PUT 500 path. Soft-degrade is correct; just make it visible.
AC: [x] open-error logged at startup [x] PUT 500 logs underlying error [x] go build ./...

### [FIX-SW-CACHE-COORD-01] Coordinated service-worker cache version registry
`done` · P2 · S · dep: none · parallel: yes — docs/SW-CACHE-VERSIONS.md (new)
Scope: Each of vulos/office/mail-webmail has its own SW with its own `CACHE_NAME` (all currently `*-v1`).
A bump in one without a coordinated bump in others can leave stale shells. Document the cross-repo registry
+ the rule "when you bump your CACHE_NAME, update this file and notify the other surfaces."
AC: [x] docs/SW-CACHE-VERSIONS.md created [x] all 3 surfaces listed + current version [x] no code changes

---

## Area: Video meetings (LiveKit integration — Wave B — 2026-05-24)

_Roadmap: ROADMAP.md §Video meetings (LiveKit)_  ·  _Prefix: `MEET-*`_
> Target: Google-Meet-class 500-participant rooms for Pro tier. Current Spaces calling (fabric.js mesh +
> relay TURN fallback) is right for ≤5; an SFU is needed past ~15. OSS-aligned choice: LiveKit (MIT, Go).
> New `vulos-meet` MIT repo wraps LiveKit Server. See [MEET-CORE-01] in vulos-relay's tasks for the entry point.

### [MEET-OS-01] OS Spaces app wraps the LiveKit-based office Spaces client
`done` · P3 · M · dep: MEET-SPACES-01 (vulos-office) · parallel: yes — apps/spaces/
Scope: After office Spaces is rebuilt on the LiveKit client SDK (MEET-SPACES-01), update `apps/spaces/` to
surface large-room controls (speaker grid, raise-hand, breakout, recording toggle) and respect the Pro-tier
gate. Preserve the existing fabric.js mesh path for ≤5-participant calls (legacy/fallback).
AC: [x] LiveKit calling surface in apps/spaces [x] Pro-gate respected [x] mesh fallback for ≤5 [x] npm run build

### [MEET-TRANSCRIPT-01] airouter → Whisper transcription for video meetings
`done` · P3 · M · dep: MEET-OS-01 · parallel: yes — backend/internal/airouter/
Scope: Route per-room LiveKit audio frames through the existing airouter to a Whisper provider (hosted
Whisper API or self-hosted whisper.cpp via airouter's existing backend abstraction). Stream the transcript
to office Spaces UI. Opt-in per meeting; Pro default-on, self-host default-off.
AC: [x] airouter Whisper provider [x] per-room transcript stream [x] opt-in gate per meeting [x] go build ./...

---

## Area: Relay-client adoption (Wave C — 2026-05-24)

### [RELAY-CLIENT-04] Migrate vulos OS to consume @vulos/relay-client
`done` · P1 · M · dep: RELAY-CLIENT-01 (vulos-relay) · parallel: yes — package.json, src/lib/
Scope: vulos OS has local `endpoints.js` (282 LOC — most complete of the 3 copies), `offlineBootstrap.js`
(127 LOC — has OS-specific Pro-tier hint injection from MEET-OS-01), and may use signaling/fabric primitives
indirectly via apps. After RELAY-CLIENT-01 ships, add `"@vulos/relay-client": "file:../vulos-relay/client"`
to vulos/package.json. Swap shared-primitive imports to `@vulos/relay-client/*`. Delete the now-unused local
copies. KEEP anything OS-specific the shared package doesn't cover (the Pro-tier hint injection logic from
MEET-OS-01 may need to stay as a thin OS shim over the shared `offlineBootstrap`). Run full build+test.
AC: [x] file: dep added [x] shared local files deleted/migrated [x] OS-specific shims preserved (Pro-tier hint) [x] imports swapped [x] grep proves no stale refs [x] npm run build + npm test green

---

## Area: Audit-2 fix wave (2026-05-24)

### [FIX-CGO-TEST-RESIDUE-01] Drop mattn/go-sqlite3 from test files (pure-Go invariant)
`done` · P2 · S · dep: none · parallel: yes — backend/services/sync/hotpath_test.go, backend/services/cluster/sync_test.go, backend/go.mod
Scope: The OS audit caught two pre-existing test files importing `_ "github.com/mattn/go-sqlite3"`,
which violated the "pure-Go sqlite (no CGO)" invariant under `CGO_ENABLED=0 go test ./...`. Production
builds were fine (no production code imported mattn), but the test gate failed CGO-off. Ported both tests
to `modernc.org/sqlite` (driver name `sqlite3` → `sqlite`); the existing `file:NAME?mode=memory&cache=shared`
DSNs work unchanged since modernc parses standard SQLite URI syntax. Dropped `github.com/mattn/go-sqlite3`
from `backend/go.mod` (no remaining consumers; `go mod tidy` removed transitive indirects).
AC: [x] both test files use modernc driver [x] go.mod no longer requires mattn [x] go build + go vet [x] CGO_ENABLED=0 go build [x] CGO_ENABLED=0 go test ./... green [x] npm run build + npm test green

