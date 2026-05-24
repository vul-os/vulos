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
| Office Suite (merged) | ROADMAP.md § Office Suite | 35 / 35 | `[██████████]` 100% |

| **Open total** | | **0 / 31** (+ 35 Office Suite done; 6 Office Suite future/open) | `[██████████]` 100% |

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

## Area: Office Suite (merged from vulos-office → `office/`)

_Roadmap: ROADMAP.md §Office Suite_  ·  _Prefix: `OFFICE-`_  ·  _Subtree: `office/` (nested Go module `vulos-office`)_

> Merged from the standalone vulos-office repo. **Status: 35 / 35 office tasks `done`** —
> Office Core, Real-time Collaboration (CRDT + fabric), PDF Auto-Sign, and Vulos Spaces are all
> shipped. All paths below are relative to the `office/` subtree. The frontend embeds into the
> Go binary; `cd office && go build ./... && npm run build` is the universal gate.
> Storage-backend / co-location / bundling for the suite is governed by the OS-wide
> §"Storage backend, multi-location & co-location" area above (STORE-*, BUNDLE-01, ANCHOR-01);
> the two office-side tasks `OFFICE-STORE-01/02` below are the office counterparts referenced
> by `BUNDLE-01` — they are kept once here (not re-documented in the OS storage area).

### Office Core (OFFICE-01…11)
- **[OFFICE-01]** `done` — Documents editor (TipTap rich text) — `office/src/apps/docs/`
- **[OFFICE-02]** `done` — Sheets editor (Fortune Sheet grid) — `office/src/apps/sheets/`
- **[OFFICE-03]** `done` — Slides editor (Reveal.js) — `office/src/apps/slides/`
- **[OFFICE-04]** `done` — PDF annotate + sign canvas (single-user) — `office/src/apps/pdf/PDFEditor.jsx`
- **[OFFICE-05]** `done` — Import/Export pipeline (docx/xlsx/pptx/pdf/md) — `office/src/lib/importFile.js`, `office/src/apps/*/`*Export.js`
- **[OFFICE-06]** `done` — Storage backends (local JSON + PostgreSQL) + file CRUD API — `office/backend/storage/`, `office/backend/handlers/files.go`
- **[OFFICE-07]** `done` — Optional password auth (JWT) + single-binary embed — `office/backend/handlers/auth.go`, `office/main.go`
- **[OFFICE-08]** `done` — Local document version history + snapshots — `office/backend/storage/`, `office/backend/handlers/files.go`
- **[OFFICE-09]** `done` — Crash-safe autosave + offline write recovery — `office/src/store/filesStore.js`, `office/src/lib/api.js`
- **[OFFICE-10]** `done` — PDF page operations (reorder/insert/delete/rotate) — `office/src/apps/pdf/PDFEditor.jsx`
- **[OFFICE-11]** `done` — Import/export fidelity hardening — `office/src/lib/`, `office/src/apps/*/`*Export.js`

### Real-time Collaboration (OFFICE-20…28) — CRDT over the Vulos fabric
- **[OFFICE-20]** `done` — Fabric client adapter (P2P data channel + relay/TURN fallback) — `office/src/lib/fabric.js`, `office/src/lib/signaling.js`
- **[OFFICE-21]** `done` — CRDT document core + bucket sync (text/grid/tree) — `office/src/lib/crdt/`
- **[OFFICE-22]** `done` — Wire Docs editor to CRDT collaborative session — `office/src/apps/docs/DocsEditor.jsx`, `office/src/lib/crdt/text.js`
- **[OFFICE-23]** `done` — Wire Sheets + Slides editors to CRDT sessions — `office/src/apps/sheets/`, `office/src/apps/slides/`, `office/src/lib/crdt/`
- **[OFFICE-24]** `done` — Presence roster (who's here) — `office/src/lib/presence.js`, `office/src/components/PresenceBar.jsx`
- **[OFFICE-25]** `done` — Live cursors + selections — `office/src/apps/docs/DocsEditor.jsx`, `office/src/apps/sheets/SheetsEditor.jsx`
- **[OFFICE-26]** `done` — Comments (anchored, threaded, resolvable) — `office/src/lib/crdt/comments.js`, `office/src/components/CommentsPanel.jsx`
- **[OFFICE-27]** `done` — Suggestion (track-changes) mode — `office/src/apps/docs/DocsEditor.jsx`, `office/src/lib/crdt/suggestions.js`
- **[OFFICE-28]** `done` — Document activity feed + named snapshots from op-log — `office/src/lib/crdt/index.js`, `office/src/components/HistoryPanel.jsx`

### PDF Auto-Sign (OFFICE-40…47) — e-signature + cryptographic audit trail
- **[OFFICE-40]** `done` — Signing data model + backend store — `office/backend/models/signing.go`, `office/backend/storage/`
- **[OFFICE-41]** `done` — Field-placement editor (assign fields to signers) — `office/src/apps/pdf/PDFEditor.jsx`, `office/src/apps/pdf/SigningSetup.jsx`
- **[OFFICE-42]** `done` — Signing-link generation + scoped signer view — `office/backend/handlers/signing.go`, `office/src/apps/pdf/SignView.jsx`
- **[OFFICE-43]** `done` — Signer ceremony (draw/type/upload + submit) — `office/src/apps/pdf/SignView.jsx`
- **[OFFICE-44]** `done` — Cryptographic token + tamper-evident audit trail (Ed25519, hash-chained) — `office/backend/signing/crypto.go`, `office/backend/handlers/signing.go`
- **[OFFICE-45]** `done` — Multi-signer orchestration + reminders — `office/backend/handlers/signing.go`
- **[OFFICE-46]** `done` — Completion certificate + sealed PDF — `office/backend/handlers/signing.go`, `office/src/apps/pdf/SignView.jsx`
- **[OFFICE-47]** `done` — Signature + audit verification tool — `office/backend/signing/crypto.go`, `office/src/components/VerifyView.jsx`

### Vulos Spaces (OFFICE-60…66) — Slack + Meet equivalent on the fabric
- **[OFFICE-60]** `done` — Spaces data model + message store (CRDT-synced) — `office/backend/models/spaces.go`, `office/src/lib/crdt/messages.js`
- **[OFFICE-61]** `done` — Channels + DMs + threads UI — `office/src/apps/spaces/`
- **[OFFICE-62]** `done` — Presence + status for Vulos Spaces — `office/src/lib/presence.js`, `office/src/apps/spaces/ChannelView.jsx`
- **[OFFICE-63]** `done` — 1:1 + group voice/video calling (WebRTC P2P + relay/TURN fallback) — `office/src/lib/call/rtc.js`, `office/src/apps/spaces/CallView.jsx`
- **[OFFICE-64]** `done` — Screen-share in calls — `office/src/lib/call/rtc.js`, `office/src/apps/spaces/CallView.jsx`
- **[OFFICE-65]** `done` — Scheduled meetings + meeting rooms — `office/backend/handlers/meetings.go`, `office/src/apps/spaces/Meetings.jsx`, `office/src/apps/spaces/Room.jsx`
- **[OFFICE-66]** `done` — In-call chat tied to channel/thread — `office/src/apps/spaces/CallView.jsx`, `office/src/lib/crdt/messages.js`

### Office Suite — Future / open

### [OFFICE-MULTI-01] Multi-target builds (web subdomain + OS-embed library) for all app surfaces
`todo` · P2 · L · dep: none · parallel: no — office/vite.config.*, office/package.json, office/src/apps/*/lib.jsx
Scope: Vite multi-entry config builds each app (docs, sheets, slides, spaces, calendar, meet) as both a standalone web bundle (subdomain serving) and an embeddable `lib.jsx` export (OS shell wrapper). The multi-target build (`build:all`) already produces dist/dist-lib/dist-office/dist-talk/dist-calendar/dist-meet; extend coverage and coordinate with the vulos-cloud subdomain routing pipeline. The OS-embed library exports a single React component per app.
AC: [ ] `cd office && npm run build:all` produces both web and lib outputs [ ] lib.jsx exports a single component per app [ ] web output deployable to app-specific subdomain [ ] no .tsx files introduced

### [OFFICE-DEEPLINK-01] Deep-link routing per app surface
`todo` · P2 · M · dep: none · parallel: yes — office/src/App.jsx
Scope: Canonical deep-link routes for each app surface: `vulos-office://docs/{id}`, `vulos-office://meet/{roomId}`, `vulos-office://calendar/{eventId}`, etc. `office/src/App.jsx` router handles both web-subdomain URL patterns and the OS deep-link scheme. Coordinate with OFFICE-MULTI-01 and the OS app-wrapper tasks.
AC: [ ] deep-link URLs for docs/sheets/slides/spaces/calendar/meet defined [ ] App.jsx routes resolve them [ ] OS launcher links tested against the routing table [ ] `cd office && npm run build`

### [OFFICE-STORE-01] Storage-backend config injection: accept Tigris or MinIO endpoint
`todo` · P1 · S · dep: OFFICE-06 · parallel: yes — office/backend/config/config.go, office/backend/storage/storage.go
Scope: Ensure the office backend accepts the storage backend endpoint + credentials from its startup configuration (env vars or config file) and passes them to the storage interface at init. No logic in office selects between Tigris or MinIO — it receives the endpoint (consistent with the OS-wide STORE-BYO-01 selector and BUNDLE-01 installer). Document the two config shapes (Tigris vs MinIO-local) in `office/docs/INSTALL.md`. Add a startup log line confirming the endpoint in use.
AC: [ ] Tigris endpoint config accepted + logged at startup [ ] MinIO-local endpoint accepted + logged [ ] storage interface uses injected endpoint [ ] no endpoint-selection logic in office source [ ] `cd office && go build ./...`

### [OFFICE-STORE-02] Co-location documentation: running with OS + mail on one box
`todo` · P3 · S · dep: none · parallel: yes — office/docs/INSTALL.md
Scope: Document co-located deployment: office running alongside OS and vulos-mail on a single instance, sharing one bucket endpoint. Include shared config variables, systemd unit ordering (office after vulos-mail), and a note that the meta-bundle installer (`BUNDLE-01` above) automates this setup. Markdown only; no code changes.
AC: [ ] `office/docs/INSTALL.md` covers co-location with OS + mail [ ] shared storage config documented [ ] reference to BUNDLE-01 included [ ] `cd office && go build ./...` unaffected

### [OFFICE-BYO-01] OS installer hook: install office alongside vulos-mail for Starter+
`in-progress` · P2 · M · dep: none · parallel: yes — office/docs/INSTALL.md
Scope: Document the OS install-wizard integration point: when a Vulos OS user selects Starter or higher, the wizard installs office (Docs, Sheets, Slides, Spaces, Calendar) as a built-in service alongside vulos-mail. Doc + install-script integration only; no office source change. Coordinate with the vulos-mail MAIL-BYO-04 bash installer and the OS-side BUNDLE-01.
AC: [ ] INSTALL.md documents office install alongside vulos-mail for Starter+ [ ] install hook point documented for OS wizard team [ ] no .go or .jsx changes [ ] `cd office && npm run build` passes unmodified

### [OFFICE-BYO-02] Pricing copy verification: Office bundled from Starter
`in-progress` · P3 · S · dep: none · parallel: yes — office/ (doc only)
Scope: Verify all user-facing copy in `office/README.md`, ROADMAP.md, and `office/docs/` reflects that Office is bundled from Starter and up — no standalone Office tier. Fix any copy that implies a standalone Office tier.
AC: [ ] copy mentions bundling from Starter [ ] ROADMAP §Office Suite present [ ] no copy implies standalone Office tier [ ] `cd office && npm run build` passes unmodified

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

