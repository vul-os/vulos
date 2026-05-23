# Vulos OS — Roadmap

Vulos is a web-native personal operating system. The full desktop — window manager, dock, every app — renders in a browser, so it reaches any device (phone, TV, car, laptop) without a separate app. Native Linux apps stream into the shell over WebRTC. The backend is Go; the shell is React/JSX. The OS is fully self-hostable, open-source, and forkable.

---

## 1. Multi-Instance & Account Routing

**The core value of Vulos is networking your instances together.** One account routes many instances — BYO bare-metal, Raspberry Pis, cloud VMs. Vulos makes them behave as one coherent system.

Every instance generates a ULID at first boot. The canonical address is `*.{ulid}.vulos.org` (or your own domain). Four connection modes are supported: Vulos fabric (Ziti, no open ports), direct (public IP + acme-dns), own domain, and local-only — all switchable post-install without reinstalling. LAN mode (mDNS, `vulos.local`) is always active regardless of which mode is selected.

Sessions sync via cr-sqlite CRDTs, so any instance in your cluster can serve any login — sticky-until-failure routing, leaderless, no split-brain. Multi-location liveness is by design; impossible-travel heuristics must not flag it.

The subdomain scheme is `{app}--{profile}.{ulid}.vulos.org`. A wildcard TLS cert covers all apps on an instance. Each profile gets isolated network namespaces and session-scoped cookies.

**Outstanding:** optional Vulos-provisioned fly.io instances (users can add managed cloud nodes alongside their BYO hardware); geo-routing control-plane advisor; fleet dashboard for multiple instances per account.

---

## 2. Identity — Mandatory Vulos Mail (vumail)

At install/onboarding every user creates or claims a **Vulos mail identity (vumail)**. There is no external email provider connected at OS level. The vumail identity is the account's persistent, portable address across the Vulos ecosystem.

The mail identity cross-references:
- **vulos-mail** (OSS mail server) — the SMTP/IMAP backend that handles actual mail delivery, peering authentication, and notification routing.
- **vulos-relay** — handles relay and peering for instances behind NAT; the relay server is the transport layer for vumail delivery when direct instance-to-instance is blocked.

The vumail identity also anchors the Ed25519 peering keypair (PEERING.md), so one identity covers messaging, notifications, mail, and peer trust — no separate accounts.

**Outstanding:** vumail provisioning in the first-boot wizard; tie the peering identity email-verification flow to vumail rather than external addresses; document the vulos-mail/vulos-relay boundary.

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

**Resource governance:** OS sync, vumail, and core system services hold a protected cgroup reservation that no webapp can starve. Published webapps receive a fair share of the remaining headroom. The reservation is enforced by cgroups at the OS level — not by app cooperation.

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

**Notifications:** structured, prioritized (low/normal/high/critical), signed, TTL-based. Per-contact permission granularity. DND with schedule and per-contact overrides. Inline action buttons. The notification system feeds vumail alerts, peering events, and system alerts through one pipe.

---

## 9. Default Web Apps

Built-in apps that ship with the OS — no streaming, no apt packages. They open instantly because they are part of the OS. All 15 base apps are shipped.

**Shipped:** Notes (knowledge base + Recall indexing), Gallery (photos/video organized by Recall), Smart Browser (ad-free + AI summaries), Calculator, Calendar, Weather, Clock, Text Editor (CodeMirror 6), PDF Viewer (PDF.js), Music Player, Video Player, Image Editor, Screenshot/Screen Capture, Voice Recorder, Camera, Maps (Leaflet + OSM).

**Outstanding:** Docs (TipTap rich-text word processor), Sheets (spreadsheet), Slides (presentation), Email client (IMAP/SMTP, or integrate with vumail), Contacts (vCard + CardDAV).

Every default app can integrate with the AI router: Docs gets summarize/rewrite/translate, Sheets gets formula generation, Email gets draft/summarize, Calendar gets scheduling inference, Text Editor gets code explanation/refactor.

---

## 10. App Store & Installable Apps

A curated registry that turns self-hosting into one click. Recipe types: apt/dpkg, Flatpak, static binary, download-and-extract. Each installed app gets its own subdomain and isolated network namespace.

**Shipped (17 in registry):** Kdenlive, Audacity, Blender, GIMP, Inkscape, Jupyter Lab, LibreOffice, Gitea, Wede, Geany, Grafana, KiCad, Firefox, FileZilla, qBittorrent, Transmission, Syncthing, Thunderbird, KeePassXC, Cockpit, VLC.

**Planned:** Shotcut, Ardour, LMMS, Darktable, Penpot, OBS Studio, MapLibre GIS Editor, QGIS, GNU Octave, GnuCash, Firefly III, Jitsi Meet, Excalidraw, draw.io, Vaultwarden, Navidrome, Jellyfin, Memos, Uptime Kuma, Stirling PDF, VS Code (code-server), Hoppscotch, LibreTranslate, Matrix client (Cinny + Conduit homeserver + mautrix bridges for WhatsApp/Telegram/Signal), Steam, Lutris, Wine.

App registry syncs across cluster nodes via cr-sqlite (intent only — each node installs natively from the registry for its own architecture). App data syncs via the file sync layer.

---

## 11. Streaming & Gaming

The WebRTC streaming pipeline carries every native app to the browser. On GPU hardware: cage (headless Wayland) → PipeWire DMA-BUF → NVENC/VA-API (zerolatency, no B-frames) → WebRTC. Zero CPU copies. On software: Xvfb → ximagesrc → vp8enc. The software path is unchanged when no GPU is present.

Gaming mode auto-enables for Wine, Lutris, Steam, and any `gaming` category app. It raises FPS to 60–144, cranks bitrate to 6–10 Mbps, switches Opus to 10ms frames, enables pointer-lock + raw mouse passthrough, increases gamepad polling to 120Hz, and elevates process priority to SCHED_FIFO.

Gaming apps (Wine, Lutris, Steam) are streamed via WebRTC with GPU encoding. Companion tools (GameMode, MangoHud, DXVK, VKD3D-Proton, Proton-GE) are auto-installed alongside their parent apps.

---

## 12. Authentication & Device Identity

The OS is a credible "possession factor": TPM-sealed device identity, TOTP vault (Google Authenticator import/export), encrypted password manager (auto-fill + TOTP + passkey integration), FIDO2 passkeys (server-side and WebAuthn bridge for remote-browser sessions), mTLS client cert store, SMS receive via VoIP number.

**Shipped:** TOTP generator, password manager, TPM/software keystore abstraction, mTLS cert store, SMS receive, server-side FIDO2/passkeys, WebAuthn bridge data channel.

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

**Shipped:** full security audit of backend services, sandbox isolation review, auth middleware hardening, rate limiting, dependency scanning CI, container image scanning.

**Outstanding:** periodic re-audit as new surfaces ship (public webapps, AI router, vumail); ZK audit proofs for compliance extensions.

---

## 17. Theming, Platform & i18n

**Shipped:** accent colour picker, terminal theme + font config, i18n scaffold, dark/light themes, CI CVE scanning.

**Outstanding:** Night Shift (auto colour temperature), dynamic wallpapers, improved mobile responsiveness, full accessibility pass (WCAG 2.1 AA), wider i18n coverage.

---

## Appendix: Stack Invariants (frozen)

- **Language:** Go for all backend, boot, and verification slices. React/JSX for frontend (never `.tsx`). No Rust rewrite. (decisions.md J)
- **SQLite:** `modernc.org/sqlite` (pure-Go, no CGO). (decisions.md D23)
- **Browser engine:** Chromium (current); Ladybird de-scoped until engine matures. (future/LADYBIRD-BROWSER.md)
- **Compositor:** cage (v1 streaming + bare-metal kiosk); labwc reserved for v2 surface transport. (decisions.md D93)
- **AI providers:** pluggable (Ollama default, Claude, OpenAI, any OpenAI-compatible endpoint). No vendor lock-in.
- **Trust:** security from signing, not access control. Public bucket + hard-baked key. Forkable.
- **Self-hostable:** every service has a self-hosted path. Cloud is an optional convenience, never a correctness requirement.
