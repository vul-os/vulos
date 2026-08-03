# Vulos OS — Roadmap

Vulos is a **sovereign, web-native personal operating system**. The full desktop — window manager, dock, every app — renders in a browser, so it reaches any device (phone, TV, laptop) without a separate app. The backend is a single Go binary that embeds the React/JSX shell. The OS is fully self-hostable, open-source, and forkable.

This is the **high-level** roadmap: vision and horizon themes. Granular, trackable work lives in [GitHub Issues](https://github.com/vul-os/vulos/issues) and Projects/Milestones. Deep per-area design documents live in [`roadmap/`](roadmap/).

---

## Vision

You run your own box — a VPS, a home server, or bare metal — and it *is* your cloud: desktop, files, calendar, contacts, AI, and reachability, all owned by you. **You self-provision.** Vulos does not host or run boxes for you — it is free, open-source software you run on your own hardware or a VPS you rent. The wedge is **agency, not just privacy**: your own server, your own AI, acting for you.

The suite is free and open-source — 100% self-hosted, nothing to buy. Reachability is via a relay your box dials out to — the built-in **Vulos relay** by default, with **Pier**, an open, self-hostable broker, as an experimental alternative (host your own or point at one someone else hosts). Storage is your own S3/MinIO bucket. Nothing is metered or billed.

### Settled invariants

- **The OS is the shell.** One React window-manager desktop is the whole surface — local or remote. There is no separate "Workspace" front end.
- **Native-first dispatch.** The most efficient compute is the browser already on the user's device. Every launch routes through the Open Router (`backend/services/openrouter/`) into one of five lanes: **web app** (host browser, ~zero server cost) · **CPU app stream** (Xvfb + WebRTC) · **GPU route** (BYO GPU peer — your own box) · **compute worker** (batch) · **local-only**.
- **Browsing is native, never streamed.** No server-side streamed browser; the host browser does all web content.
- **Reachability via a relay you choose.** A box is the authority; a relay it dials out to — the built-in **Vulos relay** by default, or **Pier**, an open, self-hostable broker, as an experimental alternative — is the single reachability ingress (direct-first, relay-fallback). Works without any central control plane.
- **Login isolates the credential, not the browsing.** Passkeys + a server-side token vault; no streamed login; no third-party SSO required.
- **Security from signing, not gatekeeping.** Signed, immutable, A/B-updatable images from a public bucket with a hard-baked trust anchor. Forkable with your own key.
- **PIM is bring-your-own.** [lilmail](docs/MAIL-LILMAIL.md) connects the user's IMAP/CalDAV/CardDAV and exposes a stable `/v1`; the OS ships standalone Calendar + Contacts over the box PIM proxy. Vulos hosts no mailbox.
- **Owned apps vs third-party comms.** **Diwan** (docs/sheets/slides/PDF/whiteboards — whiteboards are a Diwan document type, not a separate Board) is the standalone `diwan` repo, reached through the App Hub. Real-time chat/video are delegated to established platforms (Matrix/Element, Element-Call/Jitsi); the OS keeps its own sovereign peer-to-peer **Messages**.
- **Stack is frozen:** Go backend (pure-Go `modernc.org/sqlite`, no CGO); React/JSX only (never `.tsx`); no Rust; cage compositor; pluggable AI providers (no vendor lock-in); every service has a self-hosted path. (see [`docs/decisions.md`](docs/decisions.md))

---

## Now — shipped / current

The core is in place and self-hostable today:

- **Web-native desktop shell** — window manager, virtual desktops, dock, ⌘K palette, proactive AI Home, persisted sessions.
- **Sovereign assistant** — on-box AI agent under a hard security contract (read/act split, proposal ledger, tier-aware egress Guard, untrusted-content framing), routed through the on-box `llmux` gateway. See [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md).
- **Auth** — WebAuthn passkeys, device PIN, QR/phone approval, TOTP, recovery-phrase master key. Unified sign-in (one Vulos account → cloud login → OS session).
- **Files** — viewer/editor/owner ACL, content-blind sealed sharing, resumable chunked upload, P2P sharing.
- **PIM** — standalone Calendar + Contacts over lilmail `/v1` via the credential-brokering box PIM proxy.
- **Peering & sync** — per-instance Ed25519 identity, full VulaID key lifecycle, leaderless CRDT sync, AirDrop-style Drop, Yjs real-time collaboration.
- **App streaming** — native Linux GUI apps and Streaming Chrome over WebRTC with GPU-accelerated encoding; gaming mode.
- **App store** — signed registry (apt / Flatpak / static-binary / web-native recipes); every entry Ed25519-signed against the shipped anchor.
- **Distribution** — one embedded binary; signed immutable image with A/B slots + rollback; netboot install.
- **Notifications** — sovereign outbound Web Push (RFC 8291, E2E-encrypted), DND-aware.

---

## Near-term

- **Reachability hardening** — direct-first/relay-fallback ingress polish; IMAP/SMTP over Relay via SNI passthrough; app-passwords.
- **App store depth** — richer catalog, install UX, per-app resource governance and public-webapp isolation.
- **Assistant depth** — broader curated toolset, better on-instance retrieval (RAG), sovereignty-tier UX.
- **Bare-metal / first-boot** — smoother first-boot + netboot install; signed `os-core.roothash.sig` verification fail-closed.
- **Multi-node depth** — full cr-sqlite CRDT across a user's own nodes; conflict-free "move to your own box" migration reusing the same identity.
- **Client type safety** — gradual JSDoc type-checking of the JSX shell (`tsc --noEmit`), starting with the security-critical `src/lib/` SDK and generated types for the Go→JS API boundary. Stays inside the frozen stack: no `.ts`, no `.tsx`, no build change. See [`roadmap/TYPE-SAFETY.md`](roadmap/TYPE-SAFETY.md) and `docs/decisions.md` D95.

---

## Mid-term

- **Device profiles & mobile** — responsive shell refinement; telephony (mobile) surface.
- **Remote Assist** — TeamViewer-class co-presence, screen view, remote control, and delegated time-boxed profile access — composed from the existing WebRTC lane, per-profile isolation, and the capability-grant model. Capability-first, consent-visible, fail-closed. See [`roadmap/REMOTE-ASSIST.md`](roadmap/REMOTE-ASSIST.md).
- **GPU route economics** — metered cloud GPU as an opt-in add-on behind the BYO-peer default; launch cost stays $0 (no code path depends on any single provider's GPUs).
- **Fediverse & social** — a single default Fediverse client (Mastodon/Pixelfed/PeerTube/Lemmy). See [`roadmap/ACTIVITYPUB.md`](roadmap/ACTIVITYPUB.md).

---

## Long-term

- **Companion browser-native tools** (parallel to the OS, not built into it):
  - **kerf CAD** — browser-native parametric CAD with a batch compute-worker lane. See [`roadmap/CAD-KERF.md`](roadmap/CAD-KERF.md).
  - **Real-time audio / DAW** — local-first live-recording workstation. See [`roadmap/REALTIME-AUDIO-DAW.md`](roadmap/REALTIME-AUDIO-DAW.md).
- **Dense-network federation** — the moat: box-to-box federation that gets more useful as more people run their own box.

---

## Design documents

Deep, per-area design lives in [`roadmap/`](roadmap/) — one document per system area (AI, App Store, Auth, Boot/Init, OS Distribution, Signing/Trust, Cluster/Sync, Peering, Notifications, Network, Streaming, Mobile, and more). Read the area doc for *what* a part of Vulos is meant to do and *how* it's structured; open a GitHub Issue to actually pick up work.

---

## License

MIT OR Apache-2.0 — see [LICENSE-MIT](LICENSE-MIT) / [LICENSE-APACHE](LICENSE-APACHE).
