# Changelog

All notable changes to Vulos Management are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Planned

- Wire the remaining operational route groups — device enrollment (RFC-8628),
  OS routing + org/box directory, the relay autoscaler + PoP fleet, the admin
  and org-admin consoles, and the storage/files surface — into `cmd/server`'s
  default `cproutes.RegisterOperational` call, via the same `RouteRegistrar`
  hook the composition root already exposes. The Go packages and their route
  handlers already live in this repo (`pkg/fleet`, `pkg/routing`, `pkg/enroll`,
  `pkg/superadmin`, `pkg/orgadmin`, `pkg/storage`, and their `pkg/cproutes/*`
  handler files); see [`docs/SELF-HOST.md`](docs/SELF-HOST.md#whats-wired-in-today)
  for exactly what the self-host binary serves today versus what still needs a
  registrar.
- Publish admin console screenshots once its route group is wired into the
  self-host binary by default.

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

[Unreleased]: https://github.com/vul-os/vulos-management/compare/v0.1.3...HEAD
[0.1.3]: https://github.com/vul-os/vulos-management/releases/tag/v0.1.3
[0.1.2]: https://github.com/vul-os/vulos-management/releases/tag/v0.1.2
[0.1.1]: https://github.com/vul-os/vulos-management/releases/tag/v0.1.1
