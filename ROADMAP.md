# Roadmap

Where the open-source Vulos control plane is headed. For what already shipped,
see [CHANGELOG.md](CHANGELOG.md). This tracks `vulos-management` only — the
self-hostable control plane + admin console. Commercial-only work (billing,
managed provisioning, the marketing site) lives in the private `vulos-cloud`
repo and isn't listed here.

## Now (v0.2.0)

- **Console SPA** — the React `/console` app (sign-in + onboarding, the user
  console, and an opt-in operator/admin section) is embedded in the Go binary
  and served by every self-host deployment.
- **Multi-provider social sign-in** — Google / Microsoft / GitHub / Discord
  linked sign-in, deployment-configured.
- **`pkg/relayscale`** — a pluggable relay-pool scaling seam (manual /
  external / kubernetes / firecracker / proxmox OSS providers) with the same
  seam shape as `BillingProvider` / `StorageProvisioner`.
- A self-host security pass: global middleware chain applied to the bare mux,
  nonce-based CSP, BYO-storage SSRF guard, presign TTL clamp, open-redirect
  rejection, step-up TOTP on the refund endpoint.

## Next

- **Finish wiring the operational surface into the default binary.** The Go
  packages and `pkg/cproutes` handlers already exist; they need a
  `RouteRegistrar` call in `cmd/server`'s composition root:
  - Device enrollment (RFC-8628) + boot-enrollment (`pkg/enroll`)
  - OS routing & directory, DNS plane, CDN, edge (`pkg/osrouter`,
    `pkg/routing`)
  - Third-party data integrations (`pkg/integrations`)
  - Storage / files / account export (`pkg/storage`, `pkg/storagesel`,
    `pkg/files`, `pkg/cloudhome`, `pkg/keydir`, `pkg/residency`)
  - Public status / incidents pages (`pkg/status`, `pkg/cloudstatus`)

  Track exact status in [`docs/SELF-HOST.md`](docs/SELF-HOST.md#whats-wired-in-today).
- **Operator console default posture.** The admin section is opt-in today
  (`VULOS_ENABLE_SUPERADMIN=1` / `VULOS_BOOTSTRAP_SUPERADMIN`) so a
  zero-config self-host stays deny-all. Revisit whether a guided first-run
  bootstrap (rather than an env var) is the safer default UX.
- Harden the OSS relay-scaling providers (kubernetes/firecracker/proxmox)
  for unattended production use, beyond the reference `manual` provider.

## Later

- A first-run setup wizard / bootstrap CLI that walks a new self-hoster
  through DB choice (SQLite vs. Postgres), the first operator account, and
  which optional route groups to enable.
- Admin-console feature parity with the equivalent commercial cloud console
  surfaces (pricing/regions/reconciliation stay cloud-only by design; the
  operational parts should not lag).
- Broader self-host observability (structured audit export, metrics) as the
  remaining route groups land.
