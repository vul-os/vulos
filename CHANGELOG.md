# Changelog

All notable changes to Vulos Management are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Removed

- **Talk/Meet product sweep**: Vulos Talk and Vulos Meet are removed products —
  comms are third-party (Matrix/Element for chat; Element Call/Jitsi for
  video), offered as downloadable OSS apps in the OS app store, never CP
  first-party products. Closed out the remaining live references left over
  from the 2026-07-15 pivot: `resolver.Service` dropped `ServiceTalk`/
  `ServiceMeet` (the `/api/resolve/backend` and `/backend/{service}` routes no
  longer accept `talk`/`meet`); the storage-presign app whitelist
  (`knownStorageApps`) dropped `"meet"` (Files and the OS gateway remain the
  only two Class-P storage audiences); `orgadmin.UsageResponse` dropped the
  dead `meet_participant_minutes`/`meet_recording_minutes` fields (never
  populated by any production adapter); the superadmin per-account billing
  view (`account_detail.js`) dropped the `meet_*` row/labels. Updated stale
  doc-comments across `pkg/auth`, `pkg/cproutes`, `pkg/customdomain`,
  `pkg/servingpool`, and test literals that still named Talk/Meet as if they
  were live app backends. No behavior change beyond narrowing accepted
  audiences/services (fail-closed, not fail-open).

### Added

- **Remaining operational route groups wired into the self-host binary**
  (`registerNetworkOperational`, called from `cproutes.RegisterOperational`):
  device enrollment (RFC-8628 `POST /enroll/{start,poll,approve,deny}` + the web
  wizard `/api/enroll{,/direct}`, `/api/connmode`), the OS routing plane (DNS
  plane, `GET /api/relay/status`, edge, BYO-CDN, multi-location), the third-party
  OAuth data broker (`/api/integrations/*`), the mail key directory
  (`/api/mail/keydir`), the cloud-home directory + peering intake, and the
  storage/files/export + storage-selection + mail-resolver plane. All mount
  against the shared auth store and are **fail-closed** — every group gates on a
  session (or device/shared-secret) gate, and the ones needing a secret
  (`INTEGRATIONS_KEK`, `CLOUDHOME_KEK`, managed `S3_*` creds) refuse/`503`
  rather than fail open. The `fleet` + `routing` stores are opened once and
  shared so an enrolled device row/binding is visible to the console views. The
  management binary is now functionally complete for self-hosting; the only
  unmounted surface is the commercial box-billing read and library packages with
  no route handler (`pkg/status`, `pkg/osrouter`, `pkg/residency`). Regression
  tests in `pkg/cproutes/wire_operational_extra_test.go` assert each group is
  mounted (never 404) and authz-gated (never a 2xx fail-open).
- **Opt-in migration checksum reconcile** (`VULOS_MIGRATE_RECONCILE_CHECKSUMS=1`):
  a controlled escape hatch for deploying an intentional clean-baseline migration
  fold against an EXISTING database that cannot be reset (e.g. production). When
  set, `cpdb`'s migration runner backfills a drifted migration's recorded
  checksum to the current file — accepting the changed content WITHOUT re-running
  its DDL — and logs a loud warning, instead of failing closed and crash-looping
  the control plane on boot. The default (env unset) keeps the strict,
  fail-closed drift guard so an *accidental* edit to an applied migration is
  still caught. (`pkg/cpdb/migrate.go`, regression test in
  `pkg/cpdb/migrate_runner_test.go`.)

### Fixed

- **Product mini-site landing pages rendered blank** in production: the apex SPA
  handler stamped the strict SPA CSP (`frame-ancestors 'none'`) plus
  `X-Frame-Options: DENY` onto the self-contained `landing.html` files, which the
  marketing site embeds in a **same-origin `<iframe>`** — so the browser refused
  to render them. First-party static `*.html` sub-pages are now served with a
  framable, inline-friendly `SelfContainedPageCSP` (`frame-ancestors 'self'` +
  `X-Frame-Options: SAMEORIGIN`); the SPA shell and hashed assets stay strict.
  (`pkg/cproutes/spa.go`, regression test in `pkg/cproutes/spa_csp_test.go`.)

### Planned

- Give the durable status store (`pkg/status`) and the OS-router library
  (`pkg/osrouter`) their own `pkg/cproutes` route groups so a *public*
  status/incidents page and the hostname→box resolver are self-host-mountable;
  today they are library packages with no operational HTTP handler. See
  [`docs/SELF-HOST.md`](docs/SELF-HOST.md#whats-wired-in-today) for exactly
  what the self-host binary serves today.

## [0.2.0] - 2026-07-17

This release is the console + auth + provisioning wave: the control plane
grows its own React console SPA (auth, user console, and an opt-in operator
admin section), a multi-provider social-login overhaul, a generic pluggable
relay-scaling seam, and a security hardening pass across the self-host mux,
storage routes, and OAuth redirects.

### Added

- **The Vulos Workspace console SPA** (`web/`) — a self-contained React app
  the Go binary embeds and serves at `/console`, talking to the same
  `pkg/cproutes` JSON APIs the control plane exposes:
  - **Auth & onboarding** — the sign-in / auth / onboarding pages, PoW +
    passkey + social sign-in client, and the route guard
    (`26b1ee5`, `44cd6cc`, `ec43706`).
  - **User console** — fleet, devices, account status, developer keys, audit
    trail, and privacy tooling, wired to the live `cproutes` handlers
    (`4a5a416`, `2696f3f`).
  - **Operator (admin) console** — a React admin section (Dashboard,
    Accounts, Audit, Security) and the JSON admin API it consumes under
    `/api/superadmin/*`; opt-in via `VULOS_ENABLE_SUPERADMIN=1` /
    `VULOS_BOOTSTRAP_SUPERADMIN`, deny-all (`403`) by default
    (`4697335`, `dd277f8`).
  - **Build-time commercial seam** (`@vulos/commercial-seam`) — every
    billing/pricing/provisioning widget resolves to a NoOp slot in this OSS
    build (`() => null`, `commercial.enabled === false`); the private cloud
    build aliases the same import to a real implementation. Byte-identical
    app code in both builds (`5ce25e7`, `e05a795`).
  - Button/Card/Pill/Stat UI primitives and an instrument-panel shell
    (sans-serif UI, monospace data) (`e05a795`).
  - WebAuthn first-passkey enrolment for bootstrap operators (`11521fc`).
  - Social-login callback redirects land inside `/console` (`228ebbc`).
- **`registerConsoleOperational`** (`pkg/cproutes/register_all.go`) — mounts
  the operational route groups the console SPA calls (fleet + invite-accept,
  account/cloud/support/per-cell status, compliance/privacy, org-admin +
  gated product catalogue, developer webhooks + MCP) into the default
  self-host binary, each against its own `cpdb` store with an in-memory
  fallback so the console always renders (`dd277f8`).
- **`pkg/relayscale`** — a generic, pluggable relay-pool scaling seam
  mirroring the `BillingProvider`/`StorageProvisioner` pattern: a pure
  `Policy` (desired per-region relay count from load + demand), a
  `RelayProvisioner` interface (`Provision`/`Destroy`/`List`), an `Actuator`
  that reconciles desired vs. actual, OSS providers (manual / external /
  kubernetes / firecracker / proxmox), and a demand API
  (`GET /api/relay/scale/demand`, `POST /api/relay/scale/observe`, the
  observe path `CP_SHARED_SECRET`-gated). The managed multi-provider is
  injected by the commercial layer through `cpserver.Deps.RelayProvisioner`,
  bypassing the registry — the OSS default provisions nothing
  (`471b498`; see [`docs/RELAY-SCALING.md`](docs/RELAY-SCALING.md)).
- **Multi-provider social login** — "Sign in with GitHub / Discord" alongside
  the existing Google/Microsoft linked sign-in, an email-force option, and
  multi-provider account linking (`94c97f7`).
- Console screenshot gallery expanded to cover the real React SPA (sign-in,
  boxes, devices, developer, account status, audit log) and the operator
  console (dashboard, security), replacing the earlier Go-template shots
  (`788a37d`, `07410c9`, `d362ae9`, `bcf87cf`, `509f9fd`, `d694959`,
  `f40257f`).

### Changed

- **De-commercialized the operator console** to an instrument-panel design
  base with consistent page headers and branded sign-in across every page,
  and dropped dead commercial-stats/model-variance packages that had no
  self-host purpose (`d0a06d4`, `02e7819`, `fb7579b`, `cf6c63e`, `b759e15`).
- **`docs/SELF-HOST.md`** rewritten as a single unified guide with a
  one-command install orchestrator, and its "what's wired in today" table
  updated to match `pkg/cproutes/register_all.go` exactly (`607d52d`).
- **Genericized remaining vendor-specific naming** in the OSS seams — the
  payment-processor naming in `billingport`, the `S3_*` env-var naming in the
  BYO storage guidance, and an empty sub-processor list shipped by default in
  the OSS control plane (no vendor commitment baked into self-host)
  (`a25a2d4`, `0ecb6f0`, `47a7ea4`).
- **Stripped Mail-as-a-product framing** from the OSS control-plane docs —
  Mail is not a Vulos-management product surface (`0912311`, `f7c22da`).
- **Vulos is no longer an OAuth2/OIDC provider.** The "Sign in with Vulos"
  IdP role (issuing `id_token`s to third-party apps) was removed from the
  control plane; Vulos remains an account authority + linked-sign-in client
  of Google/Microsoft/GitHub/Discord, never an issuer (`f7c22da`).
- Storage bucket provisioning now always routes through the `storageport`
  seam — management itself never provisions a bucket (`3b29062`).

### Security

- **H1** — Fixed a `Fly-Client-IP` spoofing hole in the superadmin allowlist
  and aligned the auth rate-limiter's client-IP source to the same
  trusted-edge resolution (`a075850`).
- **H2** — Guarded the BYO storage endpoint against SSRF, and stopped
  reflecting raw BYO-upstream errors back to the caller (`2614b13`,
  `b02d062`).
- **M1** — Wrapped the self-host mux in the global DDoS/security middleware
  chain; previously those layers were registered but never applied to a
  standalone binary's bare mux (`e66764c`).
- **M2** — Strict nonce-based CSP for the console SPA: dropped
  `script-src 'unsafe-inline'`, added `object-src 'none'` (`abbbc9d`).
- **M3** — Enforced step-up TOTP on the JSON refund endpoint (`1eddfae`).
- **M4** — Reject backslash/protocol-relative open redirects in the OAuth
  `?return=` parameter (`7e2e75b`).
- **M5** — Presign TTL is now clamped to a maximum (`b02d062`).
- **L1** — Legacy presign path-traversal attempts are rejected (`b02d062`).

## [0.1.4] - 2026-07-17

### Fixed

- **External builds of this module were broken.** The broad `data/`
  `.gitignore` rule excluded `pkg/security/data/legit_ja3.txt`, which
  `pkg/security/bot.go` embeds via `//go:embed data/legit_ja3.txt`. The file
  existed only on developer disks, so local builds via a `replace` directive
  worked, but the tagged module (`v0.1.1`..`v0.1.3`) failed to build for any
  external consumer with `pattern data/legit_ja3.txt: no matching files
  found`. Committed the required embed asset (`4ef4678`).

## [0.1.3] - 2026-07-16

The operational control plane itself — previously documented only as a plan —
is now extracted and living in this repo.

### Added

- **`pkg/cproutes`** — the operational route handlers moved from `vulos-cloud`:
  auth (registration, sessions, 2FA, WebAuthn, OAuth sign-in), account
  recovery, developer & LLM API keys, mobile push, DDoS/abuse/security,
  legal pages, the public product catalogue, boot endpoints, and the SPA
  fallback, plus a long tail of handler files (fleet, enrollment, routing,
  storage, DNS plane, CDN, edge, compliance, cloud-home, keydir, residency,
  telemetry, …) staged for wiring.
- **`pkg/cpserver`** — the deployment-agnostic `cpserver.New(cfg, deps)`
  composition root: a host-neutral `Config`, a `Deps` struct of injectable
  provider seams (`Billing`, `Entitlements`, `StorageProvisioner`) that default
  to the free no-op/BYOB implementations, and a `RouteRegistrar` hook so a
  commercial build can append its own route set without `cpserver` importing
  it.
- **`pkg/cproutes.RegisterOperational`** — the composition root `cmd/server`
  calls today. It mounts the core operational surface (see
  [`docs/SELF-HOST.md`](docs/SELF-HOST.md#whats-wired-in-today)) against the
  injected seams and never opens a commercial store or charges money.
- **`cmd/server`** — the self-host main. Reads generic env config, leaves the
  provider seams nil (accepting the free no-op billing rail and
  bring-your-own-bucket storage), and serves the control plane via `cpserver`.
- **`internal/archtest`** — `TestNoManagementPackageImportsCommercialBilling`,
  a `go list -deps ./...` guard that fails the build if any package in this
  module ever imports the private `vulos.cloud/cp` module.
- **De-vendored storage** — the Tigris-specific storage implementation stayed
  in `vulos-cloud`; `pkg/storage` in this repo names no vendor.

### Changed

- `billingport.EntitlementResolver` widened to cover the entitlement checks the
  newly-migrated routes need, still defaulting to the unlimited
  `NewNoopResolver()` for self-host.

## [0.1.2] - 2026-07-16

### Changed

- Renamed the operator console from "superadmin console" to **admin console**
  throughout the user-facing docs (`README.md`, `docs/ARCHITECTURE.md`,
  `docs/SELF-HOST.md`, this changelog). Literal code identifiers that still carry
  the `superadmin` name — the `/superadmin/*` route prefix, the
  `internal/superadmin` package, the `RequireSuperAdmin` gate, and the
  `superadmins` table — are preserved for accuracy and called out as code tokens.
- Renamed `docs/SUPERADMIN-CONSOLE.md` → `docs/ADMIN-CONSOLE.md` and updated all
  links and references.

## [0.1.1] - 2026-07-16

Initial public bootstrap of the open-source (MIT) Vulos control-plane repo.

### Added

- **Repo bootstrap** — `README.md`, `LICENSE` (MIT), and `.gitignore` (Go + Node
  + common) for the open-source control plane.
- **Brand** — self-hosted Vulos logo assets (`assets/vulos-logo.png` plus a
  teal `assets/vulos-logo-dark.svg` dark-theme variant) used in a theme-aware,
  centered README header. No hotlinked or broken images.
- **High-craft README** — centered logo header, value prop, badge row (MIT, Go
  1.26, self-hosted, tests, release), feature grid, a mermaid architecture
  diagram covering the two-repo split plus the `BillingProvider` and
  `StorageProvisioner` seams, a self-host quickstart, and "self-host is free /
  cloud is optional" framing.
- **Operational docs** in `docs/`, adapted from `vulos-cloud` to read as
  Management docs: `docs/SELF-HOST.md` and the admin console doc
  (`docs/SUPERADMIN-CONSOLE.md`, later renamed `docs/ADMIN-CONSOLE.md` in
  0.1.2).
- **`docs/ARCHITECTURE.md`** — the two-repo split, the `BillingProvider` seam
  (no-op default), the `StorageProvisioner` seam (BYOB default), and the Go
  module strategy by which `vulos-cloud` consumes this repo as a library.
- **Versioning** — this `CHANGELOG.md` and a `VERSION` file.

### Notes

- No control-plane Go code is in this repo yet; the extraction from `vulos-cloud`
  is planned and documented but not executed.
- Nothing here contains production secrets, runbooks, or internal package maps —
  those remain in the private `vulos-cloud` repo.

[Unreleased]: https://github.com/vul-os/vulos-management/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/vul-os/vulos-management/compare/v0.1.4...v0.2.0
[0.1.4]: https://github.com/vul-os/vulos-management/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/vul-os/vulos-management/releases/tag/v0.1.3
[0.1.2]: https://github.com/vul-os/vulos-management/releases/tag/v0.1.2
[0.1.1]: https://github.com/vul-os/vulos-management/releases/tag/v0.1.1
