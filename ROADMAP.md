# Vulos OS — Roadmap

Vulos is a web-native personal operating system. The full desktop — window manager, dock, every app — renders in a browser, so it reaches any device (phone, TV, car, laptop) without a separate app. The backend is Go; the shell is React/JSX. The OS is fully self-hostable, open-source, and forkable.

---

## 0. North Star — native-first dispatch

**The most efficient compute is the browser already on the user's device.** The Vulos shell already runs in a real browser in every deployment mode (cage/WPE-WebKit kiosk on bare metal; the user's device browser on remote access). A server-side streamed browser is therefore redundant and strictly worse, and most "apps" can run as web apps in that host browser at near-zero server cost.

Vulos routes every launch through one of five **dispatch lanes** (the Open Router — `backend/services/openrouter/`):

| Lane | Workloads | Path | Server cost |
|---|---|---|---|
| **Web app** (host browser) | office, mail, PDF, terminal, notes, code, photos, image-edit, vector, diagrams, media, **kerf CAD**, browser | native in host browser (window/tab or in-shell web view via subdomain proxy) | ~zero |
| **CPU app stream** (default, w/ fallback) | light native Linux GUI apps with no web form | Xvfb + SW-encode + WebRTC | low |
| **GPU route** (BYO peer now, cloud later) | **games, Blender, heavy video edit** | stream from a GPU-capable Vulos peer | paid / BYO |
| **Compute worker** (batch, non-interactive) | heavy CAD solves (FEA, big regen) | kerf worker; results → browser | per-job |
| **Local-only** | full DAWs (live recording) | bare metal / same machine | n/a |

Settled invariants:
- **Browsing is native, never streamed.** No server-side streamed browser; the host browser does all web content. See §11.
- **GPU access is peer-based.** A BYO GPU host is a full Vulos peer. GPU-capable apps default to CPU-stream with fallback; games need a GPU. Cloud GPU is a later, metered add-on — launch cost is $0 (sidesteps the Fly GPU deprecation, Aug 1 2026). See §11.
- **Login isolates the credential, not the browsing.** Passkeys + a server-side token vault, never a streamed login. See §12.
- **One uniform isolation model:** a Firecracker microVM per tenant on Fly Machines, scale-to-zero. See §18.
- **CAD and DAW are separate tools**, parallel to the OS — designed but not built into the OS. See `roadmap/CAD-KERF.md` and `roadmap/REALTIME-AUDIO-DAW.md`.
- **Remote Assist** (TeamViewer-class: co-presence/shared cursors, screen view, remote control, and delegated *temp* profile access) is **part of the OS, not a separate repo** — it composes the existing WebRTC session-stream lane, per-profile isolation, and the capability-grant/gateway-auth model. Capability-first, consent-visible, time-boxed, fail-closed. Designed, not built. See `roadmap/REMOTE-ASSIST.md`.

---

## 1. Multi-Instance & Account Routing

**The core value of Vulos is networking your instances together.** One account routes many instances — BYO bare-metal, Raspberry Pis, cloud VMs. Vulos makes them behave as one coherent system.

Every instance generates a ULID at first boot. The canonical address is `*.{ulid}.vulos.org` (or your own domain). Four connection modes are supported: Vulos fabric (Ziti, no open ports), direct (public IP + acme-dns), own domain, and local-only — all switchable post-install without reinstalling. LAN mode (mDNS, `vulos.local`) is always active regardless of which mode is selected.

Sessions sync via cr-sqlite CRDTs, so any instance in your cluster can serve any login — sticky-until-failure routing, leaderless, no split-brain. Multi-location liveness is by design; impossible-travel heuristics must not flag it.

The subdomain scheme is `{app}--{profile}.{ulid}.vulos.org`. A wildcard TLS cert covers all apps on an instance. Each profile gets isolated network namespaces and session-scoped cookies.

**Outstanding:** optional Vulos-provisioned cloud instances (Fly.io — users can add managed cloud nodes alongside their BYO hardware); geo-routing control-plane advisor; fleet dashboard for multiple instances per account.

---

## 2. Identity — Vulos Account (decoupled from mail)

At install/onboarding every user creates or claims a **Vulos account** — a handle-based identity (`@vulos.net`) that is the account's persistent, portable anchor across the Vulos ecosystem. **Identity is decoupled from mail.** You do not need a mailbox to have an account (a free account needs only email/OAuth); authentication is passkeys + TOTP + recovery codes + a backup email, never a mail-server login. A mailbox is an optional, separately-billed add-on (see the billing section), not the account anchor.

The account identity cross-references:
- **Mail is a connector, not a service the OS runs.** The bundled inbox (**LilMail** + `@vulos/mail-ui`, see `docs/MAIL-LILMAIL.md`) connects to whatever mailbox the user already has — Gmail, Outlook, or any IMAP/SMTP account. The Vulos-hosted mail engine (`vulos-mail`, OSS, separate repo) is **dormant/experimental** — resurrectable, but not a primary on-box mail server and not the identity anchor.
- **circuit relay** (`vulos-cloud/backend/circuit`) — media/TURN PoP for WebRTC NAT traversal; not a mail transport. (The retired `vulos-relay` mail-delivery daemon is distinct and no longer in use.)

The account identity also anchors the Ed25519 peering keypair (PEERING.md), so one identity covers messaging, notifications, peering, and (optionally) a connected mailbox — no separate accounts.

**Outstanding:** Vulos account provisioning in the first-boot wizard; keep the peering identity verification flow decoupled from any mailbox (use the account's backup email).

---

## 3. AI Router (OS-Level)

The OS contains a built-in model-API router so every AI feature — Notes semantic indexing, Smart Browser summaries, document generation, mission orchestration, proactive alerts — works without manual setup.

**Two modes, same model choice:**

| Mode | Key source | Setup |
|---|---|---|
| **BYO / self-host** | User enters provider keys in Settings → AI | Keys stored locally, never leave the device |
| **Cloud / zero-setup** | Keys held server-side, billed through the Vulos account | OS authenticates with the account session; no local key needed |

Model choice is preserved in both modes. The router layer (LiteLLM as sidecar or embedded proxy) normalizes Ollama, Claude, OpenAI, and any OpenAI-compatible endpoint behind one API surface. The UI layer uses Vercel AI SDK for streaming.

Every AI surface in the OS — the Cmd+K chat (Portal), app empty/error states, right-click context menus, the dock AI button, Recall embeddings — calls the router. Apps do not connect directly to providers.

**Current stack:** custom Go AI service, Ollama default, multi-provider with streaming, chat history, embeddings, sandbox Python execution for AI-generated apps, multi-step missions, proactive agent. All existing tasks shipped.

**Outstanding:** wire AI router to cloud billing path; enforce model-choice preservation across mode switches; LiteLLM evaluation.

---

## 4. Boot, Init & Bare Metal

Vulos boots from a signed, immutable squashfs image. The same image boots a VM, a USB stick, or a bare-metal install. `vulos-init` is PID 1.

**v1 (shipped, always-stream):** `cage` runs Cog/WPE fullscreen as the sole Wayland compositor. The React shell is the window manager for every pixel. Native apps stream into `<StreamViewer>` JSX windows via GStreamer/WebRTC — identical pipeline to remote access. No compositor complexity; one tested code path.

**v2 (planned, surface transport):** `labwc` replaces cage on bare metal. Each JSX window becomes its own `xdg-toplevel`. Native apps use zero-copy DMABUF passthrough. Traffic-light theme via openbox SSD. `wlr-foreign-toplevel` unifies dock/focus across JSX and native windows.

Boot sequence: UEFI → systemd-boot → Plymouth splash (determinate progress bar tied to real milestones) → `vulos-init` phases (filesystems, hardware detection, networking, PipeWire, cage, vulos-server, browser) → Plymouth fade → desktop. Cold boot to desktop in 5–15 seconds.

The live-USB path (`build.sh --live`) produces the squashfs used for "Try Vulos" sessions and for the OTA artifact. The installer is a React app inside the running OS that partitions, copies, and installs the bootloader — no separate installer environment.

**Outstanding:** `--live` bootable ESP fix (BMINIT-14 reopened); v2 surface transport (BMINIT-17/18); ARM device variants (Raspberry Pi, PinePhone).

---

## 5. OS Distribution, Signing & Trust

OS images are signed, versioned squashfs artifacts in a **public** S3 bucket. Security comes from signing, not access control — anyone can download the OS; only the holder of the release key can produce one that verifies.

**Artifact chain:** `os/stable.json` (root-signed manifest, carries version + dm-verity root hash + min-epoch) → `os/vNN/os-core.squashfs` + detached `.sig`. A device: fetches the manifest, verifies the signature against its baked trust anchor, checks `min_epoch ≥` its floor (monotonic, no CRL, no clock), downloads to the inactive A/B slot, verifies verity + sig, flips the slot, reboots.

**A/B slots + auto-rollback:** the bootloader increments a boot counter; `vulos-init` marks healthy after services come up clean; if the counter exceeds the threshold, the bootloader falls back to the last-known-good slot automatically. User data partition is separate — a rollback never touches user state.

**Trust model:** the seed (bootloader + verify-capable initramfs + baked Ed25519 public key) is what gets flashed. The bucket URL is soft/runtime config — the key is the anchor. A forker generates their own keypair, stands up their own bucket, rebuilds the seed with `VULOS_TRUST_ANCHOR_PUBKEY=... VULOS_OS_BUCKET_URL=... ./build.sh` — fully independent, no upstream allow-list.

**Signing chain:** offline root key → root-signed release-key cert (fetched alongside manifest) → per-image signatures. Key rotation bumps `min_epoch`, permanently retiring the old key on any device that has seen the new manifest. dm-verity enforces block-level integrity at runtime.

**Netboot:** UEFI HTTP Boot from `boot.vulos.org` (configurable), or a ~1MB one-time iPXE stick — both chainload the same signed kernel + initramfs + squashfs. Safety: TLS (transport) + signature verification (payload) are independent layers. Plain-HTTP safety works via signature chain alone. The live-RAM session runs first; install is an explicit user action.

---

## 6. Multi-Node Cluster & Sync

Multiple instances share state via cr-sqlite CRDTs over S3/MinIO. No primary node; every instance holds a full, mergeable copy. S3 is the sync hub and a redundancy copy; any instance works fully offline.

**Two-tier sync:**
- **Hot path:** live instances stream `crsql_changes` directly over the peering mesh (relay fallback for NAT). Near-real-time convergence between active instances.
- **Cold path:** periodic durable checkpoint to the S3 bucket. Catches up offline instances.

**Snapshot/compaction:** a lease-owned compaction job writes a snapshot to `cluster/snapshot/` periodically. New instances bootstrap from snapshot + short changeset tail — bounded bootstrap cost, no unbounded log replay.

**Coordination:** mutual exclusion uses bucket-backed leases with monotonic fencing tokens (`If-Match` CAS on an always-present object). One primitive serves run-leases (singleton apps), singleton jobs, and compaction ownership. No leader election; no external coordination service.

**Per-data-type conflict policy:** settings/preferences → LWW per field; counters/quotas → CRDT counter; co-edited documents → collaborative CRDT (Automerge/Yjs); exclusive resources → lease.

**Concurrency manifest:** apps declare `"concurrency": "singleton" | "replicated" | "collaborative"`. Default is `singleton` (safe, active-passive, infra-enforced run-lease). `replicated` opts into active-active CRDT merge. `collaborative` adds a presence/awareness channel on the peering/relay hot path for real-time co-editing. The declaration is signed with the manifest and cannot be flipped post-publish.

**Encryption at rest:** all S3 data is SSE-C encrypted (client-side key derived via Argon2id from a passphrase held only locally).

---

## 7. Public Webapps & Resource Governance

Users can publish apps to a public domain from their instance. The visibility system (`private | local | public`) lives in the app manifest and is enforced by the reverse proxy.

**Resource governance:** OS sync, the mail connector, and core system services hold a protected cgroup reservation that no webapp can starve. Published webapps receive a fair share of the remaining headroom. The reservation is enforced by cgroups at the OS level — not by app cooperation.

**Edge cache:** published apps are served through an edge cache. The cache is transparent to the app; invalidation is triggered by the app's publish toggle. Bandwidth monitoring and auto-disable on low-power/mobile-data conditions protect users from accidental overuse.

**Topbar warning:** whenever any app is visible at `local` or `public`, a persistent topbar indicator shows the count, color-coded (yellow = local network, red = public internet). The warning cannot be dismissed while non-private apps exist. First-time public exposure triggers a confirmation dialog.

**Dashboard publish toggle:** Settings → Apps → each app has a visibility selector. The reverse proxy (Caddy) enforces visibility at the edge — private apps redirect unauthenticated requests to login.

---

## 8. Peering & Communication

Every Vulos instance is a server. Direct communication between instances uses Ed25519 identity (`vula:ed25519:<base58>`), signed canonical-JSON envelopes, and server-to-server HTTPS. Instances you haven't approved cannot reach your inbox.

**Trust model:** Unknown → Pending → Approved → Blocked. No data transfers from pending contacts. Blocking is silent.

**Messaging & media:** server-to-server HTTPS delivery, store-and-forward, durable inbox, E2E encrypted (X25519 + XChaCha20-Poly1305 per conversation). Offline messages queue with exponential backoff.

**Voice/video calls:** signaling via servers; media direct browser-to-browser (WebRTC). For groups (5+), a Pion SFU runs on the host's instance — simulcast, Last-N, dominant-speaker audio mixing. Pre-call bandwidth visibility for SFU host selection. Hard cap: 50 participants.

**Real-time collaboration:** Yjs CRDTs over the peering WebSocket. Docs, Sheets, Slides, Notes, and Text Editor all support live co-editing with cursor presence. Offline catch-up via state-vector diff. This is the `collaborative` concurrency mode from the manifest system.

**Drop (AirDrop equivalent):** LAN discovery via mDNS, BLE on bare metal, 6-digit proximity code as browser fallback. Transfer goes over the peering layer (same HTTPS path as messages). Discoverability: everyone / peers only / nobody.

**Extensions (advanced, designed):** relay peers (offline delivery via trusted mutual contacts or TEE-backed relays); cluster anycast (multiple endpoints per identity for HA); signed feeds (append-only public/link publishing); gossip protocol (O(log N) fan-out for large groups); MLS group encryption (RFC 9420, O(1) encrypt for N-member groups); ring signatures (anonymous group participation); ZK discovery (find peers by domain/location without revealing identity); compliance extensions (threshold key escrow, ZK audit proofs, legal hold).

**Notifications:** structured, prioritized (low/normal/high/critical), signed, TTL-based. Per-contact permission granularity. DND with schedule and per-contact overrides. Inline action buttons. The notification system feeds Vulos Mail alerts, peering events, and system alerts through one pipe.

---

## 9. Default Web Apps

Built-in apps that ship with the OS — no streaming, no apt packages. They open instantly because they are part of the OS. All 15 base apps are shipped.

**Shipped:** Notes (knowledge base + Recall indexing), Gallery (photos/video organized by Recall), Smart Browser (ad-free + AI summaries), Calculator, Calendar, Weather, Clock, Text Editor (CodeMirror 6), PDF Viewer (PDF.js), Music Player, Video Player, Image Editor, Screenshot/Screen Capture, Voice Recorder, Camera, Maps (Leaflet + OSM).

**Outstanding:** Docs (TipTap rich-text word processor), Sheets (spreadsheet), Slides (presentation), Email client (IMAP/SMTP, or integrate with Vulos Mail), Contacts (vCard + CardDAV).

Every default app can integrate with the AI router: Docs gets summarize/rewrite/translate, Sheets gets formula generation, Email gets draft/summarize, Calendar gets scheduling inference, Text Editor gets code explanation/refactor.

---

## 10. App Store & Installable Apps

A curated registry that turns self-hosting into one click. Recipe types: apt/dpkg, Flatpak, static binary, download-and-extract. Each installed app gets its own subdomain and isolated network namespace.

**Shipped (in registry):** Kdenlive, Audacity, Blender, GIMP, Inkscape, Jupyter Lab, LibreOffice, Gitea, Wede, Geany, Grafana, KiCad, Firefox, FileZilla, qBittorrent, Transmission, Syncthing, KeePassXC, Cockpit, VLC. (Thunderbird was removed — mail is handled by LilMail + vulos-mail.)

**Planned:** Shotcut, Ardour, LMMS, Darktable, Penpot, OBS Studio, MapLibre GIS Editor, QGIS, GNU Octave, GnuCash, Firefly III, Jitsi Meet, Excalidraw, draw.io, Vaultwarden, Navidrome, Jellyfin, Memos, Uptime Kuma, Stirling PDF, VS Code (code-server), Hoppscotch, LibreTranslate, Matrix client (Cinny + Conduit homeserver + mautrix bridges for WhatsApp/Telegram/Signal), Steam, Lutris, Wine.

App registry syncs across cluster nodes via cr-sqlite (intent only — each node installs natively from the registry for its own architecture). App data syncs via the file sync layer.

**Web-app curation (maximize the web-app lane, shrink the streamed set).** The streamed set should shrink to genuine native-only apps. Already web-native — point users here, never stream: vulos-office (docs/sheets/slides/PDF), vulos-mail webmail, xterm.js terminal, Notes, host browser. Adopt as first-class self-hostable web apps in `registry.json`, tagged `web`:

| Replaces | Web app | License |
|---|---|---|
| GIMP/Photoshop (raster) | miniPaint | MIT |
| VS Code / IDE | code-server | MIT (Open VSX) |
| digiKam/Shotwell (photos) | Immich or PhotoPrism | AGPL |
| draw.io/Visio (diagrams) | diagrams.net | Apache-2 |
| Audacity (light audio) | AudioMass | open |
| Inkscape (light vector) | SVG-Edit | MIT |
| VLC (media playback) | Jellyfin / HTML5 | GPL |
| CAD (Fusion/SolidWorks) | **kerf** — browser-native, client-side WASM geometry (see `roadmap/CAD-KERF.md`) | open |

Curation priority by commonality × clean self-host: image-edit → IDE → photos → diagrams → media → kerf CAD → audio/vector. Each registry entry carries lane flags (`web`, `needs_gpu`, `game`, `local_only`, `compute_job`) consumed by the Open Router (§0). **Kept on the GPU route (BYO):** Blender (kerf does not replace mesh/sculpt/render), heavy video editing (DaVinci/Kdenlive), games.

---

## 11. Browser, Streaming, GPU Route & Gaming

### Browser — native, never streamed

All web content opens in the **host browser**: web apps and arbitrary URLs render as a host-browser window/tab, or as an in-shell web view (iframe/web view inside the React shell via the existing subdomain/HTTP proxy). No Xvfb/GStreamer/WebRTC for web content. On bare metal a browser window is a local compositor window (WPE WebKit or Chromium-kiosk), not a streamed session.

The server-side streamed "Browser" app is **retired** — removed from `registry.json` and the dock, and the server-side Chromium streaming path (`backend/services/webbrowser/`, the `xvfb chromium` install in `build.sh`) is audited and removed to shrink the attack surface. The bare-metal kiosk Chromium stays (it *is* the host browser). The Isolated/Disposable Browsing (RBI) stub has also been removed; revisit if a concrete use case arises. Engine choice (WPE WebKit vs Chromium) is a compat/footprint decision, not an efficiency one — streaming cost is engine-agnostic; Ladybird does not help and is not pursued for streaming.

### CPU app stream — retained, efficient

For genuine native Linux GUI apps with no web form. On software: Xvfb → ximagesrc → vp8enc. The pipeline carries five efficiency wins (`backend/services/stream/`, `backend/services/gpu/`):

1. **Stop encoding when no peer is connected** — connected-peer refcount on `Session`; start `gstVideo` on 0→1, stop on 1→0.
2. **Dirty-region capture** for non-gaming — `gpu.CaptureArgs` `use-damage=true` unless `opts.Gaming`.
3. **Idle FPS + idle suspend** — static content drops to ~1–5 fps and ramps on activity; idle + unwatched reclaims Xvfb/app RAM after a timeout.
4. **Resolution adaptation** — ABR steps resolution (1080→720→480) alongside bitrate on sustained loss, recovering when the link improves.
5. **Live bitrate/FPS change** — set bitrate on the named encoder element + a `videorate` element, no full pipeline restart / no black blip.

**Gaming guardrail:** none of the throttling wins (2–4) apply when `opts.Gaming` — games want constant high fps, `use-damage=false`, and fat bitrate.

### GPU route — peer-based BYO (cloud later)

Games, Blender, and heavy video editing run on the **GPU route**. A BYO GPU host is a full Vulos peer: Ed25519 identity, advertises a `gpu` capability descriptor (encoder, VRAM) over the fabric, reuses peering NAT-traversal/sync/identity (`backend/internal/gpuhost/`). Two sub-cases: **same-machine** (a local window, no encode, ~0 latency) and **remote-to-own-hardware** (your GPU rig streams to your other devices, Moonlight-style, brokered through the fabric).

A `GPUProvider` seam abstracts the source: `BYOPeerProvider` (now), `OnDemandCloudProvider` (later — RunPod/Lambda/CoreWeave), `WarmPoolProvider` (later — bare metal). The session pipeline is identical regardless of source.

**Media-plane invariant:** the GPU node runs the pipeline (cage + GStreamer + NVENC + WebRTC) and encodes + streams **directly to the user's browser**. The control plane does signaling + brokering only; the TURN/circuit relay is a NAT fallback only. Raw/rendered frames are **never** relayed through the cloud — perceived latency is GPU-node→user, so near/BYO nodes win.

**Metering** counts GPU-seconds per session, provider-agnostic. BYO = free/credited; cloud (later) = passed-through + margin. No GPU code path depends on Fly GPUs.

### Gaming mode

Auto-enables for Wine, Lutris, Steam, and any `gaming` category app. Raises FPS to 60–144, cranks bitrate to 6–10 Mbps, switches Opus to 10ms frames, enables pointer-lock + raw mouse passthrough, increases gamepad polling to 120Hz, elevates process priority to SCHED_FIFO. On GPU hardware: cage → PipeWire DMA-BUF → NVENC/VA-API (zerolatency, no B-frames) → WebRTC, zero CPU copies. Companion tools (GameMode, MangoHud, DXVK, VKD3D-Proton, Proton-GE) auto-install alongside their parent apps. Gaming is always the GPU route — no CPU fallback for games.

---

## 12. Authentication & Device Identity

The OS is a credible "possession factor": TPM-sealed device identity, TOTP vault (Google Authenticator import/export), encrypted password manager (auto-fill + TOTP + passkey integration), FIDO2 passkeys (server-side and WebAuthn bridge for remote-browser sessions), mTLS client cert store, SMS receive via VoIP number.

**Auth model:** email + password + 2FA (TOTP) as the baseline; passkeys (WebAuthn/FIDO2) as the primary login for new accounts; QR / phone-approval login for kiosk/shared clients. **No third-party OAuth or Google sign-in** at the OS level. (LOGINISO-03 OAuth BFF was evaluated and descoped — Vulos identity is self-contained.)

**Shipped:** TOTP generator, password manager, TPM/software keystore abstraction, mTLS cert store, SMS receive, server-side FIDO2/passkeys (full registration + assertion login flow, `backend/services/passkeys/`), WebAuthn bridge data channel, QR / phone-approval login (`backend/services/passkeys/qrlogin.go`).

**Login isolation (isolate the credential, not the browsing) — shipped:**
- **Passkeys as primary login.** Full WebAuthn registration + assertion login flow for Vulos accounts (`backend/services/passkeys/login.go`). The private key never leaves the authenticator → a keylogger/extension on the client gets nothing reusable; phishing-resistant (origin-bound). Password+2FA stays as fallback; new accounts default to passkeys.
- **QR / phone-approval login** for shared/streamed/kiosk clients — a short-lived, single-use challenge approved by an already-authenticated phone, so no reusable secret is typed on an untrusted client (`backend/services/passkeys/qrlogin.go`).
- **Threat-model honesty** (`THREAT-MODEL.md`): passkeys / out-of-band auth are the only things that make the credential un-capturable by an untrusted client; pixel-streaming a login does **not** protect a secret typed on a compromised client.

**Planned (advanced):** Verifiable Credentials wallet (eIDAS 2.0, ISO mDL, ZK selective disclosure); behavioral authentication (local ML model, continuous trust score); Open Banking API integration (OAuth 2.0 + mTLS, PSD2/UK Open Banking); device attestation service (TPM quote + cert chain for external service trust).

---

## 13. Device Profiles

One codebase, four UIs. Profile is selected at first boot (auto-detected from screen size/device type, overridable). The `data-device-profile` attribute on the root element drives all layout and behavior differences — no separate forks.

| Profile | Base | UI |
|---|---|---|
| **PC / Tablet / Mobile** | Debian Trixie (postmarketOS for mobile) | Full desktop, responsive |
| **TV** | Debian Trixie | 10-foot layout, D-pad spatial nav, CEC, voice-first |
| **Car** | Debian Trixie | Large touch targets, voice-first Portal, fast suspend-to-RAM resume, auto-DND |
| **Watch** | AsteroidOS (Qt/QML) | Companion — AI chat, notifications, quick actions, health/fitness |

**Shipped:** profile model + detection + persistence, TV spatial navigation, TV 10-foot home.

**Outstanding:** car driving mode (DEVPROF-06), watch companion app, cross-device notification sync.

---

## 14. Fediverse & Social

A bundled `social` app that speaks ActivityPub natively — one client for microblogging (Mastodon), photos (Pixelfed), video (PeerTube), and forums (Lemmy). Three usage modes: read-only (no account), existing account (OAuth2 to remote server), self-hosted identity (GoToSocial as a Vulos service, single binary + SQLite).

**Shipped:** ActivityPub scaffold, OAuth2 login, post/boost/fav, Photos + Video views, Lemmy forums, push-to-notify.

---

## 15. Telephony (Mobile)

SMS, voice calls, and eSIM management via ModemManager (D-Bus) and lpac. A Go webapp (`phone`) handles Messages + Dialer. Call audio routes through the existing WebRTC/PipeWire streaming pipeline — no separate audio system. Works identically local or remote.

**Shipped:** telephony service, SMS + voice + eSIM backend, Messages + Dialer UI.

**Outstanding:** shell polish for mobile form factor; camera improvements via libcamera; push daemon for reliable offline wakeup.

---

## 16. Security

Security is structural, not bolted on. Key invariants: no remote code execution without explicit user action; sandbox isolation for AI-generated apps and public webapps; Ed25519 signatures on every peering message; dm-verity on the OS image at runtime; TPM-sealed secrets where hardware allows; rate limiting on all inbound peering endpoints; dependency and container CVE scanning in CI.

**Shipped:** full security audit of backend services, sandbox isolation review, auth middleware hardening, rate limiting, dependency scanning CI, container image scanning, attacker-style pentest suites (one finding — CRDT-QUORUM-01 — found and fixed). Security-hardening pass: 35 privileged endpoints now gated behind admin checks, IDOR fixes across mission/profile/peering endpoints, command-injection fixes in firstboot hostname validation, SSRF blocking on `/api/open` and the web proxy.

**Multi-tenant posture.** The microVM boundary (§18) is the *easy* part — Fly/Firecracker gives a best-in-class one. The real multi-tenant risk lives in our own code: tenant-routing, data-partitioning, auth, and the control plane (where CRDT-QUORUM-01-class bugs come from). The attacker-style pentest suites therefore target the **app-level multi-tenancy layer** (tenant isolation, IDOR, auth, open-relay, quorum); every cross-tenant code path is treated as the primary attack surface and the VM boundary is defense-in-depth, not the whole defense.

**Outstanding:** periodic re-audit as new surfaces ship (public webapps, AI router, mail connector, GPU route); extend pentest coverage of the multi-tenancy layer (PENTEST-01); ZK audit proofs for compliance extensions.

---

## 17. Theming, Platform & i18n

**Shipped:** accent colour picker, terminal theme + font config, i18n scaffold, dark/light themes, CI CVE scanning.

**Outstanding:** Night Shift (auto colour temperature), dynamic wallpapers, improved mobile responsiveness, full accessibility pass (WCAG 2.1 AA), wider i18n coverage.

---

## 18. Secure Multi-Tenant Topology

**One uniform isolation model: a Firecracker microVM per tenant, scale-to-zero.** No shared multi-tenant runtime — one model to secure; idle tenants ≈ $0; the only cost is a small density penalty on the simultaneously-active tiny long tail (accepted).

- **Platform now: Fly Machines** (Firecracker microVMs — the same isolation primitive AWS Lambda/Fargate use). Autostop/autostart = scale-to-zero. UDP for the WebRTC media plane. Edge/anycast for proximity. The Machines API drives the programmatic per-tenant fleet.
- **Stay pluggable.** Keep `ComputeProvider` (in vulos-cloud) clean — no hardwired Fly-proprietary bits — so a move to DIY-Firecracker-on-AWS or Kata-Containers-on-k8s is a migration, not a rewrite. Pick those only when scale/economics justify the ops headcount.
- **Durable state is S3/Tigris + cr-sqlite CRDT — the source of truth.** Fly Volumes are cache, not truth (host-pinned, unreplicated). Multi-region. Design for any Machine/host to vanish.
- **Stateless control plane / shared services** (auth, routing, signaling, web-proxy, relay) → a separate shared autoscaling group.
- **Operationally still "one scaling group":** you declare per-tenant Machines; Fly bin-packs them; idle ones scale to zero. No hand-provisioning per client.
- **Graduation to dedicated, reusing existing investments.** Identity + data are instance-independent from day one (Ed25519 identity, leaderless cr-sqlite CRDT, peering). "Move to your own instance" = spin up a new instance with the same identity, sync the CRDT, optionally retire the shared-pool presence. No hard cutover (leaderless → no split-brain); the dedicated instance peers back via the fabric. Self-host/OSS targets bare-metal Firecracker or plain containers through the same abstraction — no Fly dependency.

**Billing alignment** (monetize relay + enterprise): free/cheap = web-app-only on scale-to-zero microVMs; paid = GPU route (BYO credited; cloud GPU passed-through + margin, metered); dedicated/enterprise = own instance(s), monetizing control plane + relay + identity. The Fly Machines orchestration itself lives in **vulos-cloud**; this repo carries the OS-side pieces (durable-state rehydration, dedicated-instance migration, identity/CRDT portability).

---

## Appendix: Stack Invariants (frozen)

- **Language:** Go for all backend, boot, and verification slices. React/JSX for frontend (never `.tsx`). No Rust rewrite. (decisions.md J)
- **SQLite:** `modernc.org/sqlite` (pure-Go, no CGO). (decisions.md D23)
- **Browser:** native host browser only — no server-side streamed browser. Bare-metal kiosk engine is WPE WebKit or Chromium (compat/footprint choice, not efficiency). Ladybird de-scoped and not pursued for streaming. (future/LADYBIRD-BROWSER.md)
- **Dispatch:** every launch routes through the Open Router into one of five lanes (web app / CPU stream / GPU route / compute worker / local-only). Web content is never streamed. (§0)
- **GPU:** peer-based BYO now; cloud provider later, metered. No code path depends on Fly GPUs. (§11)
- **Tenant isolation:** one uniform model — a Firecracker microVM per tenant on Fly Machines, scale-to-zero. Fly Volumes are cache; S3/Tigris + CRDT is truth. (§18)
- **Compositor:** cage (v1 streaming + bare-metal kiosk); labwc reserved for v2 surface transport. (decisions.md D93)
- **AI providers:** pluggable (Ollama default, Claude, OpenAI, any OpenAI-compatible endpoint). No vendor lock-in.
- **Trust:** security from signing, not access control. Public bucket + hard-baked key. Forkable.
- **Self-hostable:** every service has a self-hosted path. Cloud is an optional convenience, never a correctness requirement.
- **CAD / DAW:** separate browser-native tools, not built into the OS. (roadmap/CAD-KERF.md, roadmap/REALTIME-AUDIO-DAW.md)

---

## Storage Backend, Multi-Location, Co-location & Identity

> **Superseded in part (2026-07-15).** The storage-backend, multi-location, and
> co-location mechanics below (§A–§C) still hold. But the **identity/anchor-inbox**
> framing (§D) and the **2-track per-active-user billing** (§E) are **retired** and
> replaced by the finalized **box model**: a free account (email/OAuth, no mailbox
> needed) + metered relay/storage/compute + per-unit mailbox and box charges.
> Identity is decoupled from mail; mail is a connector, not a central Vulos service.
> See the rewritten §D and §E.

### A. Storage-backend choice (per account)

The S3-compatible blob store that underpins all Vulos data (mail, office, OS sync) supports
two backends — customer's choice at account setup:

| Backend | Who it suits | How it works |
|---|---|---|
| **Tigris (default)** | Hosted + self-host customers who want managed storage | Per-org bucket prefix on our Tigris tenant; durable, replicated, geo-selectable |
| **MinIO local (complete BYO)** | Complete-BYO customers with their own hardware | Customer runs MinIO; data never touches Vulos storage |

Both backends expose the same S3-compatible API surface. The only code-level difference is
endpoint + credentials. The OS, mail server, and office suite all consume this unified interface.
A per-account storage-backend selector is needed (see TASKS item `STORE-BYO-01`).

### B. Multi-location topology — central bucket (v1 default)

A BYO org can connect multiple boxes and locations as a single logical deployment:

**v1 (default): central bucket.** All compute instances for the org point at ONE shared bucket
(Tigris for hosted; or one MinIO instance for complete-BYO, at the customer's most-reliable site
or VPS). CRDT + bucket-lease coordinator handle concurrent writes. The per-location local cache
(`serving/cache.go`, already built) provides read-latency reduction. Compute instances at multiple
locations give redundancy and HA — any location can serve any session.

**Future work (NOT v1): replicated peer-sync.** Each location runs its own MinIO with
peer-to-peer CRDT sync over the Vulos fabric. Designed for strict per-site data sovereignty or
offline-must-work requirements. Explicitly out of v1 scope. See TASKS `STORE-MULTLOC-01`.

Multi-location implementation requires: enrollment flow for joining box #2 and #3 to an org
(`STORE-MULTLOC-02`) and cross-location inbound-mail routing that picks a healthy site
(`MAIL-MULTLOC-01`).

### C. Co-located single-instance bundle

OS + office share one bucket, one CRDT/peering fabric, and one identity, and can all run on ONE
instance. The BYO story is: **"one box = your whole Vulos."** Mail is a connector to your existing
mailbox rather than a co-located service; the dormant/experimental `vulos-mail` engine can
optionally join the same box (see "Self-hosted mail support" below).

Roadmap item: a `vulos` meta-bundle/installer that installs and supervises the co-located
services (OS, vulos-office, and optionally the dormant vulos-mail engine). A unified installer
(`BUNDLE-01`) removes the multi-step setup currently required.

### D. Cloud-held identity (decoupled from mail)

Identity is always cloud-held regardless of storage or compute choice — but it is **anchored by
a handle, not a mailbox**:

- The `@vulos.net` handle, login credential, and account recovery live in the Vulos cloud control
  plane (keydir/identity service). This applies to both hosted and complete-BYO accounts. A free
  account needs only email/OAuth — **no mailbox is required or provisioned**.
- **There is no central "anchor inbox."** Mail is a connector (bring your own Gmail/Outlook/IMAP);
  the cloud does not run a mailbox for you as an identity backstop. A mailbox is a separately-billed
  optional add-on, never the account anchor.
- **Recovery ladder:** (1) passkeys, (2) TOTP + recovery codes, (3) a backup email, (4) ID-verified
  recovery as the ultimate backstop. No account can become permanently inaccessible.
- A compute instance (box) going offline is never data loss: Tigris is durable; for complete-BYO,
  customers must back up their MinIO (their responsibility — documented at onboarding).

**The "never lose your Vulos account" guarantee:** As long as the Vulos control plane is
reachable, your identity and your handle are intact — independent of what happens to your local
hardware and independent of any connected mailbox.

### E. Billing model — pure box model (finalized 2026-07-15)

**Flat per-active-user tiers are killed.** There is no Free/Personal/Pro/Team ladder and no
per-active-user charge. Billing is a **box model**: a free account plus metered infrastructure
and per-unit add-ons.

| Component | Price | Notes |
|---|---|---|
| **Account** | Free | Email/OAuth sign-in; no mailbox needed. Passkeys + TOTP + recovery codes + backup email. |
| **Mailbox (optional)** | $2 / mailbox / mo | Only if you want a Vulos-connected mailbox; most users bring their own Gmail/Outlook/IMAP for $0. |
| **Managed box** | $10 / $30 / $450 per box / mo | Size tiers for a Vulos-provisioned managed box. Self-host on your own hardware is **$0**. |
| **Storage** | $0.05 / GB-mo | Metered object storage (Tigris). |
| **Relay** | $0.05 / GB (EU; region-scaled) | Metered reachability. **25 GB/mo free relay is included with any box.** |
| **Compute** | Metered | GPU-seconds / compute workers, provider-agnostic (see §11, §18). |

Relay, peering, identity, and provisioning fabric are always Vulos-operated — that is the product.
Self-host saves hosting cost (box = $0); a managed box saves hardware and ops overhead. Everything
above scales with actual usage, not seat count.

**Storage-backend note:** any account may choose either Tigris (simpler, managed) or MinIO local
(complete BYO, strongest data sovereignty) as its storage backend. The billing is the same metered
$0.05/GB-mo on Tigris, or $0 on your own MinIO; the only difference is the endpoint configuration.

---

## Self-hosted mail support (dormant/experimental)

**Mail is a connector, not a service the OS runs by default.** The primary path is the bundled
inbox (LilMail + `@vulos/mail-ui`) connecting to a mailbox the user already owns
(Gmail/Outlook/any IMAP/SMTP). Most users need no Vulos-hosted mailbox at all.

Running the Vulos-hosted mail **engine** (`vulos-mail`) on your own box is a **dormant/experimental**
option — the code exists and is resurrectable, but it is not a first-class first-boot step and not the
account anchor. If you opt in, Vulos can provide the deliverability stack (MX gateway, warm-up relay,
spam, reputation, encrypted queue, health monitoring); no static IP or port forwarding required.
Billing follows the box model (metered relay/storage + the optional $2/mailbox add-on), **not** a
per-active-user mail tier.

**OS integration points (experimental):**

- **Installer step:** an optional "Mail service" step could run `vulos-mail byo setup` and register
  the instance as a BYO mail server — kept behind the experimental flag, off by default.
- **Dashboard surface:** if enabled, the OS dashboard shows vulos-mail service status
  (running/stopped/offline) alongside other services, with health from the cloud BYO health-check.
- **Notification routing:** the cloud's offline alert routes to the OS notification system if the
  OS is online at another node (e.g., mobile companion).

**Distribution:** the bash installer and GHCR Docker image (MAIL-BYO-04) remain the channels for
anyone who wants to run the dormant engine.

Cross-repo: see `vulos-mail/ROADMAP.md` and `vulos-cloud/ROADMAP.md` (mail engine kept dormant).

---

## Future work

### OS app wrappers for office / spaces / calendar / meet
Add installable OS app wrappers for `vulos-office` surfaces (docs, sheets, slides, spaces,
calendar, meet) following the existing `vulos/apps/mail/` pattern. Each app wrapper registers
with the OS launcher, gets a `{app}--{profile}.{ulid}.vulos.org` subdomain, and integrates
with the OS notification and session system. Wrappers are thin JSX shells — the app logic
lives in the respective repos.

### airouter mail-specific endpoints
`airouter` already ships smart-compose, summarize, reply-suggestions, and extract-actions for
mail. Confirm these endpoints are reflected in ROADMAP.md §3 (AI Router) and that the mail
app wrapper calls them via the standard router path. No new implementation needed if already
shipped — verify and update the roadmap status marker.

### LLM phishing classifier endpoint via airouter
Add a phishing / malicious-link classification endpoint to `airouter`. Mail and browser call
this endpoint before rendering external links or attachments. The classifier runs on a small
local model (Ollama) with a fallback to the cloud-billed path. Returns a risk score +
confidence + suggested action (show/warn/block). Feeds the URL safety feeds in `vulos-mail`.

---

## Offline LAN access & local-first storage (v6 — Decisions F+G)

Opt-in (not default). When the internet/cloud is down but the client is on the box's LAN, the OS, office,
and mail keep working by talking to the box directly over the LAN. The box advertises via mDNS
(`vulos.local`) + a local DNS responder for `box.<id>.lan.vulos.org`, serves a publicly-trusted DNS-01
cert (issued by the control plane, key lives on the box), and the web clients fail over between cloud and
LAN endpoints automatically. An opt-in `local-minio-sync` storage mode makes a local MinIO the source of
truth (offline-capable), syncing its CRDT index + blobs via a central Tigris rendezvous (v1) and direct
fabric-P2P (fast-follow, incl. same-LAN offline sync). Default stays central Tigris; the `@vulos.net`
account identity stays cloud-held (handle-based, decoupled from mail). Tasks: `OFFLINE-01..03`, `STORE-LOCAL-01`, `STREAM-BYO-01`.

**FABRIC-P2P-01 (same-LAN P2P CRDT sync) — now REAL, not a stub.** Task #119 previously shipped only the
transport-agnostic merge (`internal/multiinstance/appsync.go`) with no peer discovery and no peer-to-peer
exchange — i.e. it could merge a changeset handed to it but had no way to obtain one from a sibling box.
The real same-LAN path now lives in `backend/internal/fabric/`:
- **Discovery:** mDNS (`vulos-fabric.local`) advertises this box and resolves sibling boxes' LAN IPs
  (`fabric.MDNSDiscoverer`); a `StaticDiscoverer` seam covers CI / manual peer lists. No cloud, no S3.
- **Transport:** authenticated HTTPS over the LAN listener —
  `GET /api/fabric/changeset?since=<cursor>` serves a box's changesets after a cursor;
  `POST /api/fabric/changeset` accepts a peer's. Both require the shared `VULOS_FABRIC_SECRET` in
  `X-Fabric-Auth` (constant-time compare, fail-closed on empty), so a random LAN host cannot inject or read
  registry state. Handlers are mounted on a LAN-only mux (pinned to the LAN IP), never the public surface.
- **Sync loop:** periodic + on-`Nudge` pull-then-push with every discovered peer, advancing a per-peer
  cursor; convergence is deterministic via the existing hardened LWW/OR-set merge.
- **Offline-first:** peers talk directly over the LAN with the internet/control-plane/Tigris down.
- Wired in `cmd/server` behind `VULOS_LAN_ENABLE=1` + `VULOS_FABRIC_SECRET`; reuses the single shared
  registry `*sql.DB` (no second handle — audit P1-4). Pure-Go `modernc.org/sqlite`, CGO disabled.
- Tests (`backend/internal/fabric/fabric_test.go`): two in-process instances on TLS httptest LAN
  listeners each make a different local change, run a sync round, and converge to identical registry state
  (`TestTwoInstancesConvergeOverLAN`); plus auth-rejection (`TestUnauthenticatedPeerRejected`,
  `TestPushFromUnauthenticatedPeerDoesNotMerge`) and a TLS-on-the-wire sanity check.

---

## Audit-fix wave A + video-meet (v7.1 — 2026-05-24)

**Audit-fix wave A** addresses the 5 findings from the #125 verification pass: the missing OS-side LANCERT
cert puller (offline-LAN HTTPS was stuck on self-signed because the cert never reached disk), the missing
`gpuhost` wire (STREAM-BYO-01 shipped a package the server never instantiated), shared LAN cert-path
constants, a startup log for the storagemode store, and a cross-repo SW-cache-version registry.
Tasks: `FIX-LANCERT-PULL-01`, `FIX-GPUHOST-WIRE-01`, `FIX-LAN-PATH-CONST-01`, `FIX-STORE-LOCAL-LOG-01`, `FIX-SW-CACHE-COORD-01`.

**Video meetings** — **WON'T-DO (LiveKit/SFU removed).** The LiveKit / `vulos-meet` approach was
evaluated and removed. Video calling in Vulos is **P2P WebRTC mesh only** (browser-to-browser;
servers handle signaling only, not media). For groups (5+), a Pion SFU runs on the host's own
instance (simulcast, Last-N, dominant-speaker audio mixing, hard cap: 50 participants). A
cloud-scale SFU (LiveKit, 500-participant rooms) is not in scope for this repo; revisit if a
concrete large-group requirement arises in vulos-office Spaces.
