# Reachability & networking

How the outside world reaches your Vulos box: direct connections, the relay fallback, LAN discovery, DNS, TLS, and the ports you need to open (or keep closed) for each way of running it.

---

## The reachability model in one paragraph

Vulos is the OS. You run it on your own hardware or on any cloud VPS you rent, from any provider — it's the same binary either way. A box always works from behind NAT or CGNAT, because reachability rides through **Ephor**: an open-source, self-hostable broker (`github.com/vul-os/ephor`) that the box dials **out** to. There is never a port to open and nothing exposed on your side. Ephor is *hired, not depended on* — it only ever forwards ciphertext, it's swappable, and you can self-host it or point at anyone's instance. If your box instead has a public IP or a domain of its own, you can skip the broker entirely and serve **direct** over TLS. Clients try direct first and fall back to Ephor when it's unreachable. Direct is a faster transport, not a different security posture: the direct listener serves the exact same authenticated handler as the Ephor-fronted path, so an unauthenticated request gets the same 401 either way. On top of both, an opt-in **LAN layer** keeps the box reachable on your local network even with the internet down.

There are three ways to make a box reachable, and you can mix them:

| # | Option | When to use it | Setup |
|---|---|---|---|
| (a) | **Use an Ephor gateway** | Zero-config default. Behind NAT/CGNAT, or you just want it to work. | [Jump to setup](#a-use-an-ephor-gateway-the-default) — nothing to configure |
| (b) | **Run your own Ephor, or point at any Ephor** | You want to control the broker, self-host it, or use a friend's/provider's instance. | [Jump to setup](#b-run-your-own-ephor-or-point-at-any-ephor) — config-driven env vars |
| (c) | **Direct** | Box has a public IP or your own domain (bare metal with a static IP, or a cloud VPS) — no broker at all. | [Jump to setup](#c-direct-public-ip-or-your-own-domain) |

If you remember one thing: **you never *have* to port-forward a Vulos box.** Everything below option (a) is opt-in performance and independence, not a requirement.

A quick map of the paths a request can take:

```mermaid
flowchart LR
  C(["client"]) -->|"internet · default"| R["Ephor<br/>(broker, fallback path)"]
  C -->|"internet · opt-in"| D["direct TLS :443"]
  C -->|"LAN"| L["vulos.local /<br/>box.&lt;id&gt;.lan.vulos.org"]
  R -->|"outbound tunnel"| B["your box<br/>one authenticated HTTP handler"]
  D --> B
  L --> B
```

All three paths land on the **same** authenticated HTTP handler. There is no "trusted because it came from the LAN" or "trusted because it came direct" bypass anywhere in the chain.

---

## Connection modes

The operator picks a high-level networking posture in Settings (persisted to `~/.vulos/db/network-mode.json`, surfaced over `GET`/`POST /api/network/mode`):

| Mode | What it means |
|---|---|
| `fabric` | Default. Traffic rides Ephor — option (a)/(b) below. No inbound ports needed. |
| `direct` | Direct WAN exposure with periodic re-enrollment (public IP + DNS kept up to date) — option (c) below. |
| `own` | You bring your own domain and reverse proxy; Vulos sits behind it — option (c) below. |
| `local` | LAN-only. External listeners are blocked entirely. |

`local` mode is enforced in code, not just cosmetic: when it is selected, the server refuses to start the direct public listener even if `VULOS_DIRECT_ENABLE=1` is set.

The mode-switching endpoint is one of the routes disabled by the `VULOS_DISABLE_EXEC` kill-switch (see [SECURITY.md](SECURITY.md)).

---

## Ephor: the reachability broker

**Ephor** (`github.com/vul-os/ephor`) is the piece that makes a NAT'd or CGNAT'd box reachable from anywhere. It's a small, open-source, self-hostable broker: your box dials **out** to an Ephor instance and holds the connection open; a client that wants to reach your box connects to that same Ephor instance, which forwards the traffic down the tunnel your box already opened. Nothing about your box ever has to accept an inbound connection.

Two properties make this a broker you *hire*, not one you *depend on*:

- **It only ever forwards ciphertext.** Ephor terminates a tunnel, not your session — the TLS/auth handshake your box already speaks travels through it unmodified. It is not a trusted intermediary in the security sense; it is transport.
- **It's swappable.** The endpoint your box talks to is a config value (`VULOS_RELAY_BASE_URL`), never a hardcoded hostname. Run the box against Vulos's own Ephor, your own self-hosted Ephor, or someone else's — with no code change.

Three concrete ways this plays out, matching the summary table above:

### (a) Use an Ephor gateway — the default

Out of the box, with nothing configured, the box uses Vulos's own hosted Ephor instance at `https://relay.vulos.org`. This is `fabric` mode (the default `network-mode.json` value) and needs **zero setup**:

```bash
# nothing to set — this is what happens with no relay env vars at all
vulos    # or: docker run ... ghcr.io/vul-os/vulos:latest
```

Your box is now reachable from anywhere behind NAT/CGNAT/hotel Wi-Fi, with no port forwarded and nothing exposed. This is the mode that "always works"; everything else in this document is an optimisation or an alternative.

### (b) Run your own Ephor, or point at any Ephor

Because the endpoint is config-driven, you can self-host Ephor (clone `github.com/vul-os/ephor` and run it on any box with a public IP — see that project's own docs for deploying it) or point at a friend's/provider's instance instead of Vulos's:

```bash
VULOS_RELAY_BASE_URL=https://ephor.example.org   # your own or a third party's Ephor
VULOS_RELAY_TOKEN=<bearer token the Ephor instance issued you>
VULOS_RELAY_NAME=my-box                          # the name this box registers under
```

| Variable | Default | Purpose |
|---|---|---|
| `VULOS_RELAY_BASE_URL` | `https://relay.vulos.org` | HTTPS base URL of the Ephor instance to register with. **Config-driven, never hardcoded** — point it at your own Ephor to self-host the broker; unset falls back to Vulos's hosted instance. |
| `VULOS_RELAY_NAME` | — | The name this box registers under |
| `VULOS_RELAY_TOKEN` | — | Bearer token the Ephor instance authorizes the box's registration/fan-out with |

No Ephor hostname is baked into the box wiring — the fallback is only ever consulted when both are unset. This is the same seam whether you're running on hardware in your closet or a rented VPS.

Under the hood, the OS side of the contract is:

- **The env seam.** When the direct listener comes up (see option (c) below — you can run direct *and* keep Ephor as a fallback), the OS publishes its advertised endpoint to a co-located Ephor client agent by setting `VULOS_RELAY_DIRECT_ENDPOINT` in the process environment. The agent hands that endpoint to Ephor in its Register frame; Ephor verifies it before ever telling a client about it.
- **The ownership probe.** The box serves an unauthenticated well-known path, `/_vulos-direct/probe`, on its direct listener. Ephor GETs it with a one-time nonce in the `X-Vulos-Direct-Probe` header and the box echoes the nonce back. Only a box that actually controls the advertised endpoint can answer, so a box cannot advertise an endpoint it does not serve. This is the *only* unauthenticated route on the direct listener, and it carries no user data.
- **Host registration.** Opt-in host roles (BYO GPU streaming host, cross-instance notify fan-out) register with an Ephor instance over HTTPS using the same `VULOS_RELAY_BASE_URL` / `VULOS_RELAY_NAME` / `VULOS_RELAY_TOKEN` triple above.

Note: the Ephor tunnel server itself, and the client agent that dials out from your machine, are separate binaries from the Ephor project (`github.com/vul-os/ephor`) — the OS deliberately does not embed either one. What this repo (the box) provides is the env seam, the ownership probe, and the registration calls described above.

---

## Direct mode: the public TLS listener

Direct mode is **off by default** and config-gated, because most boxes are NAT'd and must stay on Ephor. Turn it on only when the box has a genuinely reachable public IP or hostname — including a cloud VPS, which almost always has one.

```bash
VULOS_DIRECT_ENABLE=1
VULOS_DIRECT_HOSTNAME=box1.example.net
```

Full env surface (from `backend/internal/directlisten`):

| Variable | Default | Purpose |
|---|---|---|
| `VULOS_DIRECT_ENABLE` | unset (off) | `1` opts in. Unset means Ephor-only. |
| `VULOS_DIRECT_HOSTNAME` | — | Public DNS name. Required for ACME and for building the advertised endpoint. |
| `VULOS_DIRECT_ADDR` | `:443` | Listen address. |
| `VULOS_DIRECT_CERT_MODE` | `acme` | `acme` (Let's Encrypt via autocert) or `provided` (your own cert files). |
| `VULOS_DIRECT_CERT_FILE` / `VULOS_DIRECT_KEY_FILE` | — | Cert + key paths for `provided` mode. |
| `VULOS_DIRECT_ACME_CACHE` | under `~/.vulos/auth/direct-acme` | Where issued certs are cached (so a restart doesn't re-hit Let's Encrypt rate limits). |
| `VULOS_DIRECT_ACME_EMAIL` | — | Optional ACME account contact. |
| `VULOS_DIRECT_ENDPOINT` | derived `https://<hostname>[:port]` | Override for the advertised endpoint. Must be `https://` — a cleartext endpoint is refused. |

Behaviour worth knowing:

- **TLS is required.** The listener only ever serves HTTPS (TLS 1.2 minimum). In `acme` mode the certificate is requested automatically for `VULOS_DIRECT_HOSTNAME` (the ACME host policy is pinned to that one hostname, so an attacker-supplied SNI can never trigger an issuance); the challenge is answered on the same `:443` listener. In `provided` mode you supply the cert — the option for boxes with only a static IP, or behind your own PKI.
- **Fail-closed configuration.** ACME mode without a hostname, or provided mode without cert files, is a startup error, not a listener that silently cannot serve.
- **Self-reachability pre-check.** After start, the box fetches its *own* probe path over the public endpoint with a fresh nonce. If a firewall silently drops the traffic, you get a log line telling you the endpoint is not externally reachable yet — clients simply keep using Ephor until it is.
- **Status route.** `GET /api/network/direct` (session-authed) reports `{enabled, endpoint, addr}` so you can confirm the fast path is active from the UI or curl.

### (c) Direct: public IP or your own domain

This is the no-broker option. It works identically whether the box is bare metal with a static IP or a cloud VPS — a VPS just makes "genuinely reachable public IP" true from the moment you rent it.

**On a cloud VPS (any provider — Hetzner, DigitalOcean, AWS, etc.):**

1. **Rent a VPS and note its public IPv4/IPv6 address.** Any provider works; Vulos is the same binary regardless of who you rent from.
2. **Point a DNS `A` (and `AAAA`, if you have IPv6) record at it** — e.g. `box1.example.net → 203.0.113.10`. Use whatever DNS provider you already use for the domain; nothing in Vulos provisions this for you in direct mode (that's different from the *published-app-subdomain* DNS automation below).
3. **Open port 443 in the provider's firewall/security group.** Cloud VPS providers usually block everything by default at the network layer, on top of any host firewall — check both. Nothing else needs to be open; see [Ports](#ports) below.
4. **Install Vulos on the VPS** (Docker, the binary, or a flashed image — see [DEPLOY.md](DEPLOY.md)) and set:
   ```bash
   VULOS_ENV=prod
   VULOS_DIRECT_ENABLE=1
   VULOS_DIRECT_HOSTNAME=box1.example.net
   VULOS_RPID=box1.example.net        # bind passkeys to the same domain — see SECURITY.md
   VULOS_ORIGIN=https://box1.example.net
   ```
5. **Start the box.** ACME issues the certificate automatically against port 443 on first start; watch the startup log for `[direct] public listener up` and the self-reachability check passing.
6. **Confirm it from outside** the VPS's own network (see [Verifying reachability](#verifying-reachability-from-the-command-line) below).

**On bare metal with a static IP** the steps are the same minus the "rent a VPS" step — you forward TCP 443 on your router to the box instead of opening a cloud security group.

**Multiple static-IP boxes, DNS load-balanced:** point the same hostname at more than one box's IP (multiple `A`/`AAAA` records, or a provider's DNS load-balancing/health-check feature) and clients fail over across them — there's no coordinator in the loop, it's ordinary DNS.

**`own` mode** is the same public-TLS idea but with your own reverse proxy (Caddy is the supported path) in front instead of the built-in direct listener — see the TLS table below and [Custom domains for published apps](#custom-domains-for-published-apps).

---

## Domains and DNS

### How your box gets its name

The box's public domain is resolved in this order (from `backend/services/network`):

1. `VULOS_DOMAIN`, if set — always wins.
2. In `fabric` or `direct` domain mode: `{instance-ulid}.vulos.org`.
3. In `own` mode: the configured domain.
4. In `local` mode: `localhost`.

The public URL is surfaced at `GET /api/network/status`:

```bash
curl -s http://localhost:8080/api/network/status | jq
# { "url": "...", "domain": "...", "instance_id": "01H...", "hostname": "mybox", "mode": "..." }
```

### App subdomains and why the domain matters

The main request handler routes by `Host` header: a request for `{appId}.<your-domain>` is dispatched to the app gateway instead of the OS shell. That is why the domain must be a real, resolvable name with a wildcard: each app lives on its own subdomain (which is also what keeps app cookies and storage out of the OS origin). In development, `dev.sh` defaults `VULOS_DOMAIN` to `lvh.me` — a public domain whose every subdomain resolves to `127.0.0.1` — so `https://{app}.lvh.me:8080` works with no DNS setup at all.

### Published app subdomains (`VULOS_DNS_API`)

When you publish an app (visibility "public"), the box provisions a subdomain of the form:

```
{app}--{profile}.{instance-ulid}.vulos.org
```

The DNS record is created by calling whatever DNS provisioning API you point the box at — there is no default host. The relevant env vars:

| Variable | Default | Purpose |
|---|---|---|
| `VULOS_DNS_API` | `noop` | The DNS provisioning endpoint. Unset means no DNS provider is configured; the sentinel value `noop` skips the network call entirely (dev/CI/self-hosted). Point it at your own provider's endpoint to have records created automatically. |
| `VULOS_BASE_DOMAIN` | _(empty)_ | The base domain used to build app FQDNs. |
| `VULOS_CADDY_DIR` | `/etc/caddy/vulos-apps` | Directory where per-app Caddyfile snippets are written so self-hosters can `include` them from their main Caddyfile. `noop` skips snippet writes. |

This surface fails closed in production: in `VULOS_ENV=prod`, if `VULOS_DNS_API` or `VULOS_CADDY_DIR` is unset the server **refuses to register the subdomain routes at all**, so customers are never falsely told their domain is being provisioned while nothing happens. In dev/local both default to `noop` with a logged warning.

Each generated Caddy snippet declares the app's hostname with ACME TLS (Caddy requests the certificate automatically) and a `reverse_proxy` to the app's upstream.

### Custom domains for published apps

You can attach your own domain to a published app instead of the generated subdomain:

1. `POST /api/apps/{id}/domain` with `{"domain": "mysite.example.com"}` — returns a challenge token.
2. Add a DNS TXT record: `_vulos-verify.mysite.example.com` → the challenge token.
3. `POST /api/apps/{id}/domain/verify` — the box does a live TXT lookup; on success it writes a Caddy vhost snippet (ACME TLS) and marks the domain verified.
4. `DELETE /api/apps/{id}/domain` reverts to the default subdomain.

### Direct-mode DNS enrollment

In direct mode the box keeps its public DNS record pointing at its current IP: it detects its public IP via an echo service, enrolls with the Vulos control API (`https://control.vulos.org` by default) which returns acme-dns credentials (persisted in-process as `VULOS_ACME_DNS_UUID` / `VULOS_ACME_DNS_KEY`), and watches for IP changes, updating the record when your ISP hands you a new address.

### LAN DNS (works with the internet down)

With the LAN layer enabled (below), the box itself answers DNS for `box.<instance-id>.lan.vulos.org` on UDP port 53, so that name resolves on your LAN even when public DNS — or your whole uplink — is down.

---

## TLS: who terminates it, where

| Deployment | TLS terminated by | Certificate source |
|---|---|---|
| Local dev (`./dev.sh`, `--env=local`) | The Go backend, *if* certs exist; otherwise plain HTTP on loopback | `~/.vulos/localhost.pem` + `~/.vulos/localhost-key.pem` — the mkcert convention. `dev.sh` mounts these into the Docker container when present. |
| Docker / single binary in prod | The Go backend | `/etc/vulos/tls/cert.pem` + `/etc/vulos/tls/key.pem` (checked at startup, after the mkcert paths) |
| Direct mode | The direct listener inside the backend | ACME/Let's Encrypt (autocert) or operator-provided files (see table above) |
| LAN layer | The LAN HTTPS listener inside the backend | An externally-issued certificate for `box.<id>.lan.vulos.org`, delivered to `/var/lib/vulos/tls/lan.crt` + `lan.key` (paths overridable via `VULOS_LAN_CERT` / `VULOS_LAN_KEY`), hot-reloaded on change; falls back to a self-signed cert until the real one arrives |
| `own` mode / published apps | Your reverse proxy (Caddy is the supported path) | Caddy's automatic ACME, driven by the snippets Vulos writes into `VULOS_CADDY_DIR` |
| Self-host bundle | Per-service listeners (OS on 8443, mail on 8444, office on 8445) | Configured via `/etc/vulos/fabric.yaml` (`domain`, `acme_email`) — see [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md) |

Fetching the trusted LAN certificate from an external issuance endpoint is itself opt-in (`VULOS_LANCERT_ENABLE=1`, endpoint at `VULOS_CLOUD_BASE_URL`, defaulting to `https://cp.vulos.org`) and hardened: the puller accepts an extra CA bundle (`VULOS_LANCERT_CA_PEM` / `VULOS_LANCERT_CA_FILE`) or SPKI pins (`VULOS_LANCERT_SPKI_PINS`), and refuses a plaintext endpoint URL unless `VULOS_LANCERT_ALLOW_INSECURE=1` is set (never do this outside a lab). Leave `VULOS_LANCERT_ENABLE` unset and the LAN layer just uses its self-signed fallback — nothing about the LAN listener depends on this being configured.

For local development with real HTTPS, generate the certs once with [mkcert](https://github.com/FiloSottile/mkcert):

```bash
mkcert -install
mkcert -cert-file ~/.vulos/localhost.pem -key-file ~/.vulos/localhost-key.pem localhost 127.0.0.1
```

The backend picks them up automatically on next start.

---

## LAN reachability and local discovery

The LAN layer (`VULOS_LAN_ENABLE=1`, off by default — not every install wants extra listeners) makes the box fully usable with the internet down. It starts three things:

1. **mDNS advertisement** — the box announces itself as `vulos.local` over multicast DNS (UDP 5353), so any client on the same network reaches it with zero configuration.
2. **A tiny DNS responder** — authoritative for `box.<instance-id>.lan.vulos.org` on UDP 53, answering with the box's LAN IP.
3. **A LAN HTTPS listener** — serves the full OS on `:443`, **pinned to the detected LAN IP** rather than all interfaces, so a multi-homed or public box never accidentally exposes this listener on its WAN side.

| Variable | Default | Purpose |
|---|---|---|
| `VULOS_LAN_ENABLE` | unset (off) | `1` enables the whole LAN layer. |
| `VULOS_LAN_HTTPS_ADDR` | `:443` | LAN HTTPS listen address (override for non-root runs). |
| `VULOS_LAN_DNS_ADDR` | `:53` | LAN DNS listen address. |
| `VULOS_LAN_CERT` / `VULOS_LAN_KEY` | `/var/lib/vulos/tls/lan.crt` / `.key` | LAN TLS material paths. |

mDNS and DNS bind failures are logged but non-fatal — a box that can still be reached by IP is better than no box.

Two practical notes:

- **Privileged ports.** 443 and 53 need root (or `CAP_NET_BIND_SERVICE`). If you run the backend as an unprivileged user, override both addresses (e.g. `VULOS_LAN_HTTPS_ADDR=:8443 VULOS_LAN_DNS_ADDR=:5354`) and adjust clients accordingly.
- **Containers and CI.** Multicast is often unavailable inside containers (no `CAP_NET_RAW`, no multicast-capable interface). The LAN layer treats mDNS as best-effort in that case: the HTTPS listener still comes up, only zero-config discovery is lost.

### Same-LAN box-to-box sync (fabric)

If you run more than one box on a LAN, the fabric sync loop discovers sibling boxes over mDNS and exchanges app-registry changesets directly — no cloud, no S3. It is gated on a shared secret: set the same `VULOS_FABRIC_SECRET` on every sibling box. Without the secret the exchange handlers stay **off (fail-closed)** rather than open an unauthenticated endpoint. The fabric handlers are mounted only on the LAN-pinned listener, never on the public surface.

### File sync is bidirectional

Files placed in the data directory are uploaded to your bucket by an fsnotify
watcher, and remote changes are fetched back on boot and then every two minutes
(`PullInterval`, default `2m`).

Downward sync is conservative about your local work. A remote file is applied
directly when the local copy is absent or unchanged since the last sync. If the
local copy was edited while the box was away, the local version is preserved
beside the remote one as `<name>.conflict-<node>-<timestamp>.<ext>` and the
remote version takes the canonical path — so a divergence costs you a file to
reconcile, never an edit.

Files this node uploaded are skipped on the way down, so a box does not
re-download its own writes.

A negative `PullInterval` disables downward sync and leaves the box upload-only.

### Box-to-box sync across the internet (fabric over rendezvous)

mDNS only sees multicast, so LAN discovery alone means two of your own boxes in
two different houses can never find each other. Set `VULOS_RENDEZVOUS_URL` to any
relay running the open rendezvous role and they can:

```sh
VULOS_RENDEZVOUS_URL=https://relay.example.org/rendezvous
```

Each box announces its reachable endpoints under its **own Ed25519 key** — the
same per-instance key the CRDT already uses to verify signed uninstall
observations, so no second identity is introduced — and resolves the keys of the
siblings in its registry roster. Everything after discovery is unchanged: the
changeset exchange still runs directly to the peer over TLS, still requires
`VULOS_FABRIC_SECRET`, and still fails closed without it.

This **composes with mDNS rather than replacing it**. A box in the same house is
still found by multicast with no round trip to anyone; the same box moved behind
a different NAT is found through the relay. If either source fails you keep the
peers the other one found.

What the relay learns is which keys are online and what endpoints they claim —
that is the price of NAT traversal. It sees no app data: announce and resolve
carry none, and it never sees a changeset. A relay that lies can withhold a peer
or point at an address you cannot authenticate to; it cannot forge a changeset,
because signature checking happens at the peer and is downstream of discovery.

Any conforming rendezvous role works here — the same Ephor instance you use for
reachability (option (a)/(b) above), a separately self-hosted one, or set
nothing at all and stay LAN-only. Consult the Ephor project
(`github.com/vul-os/ephor`) for the rendezvous protocol details.

| Variable | Effect |
|---|---|
| `VULOS_RENDEZVOUS_URL` | Ephor rendezvous prefix. Unset = mDNS only (previous behaviour). |
| `VULOS_PUBLIC_URL` | Announced ahead of the LAN address, for peers resolving from outside. |

### Drop (nearby file sharing)

Drop advertises the service `_vula-drop._tcp.local` over mDNS so nearby Vulos peers discover each other, then transfers files over HTTP on the box's main port (8080). Discoverability is a per-box setting; inbound requests from non-contacts require approval. Cross-box shares that target private/LAN addresses are blocked by the SSRF guard unless you explicitly allow LAN peers with `VULOS_PEER_ALLOW_LAN=1` — legitimate for self-hosted boxes that genuinely live on the same network. See [PEERING.md](PEERING.md) and [FILES.md](FILES.md).

---

## Ports

Ports actually bound by the software in this repo, plus the self-host bundle's documented ports:

| Port | Protocol | What | When it exists | Exposure guidance |
|---|---|---|---|---|
| 8080 (or `PORT`) | TCP | Main backend: API, shell, apps, Drop transfers | Always | Loopback-only in `local`/`dev` env; all interfaces in `prod`. Front with TLS before exposing. |
| 5173 | TCP | Vite dev server (HMR), proxies `/api` to 8080 | `npm run dev` only | Never expose. Development only. |
| 443 | TCP | Direct public TLS listener | `VULOS_DIRECT_ENABLE=1` | The one port to forward for direct mode. |
| 443 | TCP | LAN HTTPS listener (pinned to LAN IP) | `VULOS_LAN_ENABLE=1` | LAN only by construction; do not forward. |
| 53 | UDP | LAN DNS responder | `VULOS_LAN_ENABLE=1` | LAN only; do not forward. |
| 5353 | UDP (multicast) | mDNS: `vulos.local`, Drop discovery, fabric sibling discovery | LAN layer / Drop / fabric | Multicast never crosses your router; nothing to forward. |
| 3478 | UDP/TCP | TURN media relay (coturn) | Only if you run your own coturn with `TURN_SECRET` set — the backend mints HMAC credentials for it but does not run the server | On the coturn host, per coturn docs. |
| ephemeral UDP | UDP | WebRTC media (calls, streaming, in-process SFU) | During calls | Outbound/NAT-traversed; TURN covers the hard cases. |
| 8443 / 8444 / 8445 | TCP | Bundle: OS / mail / office HTTPS | Self-host bundle | See [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md). |
| 25, 587 | TCP | Bundle: SMTP inbound / submission | Self-host bundle with mail | Must be open for self-hosted mail; many ISPs block 25. |
| 9000 | TCP | Bundle: MinIO | `--storage=minio` | Loopback-only by design; never expose. |

AI-generated sandbox backends bind `127.0.0.1` only and are reached through the gateway — they never listen externally.

---

## Firewall guidance by mode

**LAN-only (`local` mode, or just never forwarding anything)**
- Inbound from WAN: nothing. Leave the router alone.
- On the box's host firewall, allow from your LAN: TCP 8080 (or 443 + UDP 53/5353 if `VULOS_LAN_ENABLE=1`).
- External listeners are blocked in software too; this mode is belt-and-braces.

**Ephor fallback (`fabric` mode — the default)**
- Inbound from WAN: nothing. The Ephor client agent dials out.
- Outbound: allow HTTPS (443) to your configured Ephor instance (`VULOS_RELAY_BASE_URL`, default `https://relay.vulos.org`).
- This is the right mode for CGNAT, and the safe default for everyone else. On a cloud VPS you can use it too — it's not just for NAT'd boxes — though a VPS usually has a real public IP, making option (c) direct available.

**Direct + domain (`direct` or `own` mode — includes most cloud VPS deployments)**
- Forward TCP 443 to the box (the direct listener; also carries the ACME challenge). On a cloud VPS this means opening port 443 in the provider's firewall/security group, not a router.
- Keep 8080 unforwarded — the direct listener already serves the full OS over TLS.
- Point DNS at your public IP (`direct` mode keeps it updated automatically; in `own` mode your proxy handles the domain).
- Work through the pre-exposure checklist in [SECURITY.md](SECURITY.md) *before* forwarding the port.

---

## Group calls: in-process SFU, no host-registry escalation

First-party Vulos Meet (and the self-host SFU host-registry escalation path
that used to back it — `internal/meethost`, `VULOS_SFU_HOST`, `/api/meethost/status`)
is retired; video calling is third-party (Jitsi Meet / Element Call, installed
from the App Store — see [COMMS.md](COMMS.md), including its own BYO LiveKit
SFU option for Element Call). What remains first-party is the sovereign P2P
**Messages** builtin's own group calling: an in-process Pion SFU
(`backend/services/peering/sfu`, `/api/sfu/rooms/*`) for peer group calls
within the small mesh cap, with **no** opt-in to advertise a box as a
dedicated big-call SFU host the way the retired Meet host registry did.

A BYO opt-in still exists for GPU streaming hosts (`VULOS_GPU_HOST=1`, `VULOS_GPU_ADVERTISE_HOST`, `VULOS_STREAMER_BINARY`; status at `/api/gpuhost/status`) — an unrelated role that uses the box's single fabric identity, the same Ed25519 keypair (VulaID) the box advertises for peering.

---

## TURN: media relay for hard NATs

WebRTC media (calls, streaming) normally flows peer-to-peer over ephemeral UDP. When both sides sit behind symmetric NATs, a TURN relay is the escape hatch. Vulos does not ship a TURN server; it integrates with a coturn instance you run yourself:

| Variable | Default | Purpose |
|---|---|---|
| `TURN_SECRET` | unset (TURN disabled) | Shared secret; setting it enables credential minting |
| `TURN_HOST` | `localhost` | Hostname/IP clients dial to reach your TURN server — set this to the box's real public hostname/IP; the previous hardcoded `localhost` only worked when the signaling client and TURN server were the same machine |
| `TURN_PORT` | `3478` | The coturn port advertised in credentials |
| `TURN_REALM` | `vulos` | The coturn realm |
| `VULOS_STUN_DISABLE_PUBLIC` | unset (public STUN included) | Drops the public Google STUN fallback from `GET /api/peering/ice` — for a fully-sovereign deployment with no third-party network dependency for call setup |

The backend mints short-lived credentials (24-hour TTL) using the standard `use-auth-secret` HMAC scheme: username is `<expiry>:<userID>`, credential is `base64(HMAC-SHA256(secret, username))`. Note the HMAC here is **SHA-256**, so your `turnserver.conf` must include the `sha256` option alongside `use-auth-secret` — coturn's default is SHA-1 and the credentials will not verify without it.

**Self-hosted STUN, for free.** Whenever `TURN_SECRET` is set, `GET /api/peering/ice` also includes a `stun:<TURN_HOST>:<TURN_PORT>` entry — coturn answers plain STUN binding requests on the same port it serves TURN, so a self-hosted TURN deployment already gives you a fully self-hosted STUN option with zero extra infrastructure. Combined with `VULOS_STUN_DISABLE_PUBLIC=1`, a box needs no third-party STUN/TURN server at all.

`GET /api/peering/federation` reports the box's current sovereign-federation posture (relay/verify/rendezvous configuration, TURN host, and whether public STUN is disabled) in one place.

---

## Wi-Fi and Ethernet on bare metal

On a bare-metal install, Settings manages the box's own network connection through the backend (wpa_supplicant/iw under the hood):

| Endpoint | What it does |
|---|---|
| `GET /api/wifi/status` | Current connection (SSID, IP, signal, band, TX rate) |
| `GET /api/wifi/scan` | Visible networks with signal and security (WPA2/WPA3/open) |
| `POST /api/wifi/connect` | Join a network (audited) |
| `POST /api/wifi/disconnect` | Drop the current connection |
| `GET /api/wifi/saved` / `POST /api/wifi/forget` | Manage remembered networks |
| `GET /api/network/status` | Box identity: URL, domain, instance ID, hostname, mode |

These endpoints shell out to system tools, so they are part of the exec surface disabled by the `VULOS_DISABLE_EXEC` kill-switch, and each mutation is written to the exec audit log. In Docker they are mostly moot — the container uses the host's networking.

---

## Verifying reachability from the command line

A short diagnostic sequence when "the box is unreachable":

```bash
# 1. Is the backend up at all? (from the box itself)
curl -s http://localhost:8080/healthz
# {"status":"ok","version":"..."}

# 2. What does the box think its identity and mode are?
curl -s http://localhost:8080/api/network/status | jq
curl -s http://localhost:8080/api/network/mode | jq        # needs a session in prod

# 3. Is the direct fast path active?
curl -s http://localhost:8080/api/network/direct | jq
# {"enabled":false}  → relay-only, as designed
# {"enabled":true,"endpoint":"https://box1.example.net", ...}

# 4. Does the direct endpoint answer from OUTSIDE? (run from another network)
curl -si https://box1.example.net/_vulos-direct/probe -H 'X-Vulos-Direct-Probe: test123'
# HTTP/2 200 with body "test123" → reachable and ownership-provable
# (without the header it returns 400 by design — it is not an open reflector)

# 5. On the LAN, does zero-config resolution work?
ping vulos.local
```

If step 4 times out but step 1 works, your firewall or NAT is dropping inbound 443 — which is exactly the situation the box's own self-reachability check logs at startup, and exactly when clients fall back to the relay. More recipes in [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

---

## See also

- [SECURITY.md](SECURITY.md) — the pre-exposure checklist, auth surface, and fail-closed defaults
- [CONFIGURATION.md](CONFIGURATION.md) — the full env var reference
- [DEPLOY.md](DEPLOY.md) and [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md) — installing and running
- [GETTING-STARTED.md](GETTING-STARTED.md) — first boot
- [PEERING.md](PEERING.md) — box-to-box identity, contacts, and Drop
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — when a box is unreachable
