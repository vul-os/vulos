# Architecture — the two-repo split, the `cpserver` builder, and the seams

Vulos ships as two repositories that build one control plane. This document
explains the split, the deployment-agnostic `cpserver` builder, the provider
seams that make it work, and the Go module mechanics that let the private cloud
repo consume this OSS repo as a library.

## The dividing line

There is exactly one rule:

> **If a self-hoster needs it to run their deployment → it lives in
> vulos-management (OSS, MIT). If it exists only because we charge money → it
> lives in vulos-cloud (private).**

| | **vulos-management** (this repo) | **vulos-cloud** (private) |
|---|---|---|
| License | MIT | Proprietary |
| Role | The complete operational control plane + admin console, deployment-agnostic | The thin deployment + commercial wrapper |
| Ships | accounts / auth / 2FA / OAuth sign-in, device enrollment (RFC-8628), OS routing + org/box directory, relay autoscaler + PoP fleet, admin + org-admin console, status pages, storage plane, infra plumbing, **the seam interfaces + free defaults**, and the `cpserver` builder | a commercial `BillingProvider` impl, commercial pricing/catalog, a managed bucket provisioner, billing-only admin panels, the hosted marketing site, **plus the deployment specifics** (Dockerfiles, host/deploy config, env/secret loading) |
| Runs standalone? | Yes — fully functional, metered-but-free | No — a thin main that imports this module and injects the commercial providers |

The important consequence: **there is no forked control plane.** The OSS control
plane is the production control plane. vulos-cloud does not re-implement routing,
auth, or the fleet — it imports them and injects providers.

## Package layout

Everything vulos-cloud must keep importing is **public** under `pkg/...` (Go
forbids importing another module's `internal/...`). The operational domains:

- **Accounts / auth / tokens** — `auth`, `apikeys`, `apptoken`, `devicelink`, `secx`
- **Device & OS enrollment** — `enroll`, `ota`, `sshrec`, `lancert`, `anchorinbox`, `contentseal`, `backup`
- **Identity / OAuth broker** — `integrations`, `oauthclient`, `oauthprovider`, `oauthfosite`, `idp`, `idents`, `keydir`, `profile`, `onboarding`
- **OS routing & directory** — `osrouter`, `routing`, `resolver`, `region`, `multiloc`, `georoute`, `residency`, `edge`, `customdomain`, `dnsplane`
- **Relay autoscaler & PoP fleet** — `relayusage`, `servingpool`, `fleet`, `turn`, `signaling`, `streamsession`, `syncrz`, `ha`, `scheduler`
- **DDoS / abuse / security** — `ddos`, `abuse`, `security`
- **Consoles & status** — `superadmin`, `orgadmin`, `status`, `cloudstatus`, `support`, `webapp`
- **Storage plane** — `storage`, `storagesel`, `cloudhome`, `files`
- **Infra plumbing** — `cpdb`, `secrets`, `kms`, `env`, `httpx`, `middleware`, `obs`, `telemetry`, `audit`, `auditlog`, `notify`, `mobilepush`, `webhooks`, `publicapi`
- **Seams & builder** — `billingport`, `storageport`, `cpserver`

## The `cpserver` builder — Config in, control plane out

The whole split turns on one deployment-agnostic builder:

```go
srv, err := cpserver.New(cfg cpserver.Config, deps cpserver.Deps) // (*Server, error)
err = srv.Run(ctx)                                                // serve until ctx cancelled
```

- **`Config`** is generic and **host-neutral** — an address, a domain, a database
  DSN, an environment label, a version. It is populated from plain env/flags/file
  by whoever builds the server and carries **no** provider-specific knowledge (no
  hosting provider, no payment processor, no storage vendor).
- **`Deps`** carries the injected provider seams and optional collaborators. It is
  a **struct of interfaces** so the set of pluggable providers scales cleanly —
  add a field, default it, expose it on `Runtime`. Nil fields are filled with the
  free self-host defaults.

```go
type Deps struct {
    Billing            billingport.BillingProvider     // default: NoopProvider (never charges)
    Entitlements       billingport.EntitlementResolver // default: NoopResolver (unlimited self-host)
    StorageProvisioner storageport.StorageProvisioner  // default: NoopProvisioner (bring your own bucket)
    Logger             *slog.Logger
    Routes             []RouteRegistrar                // feature routes mount through this hook
    OnStart            []func(ctx, *Runtime)           // background jobs (e.g. a billing sweeper)
}
```

`New` opens the configured database, mounts the always-on operational endpoints
(`/healthz`, `/readyz`, `/version`), then runs each `RouteRegistrar` in order. A
registrar builds handlers against a shared `Runtime` (the DB, the injected seams,
the domain), so the operational route set (this module) and a distributor's
commercial route set (its module) compose **without `cpserver` importing either**.

### Two thin mains, one engine

- **`cmd/server` (this repo)** is the self-host main: it reads generic env
  config, leaves the `Deps` provider fields nil (accepting the free
  no-op/BYOB defaults), and runs the control plane. This is what a self-hoster
  runs on any host.
- **vulos-cloud's `cmd/server`** is the commercial main: it loads its
  host/deploy-specific env, builds the same generic `cpserver.Config`, constructs
  the commercial `Deps` (a real payment rail, a tier/quota resolver, a bucket
  provisioner), and calls the same `cpserver.New(cfg, deps).Run()`. Deployment
  specifics (Dockerfiles, host config, secret loading) stay entirely in cloud;
  the engine is portable.

## The seams

Two provider-agnostic packages define the only intentional coupling points. Both
name **no vendor** and import **no** billing/payment/storage implementation.

### `pkg/billingport`

- **`BillingProvider`** — the payment rail (init / verify / charge / refund /
  webhook-verify), with currency-neutral request/response types (amounts in minor
  units + an explicit currency code). Default: **`NoopProvider`** — never contacts
  a network, never pretends a charge succeeded.
- **`EntitlementResolver`** — resolves an account's tier, included-seat cap, and
  managed-storage quota (`EffectiveTierFor` / `MaxActiveUsersForTier` /
  `CheckStorageQuota`). Default: **`NoopResolver`** — grants an unlimited
  `selfhost` tier, never caps seats, never caps storage. Self-hosting is never
  tier-limited.
- **`RelayUsageSource`** — the read-back interface the resolver uses to enforce
  relay/TURN over-cap behaviour.

### `pkg/storageport`

- **`StorageProvisioner`** — creates managed object-storage buckets. Default:
  **`NoopProvisioner`** — bring-your-own-bucket; `Enabled()` reports false so the
  composition root skips provisioning entirely. **Management never provisions
  buckets** — that is a commercial concern. Serving objects (presign/put/get) is a
  first-class control-plane concern and lives in `pkg/storage`, which a
  self-hoster points at any S3-compatible endpoint.

## How vulos-cloud injects the real implementations

vulos-cloud keeps its commercial impls private and bridges them to the seams in a
single adapter package (`internal/seamadapter`) — the **only** place the two
worlds meet:

```go
deps := cpserver.Deps{
    Billing:            seamadapter.NewBillingProvider(paymentRail),   // wraps the commercial rail
    Entitlements:       seamadapter.NewEntitlementResolver(billing),   // wraps the billing store
    StorageProvisioner: seamadapter.NewStorageProvisioner(bucketProv), // wraps the managed provisioner
    OnStart:            []func(ctx, *cpserver.Runtime){startBillingSweeper},
}
srv, _ := cpserver.New(cfg, deps)
```

The adapter translates the currency-neutral seam types to the commercial impls'
types and normalises sentinel errors. The operational control plane never imports
any of it.

## Go module mechanics

- This module is `github.com/vul-os/vulos-management` (Go 1.26). It inherits the
  sibling `replace`s for `github.com/llmux/llmux` and `github.com/vul-os/openrate`.
- vulos-cloud (`vulos.cloud/cp`) keeps the moved packages out of its tree and adds:

  ```
  require github.com/vul-os/vulos-management v0.x.y
  replace github.com/vul-os/vulos-management => ../../../vulos-management  // co-dev
  ```

- The extraction was a mechanical `internal/ → pkg/` promotion plus an import
  rewrite (`vulos.cloud/cp/internal/X` → `github.com/vul-os/vulos-management/pkg/X`).
  Only import paths changed; no operational logic moved.

## Boundary guards

Two tests keep the split honest:

- **`internal/archtest`** — a module-wide guard: `go list -deps ./...` must never
  reach a package under the commercial module path (`vulos.cloud/cp`). Because
  that module is not even a dependency of this one, a violation would require
  someone to add a `require` and import it — exactly the regression to catch.
- **`pkg/idp/boundary_test.go`** — the identity/login boundary stays minimal and
  must not transitively import `billingport`, `superadmin`, or `orgadmin`, so a
  login never shares fate with the entitlement seam or the admin consoles.

Please keep them green: route any new billing/quota decision through
`pkg/billingport`, and any bucket creation through `pkg/storageport` — never
import a payment processor or a bucket provider into a package in this module.
