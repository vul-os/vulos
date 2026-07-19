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
make dev        # = make build && VULOS_ENV=local ./bin/cp — serves on :8080
```

`make build` (`go build -o bin/cp ./cmd/server`) produces the binary; `make dev`
also sets `VULOS_ENV=local` before running it. That env var matters: the control
plane **fails safe to production posture whenever `VULOS_ENV` is unset**, and
prod posture refuses to start without a real `SESSION_SECRET` (and several other
provider secrets). A bare `./bin/cp` with no environment therefore exits
immediately with `SESSION_SECRET is unset in prod — refusing to start` — that is
the guard working as intended, not a bug, but it means **`./bin/cp` alone is not
the self-host quickstart.** `make dev` (or manually exporting `VULOS_ENV=local`)
is. See [the `VULOS_ENV` note below](#a-note-on-vulos_env) for the full story and
`make run` / `VULOS_ENV=prod` for a real deployment.

The binary itself is the **thin self-host main**: it reads generic configuration
from the environment, wires the free no-op billing seam and bring-your-own-bucket
storage seam, and starts the control plane via the `pkg/cpserver` builder.

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
| `VULOS_ENV` | *(unset ⇒ prod posture)* | **The important one.** `local`\|`dev`\|`prod`. Governs the fail-safe-to-prod guards (`SESSION_SECRET`, KEKs, push credentials): unset or `prod` refuses to boot without real secrets; `local`/`dev` fall back to dev-mode defaults so you can evaluate the binary with zero secrets configured. `make dev` sets this to `local` for you. See [the note below](#a-note-on-vulos_env). |
| `CP_ADDR` | `:8080` | TCP listen address. |
| `VULOS_DOMAIN` | `vulos.org` | Your deployment's own domain — identity emails, cookie scope, CT zone. Set this to your domain. |
| `DATABASE_URL` | *(empty)* | Postgres DSN. **Empty ⇒ local SQLite** (the zero-config default). |
| `CP_SQLITE_PATH` | *(empty)* | On-disk SQLite path used when `DATABASE_URL` is empty. Empty ⇒ in-memory (non-durable; dev/smoke only — set a path for anything you want to keep). |
| `CP_ENV` | `dev` | Free-form environment label surfaced on `/version`. **Not** the same knob as `VULOS_ENV` above — this one is cosmetic only and does not affect any safety guard. |
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
export VULOS_ENV=prod                          # explicit; unset also resolves to prod (fail-safe)
export SESSION_SECRET=$(openssl rand -base64 32)  # REQUIRED in prod posture — persist this, don't regenerate it on restart
./bin/cp
```

Prod posture is deliberately strict: with `VULOS_ENV` unset or `prod` and no
`SESSION_SECRET`, the binary refuses to start (`SESSION_SECRET is unset in prod`)
rather than run with a guessable dev secret. Generate the secret once and keep it
— rotating it invalidates every existing session.

Put the binary behind your own TLS-terminating reverse proxy (Caddy, nginx,
Traefik) for an internet-reachable deployment. The control plane speaks plain
HTTP and expects the proxy to handle TLS and forward the client IP.

## What's wired in today

The composition root is `pkg/cproutes.RegisterOperational` (called from
`pkg/cpserver.New`, called from `cmd/server`). It mounts, against the injected
seams, and never opens a commercial store or charges money:

- **Accounts & auth** — registration, sessions, TOTP 2FA, WebAuthn hardware
  keys, OPAQUE password auth, and linked OAuth sign-in (Google / Microsoft).
- **Account recovery** — gated behind `RECOVERY_KEK`; absent that secret the
  recovery routes are simply not mounted (fail-closed, not fail-open).
- **Developer & LLM API keys** — issuance, listing, and revocation.
- **Mobile push** — subscriber registry + dispatch.
- **DDoS / abuse / security** — the honeypot, captcha, rate limiting, IP
  reputation, and the fail-closed security dashboard gate.
- **Legal pages** — ToS / privacy / DPA acceptance tracking.
- **Public product catalogue** — the gated `GET`/`PATCH` surface (org audit
  reads share the same registration).
- **Boot / first-run endpoints**, plus the always-on `/healthz`, `/readyz`,
  and `/version` operational endpoints and the SPA fallback.
- **Your own fleet & devices, account/cloud/support/per-cell status,
  compliance/privacy requests, developer webhooks + MCP** — the operational
  surfaces the React `/console` SPA calls (`registerConsoleOperational`).
  Each opens its own store via `cpdb` (SQLite by default, Postgres when
  `DATABASE_URL` is set, in-memory fallback so the console always renders).
- **Relay-scaling demand API** (`GET /api/relay/scale/demand`,
  `POST /api/relay/scale/observe`) — publishes desired per-region relay
  counts for an external scaler through the `relayscale.RelayProvisioner`
  seam, regardless of which provisioner is active (**manual** by default —
  see [RELAY-SCALING.md](RELAY-SCALING.md)).
- **Device enrollment, OS routing, integrations & storage**
  (`registerNetworkOperational`) — mounted against the same shared auth store,
  all fail-closed:
  - **Device enrollment** — the RFC-8628 device-authorization grant
    (`POST /enroll/{start,poll,approve,deny}`; `start`/`poll` are public +
    per-IP rate-limited, `approve`/`deny` are session-gated) plus the
    session-driven web-wizard enrollment (`POST /api/enroll`,
    `/api/enroll/direct`, `/api/connmode`). The MIT default mints device certs
    with a process-local CA signer (a persistent CA is a configured concern).
  - **OS routing plane** — the DNS plane (`/api/dnsplane/*`, in-process
    `MemProvider`; the cloud root injects Cloudflare), relay status
    (`GET /api/relay/status`), the edge control plane (`/api/edge/*`), BYO-CDN
    (`/api/cdn/*`), and multi-location management (`/api/multiloc/*`).
  - **Integrations** — the third-party OAuth data broker
    (`/api/integrations/*`). Without `INTEGRATIONS_KEK` the broker refuses to
    custody tokens (Connect/Mint error; the routes still report status).
  - **Mail key directory** (`/api/mail/keydir`) and the **cloud-home directory
    + peering intake** (`/api/verify/lookup`, `/api/peering/*`,
    `/api/cloudhome/*`). Cloud-home fail-closes to `503` on every route unless
    `CLOUDHOME_KEK` is configured — the cell never custodies peering keys under
    a default key.
  - **Storage / files / export** (`/api/storage/*`, `/api/account/export`,
    the Files/Drive control plane), **storage-backend selection**
    (`/api/storage/backend`, `/api/storage/sync-mode`), and the **mail-backend
    resolver** (`/api/resolve/backend`). Bring-your-own-bucket by default: the
    no-op `StorageProvisioner` never creates a bucket, and a managed presign
    with no `S3_*` credentials answers an honest `503`.
- **Operator (admin) console** — **opt-in**, off by default. Set
  `VULOS_ENABLE_SUPERADMIN=1` (or `VULOS_BOOTSTRAP_SUPERADMIN=<email>`) to
  mount the operator HTML pages, session/login, and the JSON admin API the
  React `/console/admin` section consumes (dashboard, accounts, audit,
  security, whoami). Left disabled by default so the zero-config self-host
  admin surface stays mounted-but-deny-all (`403`). See
  [ADMIN-CONSOLE.md](ADMIN-CONSOLE.md).
- **Org-admin console** (`pkg/orgadmin`) — mounted unconditionally alongside
  the product catalogue, gated by the shared auth store.

### What's in the module, not wired into the default binary

Only two things remain unmounted by the zero-config default, and neither is a
mechanical migration gap:

- **Box billing read** (`GET /api/box`, `GET /api/box/billing`) and the whole
  commercial billing/pricing/admin-billing surface — inherently
  commercial-only; the private `vulos-cloud` composition root mounts these.
- **Library packages with no operational HTTP handler** —
  `pkg/status` (the durable uptime-sample + operator-incident store that would
  back a *public* status/incidents page), `pkg/osrouter` (the hostname→box
  resolver library the routing plane calls in-process), and `pkg/residency`
  (the residency contract). These have no `pkg/cproutes` route group to mount
  yet. Note the account/cloud/support/per-cell **status** surface the console
  consumes (`pkg/cloudstatus`) *is* wired — see `registerConsoleOperational`.

Everything else — device enrollment, the OS routing plane (DNS/relay/edge/CDN/
multi-location), integrations, the mail key directory, cloud-home, and the
storage/files/selection/resolver plane — is now mounted by the default binary
via `registerNetworkOperational`, fail-closed (see the note at the bottom of
`pkg/cproutes/register_all.go`). None of it is a licensing gate. If you need the
box-billing surface, build your own thin `main` the same way `vulos-cloud`'s is,
against `pkg/cpserver`, and pass a `RouteRegistrar` in `cpserver.Deps.Routes`.

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

## Applications behind the control plane

This repo is the **control plane**, not an application server or an application
suite. Chat, documents, and similar application backends are **bring-your-own
connectors**: the control plane authenticates users and points routing at your
boxes, and you run whichever application backends you choose behind it. There are
no first-party application products bundled in this module.

## Build hygiene: no CI, no lint config

This repo currently ships **no `.golangci*` config and no `.github/workflows/`
at all** — there is no automated lint or CI gate on push or PR. `go build ./...`
and `go vet ./...` are clean as of this writing, but that is only true because
whoever last touched the tree ran them by hand; nothing enforces it going
forward. If you're evaluating this repo for a fork or a production dependency,
treat that as a known gap, not an oversight to assume away. Setting up CI is a
deliberate future addition, tracked in [CHANGELOG.md](../CHANGELOG.md).

## Upgrading

Pull, rebuild, restart with whatever environment your deployment already runs
with (`make dev` for local evaluation; `make run` with your real `VULOS_ENV` /
`SESSION_SECRET` / other secrets exported for anything durable):

```sh
git pull
make build
make dev     # or: make run   (see "A note on VULOS_ENV" above)
```

Database migrations run automatically on start. On SQLite this is transparent; on
Postgres the migration runner applies any new baselines idempotently.

---

<!-- ========================================================================= -->
<!-- APPENDED: Unified self-host — tiers, the optional relay, one-command setup -->
<!-- ========================================================================= -->

# Unified self-host: the whole picture

The sections above cover the **control plane** in isolation. This section places
it in the wider self-host story, explains where the **relay** fits (and when you
don't need it), and gives you a **single install script** that stands up the
control plane and — on request — a relay alongside it.

The two pieces stay **separate binaries** from **separate repos** — this is a
unified *experience*, not a merged program:

| Piece | Repo | Binary | Role |
|---|---|---|---|
| **Control plane** | `vulos-management` (this repo) | `./bin/cp` | Accounts, device enrollment, OS routing, relay fleet, admin console. |
| **Relay** | [`vulos-relay`](https://github.com/vul-os/vulos-relay) | `vulos-relayd` (+ `vulos-relay-agent`) | Public reverse-tunnel ingress so a NAT'd box is reachable. |

## Which tier are you?

Self-hosting scales from a single box to a managed fleet. You only run what your
tier needs:

| Tier | What you run | Notes |
|---|---|---|
| **Individual** | a **box** + *(optional)* a **relay** | You do **not** need the control plane. Run one box; add a relay only if the box isn't publicly reachable. |
| **Fleet / Enterprise** | **+ the control plane** (this repo) + *(optional)* one or more relays | Central accounts, enrollment, OS routing, and a managed relay fleet across your boxes. |

### The relay is OPTIONAL

A relay is the **public half of a reverse tunnel**: a box dials one outbound
`wss://` connection to it, and the relay serves the box a public URL. You need one
**only when a box lacks public reachability** — i.e. it sits behind **NAT/CGNAT**,
has no static IP, or you want a stable public hostname you control.

- **Box has a public IP + domain?** Reach it directly. **Skip the relay entirely.**
- **Box behind NAT/CGNAT (home server, laptop, cheap VPS with no ports)?** Stand up
  a relay so the box is reachable without opening inbound ports.

The control plane runs perfectly well with **zero** relays; a relay runs perfectly
well with **no** control plane (pure sovereign self-host, unbilled). They are
composable, not co-dependent.

## One-command self-host

`scripts/selfhost/install.sh` orchestrates the whole thing. It builds the control
plane, writes a self-host config (free no-op billing, bring-your-own-bucket), and
can optionally stand up a relay next to it by delegating to the relay repo's own
installer.

```sh
# Control plane only (fleet/enterprise, boxes are publicly reachable):
scripts/selfhost/install.sh --domain cp.example.com

# Control plane + a relay (some boxes are behind NAT/CGNAT):
scripts/selfhost/install.sh --domain cp.example.com \
    --with-relay --relay-domain relay.example.com

# Point at a specific relay checkout (default: a sibling ../vulos-relay):
scripts/selfhost/install.sh --domain cp.example.com \
    --with-relay --relay-domain relay.example.com \
    --relay-repo /path/to/vulos-relay

# Postgres instead of the default on-disk SQLite:
scripts/selfhost/install.sh --domain cp.example.com \
    --database-url postgres://vulos:secret@db.internal:5432/cp

# Write build + config but don't start anything:
scripts/selfhost/install.sh --domain cp.example.com --no-run
```

What it does, step by step:

1. **Builds** `./bin/cp` (`make build`).
2. **Writes** a self-host env file to `~/.vulos/selfhost/cp.env` (override with
   `--data-dir` / `VULOS_DATA_DIR`) — kept **outside the repo** so generated
   secrets are never committed. It generates a real `SESSION_SECRET`, defaults to
   durable on-disk SQLite (or your `--database-url`), and sets `VULOS_ENV=local`
   (the working self-host posture — see the note below).
3. **Relay (optional):** with `--with-relay`, it finds your `vulos-relay` checkout
   (a sibling `../vulos-relay` by default, or `--relay-repo <path>`) and runs that
   repo's `scripts/install.sh --domain <relay-domain>`, which brings the relay up
   in Docker. If no checkout is found it prints the `git clone` + install steps
   instead of failing.
4. **Runs** the control plane and health-checks `GET /healthz`, then prints
   `GET /version` (you'll see `"billing_rail":"noop"` — confirmation it can never
   charge). Logs and a PID file land in the data dir.

### A note on `VULOS_ENV`

The control plane **fails safe to production posture** when `VULOS_ENV` is unset —
and full prod additionally requires provider secrets (Apple/Google **push** creds,
**KEK**s) that a self-hoster usually has no reason to hold, so those fail-closed
gates would block startup. The installer therefore sets `VULOS_ENV=local`, the
working self-host posture, while still generating a **real** `SESSION_SECRET`
(never a dev fallback). To run the full production hardening, set `VULOS_ENV=prod`
in `cp.env` and supply the push/KEK variables, then restart.

### Standing up a relay by hand

If you'd rather set the relay up directly (or on a different host from the control
plane), use the relay repo's own one-command installer — the control-plane
installer simply calls it for you:

```sh
git clone https://github.com/vul-os/vulos-relay
cd vulos-relay
./scripts/install.sh --domain relay.example.com     # Docker Compose, health-checked
```

That script generates the agent grant token, writes `.env` + `grants.json`, brings
`vulos-relayd` up behind a TLS-terminating edge, and prints the exact
`vulos-relay-agent` command to run on each box. See the relay repo's
**"Self-hosting a Vulos relay"** README section and its `docs/GETTING-STARTED.md`
for the full walkthrough, DNS/TLS options, and the flag/env reference.

### Putting it on the internet

Both binaries expect a **TLS-terminating reverse proxy** in front (Caddy, nginx,
Traefik, or a CDN):

- **Control plane** (`./bin/cp`) speaks plain HTTP on `CP_ADDR` (default `:8080`);
  proxy `https://cp.example.com` → it.
- **Relay** (`vulos-relayd`) speaks plain HTTP on `:8443`; proxy `:443` → `:8443`,
  and for subdomain mode terminate a `*.relay.example.com` wildcard cert there.

Neither phones home, charges money, nor provisions managed buckets on its own — the
commercial `vulos-cloud` layer is the only thing that adds those, by injecting real
providers into the same seams (see [ARCHITECTURE.md](ARCHITECTURE.md)).
