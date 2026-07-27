# Reach — Vulos's own reachability stack

**How your box is reachable from anywhere, using only software that ships in this
repository.** No third-party tunnel service, no external relay project, no account
with anyone.

> **Setting one up?** Jump to [RELAY-SELF-HOST.md](RELAY-SELF-HOST.md) for
> step-by-step recipes on Hetzner and Fly.io. This document explains what you are
> setting up and why it is built the way it is.

---

## Table of contents

1. [The problem, stated honestly](#the-problem-stated-honestly)
2. [The three facets](#the-three-facets)
3. [Pick your route](#pick-your-route)
4. [How the tunnel works](#how-the-tunnel-works)
5. [Multiple relays](#multiple-relays)
6. [Configuration](#configuration)
7. [Discovery: finding your other boxes](#discovery-finding-your-other-boxes)
8. [Security model](#security-model)
9. [What a relay can and cannot do](#what-a-relay-can-and-cannot-do)
10. [Alternatives, and when to prefer them](#alternatives-and-when-to-prefer-them)
11. [Status and troubleshooting](#status-and-troubleshooting)

---

## The problem, stated honestly

Most boxes sit behind NAT, CGNAT, or a router nobody can configure. They can dial
**out** but nothing can dial **in**. To be reachable from the internet, such a box
needs a machine with a public IP to accept connections on its behalf.

There is no way around that. It is a property of the network, not of any design.
What *is* a design choice is **who runs that machine and what they can do**. Vulos's
answer:

- The public-IP machine runs **`vulos relay serve`** — the same binary you already
  run, in a different role. Not a separate product, not a separate download, not a
  separate vendor.
- It is **swappable**: a relay is named by config, never compiled in. Point at your
  own, a friend's, or an [Ephor](https://github.com/vul-os/ephor) instance.
- It is **plural**: your box holds tunnels to *every* configured relay at once, so
  no single one is load-bearing.

If your box already has a public IP, you need none of this — see
[Pick your route](#pick-your-route).

---

## The three facets

Reachability is three independent concerns. Conflating them is the most common way
to break something while trying to fix something else, so the code models them
separately (`backend/services/relayconfig/providers.go`):

| Facet | What it is | Who serves it |
|---|---|---|
| **A — app-media ICE** | STUN/TURN, so Meet/Talk calls connect | public STUN + your own coturn |
| **B — HTTP ingress** | reaching the box's web surface from outside NAT | the reverse tunnel, *or* a public IP |
| **C — rendezvous** | box↔box discovery across the internet | the relay's discovery role, *or* mDNS on a LAN |

Picking a provider for one facet never silently zeroes another. Choosing a
WireGuard mesh for ingress leaves call media working; choosing a bring-your-own
TURN server leaves ingress alone.

---

## Pick your route

```
Does the box have a reachable public IP or hostname?
│
├─ YES ──► Direct mode. No relay, no tunnel, nothing to run elsewhere.
│          VULOS_DIRECT_ENABLE=1 + VULOS_DIRECT_HOSTNAME. See NETWORKING.md.
│          (You can still run a relay as a fallback; the two compose.)
│
└─ NO ───► You need a public-IP machine. Three ways:
           │
           ├─ `vulos relay serve` on a small VPS   ◄── this document
           │   ~€4/month. Gives you all three facets in one binary.
           │
           ├─ An Ephor relay
           │   Same idea, separate project. Supported alternative.
           │
           └─ Generic tunnel (Tailscale/Headscale, wg + Caddy, frp)
               Covers facet B only. Facets A and C stay on their own paths.
```

**Both at once is the best answer** when you can manage it: enable direct mode *and*
register with a relay. The relay verifies your direct endpoint (see
[ownership probe](#the-direct-endpoint-ownership-probe)) and advertises it, so
clients go direct for speed and fall back to the tunnel when direct fails.

---

## How the tunnel works

Your box dials **one outbound `wss://` connection** to the relay and holds it open. A
public client hits the relay; the relay opens a multiplexed stream down that
already-open connection and reverse-proxies the request. Nothing on your box ever
accepts an inbound connection.

```
public client ──HTTPS──► relay (public IP, `vulos relay serve`)
                            │  one yamux stream per request,
                            │  over ONE held-open wss connection
                            ▼
                         your box (agent embedded IN the OS process)
                            │  in-process handler call
                            ▼
                         the OS's own handler chain
```

### The agent is embedded, not a sidecar

The agent serves the OS's own `http.Handler` **in process**. Consequences worth
stating:

- **No second binary** to install, supervise, or version-skew against the OS.
- **No loopback listener** for it to dial, and therefore no loopback SSRF surface.
- **Identical security posture**: a tunnelled request runs the exact same auth,
  session, CSRF, rate-limit, and security-header chain as a request arriving on the
  box's own listener. "Arrived via tunnel" grants a request nothing.

### The layers are all off-the-shelf

| Layer | What | Why |
|---|---|---|
| HTTP/1.1 | stdlib `http.Server` (agent), `httputil.ReverseProxy` (relay) | protocol upgrades (WebSocket) pass through natively |
| yamux | stream multiplexing | already in this module's dependency graph via libp2p — **no new supply-chain surface** |
| WebSocket | the outbound transport | `wss://` traverses corporate proxies, captive portals, and CDNs that pass nothing else — precisely the networks a tunnel exists to serve |
| TLS | ordinary https to the relay | — |

The only novel code is the handshake, the routing table, and the header-trust
boundary. That is deliberate: a bespoke mux or framing layer would be the most
likely home for a memory-corruption or desync bug and would buy nothing.

---

## Multiple relays

**The agent holds a live tunnel to every configured relay simultaneously.** It does
not pick one.

This matters because relay tunnels are **affinity-bound**: a client arriving at
relay B cannot be served a box whose tunnel is only on relay A, because relays do
not forward to one another. Holding all of them is what makes a second relay
actually redundant rather than decorative — DNS or the client can move between
relays with no coordination and no failover delay.

> **Do not DNS round-robin across relays** unless every box holds a tunnel on every
> relay. With the built-in agent that is exactly what happens, so round-robin *is*
> safe here — but it is not safe for a fleet where some boxes register with only one.

Links fail independently. A relay that is down, draining, or refusing your grant
affects only its own link.

**One relay is a single point of failure. Two under different operators is not.**
The box logs a note when only one is configured.

---

## Configuration

### Box side

Endpoints come from one of three sources, highest precedence first. **They do not
merge** — a box configured with a file ignores the others entirely, so there is
exactly one answer to "where did this come from".

#### 1. `VULOS_RELAY_ENDPOINTS_FILE` (preferred)

A JSON file at **mode 0600** — the box refuses to start if it is world-accessible,
because it holds bearer tokens.

```json
[
  { "url": "https://relay-a.example.com", "name": "box1", "token": "…", "region": "eu" },
  { "url": "https://relay-b.example.com", "name": "box1", "token": "…", "region": "us" }
]
```

```bash
VULOS_RELAY_ENDPOINTS_FILE=/etc/vulos/relays.json
```

Preferred because the process environment leaks into `ps`, crash dumps, and child
processes; a 0600 file does not.

#### 2. `VULOS_RELAY_ENDPOINTS` (inline JSON)

The same array as an environment variable, for Fly/Docker/Kubernetes secret
injection where a file is awkward.

#### 3. Legacy single-endpoint form

```bash
VULOS_RELAY_BASE_URL=https://relay.example.com
VULOS_RELAY_NAME=box1
VULOS_RELAY_TOKEN=…
```

Still fully supported. Upgrading a box changes nothing until you opt into a set.

#### Field reference

| Field | Meaning |
|---|---|
| `url` | The relay's https base URL. Plain http is refused (the token rides these requests) except for a loopback dev relay with `VULOS_RELAY_ALLOW_INSECURE=1`. |
| `name` | The name this box claims on that relay — one DNS label (`a-z0-9-`). Becomes `https://<name>.<relay-domain>`. |
| `token` | The bearer grant the relay authorised this name with. Per-relay: the same box legitimately holds a different grant on each. |
| `region` | Optional operator label. Display only. |
| `priority` | Optional. Lower is preferred. Defaults to configuration order. |

**One bad entry refuses the whole set**, with an error naming the index. An operator
who typo'd relay B must not silently get a box running on relay A alone while
believing it has two.

### Relay side

See [RELAY-SELF-HOST.md](RELAY-SELF-HOST.md) for full recipes. In brief:

```bash
vulos relay grant box1                 # mint a token; paste into grants.json
vulos relay serve \
  -domain relay.example.com \
  -grants-file /etc/vulos/grants.json \
  -rendezvous
```

---

## Discovery: finding your other boxes

mDNS only sees the local link, so two of your own boxes in two different houses can
each be perfectly reachable and still never find each other. The relay's
**discovery role** closes that.

```bash
# relay:
vulos relay serve … -rendezvous

# every box (comma-separated — list two or three under different operators):
VULOS_RENDEZVOUS_URL=https://relay-a.example.com/rendezvous,https://relay-b.example.com/rendezvous
VULOS_FABRIC_SECRET=<identical on every box>
```

Each box announces its endpoints under **its own Ed25519 key** and resolves its
siblings' keys. Everything after discovery is unchanged: the changeset exchange runs
directly to the peer over TLS and checks signatures of its own.

Discovery **composes with mDNS** rather than replacing it — a box in the same house
is still found by multicast with no round trip to anyone. A source that errors is
skipped rather than failing the set, so losing multicast never costs you your remote
siblings, and vice versa. This is the substrate spec's shape (KOTVA §4.2.1(3): a
home rendezvous set of ≥3 nodes under disjoint operators).

**Wire-compatible with Ephor.** The protocol is byte-identical to Ephor's rendezvous
role and is pinned by a test that drives the real box-side client against this
server. You can mix Vulos and Ephor rendezvous nodes in one list.

---

## Security model

### The header-trust boundary

This is the part worth reading carefully.

A request arriving **at the relay** is attacker-controlled, headers included. A
request arriving **at the agent** came over an authenticated tunnel and carries
metadata the agent must be able to trust. Reconciled in exactly one place:

- The relay **strips every `X-Vulos-Reach-*` header** from the inbound client
  request — unconditionally, no exceptions — and then sets the ones it vouches for.
  The strip is **prefix-based**, not a list of known names, so a header added later
  is covered automatically.
- The agent trusts those headers, and only because they cannot have come from
  anywhere but the relay it authenticated to.
- The agent then **strips them again** before the request reaches any OS handler, so
  no downstream code can grow a dependency on trusting a header.

### Translation at the boundary

The agent translates the vouched values into the request fields the OS **already**
trusts, rather than teaching the OS about tunnels:

| Vouched header | Becomes | Why it matters |
|---|---|---|
| `X-Vulos-Reach-Client-Ip` | `r.RemoteAddr` | Otherwise every tunnelled client shares one rate-limit bucket, and one attacker locks out every remote user. An unparseable value **fails closed** — the tunnel address stays, so the request shares a bucket rather than getting a forged one. |
| `X-Vulos-Reach-Proto: https` | a synthetic completed `r.TLS` state | Otherwise session cookies silently lose `Secure` and step-up treats a public request as local. Set **only** from the vouched header. |
| `X-Vulos-Reach-Name` | checked against the agent's own registration | A mismatch means the relay misrouted; the agent answers 421 rather than serving as another box. |

The synthetic TLS state is a deliberate, load-bearing assertion. The client's
connection to the relay was TLS and the relay's to your box is TLS; only the relay's
own memory holds plaintext. It is exactly as trustworthy as the relay — and a relay
that lies there could already read and rewrite the request.

### Authorization

- **A relay never runs open.** No grants configured → it refuses to start. An open
  relay is an open proxy: anyone could publish content under the operator's domain
  and TLS certificate.
- **A grant binds a token to specific names.** box1's grant cannot claim box2's name.
- **Unauthorised upgrades are rejected before the WebSocket exists.** No allocation
  on behalf of an unauthenticated peer.
- **Every rejection looks identical.** Distinguishing "unknown token" from "expired
  grant" would let an attacker learn which guesses were once valid.
- **Revocation reaches established tunnels.** Live sessions are re-checked against
  the grant store every 20s — a working tunnel never reconnects, so a revocation
  that only applied to new connections would leave a compromised box connected
  indefinitely.
- **Rotation without a flag day.** `previous_token` is accepted alongside `token` for
  as long as you leave it there. `expires_at` makes a grant self-revoking.

### Reconnect vs. hijack

A box that restarts leaves a session the relay has not yet noticed is dead (a
half-open TCP connection can survive minutes). So:

- A registration presenting the **same** credential **evicts** the old session
  immediately — otherwise a box would be unreachable for that whole window, right
  after a reboot.
- A registration presenting a **different** credential is **refused**. Two grants can
  legitimately both list a name; eviction is a convenience for the credential that
  already holds the name, never a lever for a different one.

> **One name = one box.** Two *live* boxes sharing a name and credential will evict
> each other in a loop — each eviction closes the other's tunnel, which it retries.
> This is the natural consequence of "same credential wins", and the alternative
> (refusing) would make a rebooted box unreachable for minutes. Give every box its
> own name and its own grant; the symptom, if you do not, is in the
> [troubleshooting table](#common-symptoms).

### The direct-endpoint ownership probe

An agent may advertise a public https origin it is also reachable at. Before ever
publishing it, the relay GETs `<endpoint>/_vulos-direct/probe` with a one-time nonce
and requires it echoed. Without that, an agent could advertise **any** URL — a bank,
a competitor — and the relay would publish it to clients as "this box, but faster".

The probe is the only outbound request a relay makes on an agent's behalf, so it is
hardened accordingly: SSRF-screened at connect time against the **resolved** IP
(defeating DNS rebinding, which matters inside a VPS with a metadata service),
redirects refused, bounded read, dedicated transport. It is **off by default**
(`-direct-probe`).

### Bounds

Every limit exists because something is unauthenticated or attacker-influenced:
grants file permissions, control-frame size, handshake timeout, concurrent streams
per tunnel, agents per relay, request body size, per-IP upgrade rate, per-tunnel and
global request rate, rendezvous table size and TTL. The rate-limiter's bucket map is
swept — an unpruned per-source-IP map *is* the memory-exhaustion vector it was meant
to prevent.

---

## What a relay can and cannot do

Stated plainly rather than glossed.

**A relay can:**

- **See the plaintext of requests it forwards.** It terminates the public TLS. This
  is true of every reverse proxy and every tunnel service; it is not special to
  Vulos. Run your own, or run one belonging to someone whose interests align with
  yours.
- Withhold service, and observe traffic volume and timing.
- Learn which names are registered and which keys are online (discovery role).

**A relay cannot:**

- Forge a signed changeset, a session, or an identity. It terminates a tunnel, not a
  session — the auth handshake your box speaks travels through it unmodified.
- Talk a client into a forged source IP or a false https-ness (the trust boundary).
- Register a name without a grant that authorises it.
- Point clients at an endpoint it has not verified ownership of.

**The mitigation is plurality, not trust.** Run more than one, under different
operators, and no single one is load-bearing.

---

## Alternatives, and when to prefer them

| Option | Use when | Trade-off |
|---|---|---|
| **Direct mode** | The box has a public IP or a forwardable port | Nothing in the middle at all. Strictly best when available. |
| **`vulos relay serve`** | Behind NAT/CGNAT and you want all three facets from one binary | You run a small VPS. |
| **Ephor relay** | You already run Ephor, or want its other coordinator kinds | A separate project to track. Fully supported; same rendezvous contract. |
| **Tailscale / Headscale / WireGuard mesh** | You want a private mesh, not a public URL | Covers facet B only. Facets A and C stay on their own paths. Ingress is actuated by the mesh daemon, outside Vulos. |
| **LAN only** | The box never needs to be reached from outside | Nothing to run, nothing to trust. |

---

## Status and troubleshooting

```bash
# Box: what does reachability look like right now?
curl -s localhost:8443/api/network/reach | jq
# → {"enabled":true,
#    "endpoints":[{"endpoint":{"url":"…","name":"box1"},"healthy":true,…}],
#    "links":[{"label":"…","state":"up","public_url":"https://box1.relay.example.com"}]}

# Box: is the direct fast path active?
curl -s localhost:8443/api/network/direct | jq

# Relay: is it alive?
curl -s https://relay.example.com/_vulos-reach/v1/health
# → {"status":"ok","protocol":1}

# Relay: who is registered? (loopback admin listener)
curl -s localhost:9090/tunnels | jq
```

Neither status surface ever contains a token — the redacted types exist for exactly
this reason.

### Common symptoms

| Symptom | Cause | Fix |
|---|---|---|
| `relay REFUSED this configuration … unauthorized` | The grant does not authorise that name | Add the name to the relay's grant, or correct `name` on the box |
| Link cycles `connecting → backoff` | Relay unreachable, or DNS/TLS wrong | `curl` the relay's health path from the box |
| `404` for a name that should exist | Box not connected, *or* no such name — deliberately indistinguishable | Check `/tunnels` on the relay |
| `502 box unavailable` | The tunnel died mid-request | Transient; the agent reconnects |
| Relay refuses to start: `world-accessible` | Grants or endpoints file is not 0600 | `chmod 600` the file |
| Two boxes on a LAN never sync | `VULOS_FABRIC_SECRET` differs | Make it identical on every box |
| Two boxes in different houses never sync | No rendezvous configured | Set `VULOS_RENDEZVOUS_URL`, and `-rendezvous` on the relay |
| A box flaps up/down every few seconds, relay logs `replacing the previous session` in a loop | **Two boxes share one `name`** — usually a config file copied between machines without editing it. Each eviction closes the other's tunnel, which it then retries. | Give each box its own `name` (and its own grant). One name = one box, always. |

---

## See also

- [RELAY-SELF-HOST.md](RELAY-SELF-HOST.md) — provider recipes (Hetzner, Fly.io)
- [NETWORKING.md](NETWORKING.md) — connection modes, direct mode, DNS, TLS, ports
- [PEERING.md](PEERING.md) — peer identity and the delivery ladder
- [SECURITY.md](SECURITY.md) — the box's overall security posture
- `backend/services/reach/` — endpoint set, tunnel, rendezvous role
- `backend/cmd/vulos/relay.go` — the `vulos relay` command
