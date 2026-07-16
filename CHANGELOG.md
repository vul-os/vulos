# Changelog

All notable changes to Vulos Management are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Planned

- Extract the Go control plane out of `vulos-cloud` into public `pkg/...`
  packages in this repo, per [`docs/EXTRACTION-PLAN.md`](docs/EXTRACTION-PLAN.md).
- Land the `BillingProvider` and `StorageProvisioner` seam interfaces with their
  no-op / BYOB defaults.
- Publish admin console screenshots once the extracted binary runs
  standalone.

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
  Management docs: `SELF-HOST.md`, `DEPLOY-CP.md`, `DEPLOY-RELAY.md`, and
  the admin console doc.
- **`docs/ARCHITECTURE.md`** — the two-repo split, the `BillingProvider` seam
  (no-op default), the `StorageProvisioner` seam (BYOB default), and the Go
  module strategy by which `vulos-cloud` consumes this repo as a library.
- **`docs/EXTRACTION-PLAN.md`** — a detailed analysis of which control-plane
  packages move here versus stay private in `vulos-cloud`, the seam boundary,
  and the module/wiring mechanics. Analysis only; no code moved yet.
- **Versioning** — this `CHANGELOG.md` and a `VERSION` file.

### Notes

- No control-plane Go code is in this repo yet; the extraction from `vulos-cloud`
  is planned and documented but not executed.
- Nothing here contains production secrets, runbooks, or internal package maps —
  those remain in the private `vulos-cloud` repo.

[Unreleased]: https://github.com/vul-os/vulos-management/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/vul-os/vulos-management/releases/tag/v0.1.2
[0.1.1]: https://github.com/vul-os/vulos-management/releases/tag/v0.1.1
