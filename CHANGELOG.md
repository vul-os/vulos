# Changelog

All notable changes to Vulos are documented in this file.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/) —
`Added` for new features, `Changed` for changes to existing behaviour,
`Deprecated` for soon-to-be removed features, `Removed` for removed features,
`Fixed` for bug fixes, `Security` for security improvements.

Versioning: [SemVer](https://semver.org/).

> **Numbering reset (2026-08-03).** `0.1.0` is the first release Vulos has ever
> tagged or published. Everything under *Pre-release development history* below
> was written during development and never shipped, so those numbers — including
> an earlier `0.1.0` and a `1.0.0`–`1.2.0` line — are demoted to plain headings
> rather than deleted. Vulos is deliberately pre-1.0: the roadmap still carries
> planned and design-only tracks, so interfaces may change. A `1.0.0` would
> promise a stability commitment the project is not ready to make.

---

## [Unreleased]

### Changed

- **Docs now say plainly that the published USB image is a live session, not
  an install.** The root filesystem is read-only with a RAM-only writable
  layer, so accounts, files, and settings do not survive a reboot on the
  currently published `.img.gz` — nothing previously said so. README.md,
  docs/GETTING-STARTED.md, docs/USER-GUIDE.md, and docs/ARCHITECTURE.md now
  distinguish the two supported ways to run Vulos on hardware you own:
  installing it (the primary path — persists like a normal computer, today
  via `./build.sh --deploy` or the self-host bundle; a dedicated bare-metal
  disk installer is in progress) versus trying it live from flash (available
  today, intentionally ephemeral).

## [0.1.0] - 2026-08-06

### Added

- **Native clients can authenticate a box by certificate pin** (`clients/core`).
  Trust is anchored to the SHA-256 of the certificate's SubjectPublicKeyInfo
  rather than the certificate itself, so the box re-mints its self-signed cert
  on every start — as it does — without invalidating a single paired client.
  `TLSConfig` refuses to return a config for an unpaired box, so no code path
  can hand back one that skips verification, and `Pair` authenticates against
  the fingerprint carried in the pairing payload rather than the one read off
  the wire. Discovery is not trust: a discovered box stays untrusted until
  paired.
- **A box can tell you its fingerprint.** `vulos -print-pairing` prints the box
  name, LAN address, human-readable fingerprint and `vulos://pair?…` payload for
  a user at the console or over SSH; `GET /api/lan/pairing` returns the same
  payload for the in-browser flow, behind the OS session gate. Both share one
  builder so they cannot drift. Without this, pinning had no way to start.
- **A console status screen.** A freshly flashed box previously booted to a bare
  `login:` prompt with no credentials configured — the one screen in front of
  the user said nothing about the address, the URL to open, or whether the
  server had started. tty1 now shows exactly that, refreshed every 15s, and
  probes the listeners rather than trusting `systemctl is-active` (which only
  proves the process exists). It grants no shell.
- **LAN HTTPS by default**, without a DNS server. Secure-context status depends
  on the scheme, not on whether the certificate is trusted, so the self-signed
  fallback is enough to make the platform work; a real certificate then only
  removes the warning. `VULOS_LAN_DNS_DISABLE=1` keeps the box from running a
  DNS responder on someone's home network uninvited.
- **`SHA256SUMS`** published with every release — there was previously no way to
  verify a 600 MB image at all.
- **Generated Go→TypeScript wire types** (`scripts/wiregen`, via `go/ast`), so
  the frontend's view of the API is derived from the Go declarations rather than
  hand-written and left to drift.


- **Reach — Vulos's own reachability stack.** A box behind NAT is now reachable
  from anywhere using only software in this repository: no third-party tunnel
  service and no dependency on an external relay project. See
  [docs/REACH.md](docs/REACH.md) and
  [docs/RELAY-SELF-HOST.md](docs/RELAY-SELF-HOST.md).
  - **`vulos relay serve`** — the relay is a ROLE of the same binary, not a
    separate product. Any Vulos install with a public IP can be one, so standing
    one up introduces no new vendor and no new supply chain.
    `vulos relay grant <name>` mints a grant with a fresh random token.
  - **Embedded agent.** The box-side tunnel agent runs IN the OS process and
    serves the OS's own `http.Handler` directly — no sidecar binary to install
    or supervise, and no loopback listener (and therefore no loopback SSRF
    surface). A tunnelled request runs the identical auth/session/CSRF/rate-limit
    /security-header chain as one on the box's own listener.
  - **Multi-relay by construction.** `VULOS_RELAY_ENDPOINTS_FILE` (preferred,
    mode 0600) or `VULOS_RELAY_ENDPOINTS` (inline JSON) configure a SET of
    relays, and the agent holds a live tunnel to every one at once — relay
    tunnels are affinity-bound, so a warm standby would not actually be
    redundant. Links fail independently with per-endpoint health and jittered
    backoff. The legacy `VULOS_RELAY_BASE_URL`/`_NAME`/`_TOKEN` form still works
    verbatim.
  - **Discovery role** (`-rendezvous`) — Ed25519-signed announce/resolve so two
    boxes in different houses find each other. **Wire-compatible with Ephor**: a
    test drives the real box-side client against this implementation, so the two
    can be mixed in one list.
  - **Automatic TLS without wildcard certificates.** The relay exposes a
    loopback `/tls-ask` gate answering from grants, so Caddy's on-demand TLS
    issues one certificate per box with no DNS-01 challenge and no DNS API
    credentials.
- **`GET /api/network/reach`** — session-authed reachability status (endpoint
  health, per-link state, public URLs). Token-free by construction.


- **Conduit homeserver, enabled.** The `conduit` registry entry (self-hosted
  Matrix homeserver) shipped `_disabled: true` pending a verified upstream
  checksum, since `famedly/conduit`'s GitLab releases remain source-archives
  only (no prebuilt binary). It now tracks
  [Continuwuity](https://continuwuity.org) instead — the actively-maintained
  community continuation of Conduit/conduwuit, which does publish signed
  prebuilt Linux binaries. `0.5.9`'s `conduwuit-linux-amd64` binary was
  downloaded directly and its sha256
  (`4189cd91086b0e46b6ab8b0b3677ccd4abfca6686e66915e1857a963430564de`)
  computed locally — no vendor-published digest exists to diff against — and
  boot-tested in a container matching the box runtime (fresh RocksDB store,
  admin room created, listening on `127.0.0.1:6167`) before being enabled.
  Switched from the old SQLite-era config shape to Continuwuity's RocksDB
  `database_path`, added the `liburing2`/`ca-certificates` runtime deps it
  needs, and restricted `arch` to `amd64` (the only binary verified so far).
  Re-signed via `make sign-registry` (55 entries, all verify) and republished
  via `make publish-feed`/`make verify-feed`. [docs/COMMS.md](docs/COMMS.md)
  drops the "self-hosting means running outside the App Store" caveat and
  adds a "Running your own homeserver" section.
- **App Hub search now matches registry keywords.** `RegistryListEntry` (the
  shape `GET /api/store/registry` returns) had no `Keywords` field even
  though `RegistryEntry.Keywords` was already modelled and signed — the data
  reached neither the API response nor the App Hub search box. Added
  `Keywords []string` to `RegistryListEntry`, populated it in
  `Registry.ListEntries` (`backend/services/appnet/registry.go`), and the App
  Hub search filter (`src/builtin/apphub/AppHub.jsx`) now matches a query
  against keywords in addition to name/description/id.
- **Comms answer: Element, Jitsi Meet, Element Call.** Founder ruling
  (2026-07-19): Vulos Talk and Vulos Meet are removed as first-party
  products; real-time chat and video are delegated to established,
  federated, self-hostable protocols instead. Three new signed
  `registry.json` App Store entries alongside the existing Cinny/Conduit
  Matrix entries: `element` (Element, Matrix chat/voice/video, Flatpak
  `im.riot.Riot`), `jitsi-meet` (Jitsi Meet video conferencing, Flatpak
  `org.jitsi.jitsi-meet`, joins any Jitsi deployment including public
  `meet.jit.si`), and `element-call` (Element Call, native Matrix group
  video calling/MSC3401, static web bundle with a pinned sha256 checksum —
  configure `static/config.json` post-install to point at your homeserver
  + LiveKit SFU). All 55 registry entries verify against the release key
  (`make verify-registry`, `make verify-feed`). New docs chapter
  [docs/COMMS.md](docs/COMMS.md) explains the reasoning, installing from
  the App Store, and how self-hosting a Matrix homeserver or Jitsi
  instance fits the sovereign-box story; cross-linked from APPS.md,
  CLOUD.md, ARCHITECTURE.md, PEERING.md, and docs/README.md.
- **Registry-as-feed, phase 1.** `registry.json` is signed but distributed
  as a single object, so a box previously had no way to detect a
  stale/rolled-back copy. Adds a signed, append-only feed alongside it
  (`backend/services/appnet/feed.go`, `registry-feed.json`) — publish-only
  and additive: it does not change `registry.json` or install-time
  verification. New targets `make publish-feed` / `make verify-feed`.

### Changed

- **The release now refuses to publish an image that does not boot** (BOOT-01).
  Nothing in CI had ever verified this: the existing live-boot job is green on
  every run because it *skips* — GitHub's runners have no QEMU. The new gate
  boots the exact artifact about to be uploaded, asserts it serves
  `/api/setup/status`, and asserts LAN HTTPS answers on the box's routable
  address rather than loopback. It runs before both publish steps, and unlike
  the old job it does not skip when QEMU is absent.
- **Generated wire types are now gated.** `gen:wire-types:check` already existed
  and worked, but ran in no workflow, no Makefile target and no hook, so a
  change to a Go response struct would leave the TypeScript view silently stale
  while every other gate stayed green.
- **The LAN TLS key now persists** across reboots, so accepting the self-signed
  certificate is genuinely a one-time action and future certificate pinning is
  possible. A world-readable key is treated as compromised and rotated.
- **`src/lib/` is TypeScript** (the security-critical client SDK), on TypeScript
  6 — 7 is the native compiler and `typescript-eslint` refuses to run against
  it, which would have silently dropped `react-hooks` linting from converted
  files.
- **Repository layout is now three peers**: `backend/` (Go), `frontend/` (web),
  `clients/` (native). `mobile/` became `clients/android`.


- **`VULOS_RENDEZVOUS_URL` accepts a comma-separated LIST**, and peer-reachability
  resolve now tries several relays in order. Each rendezvous entry becomes its own
  discovery source and a source that errors is skipped rather than failing the
  set, so listing two or three under different operators removes discovery as a
  single point of failure — the substrate spec's shape (KOTVA §4.2.1(3)). A single
  URL is a one-element list and behaves exactly as before.
- **The default relay provider is now `vulos`, not `ephor`.** Ephor remains a
  fully supported alternative — it speaks the same rendezvous contract, so
  selecting it is a genuine swap rather than a downgrade. Any unrecognised
  persisted provider name (including the old `"ephor"` default) fails safe to the
  new default exactly as before.
- **Ingress status reports what is TRUE.** `relayconfig.IngressInfo()` previously
  returned a hardcoded `https://relay.vulos.org` whether or not anything was
  running there, so Settings could show a confident "relay-tunnel" for a box that
  was in fact unreachable from the internet. It now reports the live tunnel state,
  or says plainly that no relay is configured.


- **ESLint cleanup + wired into CI.** An independent verification pass found
  `npm run lint` reporting 12 errors / 18 warnings — small enough to actually
  clear rather than defer. Fixed all 12 errors: an unused `req` param in an
  e2e route stub, a genuinely-unused `no-unused-vars`-flagged arg in
  `webPush.js` (documented inline, matching the repo's existing
  underscore-prefix-plus-disable-comment convention), and 10
  `react-refresh/only-export-components` hits in `src/auth/CloudSignIn.jsx`,
  `src/auth/GatewayChoice.jsx`, and `src/builtin/drive/Drive.jsx` — each of
  these files deliberately co-locates a component with plain helper
  functions/hooks it exports for direct unit testing (documented in each
  file's header comment); rather than a blanket rule disable, added
  file-scoped `allowExportNames` overrides in `eslint.config.js` naming the
  exact non-component exports. Of the 18 warnings, fixed 5: a dead
  `eslint-disable-line no-proto` (the rule isn't even enabled) in
  `appBridge.test.js`, and 4 real `react-hooks/exhaustive-deps` hits caused by
  un-memoized values recreated every render — `ShellProvider.jsx`'s
  `allWindows` array (now wrapped in `useMemo`, which also stabilizes
  `closeWindow`/`focusWindow`/`minimizeWindow` identities) and `Window.jsx`'s
  `applySnap` (now a stable `useCallback`), plus a genuinely-unused
  `resizeWindow` dep that fell out of that same callback. The remaining 13
  warnings are deliberate patterns, left as warnings rather than silenced:
  mount-only-fetch effects with intentionally empty deps (`Setup.jsx`,
  `FileManager.jsx`), effects/memos deliberately narrowed to specific
  primitive fields of a larger object to avoid over-firing (`Setup.jsx`
  `config`, `ShellProvider.jsx` `state`, `ThemeProvider.jsx`'s `tick`
  invalidation-signal trick), unmount-only cleanup effects with `[]` deps
  (`useMeshCall.js`, `useSFUCall.js`), forward-reference TDZ cases where the
  callback is declared before the helper it calls and adding the dep would
  crash the component at mount (`useSFUCall.js` ×2, `useVideoCall.js`,
  `Portal.jsx` — the same TDZ reasoning is already documented in-file for a
  sibling callback), and `FileManager.jsx`'s `goUp`, whose only real
  dependency (`cwd`) is already tracked. `npm run lint` now runs as part of
  the `frontend` CI job (`.github/workflows/ci.yml`), alongside the existing
  `npm run build` and `npm run test:e2e` frontend checks.
- **Docs: retired the last live-looking meethost/SFU-host references.**
  `docs/NETWORKING.md`'s "Hosting big calls: BYO SFU" section still described
  `VULOS_SFU_HOST`, `VULOS_SFU_ENDPOINT`, `VULOS_SFU_WORKER_BINARY`,
  `VULOS_SFU_REGION`, and `GET /api/meethost/status` as if they were live
  config — none of it exists in the code anymore (only a `// former Meet-SFU
  host registry` comment remains in `main.go`), so it read as a working
  feature. Replaced with an accurate description of the sovereign P2P
  Messages builtin's in-process Pion SFU (`/api/sfu/rooms/*`, small mesh cap,
  no host-registry escalation) and pointed to COMMS.md for third-party
  large-group video. Also dropped `docs/ARCHITECTURE.md`'s name-drop of the
  dead `internal/meethost` / `VULOS_SFU_HOST` identifiers (the retirement
  note itself was already accurate) and swapped the dead `[meethost]` log tag
  in `docs/TROUBLESHOOTING.md`'s grep list for the real `[gpuhost]` tag. This
  doc set is ingested by the public docs site build, so fixing it here is the
  actual fix for the stale copy found there.
- **Documented the Web Push / DMTAP Wake capability deviation.** The DMTAP
  substrate spec (`substrate/ROLES.md` §8, capability ⑤ Wake) defines wake
  pushes as strictly content-free — an opaque token, device pulls the real
  object afterward. `backend/internal/webpush` instead sends real
  RFC-8291-encrypted notification content (title/body/tag/url) directly to
  the vendor. Assessed this as a deliberate superset (the vendor still never
  reads plaintext either way; sending real content saves a round trip) that
  gives up the spec's fixed ciphertext-size metadata privacy (payload size
  now correlates with notification length) — no silent behavior change, no
  new mode added, just written down. Full rationale in
  `backend/internal/webpush/README.md`, cross-linked from
  `docs/ARCHITECTURE.md` and `docs/CLOUD.md`.

### Fixed

- **LAN HTTPS bound loopback on any box whose DHCP lease arrived late.** The
  address was resolved once at construction with no rebind, and the unit started
  on `network.target`, which fires when networking starts rather than when an
  address exists. Such a box served LAN HTTPS that nothing on the LAN could
  reach, permanently, while looking healthy — and a browser on a plain-HTTP LAN
  origin is not a secure context, so `crypto.subtle` is undefined and none of
  `src/lib` can run.
- **A wordlist entry containing the separator broke generated passphrases.** The
  EFF list has four hyphenated words and the default separator is `-`, so a
  three-word passphrase could split into four parts with an uncapitalised one
  among them (`Saloon-Hardcopy-Yo-yo`). Generation now draws from a wordlist
  filtered of any word containing the separator; the entropy cost is nil at the
  stated precision.
- **The live loader entry and `isLiveBoot()` disagreed.** The entry wrote a bare
  `vulos.live` while the code tests for `vulos.live=1`. Harmless today, since
  systemd is PID 1 on the live image, but a trap for anything later routing a
  live boot through `vulos-init`.
- **The OS image booted but never ran Vulos.** `vulos.service` invoked
  `vulos-server -env main`; `env.Parse` accepts only `local`/`dev`/`prod` and
  treats anything else as fatal, so the server exited immediately and
  crash-looped every 3s while systemd still reported "Started vulos.service"
  (`Type=simple` only means the process was forked). Every image before this
  one served nothing. Now `-env prod` — the only environment that binds all
  interfaces; `local`/`dev` bind loopback and would leave a box unreachable.
  Verified by booting the built image on both architectures and confirming
  `GET /api/setup/status` returns 200.
- **The live-boot smoke gate could not fail.** It passed on *either* a serial
  pattern or an HTTP probe, and the serial pattern (`login:`) always matches a
  successful boot — so the HTTP check its own comments called "the gold-standard
  fully-running check" never gated anything, which is how the above shipped.
  HTTP is now the only signal that can pass the gate.
- **`crypto.subtle` was undefined on the box's most likely address.** A browser
  on `http://<lan-ip>` is not a secure context, so `masterKey`, `contentSeal`
  and `offlineAuth` could not run at all. Those entry points now fail with a
  clear message naming both remedies instead of a bare `TypeError`.
- **Padding vanished wherever `safe-px` met a Tailwind `px-*` utility.** Both
  set the same property at equal specificity, and with no left/right inset
  (portrait, essentially every phone) the `env()` won at `0px` — content sat
  flush against the screen edge.
- **The phone status bar clipped its public-apps warning badge** at 390px. The
  date is now hidden below the `sm` breakpoint; the badge is the one element in
  that bar that must never be lost.


- **Data race and handshake desync in the tunnel control channel.** The yamux
  session was constructed before the relay finished writing its `ready` frame, so
  two goroutines could write the same WebSocket concurrently and a peer could read
  a binary frame where it expected the text handshake. The handshake now completes
  fully before the connection is handed to yamux. Caught by the package's own
  race-enabled tests.


- Stale "52 committed registry.json entries" comments in
  `registry_acceptance_test.go`, `registry_lossless_test.go`, and
  `docs/KEY-CEREMONY.md`, left over from before the three comms apps
  were added (registry is now 55 entries).

---

### Security

- **The allow-list of unauthenticated routes was never actually checked.** The
  test asserting `publicPaths` is exhaustive built its expected list and then
  only logged it. The two had drifted to 39 live entries against 18 expected —
  in both directions — and it stayed green throughout. Removing authentication
  from a route, the highest-consequence edit in that file, required no review.
  It now compares both directions and covers the path *prefixes* too, which are
  more dangerous still because they exempt an entire subtree.
- **Push is not equally sovereign on every platform, and now says so.** Apple
  requires all background push, Safari Web Push included, to transit APNs. The
  payload stays end-to-end encrypted, but the fact and timing of a push are
  visible to Apple regardless. Documented as the platform restriction it is.
- **Production signing ceremony run.** The App Hub registry and the OS trust
  chain are now signed with a production release key (root-anchored), replacing
  the development anchor. `make verify-registry-prod` — the release gate — now
  passes, so tagged releases build and publish flashable images again instead of
  halting. A one-command ceremony (`make ceremony`) generates the root + release
  keypairs, signs the cert and registry, installs the public trust material into
  `keys/`, and collects the private keys into an offline vault.


- **Header-trust boundary between relay and box.** The relay strips every
  `X-Vulos-Reach-*` header from inbound client requests — unconditionally and by
  prefix, so a header added later is covered automatically — then sets the ones it
  vouches for; the agent translates those into `r.RemoteAddr` and a synthetic
  `r.TLS` state and strips them again before any OS handler sees them. Without the
  translation every tunnelled client would share one rate-limit bucket and session
  cookies would lose `Secure` on exactly the requests that crossed the public
  internet. An unparseable vouched client IP **fails closed**.
- **A relay never runs open.** No grants configured is a startup refusal, not a
  permissive default: an open relay is an open proxy under the operator's own
  domain and certificate.
- **Revocation reaches ESTABLISHED tunnels**, swept every 20s. A working tunnel
  never reconnects, so a revocation applying only to new connections would leave a
  compromised box connected indefinitely.
- **Reconnect without hijack.** A re-registration presenting the same credential
  evicts the stale session immediately (so a rebooted box is not unreachable while
  a half-open TCP connection times out); one presenting a *different* credential is
  refused, even when both grants list the name.
- **Direct-endpoint ownership probe** — an advertised direct endpoint must echo a
  one-time nonce before the relay will publish it, so an agent cannot point clients
  at a third party. The probe is the relay's only agent-influenced outbound request
  and is SSRF-screened at connect time against the resolved IP (defeating DNS
  rebinding), refuses redirects, and is off by default.
- **Secret-bearing files must be mode 0600** — both the box's endpoints file and
  the relay's grants file are refused, not warned about, if world-accessible.
- **Uniform rejections.** Tunnel-registration and rendezvous-announce failures
  answer identically regardless of cause, so probing cannot reveal which part of a
  forgery to fix.

## Pre-release development history

The sections below were written during development. **None of them was ever
tagged or published** — `0.1.0` above is the first actual release. They are
kept for historical accuracy, with their version numbers demoted to plain
headings so they do not compete with released versions.

### 1.2.0 — 2026-07-17

#### Added

- **Streaming Chrome, restored.** A real Chromium instance running on the box,
  streamed to the shell over WebRTC with a **persistent per-user profile**
  (cookies/history/logins), launched on demand via `POST /api/browser/launch`
  (`backend/services/webbrowser/`). It ships **alongside** the client-side
  "Smart Browser" as a second, user-selectable launcher tile — pick per task.
- **Gaming mode for streamed apps.** Streaming now engages a low-latency gaming
  profile automatically, but **only for real games** — the launcher classifies
  the command (Wine / Lutris / Steam / steam-runtime) or an app manifest with
  `category: gaming` (`backend/cmd/server/gaming_detect.go`). Gaming uses
  full-frame capture, a zero-latency encoder profile (no B-frames/lookahead,
  CBR, 1-second GOP), and a minimal client-side jitter buffer (Chromium
  `playoutDelayHint = 0`). Ordinary desktop/GPU apps (e.g. Blender) keep the
  dirty-region, idle-throttled profile. Real latency/GPU behaviour is
  deployment-dependent.
- **Real instance rename/remove.** Multi-instance management endpoints make
  device rename and removal actually work (`routes_instances_manage.go`),
  replacing an invite flow that could not complete.
- **Live per-app resource usage.** The dashboard's per-app CPU/RAM figures are
  now served from live cgroup data (`internal/cgroups/governor_http.go`).

#### Changed

- **Setup wizard trimmed.** Dropped the post-signup wizard whose steps hit
  CP-only routes that a self-hosted box cannot serve.

#### Security

- **Cloud broker pubkey pinned at enrollment.** The cloud login-broker public
  key is now pinned when the box enrolls, instead of trust-on-first-use at first
  login (`services/cloudenroll/`).
- **Software keystore refused in cloud-managed mode.** A plaintext software
  keystore is rejected on cloud-managed boxes; those deployments must use a
  hardware-backed keystore (`internal/deploymode/`).
- **Per-user app-filesystem scoping.** The app filesystem sandbox is scoped
  per-user, not just per-app (`services/appfs/`), and storage presign/delete
  bind the `app_id` to the calling app's own secret (`services/gateway/`).
- **Instance-management authorization** enforced on rename/remove endpoints.
- **Honest stream auth reporting.** Stopped reporting passkey assertions that
  never actually happened in the stream WebAuthn gate.

#### Removed

- **Board (whiteboard sync) retired.** Deleted the dead `/api/board/token`
  HMAC-minting surface (`registerBoardRoutes`, `BOARD_AUTH_SECRET`) and its
  fail-closed table/env-var entries in `docs/SECURITY.md`. Whiteboards were
  already folded into Diwan as a first-class document type (routes, sidebar,
  thumbnails, E2E-encrypted P2P collab, CRDT persistence); this repo's Board
  route had no consumer — nothing called it, no `board-ui` exists, there was
  no Board data anywhere. Pure dead-code removal.

---

### 1.1.0 — 2026-07-07

The **sovereign assistant** release. Vulos gains an on-box AI agent that is
aware of your calendar, contacts, files, and reminders and can act on your
behalf — under a hard security contract: every side-effecting action is a
confirmation-gated *proposal*, egress is fenced by a tier-aware sovereignty
Guard, and the LLM runs through your own on-box gateway by default. Plus
one-click account portability, passkey clone/replay hardening, content-blind
file sharing, and a deep shell polish pass.

#### Added

- **Sovereign assistant — read-only awareness.** The agent can read your
  agenda and pending invites (calendar), look up contacts (`find_contact`),
  and find/read files (`find_file` / `read_file`) — all read-only,
  scoped to the signed-in user (`backend/services/assistant/`).
- **Sovereign assistant — reminders.** New reminders capability with an
  on-box poll scheduler that fires due reminders as notifications.
- **Proposal ledger + id-only execute gate.** Any action with side effects
  (create-event, add-contact, …) is returned as an opaque *proposal* recorded
  in a server-side ledger. Approving posts **only** the proposal id to
  `POST /api/assistant/execute` — never client-supplied arguments — so a
  compromised client cannot smuggle new parameters past the confirmation
  dialog. Rejecting sends nothing.
- **Tiered sovereignty + egress Guard.** A tier-aware egress Guard fences what
  the assistant may send off-box; the shell shows an honest tier badge and
  picker so the user can see and choose their sovereignty level.
- **On-box LLM gateway (llmux) routing.** Opt-in routing of assistant LLM/
  embeddings traffic through the on-box `llmux` sovereign gateway
  (`backend/internal/llmuxclient/`); canonical env var `LLMUX_URL`
  (`VULOS_LLMUX_URL` also accepted).
- **Streaming assistant turns (SSE).** The agentic turn streams live tokens
  over Server-Sent Events for real-time answers.
- **Sovereign semantic mail RAG.** On-instance embeddings + vector index over
  mail power the assistant's retrieval, wired to the real lilmail `/v1` API.
- **Proactive AI Home surface.** The desktop opens as a home (agenda, focus,
  proposals), not just a launcher; unified OS `⌘K` command palette.
- **Real notifications system** (`backend/services/notify/`) with settings
  depth, plus a full keyboard cheat-sheet and window-control commands.
- **Window tiling & session depth** — snap/keyboard geometry, dock/taskbar
  with running-app indicators, persisted window sessions.
- **Export my data (account portability).** A user-facing "Export my data"
  flow packages the account's data for portability off the box.
- **Content-blind file sharing (VSEAL).** Sealed folders, sealed metadata, and
  content-key lookup complete the client-crypto file-share model
  (`backend/services/files/`); share-by-email with locality routing.
- **Legible-trust surface.** Visible, provable sovereignty indicators in the
  shell; forced recovery-phrase signup with client-side master-key unwrap.
- **Tier-2 active-session password reset** that preserves zero-access.
- **Peering key lifecycle** — VulaID rotation/revocation, account-anchored
  recovery, X3DH-style forward secrecy for message content, per-sender
  one-time-prekey claim, and real Nitro `COSE_Sign1` attestation verification.
- **Board/whiteboard integration** — embedded board surface gated by
  `BOARD_AUTH_SECRET` (fails closed in prod when unset).

#### Fixed

- **Passkey clone/replay (AUTH-13).** Closed a WebAuthn signature-counter
  clone-detection gap; added a virtual-authenticator test harness that closes
  the OS passkey/WebAuthn coverage gap.
- Prompt-injection hardening for untrusted mail inside the agent loop — mail
  text can no longer inject tool calls or leak as tool arguments.
- Align assistant create-event / add-contact payloads and `MessageInvite`
  JSON tags to the real lilmail `/v1` wire shape.
- `appnet` fails closed when a proxy-config write fails; `stream` arms the
  AUTH-13 input-injection gate safely.
- Redact email-verification token from production peering logs.

#### Security

- Default-deny attestation policy with fail-closed Nitro/noop verifiers;
  Ed25519-signed peer profiles verified against the Vula ID.
- Per-document ACL enforced on inbound CRDT and WebSocket collab join;
  fail-closed on no-envelope inbound with WS authz bound to an un-spoofable
  identity.
- Adversarial security-review passes over the new assistant capabilities
  (calendar, files, reminders) and expanded HTTP-route + registrar coverage
  (join/joincode/files/aiapps, notify, assistant execute).

#### Changed

- Files ACL role hierarchy is **viewer < editor < owner**, enforced
  server-side on every share and collab join.
- Deep UI/UX polish across the shell — assistant Home, `⌘K`, notifications,
  transparency/trust surfaces, Setup/Drive papercuts, accessibility and mobile.

---

### 1.0.0 — 2026-06-16

Milestone release. First feature-complete, security-hardened Vula OS merged to
`main`: email/password + passkey/2FA auth (no third-party OAuth), GPU-accelerated
streaming with adaptive bitrate/resolution and idle/peer-aware encoder lifecycle,
leaderless multi-instance CRDT sync with signed quorum, P2P WebRTC mesh,
rehydration + instance migration, per-account storage selection, anchor inbox,
and the headless `vulos-managed` cloud box image. Mail is fully separated
(lilmail client + vulos-mail server are independent repos). Note: GPU-streaming
and cloud/infra paths are implemented and unit-tested but await verification on
real hardware/live services.

#### Fixed

- Wire `RegisterAnchorHandlers` in `main.go` — ANCHOR-01 routes
  (`POST /api/anchor-inbox/provision`, `GET /api/anchor-inbox/status`) were
  implemented but never mounted on the mux; they now register correctly
- Fix URL mismatch in `src/core/settings/StoragePanel.jsx` — the component
  called `/api/settings/storage` but the backend registers `/api/storagemode`;
  URLs now match so the Storage settings panel loads correctly
- Wire `SelfDisplayName` callback on `ContactAPI` — peer approval notifications
  now include the local user's display name (populated from `profile.json`)
  rather than always sending an empty string
- Add `aria-label` to icon-only buttons in `Launchpad.jsx` (clear-search `×`
  and app tiles) for screen-reader accessibility

---

### 0.2.0 — 2026-06-15

#### Security

- Admin-gated 35 privileged endpoints across `backend/cmd/server/` — system
  mutation routes (networking, energy, exec, process control, sandbox) now
  require an authenticated admin session; previously accessible to any
  authenticated user
- IDOR fixes across mission, profile, and peering endpoints
  (IDOR-MISSION-01): owner or admin only for read/write/cancel
- Command-injection fix in firstboot hostname validation — input is now
  validated against `[a-zA-Z0-9\-]{1,63}` before being passed to shell
  wrappers
- SSRF blocking on `POST /api/open` — resolves host IPs and rejects loopback,
  RFC 1918, link-local, and cloud-metadata ranges; fail-closed on resolution
  error
- Rate-limit cap on `/api/open` (10 concurrent requests, `SEC-H H6`)
- CRDT-QUORUM-01 fixed and regression-tested: per-instance Ed25519 quorum
  signing prevents forged-origin uninstall attacks; observation-set GC closes
  re-quorum-after-reinstall vector

#### Added

- Passkeys (WebAuthn/FIDO2) as primary login method — full registration +
  assertion ceremony (`backend/services/passkeys/login.go`,
  `src/auth/PasskeyButton.jsx`); private key never leaves the authenticator
- QR / phone-approval login for kiosk and shared clients
  (`backend/services/passkeys/qrlogin.go`, `src/auth/QRLogin.jsx`)
- Attacker-style pentest suite (`backend/security/`) — 28 top-level tests,
  45 including sub-cases, covering LAN cert MITM, fabric CRDT injection,
  SSRF, multi-instance provisioning auth, and quorum forgery
- OAuth security test suite (`backend/cmd/server/oauth_security_test.go`)
- Token vault / credvault base (`backend/services/credvault/`) for
  server-side encrypted credential storage
- Passkey login test coverage
  (`backend/services/passkeys/passkeys_security_test.go`,
  `passkeys_l1_test.go`)
- Router-level test coverage (`backend/cmd/server/routes_router_test.go`)
- Quorum security test suite
  (`backend/internal/multiinstance/quorum_security_test.go`)

#### Changed

- Auth model clarified: email + password + 2FA/TOTP baseline; passkeys
  (WebAuthn) primary for new accounts; QR/phone-approval for kiosk clients.
  **No Google OAuth or third-party identity providers.**
- Mail is fully separated: **LilMail** is the bundled default IMAP/SMTP
  webmail client (external repo); the mail server is the separate
  **vulos-mail** repository. No mail code lives in this repo.
- Browser is **host-browser native** — `POST /api/open` returns an
  open-in-host-browser instruction; no server-side streamed Chromium session.
  The `services/webbrowser` package and streaming-only `xvfb`/`chromium`/
  `xdotool` packages have been removed.
- P2P WebRTC mesh is the video-calling model — browser-to-browser, servers
  handle signaling only. Pion SFU for groups of 5+. LiveKit/SFU cloud scale
  was evaluated and removed from scope.
- OAuth BFF / connected-accounts (LOGINISO-03) descoped — won't-do; Vulos
  identity is self-contained.

#### Removed

- `backend/services/webbrowser/` — server-side Chromium streaming (BROWSER-02)
- `backend/services/isolatedbrowser/` — Isolated/Disposable Browsing (RBI)
  stub removed
- `vulos-relay` mail-delivery daemon — retired; circuit relay
  (`vulos-cloud/backend/circuit`) handles WebRTC TURN, not mail delivery
- Connected Accounts panel (`src/core/settings/ConnectedAccountsPanel.jsx`)
  and OAuth provider routes — no OAuth in Vulos

---

### 0.1.2 — 2026-05-26

#### Added

- Native-first re-architecture (v8): Open Router dispatch lanes, host-browser
  native browsing, GPU route (BYO peer), streaming efficiency wins
  (STREAMWIN-01–05), web-app curation
- Same-LAN P2P CRDT sync (FABRIC-P2P-01): mDNS discovery + authenticated
  HTTPS fabric sync (`backend/internal/fabric/`)
- OS distribution: signed squashfs A/B updates, dm-verity, netboot
- Multi-instance sync: cr-sqlite CRDT hot/cold path, snapshot/compaction,
  bucket-backed lease coordination
- S3/Restic backup and restore (Compactor + Restorer) wired to CLI and admin
  HTTP entrypoints
- Fabric key rotation/revocation + key-at-rest encryption, restore-from-S3,
  IndexedDB queue, conflict UX
- CRDT-QUORUM-01 fix (distinct-origin uninstall quorum + OS pentest suite)

---

### 0.1.1 — 2026-05-10

#### Added

- Peering: Ed25519 identity, signed canonical-JSON envelopes, server-to-server
  messaging, media transfer, WebRTC voice/video signaling, Drop (AirDrop-style)
- Multi-user with per-user Linux accounts and profile isolation
- AI Router: Ollama default, multi-provider (Claude, OpenAI, OpenAI-compatible),
  chat history, embeddings, sandbox Python execution

#### Fixed

- Live-USB bootable ESP fix (BMINIT-14)

---

### 0.1.0 — 2026-04-18

#### Added

- Initial public release
- Web-native window manager (React 19, Tailwind CSS 4, Vite)
- Go backend — single binary, 24 services, 110+ API endpoints
- GStreamer/WebRTC streaming for native Linux GUI apps and games
- App store with apt/Flatpak recipes, isolated network namespaces
- Bare-metal image builder (`build.sh`) producing signed squashfs images
- Docker image for `linux/amd64` and `linux/arm64`
- CI (build, vet, test, gofmt, Docker) and release pipeline (tag-triggered)

[Unreleased]: https://github.com/vul-os/vulos/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/vul-os/vulos/releases/tag/v0.1.0
