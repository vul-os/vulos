# Running your own relay

Step-by-step recipes for standing up `vulos relay serve` — the public half of
Vulos's built-in reverse tunnel — so your NAT'd boxes are reachable from anywhere.

Two providers are documented in full:

| | [Hetzner Cloud](#recipe-a--hetzner-cloud) | [Fly.io](#recipe-b--flyio) |
|---|---|---|
| **Cost** | ~€4/month, 20 TB traffic | ~$2–5/month + bandwidth |
| **You manage** | a Linux VM | a container image |
| **TLS** | Caddy, automatic per-hostname | Fly terminates it |
| **Best when** | you want the cheapest, most predictable option | you do not want to run a VM |

Any VPS works — DigitalOcean, Vultr, OVH, Scaleway, a spare machine with a public
IP. The Hetzner recipe is a generic Linux recipe; only step 1 is provider-specific.

> **New here?** [REACH.md](REACH.md) explains what a relay is and why it is built
> this way. This document is the how.

---

## Before you start

**What you need:**

- A machine with a **public IP**.
- A **domain** you control, and the ability to add DNS records.
- A few minutes.

**What you are about to run:** the `vulos` CLI from this same repository
(`backend/cmd/vulos`), in its relay role. It accepts outbound tunnel connections
from your boxes and reverse-proxies public traffic down them. Note this is **not**
the binary a box runs — a box runs `vulos-server` (`backend/cmd/server`) — and no
release artifact ships it, so you build it yourself in step 3 below.

**Decide your relay domain now.** This document uses `relay.example.com`. Each box
gets `https://<name>.relay.example.com`.

---

## Recipe A — Hetzner Cloud

### 1. Create the server

Hetzner Cloud console → **Add Server**:

- **Location**: nearest to *you*, not to your boxes — client latency dominates.
- **Image**: Ubuntu 24.04
- **Type**: **CX22** (2 vCPU, 4 GB, 20 TB traffic). A relay forwards bytes; it does
  not need CPU. This is the cheapest type that comes with plenty of traffic.
- **Firewall**: create one allowing **inbound TCP 80 and 443** only.

Note the IPv4 and IPv6 addresses.

### 2. DNS

Two records, both pointing at the server:

```
relay.example.com       A     203.0.113.10
*.relay.example.com     A     203.0.113.10
```

Add `AAAA` records too if you have IPv6. The wildcard is **DNS only** — you do not
need a wildcard *certificate*, because step 5 issues one certificate per box
on demand.

Verify before continuing:

```bash
dig +short relay.example.com
dig +short box1.relay.example.com    # must return the same address
```

### 3. Install the binary

Build it from the repository (Go 1.25+), then copy it over:

```bash
# on your workstation, in the vulos repo:
cd backend
GOOS=linux GOARCH=amd64 go build -o vulos ./cmd/vulos     # use arm64 for a CAX server
scp vulos root@203.0.113.10:/usr/local/bin/vulos
```

On the server, create an unprivileged user and a config directory:

```bash
ssh root@203.0.113.10
useradd --system --no-create-home --shell /usr/sbin/nologin vulos-relay
install -d -o vulos-relay -g vulos-relay -m 0750 /etc/vulos-relay
chmod 0755 /usr/local/bin/vulos
```

### 4. Mint a grant for each box

```bash
vulos relay grant box1
```

```json
[
  {
    "token": "8f3c…",
    "names": ["box1"]
  }
]
```

**Use one grant per box.** A token then only ever authorises that box's name, so a
token stolen from one box cannot impersonate another and revoking one does not
disturb the rest.

Collect them into `/etc/vulos-relay/grants.json`:

```json
[
  { "token": "8f3c…", "names": ["box1"], "label": "home nas" },
  { "token": "b71a…", "names": ["box2"], "label": "office pi" }
]
```

```bash
chown vulos-relay:vulos-relay /etc/vulos-relay/grants.json
chmod 600 /etc/vulos-relay/grants.json
```

**The relay refuses to start if this file is world-accessible.** It holds bearer
tokens, and a warning nobody reads is not a control.

Optional per-grant fields:

| Field | Effect |
|---|---|
| `expires_at` | RFC 3339. The grant stops authorising after this time — self-revoking if it leaks. |
| `previous_token` | Accepted alongside `token`, so you can rotate without a flag day. |
| `label` | An operator note. Never used for authorization. |

Save the tokens now — the relay never displays them again.

### 5. TLS with Caddy (automatic, no wildcard certificate needed)

```bash
apt install -y caddy
```

`/etc/caddy/Caddyfile`:

```caddyfile
{
	# Ask the relay whether a hostname is one it serves before issuing a
	# certificate for it. Without this gate, Caddy would attempt a certificate
	# for every hostname anyone points at this IP — an ACME rate-limit hazard,
	# and free work for a stranger.
	on_demand_tls {
		ask http://127.0.0.1:9090/tls-ask
	}
}

# A catch-all site: every hostname the ask endpoint approves gets its own
# certificate, issued on first request. This is why no wildcard certificate
# (and therefore no DNS-01 challenge, and no DNS API credentials) is needed.
https:// {
	tls {
		on_demand
	}
	reverse_proxy 127.0.0.1:8443
}
```

```bash
systemctl restart caddy
```

The `ask` endpoint is served on the relay's **loopback-bound admin listener**, so it
is reachable by Caddy on the same host and by nobody else. It answers from the
*grants*, not from live sessions, so a certificate is ready before a box first
connects rather than after its first request fails.

> **Prefer a wildcard certificate?** You can use one instead, but it needs a DNS-01
> challenge, which needs a Caddy DNS-provider module (`xcaddy build --with …`) and
> an API token for your DNS provider. The on-demand route above avoids all of that.

### 6. systemd unit

`/etc/systemd/system/vulos-relay.service`:

```ini
[Unit]
Description=Vulos relay
After=network-online.target
Wants=network-online.target

[Service]
User=vulos-relay
Group=vulos-relay
ExecStart=/usr/local/bin/vulos relay serve \
  -addr 127.0.0.1:8443 \
  -domain relay.example.com \
  -grants-file /etc/vulos-relay/grants.json \
  -revoked-file /etc/vulos-relay/revoked.json \
  -trust-proxy-headers \
  -rendezvous
Restart=always
RestartSec=2

# Hardening. A relay reads two config files and forwards bytes; it needs
# nothing else from the system.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadOnlyPaths=/etc/vulos-relay
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6
MemoryDenyWriteExecute=yes
LockPersonality=yes

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now vulos-relay
journalctl -u vulos-relay -f
```

**Two flags worth understanding:**

- **`-addr 127.0.0.1:8443`** — the relay binds loopback only. Caddy is the sole
  thing that can reach it.
- **`-trust-proxy-headers`** — correct **because** of the line above. Caddy is a
  trusted TLS terminator, so its `X-Forwarded-For` is believable. If you ever expose
  the relay directly instead, **remove this flag**: an internet-facing relay that
  believes the header lets any client choose its own apparent source IP, defeating
  every per-IP rate limit and every access log.

### 7. Verify

```bash
curl -s https://relay.example.com/_vulos-reach/v1/health
# {"status":"ok","protocol":1}

curl -s http://127.0.0.1:9090/tunnels | jq     # on the relay; empty until a box connects
```

Now configure a box — see [Configuring your boxes](#configuring-your-boxes).

---

## Recipe B — Fly.io

Fly runs your container on anycast IPs and terminates TLS for you. No VM to patch.

### 1. Dockerfile

At the repository root:

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY backend/ ./backend/
WORKDIR /src/backend
RUN CGO_ENABLED=0 go build -o /out/vulos ./cmd/vulos

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/vulos /usr/local/bin/vulos
ENTRYPOINT ["/usr/local/bin/vulos", "relay", "serve"]
```

### 2. `fly.toml`

```toml
app = "my-vulos-relay"
primary_region = "ams"       # nearest to you

[build]

[env]
  VULOS_RELAY_ADDR       = "0.0.0.0:8443"
  VULOS_RELAY_DOMAIN     = "relay.example.com"
  VULOS_RELAY_RENDEZVOUS = "1"
  # Fly's proxy is a trusted TLS terminator in front of this process, so its
  # X-Forwarded-For is believable. Correct ONLY because nothing else can reach
  # the internal port.
  VULOS_RELAY_TRUST_PROXY_HEADERS = "1"
  # Fly's health checks reach the process directly; keep the admin listener
  # loopback-bound so the roster is never exposed.
  VULOS_RELAY_ADMIN_ADDR = "127.0.0.1:9090"

[http_service]
  internal_port = 8443
  force_https = true
  auto_stop_machines = false   # a relay must stay up to hold tunnels
  auto_start_machines = true
  min_machines_running = 1

[[http_service.checks]]
  path = "/_vulos-reach/v1/health"
  interval = "15s"
  timeout = "2s"
```

> **`auto_stop_machines = false` is not optional.** A relay holds long-lived tunnel
> connections. A machine that suspends when it looks idle would drop every box's
> tunnel, and idle-looking is exactly what a healthy held-open tunnel looks like.

### 3. Grants as a secret

On Fly the environment *is* the secret channel, so use the inline form:

```bash
fly secrets set VULOS_RELAY_GRANTS='[
  {"token":"8f3c…","names":["box1"],"label":"home nas"},
  {"token":"b71a…","names":["box2"],"label":"office pi"}
]'
```

Mint each token with `vulos relay grant <name>` as in recipe A, step 4.

### 4. Deploy and attach certificates

```bash
fly launch --no-deploy --name my-vulos-relay
fly deploy
fly ips list                                    # note the v4/v6 addresses
```

DNS:

```
relay.example.com       A/AAAA   <fly ips>
*.relay.example.com     A/AAAA   <fly ips>
```

Then a certificate per hostname:

```bash
fly certs add relay.example.com
fly certs add box1.relay.example.com
fly certs add box2.relay.example.com
```

Simple and fine for a personal fleet. For many boxes, request a wildcard instead —
Fly will give you a DNS record to add for validation:

```bash
fly certs add "*.relay.example.com"
```

### 5. Verify

```bash
curl -s https://relay.example.com/_vulos-reach/v1/health
# {"status":"ok","protocol":1}

fly logs
```

---

## Configuring your boxes

On each box, write the endpoint file (mode **0600** — the box refuses to start
otherwise):

`/etc/vulos/relays.json`

```json
[
  { "url": "https://relay.example.com", "name": "box1", "token": "8f3c…" }
]
```

```bash
chmod 600 /etc/vulos/relays.json
```

Then, in the box's environment:

```bash
VULOS_RELAY_ENDPOINTS_FILE=/etc/vulos/relays.json

# Discovery, so your boxes find each other across the internet:
VULOS_RENDEZVOUS_URL=https://relay.example.com/rendezvous
VULOS_FABRIC_SECRET=<the same value on every box>
```

Restart the box and watch for:

```
[reach] 1 relay endpoint(s): box1@https://relay.example.com (source: file)
[reach] https://relay.example.com: tunnel up — this box is served at https://box1.relay.example.com
```

Confirm from anywhere:

```bash
curl -s https://box1.relay.example.com/api/health
curl -s localhost:8080/api/network/reach | jq     # on the box
```

---

## Adding a second relay

One relay is a single point of failure. The box holds a tunnel to **every**
configured relay at once, so a second one is genuinely redundant — not standby.

Stand up a second relay (a different provider and a different region is the point),
mint a grant for the same box name on it, and extend the box's file:

```json
[
  { "url": "https://relay-a.example.com", "name": "box1", "token": "…", "region": "eu" },
  { "url": "https://relay-b.example.com", "name": "box1", "token": "…", "region": "us" }
]
```

The box is now served at **both** `box1.relay-a.example.com` and
`box1.relay-b.example.com`. Point your own hostname at whichever you prefer, or use
health-checked DNS failover between them.

Add both to discovery too:

```bash
VULOS_RENDEZVOUS_URL=https://relay-a.example.com/rendezvous,https://relay-b.example.com/rendezvous
```

A rendezvous node that errors is skipped rather than failing the set, so one being
down costs nothing.

> **A note on DNS round-robin.** Round-robin across relays is safe **only** because
> every box holds a tunnel on every relay. If some box registers with only one
> relay, a client landing on the other gets a 404 — relays do not forward to each
> other.

---

## Operations

### Add a box

```bash
vulos relay grant box3          # on any machine
# add the entry to grants.json, then:
systemctl restart vulos-relay   # or: fly secrets set VULOS_RELAY_GRANTS='…'
```

### Rotate a token

1. Add the new token to the grant, moving the old one to `previous_token`:
   ```json
   { "token": "<new>", "previous_token": "<old>", "names": ["box1"] }
   ```
2. Restart the relay. **Both** tokens now work.
3. Update the box at your leisure and restart it.
4. Drop `previous_token` and restart the relay.

No flag day, no simultaneous edits.

### Revoke immediately

`/etc/vulos-relay/revoked.json`:

```json
{ "tokens": ["<the leaked token>"], "names": ["box2"] }
```

Restart the relay. **Live tunnels are torn down within 20 seconds** — the relay
re-checks established sessions against the grant store on a timer, because a
working tunnel never reconnects and so would never re-present its credential.

Revocation is a separate file from the grants on purpose: it is the urgent
operation, often done under pressure, and it should not require editing the list of
things that *do* work.

### Drain before maintenance

`SIGTERM` drains first — the relay stops accepting **new** tunnels while existing
ones keep serving, then shuts down within `-grace` (default 20s). Agents receiving
the draining signal move to their next relay immediately rather than backing off.

```bash
systemctl stop vulos-relay      # drains, then stops
```

### Watch it

```bash
journalctl -u vulos-relay -f
curl -s localhost:9090/tunnels | jq       # who is connected
curl -s localhost:9090/healthz
```

`/tunnels` names every registered box, so it stays on the loopback admin listener.
Binding that listener to a routable address **requires** `-admin-token`; the relay
refuses to start otherwise rather than quietly publishing your roster.

---

## Sizing and cost

A relay forwards bytes. It needs bandwidth, not CPU.

| Fleet | Machine | Notes |
|---|---|---|
| 1–10 boxes, personal use | Hetzner CX22 (~€4/mo) | Default caps (64 agents) are far above this |
| 10–50 boxes | CX22 still fine | Watch traffic, not CPU |
| Heavy media/file traffic | Pick for **egress allowance**, not cores | Hetzner's 20 TB is generous; metered providers are not |

All traffic to your boxes flows through the relay, so egress pricing is the number
that matters. Enable direct mode on any box that has a public IP and it will bypass
the relay entirely once the ownership probe passes (`-direct-probe`).

---

## Security checklist

Before you call it done:

- [ ] `grants.json` is **0600** and owned by the relay user
- [ ] One grant **per box**, not one shared token
- [ ] `-trust-proxy-headers` is set **only** when a trusted proxy fronts the relay,
      and the relay itself binds loopback in that case
- [ ] The admin listener is loopback-bound, or has `-admin-token`
- [ ] Firewall allows 80/443 only
- [ ] Tokens were saved somewhere safe — the relay never shows them again
- [ ] You know that a relay **terminates the public TLS** and therefore sees the
      plaintext of requests it forwards (see
      [What a relay can and cannot do](REACH.md#what-a-relay-can-and-cannot-do))

---

## See also

- [REACH.md](REACH.md) — architecture, security model, configuration reference
- [NETWORKING.md](NETWORKING.md) — connection modes, direct mode, DNS, TLS, ports
- [CONFIGURATION.md](CONFIGURATION.md) — every environment variable
