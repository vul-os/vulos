# Vulos OS — Task Backlog

**Status: All 31 tasks in this file are `done`. 235 legacy tasks also `done`.**

BMINIT-14 resolved. Four feature tracks (AIROT, IDENTITY, PUBWEB, MINST) added and 30/31 complete.
See ROADMAP.md §§ 1–3, 7 for full context.

> **Stack invariants (FROZEN):** Go backend; pure-Go `modernc.org/sqlite` (never CGO);
> JSX-only frontend (NEVER `.tsx`); cage v1 / labwc v2 (D93); no Rust. Auth: email+password
> + TOTP; no Google OAuth. Cloud control-plane lives in the separate vulos-cloud repo; all
> OSS tracks below must work without it (cloud is an optional accelerator).

---

## At-a-glance

| Area | Roadmap section | Done / Total | Progress |
|---|---|---:|:---|
| BMINIT legacy | ROADMAP.md § Boot, Init & Bare Metal | 1 / 1 | `[██████████]` 100% |
| AI Router | ROADMAP.md § AI Router | 8 / 8 | `[██████████]` 100% |
| Vulos Mail Identity | ROADMAP.md § Identity — Vulos Mail | 7 / 7 | `[██████████]` 100% |
| Public Webapps | ROADMAP.md § Public Webapps | 8 / 8 | `[██████████]` 100% |
| Multi-Instance Routing | ROADMAP.md § Multi-Instance | 7 / 7 | `[██████████]` 100% |

| **Open total** | | **0 / 31** | `[██████████]` 100% |

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

