# Superadmin operator console

> Part of [vulos-management](https://github.com/vul-os/vulos-management), the OSS
> control plane. The console and its access gate ship in this repo. Most pages
> (Accounts, Orgs, Fleet, Relay & usage, Incidents, Reserved handles, Migrations,
> Audit log) are fully OSS. The **billing-adjacent pages — Pricing, Regions, and
> Billing recon — render commercial pricing/COGS data and are only meaningfully
> populated in the private `vulos-cloud` build**, which injects a Paystack
> `BillingProvider`; in a pure vulos-management deployment with the no-op billing
> provider they show the free/empty catalog. The package layout below refers to
> the control-plane Go module (`cmd/server`, `internal/superadmin`); once the
> extraction lands (see [EXTRACTION-PLAN.md](EXTRACTION-PLAN.md)) the OSS packages
> move to public import paths in this repo.

The superadmin console (`/superadmin/*`) is the server-rendered HTML operator
surface for the whole deployment. It is deliberately **not** a React app: it is the
highest-value target in the system (it changes what customers are charged, where
their machines run, and who has access), so it gets the hardened, minimal,
no-client-framework treatment — strict CSP (`script-src 'self'`, no inline JS),
CSRF-protected POSTs, audit-logged mutations, and a triple gate on every request.

Code lives in `internal/superadmin/` (pages, stores, templates) with the data
assemblers that need other stores in `cmd/server/wire_superadmin*.go`.

## Access gate (fail-closed)

Every `/superadmin/*` page and every `/api/superadmin/*` endpoint passes through
`RequireSuperAdmin` (`middleware.go`), which enforces, in order and failing
closed at each step:

1. **IP allowlist** — `VULOS_ADMIN_IP_ALLOWLIST` (comma-separated CIDRs). Empty
   in prod is a fatal startup error; the client IP is read from `Fly-Client-IP`
   (never the forgeable `X-Forwarded-For`).
2. **Main session** — a valid Vulos session cookie.
3. **Super-admin status** — an active row in `superadmins`.
4. **Admin session** — a separate short-lived (`vulos_admin_session`, 15-min
   idle / 4-hour absolute) cookie minted only after a **WebAuthn hardware-key**
   step on top of password + TOTP.

Every allowed or denied request is written to the tamper-evident audit log. State-
changing HTML POSTs additionally pass `CSRFProtect` (double-submit token). New
console pages reuse this existing gate — no page invents its own auth.

## Pages

| Page | Route | Data source |
| --- | --- | --- |
| Dashboard | `GET /superadmin/` | `users`/`superadmins` counts + fleet billing cockpit + recent audit |
| Accounts | `GET /superadmin/accounts`, `.../{id}` | `users`, `sessions`, `billing_transactions`; suspend/reactivate/2FA-reset/force-reset via confirm flow |
| Orgs | `GET /superadmin/orgs`, `.../{id}` | orgadmin store (via seam) |
| **Fleet** | `GET /superadmin/fleet` | managed `SQLStore.ListRunning` priced from the provider registry (read-first) |
| **Relay & usage** | `GET /superadmin/relay` | `relayusage` per-account GB + routing-store PoP health per region |
| Billing recon | `GET /superadmin/billing-recon` | billing store; per-tenant COGS vs revenue drift |
| Pricing | `GET/POST /superadmin/pricing` | billing catalog (box-model SKUs) |
| Regions | `GET/POST /superadmin/regions` | billing regions + Fly cost poller |
| **Providers** | `GET/POST /superadmin/providers` | `provider_registry` (see below) |
| **Incidents** | `GET/POST /superadmin/incidents/*` | `internal/status` store (what the public status page reads) |
| Reserved handles | `GET/POST /superadmin/reserved-handles` | `additional_reserved_handles` |
| Migrations | `GET /superadmin/migrations` | polled product migration-status endpoints |
| Audit log | `GET /superadmin/auditlog` | `auditlog_entries` (searchable by actor/action/date) |

### Accounts — impersonation

Account impersonation ("log in as this user for support") is **intentionally not
built**: there is no safe existing mechanism for it, and a superadmin console is
the wrong place to grow an account-takeover primitive. Support actions are limited
to the audited, reversible operations already present (suspend/reactivate, force
password reset, 2FA reset, refund).

### Fleet — read-first

The fleet page shows the boxes the cloud is running right now (owner, provider,
region, state, kind, age, estimated $/hr). It exposes **no lifecycle buttons**:
destroying or migrating someone's box is an account-affecting action, and it lands
here only once the provisioning adapter (which owns the provider registry) is
wired to the console. Costs are estimated from the cheapest active provider lane
in each region.

### Incidents

The public status page already reads `GET /api/status/incidents`. The incidents
page is the HTML author surface over the same `internal/status` store: open an
incident, post timeline updates, resolve it, and schedule maintenance windows.
Every mutation is CSRF-protected and audit-logged (`incident.create`,
`incident.update`, `incident.resolve`, `maintenance.schedule`).

## Provider registry (`provider_registry`)

The registry is the operator-editable catalogue of compute/relay **lanes** the
cloud can place managed boxes on. Each row is one `(provider, region)` lane.

Migration: `internal/superadmin/migrations/0002_provider_registry.{postgres,sqlite}.sql`
— an **additive, CREATE-only** baseline (it adds one table and alters nothing
0001 created; applying 0001+0002 to a fresh DB yields 0001's schema plus this
table). Both dialects carry the same column set (proved by
`TestProviderRegistry_SchemaEquivalent`).

| Column | Meaning |
| --- | --- |
| `id` | operator slug, e.g. `fly-fra` (primary key; re-submitting edits) |
| `provider` | `fly`, `hetzner`, `vultr`, … |
| `region` | provider region slug, e.g. `fra`, `jnb` |
| `cost_per_hour_usd_cents` | what the provider charges us per running hour |
| `capacity` | max concurrent boxes we will place on this lane |
| `in_use` | current placements — **maintained by the provisioning adapter (item 2)**, 0 until then |
| `ip_type` | `static` or `dynamic` |
| `is_relay` | relay-capable PoP lane (egress is priced) |
| `egress_cost_cents_gb` | what the provider charges us per GB egress |
| `relay_price_cents_gb` | what we charge per GB relay egress |
| `active` | `0` = withdrawn (no new placement) |
| `updated_at` / `updated_by` | RFC3339 UTC + superadmin email of last editor |

### Validation (fail-closed)

`UpsertProvider` refuses to write on any of: missing id/provider/region; an
`ip_type` other than `static`/`dynamic`; a negative cost/capacity/price; and — the
money-leak guard mirrored from the regions console — a **relay lane whose price is
at or below its egress cost** (we never resell bandwidth below what the provider
charges us). Non-relay lanes price no egress, so that guard does not apply to them.

### Placement policy

`PlacementPolicy` computes, per region, the lane the provisioning adapter would
choose right now: the **cheapest active lane with spare capacity** (`in_use <
capacity`). The console renders this as a placement preview so an operator can see
where the next box in each region lands — and spot a region whose only lanes are
full or withdrawn *before* a provision fails.

### What wires on later (item 2)

This registry + its validation + its admin surface is the **groundwork**. The real
provisioning adapters (Fly-first live, Hetzner optional) wire onto it separately:
they read a lane via the placement policy, provision on that provider, and maintain
`in_use`. This console builds and validates the catalogue; it does not itself
provision.

## Audit

Every mutating admin action writes an `auditlog_entries` row via the shared
`auditlog.Logger` (e.g. `provider.upsert`, `provider.delete`, `region.upsert`,
`pricing.set`, `incident.*`, `maintenance.schedule`, `admin.account.*`). The audit
log page is a searchable view over that table; the chain is verifiable from the
maintenance page.
