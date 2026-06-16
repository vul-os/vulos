# Changelog

All notable changes to Vulos are documented in this file.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/) —
`Added` for new features, `Changed` for changes to existing behaviour,
`Deprecated` for soon-to-be removed features, `Removed` for removed features,
`Fixed` for bug fixes, `Security` for security improvements.

Versioning: [SemVer](https://semver.org/).

---

## [Unreleased]

## [1.0.0] - 2026-06-16

Milestone release. First feature-complete, security-hardened Vula OS merged to
`main`: email/password + passkey/2FA auth (no third-party OAuth), GPU-accelerated
streaming with adaptive bitrate/resolution and idle/peer-aware encoder lifecycle,
leaderless multi-instance CRDT sync with signed quorum, P2P WebRTC mesh,
rehydration + instance migration, per-account storage selection, anchor inbox,
and the headless `vulos-managed` cloud box image. Mail is fully separated
(lilmail client + vulos-mail server are independent repos). Note: GPU-streaming
and cloud/infra paths are implemented and unit-tested but await verification on
real hardware/live services.

### Fixed

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

## [0.2.0] — 2026-06-15

### Security

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

### Added

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

### Changed

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

### Removed

- `backend/services/webbrowser/` — server-side Chromium streaming (BROWSER-02)
- `backend/services/isolatedbrowser/` — Isolated/Disposable Browsing (RBI)
  stub removed
- `vulos-relay` mail-delivery daemon — retired; circuit relay
  (`vulos-cloud/backend/circuit`) handles WebRTC TURN, not mail delivery
- Connected Accounts panel (`src/core/settings/ConnectedAccountsPanel.jsx`)
  and OAuth provider routes — no OAuth in Vulos

---

## [0.1.2] — 2026-05-26

### Added

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

## [0.1.1] — 2026-05-10

### Added

- Peering: Ed25519 identity, signed canonical-JSON envelopes, server-to-server
  messaging, media transfer, WebRTC voice/video signaling, Drop (AirDrop-style)
- Multi-user with per-user Linux accounts and profile isolation
- AI Router: Ollama default, multi-provider (Claude, OpenAI, OpenAI-compatible),
  chat history, embeddings, sandbox Python execution

### Fixed

- Live-USB bootable ESP fix (BMINIT-14)

---

## [0.1.0] — 2026-04-18

### Added

- Initial public release
- Web-native window manager (React 19, Tailwind CSS 4, Vite)
- Go backend — single binary, 24 services, 110+ API endpoints
- GStreamer/WebRTC streaming for native Linux GUI apps and games
- App store with apt/Flatpak recipes, isolated network namespaces
- Bare-metal image builder (`build.sh`) producing signed squashfs images
- Docker image for `linux/amd64` and `linux/arm64`
- CI (build, vet, test, gofmt, Docker) and release pipeline (tag-triggered)

[Unreleased]: https://github.com/vul-os/vulos/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/vul-os/vulos/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/vul-os/vulos/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/vul-os/vulos/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/vul-os/vulos/releases/tag/v0.1.0
