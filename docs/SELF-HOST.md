# Self-host the whole Vulos suite

**Run the entire Vulos suite on your own box, with no cloud.** Workspace as the
front door, the Vulos OS serving `/api` as your local control plane, and mail,
office, board, talk, meet and a private-AI gateway alongside — all under one
`docker compose`.

> **This doc lives in [vulos-management](https://github.com/vul-os/vulos-management)**,
> the open-source (MIT) control plane. The control plane in this repo is fully
> functional on its own — accounts, routing, relay autoscaling, and the
> admin console — with billing behind a `BillingProvider` seam whose
> **default is a no-op** (metered but free, no phone-home). The only thing the
> private `vulos-cloud` layer adds on top is commercial billing (a commercial provider), a
> few billing-only admin panels, and the hosted marketing site. **The suite is
> fully functional without any of that.** This guide is the single source of
> truth for running Vulos sovereignly.

---

## One command

From a directory that has the sibling repos checked out next to `vulos-cloud`
(`vulos`, `lilmail`, `vulos-mail`, `vulos-office`, `board-ui`, `vulos-meet`,
`vulos-talk`, `llmux`, `vulos-workspace`), run **from inside `vulos-cloud`**:

```sh
docker compose -f docker-compose.sovereign.yml up --build
```

Then open the front door:

```
http://localhost:8088
```

Tear down (keep data): `docker compose -f docker-compose.sovereign.yml down`
Tear down (wipe data): `docker compose -f docker-compose.sovereign.yml down -v`

The compose ships **safe throwaway defaults for every secret**, so `up` works
with zero configuration for a localhost / LAN trial. For anything reachable from
the internet, set the real values below.

---

## What is in the stack

| Service | Role | Port(s) | Notes |
|---|---|---|---|
| `workspace` | Front door (static SPA + nginx) | `8088` | Proxies `/api` → OS, `/board/ws` → board. Built with `VITE_SELF_HOST=1`. |
| `vulos-os` | **Local control plane** — serves `/api` | `8080` | `VULOS_CP_URL` **unset** → box-is-identity, BYO-OAuth, local Meet minter. |
| `vulos-board` | Whiteboard sync (Yjs CRDT) | (internal) | Shares `BOARD_AUTH_SECRET` with the OS. |
| `lilmail` | Webmail client (`/v1`) | `3000` | Ships pointed at `mailpit`; repoint at a real IMAP/SMTP for a real mailbox. |
| `mailpit` | Local mail catcher (dev) | `8025` (web UI) | Lets lilmail boot without a real mail server. |
| `vulos-mail` | Sovereign mail **server** (IMAP/SMTP/JMAP) | `2525/2587/2143/2080` | Needs a real domain + MX for actual delivery (see below). |
| `vulos-office` | Documents | `8081` | Standalone; no CP needed. |
| `vulos-talk` | Chat / spaces / huddles | `8082` | Standalone; no CP needed. |
| `vulos-meet` | Video (bundled LiveKit SFU) | `7883`, `50000-50200/udp` | Shares the LiveKit key pair with the OS minter. |
| `meet-redis` | Meet cascade coordination | (internal) | Only needed for multi-SFU; kept for parity. |
| `llmux` | Private-AI gateway | `4000` | Routes to an on-box model server by default; nothing egresses unless you opt in. |

`vulos-relay` is a **client library**, not a server — it ships inside the apps to
give your box public reachability without opening ports (see "Being reachable").

---

## The "no-CP" wiring (what makes this sovereign)

The single most important env decision: **`VULOS_CP_URL` is left UNSET on the OS.**
That one absence flips the box into sovereign mode:

- **Identity** — the box is its own identity authority (no cloud login broker).
- **Integrations** — Google/Microsoft/Dropbox OAuth is **BYO**: you register your
  own OAuth app and give the box the client id/secret. Unset providers are simply
  unavailable — no error.
- **Meet** — the OS mints LiveKit join tokens **locally** at `POST /api/meet/token`,
  signed with the **same** `LIVEKIT_API_KEY`/`LIVEKIT_API_SECRET` pair the
  co-located SFU verifies. (If those are unset, that route returns `503 Meet not
  configured` — the rest of the suite is unaffected.)

Do **not** set `VULOS_CP_URL` (or `VULOS_CLOUD_URL`) unless you actually want the
box to enroll with a managed control plane.

---

## Env a self-hoster sets

Create a `.env` next to the compose file (docker compose reads it automatically)
and override the defaults you care about. **Every one has a working throwaway
default**, so set only what applies to you.

### Always set for a real (internet-reachable) deployment

```sh
# Secrets — generate fresh values (do NOT ship the compose defaults publicly).
BOARD_AUTH_SECRET=$(openssl rand -hex 32)
DEVICE_SHARED_SECRET=$(openssl rand -hex 32)
INTEGRATIONS_KEK=$(openssl rand -hex 32)          # 64 hex chars; encrypts OAuth tokens at rest
LIVEKIT_API_KEY=$(openssl rand -hex 8)
LIVEKIT_API_SECRET=$(openssl rand -hex 32)         # OS minter + SFU MUST share this pair
MEET_ADMIN_TOKEN=$(openssl rand -hex 16)
MEET_CLUSTER_REDIS_PASSWORD=$(openssl rand -hex 16)
LLMUX_MASTER_KEY=$(openssl rand -hex 16)

# Where the board's WS allow-list expects the Workspace origin to be:
BOARD_ALLOWED_ORIGINS="https://workspace.example.com"
```

### Meet (video)

`LIVEKIT_API_KEY` + `LIVEKIT_API_SECRET` are the **only** required Meet secrets,
and they must be **identical** on `vulos-os` and `vulos-meet` (the OS signs the
join token the SFU verifies — the compose wires both from the same variables, so
just set them once in `.env`). Local/LAN calls work as-is. Remote participants
across NAT additionally need the signal port (`7883`) publicly reachable and a
TURN server or the media UDP range (`50000-50200/udp`) open — see below.

### Integrations (Google / Microsoft / Dropbox — BYO OAuth)

Register your own OAuth app with each provider you want, then set:

```sh
OAUTH_REDIRECT_BASE="https://your-box.example.com"    # callbacks land at <base>/api/integrations/{provider}/callback
INTEGRATIONS_KEK=<64 hex chars>                        # required; encrypts refresh tokens at rest
GOOGLE_OAUTH_CLIENT_ID=...
GOOGLE_OAUTH_CLIENT_SECRET=...
MICROSOFT_OAUTH_CLIENT_ID=...
MICROSOFT_OAUTH_CLIENT_SECRET=...
DROPBOX_OAUTH_CLIENT_ID=...
DROPBOX_OAUTH_CLIENT_SECRET=...
```

The redirect URI you register with each provider is
`<OAUTH_REDIRECT_BASE>/api/integrations/{provider}/callback`.

### Mail domain

```sh
VULOS_MAIL_DOMAIN="mail.example.com"
```

### Private AI (llmux)

By default `llmux` routes to an **on-box** model server (Ollama / llama.cpp at
`host.docker.internal:11434`) so nothing leaves your box. To add a hosted
provider you must edit the config and set `allow_egress: true` on it (the
sovereignty gate blocks remote endpoints otherwise). Copy the template and
repoint the mount:

```sh
cp dev/config/llmux.config.json.example dev/config/llmux.config.json   # then edit
# and change the compose mount to your copy
```

---

## What works fully vs. what needs real infra (honest)

**Works fully, out of the box (localhost / LAN):**

- OS control plane + the OS local control plane (`/api`).
- Office (documents), Talk (chat/spaces/huddles), Board (whiteboard) — end to end.
- Webmail UI (lilmail) boots and is browsable against the `mailpit` catcher.
- Private AI via llmux, pointed at a model server running on your host.
- Meet calls between machines on the **same LAN**.

**Needs real external infra (documented, not hidden):**

- **Real email deliverability** — `vulos-mail` boots and serves its API locally,
  but sending/receiving real mail needs a public IP, a mail **domain** with
  `MX` / `SPF` / `DKIM` / `DMARC` DNS records, and TLS. On a laptop behind NAT it
  receives no external mail. Point a domain at it (`VULOS_MAIL_DOMAIN`) and open
  the SMTP/IMAP ports to use it for real.
- **Remote Meet participants** — calls beyond your LAN need the signal port
  (`7883`) publicly reachable **and** a TURN server (or the `50000-50200/udp`
  media range opened on your firewall/router).
- **Public reachability without opening ports** — use `vulos-relay` (the sovereign
  reverse tunnel) so your box is reachable from the internet without a public IP
  or port-forwarding. It ships as a client inside the apps.

None of these are CP features — they are ordinary internet-infrastructure needs
for any self-hosted server.

---

## Being reachable from the internet

Three options, in order of simplicity:

1. **vulos-relay** (sovereign reverse tunnel) — no public IP, no port-forwarding.
   The relay client inside the apps dials out to a relay endpoint and exposes your
   box over `wss`. Best for home/NAT deployments.
2. **A reverse proxy + your own domain** — put Caddy/nginx/Traefik in front,
   terminate TLS, and forward `443 → workspace:80`. Point DNS at your box.
3. **Direct port exposure** — open `8088` (or `443` via a proxy), plus `7883` +
   `50000-50200/udp` for remote Meet, and the mail ports if you run mail.

---

## Where the CP fits (and why you don't need it)

The Vulos Cloud layer is the **optional commercial layer** for people who don't
want to run infrastructure: hosted billing (a commercial provider) injected into the
`BillingProvider` seam, plus the hosted marketing site. The fleet/admin
console and the multi-tenant OAuth broker are **part of this OSS
vulos-management repo**, not the paid layer. Managed enrollment plugs in on top
of this same `/api` seam — you'd set `VULOS_CP_URL` to enroll. Everything in
this guide runs **without ever touching the commercial layer.** Self-hosting is
a first-class, supported mode, and billing defaults to a free no-op.

---

## Verify the stack

```sh
# Structural validation (no build/run):
docker compose -f docker-compose.sovereign.yml config >/dev/null && echo OK

# After `up`, liveness probes:
curl -fsS http://localhost:8080/healthz   # OS / local control plane
curl -fsS http://localhost:3000/health    # lilmail webmail
curl -fsS http://localhost:2080/healthz   # vulos-mail server
curl -fsS http://localhost:8081/healthz   # office
curl -fsS http://localhost:8082/healthz   # talk
open    http://localhost:8088             # OS control plane
```

---

*The sovereign `docker-compose.sovereign.yml` stack is assembled from the Vulos
product repos checked out as siblings. Self-hosting is a first-class, supported
mode; billing is a no-op by default.*
