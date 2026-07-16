# Self-host the Vulos control plane

**Run the operational control plane on your own host, with no cloud and no
billing.** This repo (`vulos-management`, MIT) is the complete control plane —
accounts, device enrollment, OS routing, the relay autoscaler + PoP fleet, and a
hardened admin console. Billing lives behind a seam whose **default is a no-op**
(usage is metered and visible, but nothing is ever charged and nothing leaves
your host). Storage is **bring-your-own-bucket** — the control plane never
provisions object storage.

The only thing the private `vulos-cloud` layer adds on top is a commercial
billing provider, a managed bucket provisioner, a few billing-only admin panels,
and the hosted marketing site. **The control plane is fully functional without
any of that.**

---

## Requirements

- **Go 1.26+** to build.
- **(Optional) Postgres** for a durable production database. With no database
  configured the control plane runs on a local SQLite database — perfect for
  evaluation and small deployments.
- **(Optional) An S3-compatible object store** (MinIO, Ceph, or any cloud object
  storage) if you want durable object storage. Bring the bucket yourself; the
  control plane serves it but never creates it.

## Build and run

```sh
make build      # produces ./bin/cp
./bin/cp        # serves the control plane on :8080
```

`make build` is a thin wrapper over `go build -o bin/cp ./cmd/server`. The binary
is the **thin self-host main**: it reads generic configuration from the
environment, wires the free no-op billing seam and bring-your-own-bucket storage
seam, and starts the control plane via the `pkg/cpserver` builder.

Verify it is up:

```sh
curl -s localhost:8080/healthz   # {"status":"ok"}                 — liveness
curl -s localhost:8080/readyz    # {"status":"ready"}              — DB reachable
curl -s localhost:8080/version   # {"billing_rail":"noop", ...}    — never charges
```

The `"billing_rail":"noop"` in `/version` is your confirmation that this
deployment has **no** payment provider wired: it records usage but cannot charge.

## Configuration

All configuration is generic and host-neutral — plain environment variables, no
provider-specific knowledge. Everything has a sensible self-host default.

| Variable | Default | Meaning |
|---|---|---|
| `CP_ADDR` | `:8080` | TCP listen address. |
| `VULOS_DOMAIN` | `vulos.org` | Your deployment's own domain — identity emails, cookie scope, CT zone. Set this to your domain. |
| `DATABASE_URL` | *(empty)* | Postgres DSN. **Empty ⇒ local SQLite** (the zero-config default). |
| `CP_SQLITE_PATH` | *(empty)* | On-disk SQLite path used when `DATABASE_URL` is empty. Empty ⇒ in-memory (non-durable; dev/smoke only — set a path for anything you want to keep). |
| `CP_ENV` | `dev` | Free-form environment label surfaced on `/version`. |
| `CP_VERSION` | `dev` | Build/version string surfaced on `/version`. |

### SQLite vs Postgres

- **SQLite (default):** set nothing, or set `CP_SQLITE_PATH=/var/lib/vulos/cp.db`
  for durability. Ideal for evaluation and single-host deployments.
- **Postgres (production):** set `DATABASE_URL=postgres://user:pass@host:5432/cp`.
  The control plane runs its migrations on start. The commercial pricing/billing
  tables are **absent** on a self-host database — every metered path is simply
  free.

### A durable self-host example

```sh
export VULOS_DOMAIN=vulos.example.com
export DATABASE_URL=postgres://vulos:secret@db.internal:5432/cp
export CP_ADDR=:8080
export CP_ENV=prod CP_VERSION=$(cat VERSION)
./bin/cp
```

Put the binary behind your own TLS-terminating reverse proxy (Caddy, nginx,
Traefik) for an internet-reachable deployment. The control plane speaks plain
HTTP and expects the proxy to handle TLS and forward the client IP.

## What you get

Once running, the control plane provides:

- **Accounts & auth** — registration, sessions, TOTP 2FA, WebAuthn hardware
  keys, OPAQUE password auth, and linked OAuth sign-in.
- **Device enrollment** — the RFC-8628 device-authorization flow so a box or
  headless device enrolls against your control plane and mints short-lived,
  audience-bound tokens.
- **OS routing & directory** — resolves your OS hostname to the best box in your
  cluster and tracks who owns what, where.
- **Relay autoscaler & PoP fleet** — a point-of-presence registry with
  heartbeats and health flags, failover routing, and an autoscaler that grows
  and shrinks the fleet against a provider registry you configure.
- **Admin & org-admin consoles** — a hardened, server-rendered operator surface
  (see [ADMIN-CONSOLE.md](ADMIN-CONSOLE.md)) and per-organization administration.
- **Status pages** — a public status/incidents surface plus per-user status.

## The seams (free by default)

Self-hosting is **metered but free** and **bring-your-own-bucket**. Two seams are
the only intentional holes, and both ship with free defaults:

| Seam | Self-host default | What it means |
|---|---|---|
| `BillingProvider` | **No-op** | Usage is recorded and visible; nothing is charged; no network call is ever made. |
| `StorageProvisioner` | **Bring-your-own-bucket** | The control plane never creates buckets. Point it at a bucket you already run. |

A commercial distributor injects real implementations into these same interfaces
at composition time — see [ARCHITECTURE.md](ARCHITECTURE.md). No package in this
module imports a payment processor or a bucket provider directly; a boundary test
enforces it.

## Mail and other products

This repo is the **control plane**, not a mail server or an application suite.
Mail, chat, documents, and similar products are **bring-your-own connectors**:
the control plane authenticates users and points routing at your boxes, and you
run whichever application backends you choose behind it. There are no first-party
application products bundled in this module.

## Upgrading

Pull, rebuild, restart:

```sh
git pull
make build
./bin/cp
```

Database migrations run automatically on start. On SQLite this is transparent; on
Postgres the migration runner applies any new baselines idempotently.
