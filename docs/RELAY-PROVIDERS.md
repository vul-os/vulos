# Relay-hosting providers — where to run `vulos relay serve`

**Choosing a host for the public-IP machine that fronts your NAT'd boxes.** This
document is about *where* to run a relay and *what it costs*. For *how* to stand one
up, see [RELAY-SELF-HOST.md](RELAY-SELF-HOST.md); for *what a relay is and why it is
built this way*, see [REACH.md](REACH.md).

> **Vulos is free and open-source — we charge nothing, ever.** There is no Vulos
> subscription, licence, or pricing. Every cost below is a *third-party* hosting
> cost you pay **directly to the VPS provider you choose** (or nothing, if you run
> the relay on hardware you already own). None of it goes to Vulos.

---

## Table of contents

1. [The default: run your own Vulos relay](#the-default-run-your-own-vulos-relay)
2. [Where to host a relay](#where-to-host-a-relay)
3. [Where the costs arise](#where-the-costs-arise)
4. [A worked example](#a-worked-example)
5. [The honest third-party caveat](#the-honest-third-party-caveat)
6. [Pier — the supported alternative](#pier--the-supported-alternative)
7. [Using a tunnel service instead (Cloudflare Tunnel, ngrok)](#using-a-tunnel-service-instead-cloudflare-tunnel-ngrok)
8. [See also](#see-also)

---

## The default: run your own Vulos relay

**The primary, recommended path is to run your own relay.** A relay is not a separate
product and not a separate vendor — it is built from this same repository. It is,
however, a **different binary** from the one your boxes run: the relay is
`backend/cmd/vulos`, a box is `backend/cmd/server` (`vulos-server`), and no
release artefact ships the relay, so build it for the relay host yourself
(`cd backend && go build -o vulos ./cmd/vulos`). Then start it in the relay role:

```bash
vulos relay serve -domain relay.example.com -grants-file /etc/vulos/grants.json -rendezvous
```

Put that binary on any machine with a public IP and **it is your relay**. It accepts
the outbound tunnel your NAT'd box holds open and reverse-proxies public traffic down
it. No third-party tunnel service sits in the path, and no account with anyone is
required — the machine is yours, and the software is built from the same tree as
the OS on it.

Two facts from [REACH.md](REACH.md) shape every decision below:

- **A public-IP box needs none of this.** Reachability is direct-first: if a box has
  a reachable public IP or a forwardable port, it is served directly and no relay is
  involved. A relay exists for boxes behind NAT or CGNAT that can dial out but cannot
  be dialled in to.
- **Run more than one.** The box holds a live tunnel to *every* configured relay at
  once — it does not pick one. A second relay under a different operator, in a
  different region, is genuinely redundant rather than standby. One relay is a single
  point of failure; two are not. The rest of this document is written so you can pick
  a *first* host and then a deliberately *different* second one.

The recipe for standing a relay up — grants, TLS, the systemd unit, verification —
lives in [RELAY-SELF-HOST.md](RELAY-SELF-HOST.md). This document only helps you
choose the machine underneath it.

---

## Where to host a relay

A relay forwards bytes. **It needs bandwidth, not CPU** — the cheapest instance a
provider offers is almost always enough compute, so the machine you pick is decided
by its **egress posture** and its price, not its cores. All the recipe needs is a
public IPv4 address, inbound TCP 80 and 443, and a Linux userland (or, on Fly, a
container runtime).

Prices below are **approximate and change often — verify current pricing with the
provider before you commit.** They are given to show the *shape* of each option and
its gotchas, not as figures to quote back later.

| Provider | Monthly cost shape | Static IP | Bandwidth / egress posture |
|---|---|---|---|
| **Hetzner Cloud** | ~€4/mo (CX22-class) | Included | **Generous.** Large monthly traffic allowance (tens of TB) bundled in; overage cheap. The default recommendation for cost. |
| **Fly.io** | ~$2–5/mo compute + metered bandwidth | Anycast v4/v6 included | Bandwidth billed per-GB and **region-priced** — cheap in NA/EU, dearer elsewhere. No VM to patch; Fly terminates TLS. |
| **DigitalOcean** | ~$4–6/mo (basic Droplet) | Included | Metered, but each Droplet ships a **bundled transfer allowance** (hundreds of GB to a few TB); overage per-GB. Simple, well-documented. |
| **Vultr** | ~$3.50–6/mo | Included | Same shape as DigitalOcean — a bundled transfer allowance per instance, then per-GB. Many regions. |
| **Home server / spare box** | Hardware + electricity only | **Your ISP's** — needs a static IP or port-forward | **No metered egress** (subject to your ISP's fair-use / data cap). Removes the third party entirely. |
| **An instance you already run** | ~€0 marginal | Whatever it already has | Reuse a VPS you already pay for; the relay's footprint is tiny. Watch that its egress allowance now covers relayed traffic too. |

### Notes per option

- **Hetzner** is the default recommendation in [RELAY-SELF-HOST.md](RELAY-SELF-HOST.md)
  precisely because the bundled traffic allowance is large and the box is cheap. Pick
  the location nearest **you** (your clients), not nearest your boxes — client latency
  dominates once the tunnel is established. An arm64 (CAX) instance is cheaper still;
  build the binary `GOARCH=arm64`.
- **Fly.io** removes the VM: you ship a container, Fly runs it on anycast IPs and
  terminates TLS. The trade is metered, region-priced bandwidth and one non-optional
  setting — `auto_stop_machines = false`, because a relay must stay up to hold tunnels
  and a healthy held-open tunnel looks exactly like an idle machine. Good when you do
  not want to patch a VM.
- **DigitalOcean / Vultr** are the "ordinary Linux VPS" middle ground — a Droplet or
  instance with a bundled transfer allowance. The Hetzner recipe applies unchanged;
  only server creation (step 1) differs. Both are reputable and heavily documented.
- **A home server or spare box** is the option with **no third party at all** and no
  metered egress (only your ISP's fair-use policy). The catch is a *routable* address:
  most residential connections give you a dynamic IP behind CGNAT, so you need either
  a static-IP add-on from your ISP, a port-forward on a real public IP, or dynamic-DNS
  plus an open inbound port. If your ISP is behind CGNAT with no static-IP option, a
  home box cannot be a relay — that is the same NAT wall the relay exists to climb.
- **Reusing an instance you already run** is often the cheapest real answer: the relay
  is a single static binary with a tiny resident footprint, so a VPS you already pay
  for can host it at ~zero marginal cost. Just confirm the added relayed traffic still
  fits the instance's egress allowance.

**Any VPS works.** OVH, Scaleway, Linode, Oracle Cloud's free tier, a Raspberry Pi on
a business line — the relay only asks for a public IP and two open ports. The list
above is where the price/bandwidth trade-offs are clearest, not an allow-list.

> **Avoid the hyperscalers for a personal relay.** AWS, GCP, and Azure meter egress
> aggressively (commonly ~$0.08–0.12/GB after a small free tier) and *all relayed
> traffic is egress*. See the cost section below — this is the single most expensive
> way to run a relay and there is no upside for this workload.

---

## Where the costs arise

Four cost lines, in rough order of how much they matter for a relay.

### 1. Bandwidth / egress — the cost driver

**All traffic to your NAT'd boxes flows through the relay**, so the bytes leaving the
relay towards your clients are the number that decides the bill. This is the one line
that can surprise you.

- **Generous-bandwidth hosts** — Hetzner bundles tens of TB with a €4 box; overage is
  a few euro-cents per TB. DigitalOcean and Vultr bundle hundreds of GB to a few TB
  per instance, then charge per-GB above it. For a personal fleet you will almost
  never touch the ceiling.
- **Metered hosts** — AWS, GCP, and Azure bill egress per-GB from close to the first
  byte (~$0.08–0.12/GB). A relay that carries file syncs, backups, or Meet/Talk media
  can move real volume, and on a metered host that becomes the dominant cost line
  overnight. Fly is metered too but far cheaper and region-priced.

Two levers reduce relayed egress:

- **Direct mode.** Any box with a public IP or forwardable port bypasses the relay
  entirely once the ownership probe passes (`-direct-probe`). Direct-first is not just
  faster; it is *free* — those bytes never touch the relay.
- **Region choice.** Put the relay near your clients so the fast path is direct and
  the relay carries only fallback traffic.

### 2. The VPS / compute fee

The base monthly charge — ~€4/mo on Hetzner, ~$2–6/mo elsewhere. A relay does not
need CPU, so the *cheapest* tier a provider sells is almost always the right one;
never size up for cores. The default caps (64 agents) sit far above a personal fleet.

### 3. A static / public IP

Usually **free and included** on a cloud VPS (Hetzner, DigitalOcean, Vultr, Fly all
give you one with the instance). Two places it costs extra:

- **Some clouds now charge for IPv4** — a small monthly fee per address as IPv4
  scarcity bites (AWS and others have moved this way). Verify before you assume it is
  free; IPv6-only is not yet enough for a public relay.
- **A residential static IP** is often a paid add-on from your ISP, if offered at all.

### 4. TLS

**Free.** The Hetzner recipe uses Caddy with on-demand Let's Encrypt certificates —
one per box hostname, issued on first request, no wildcard certificate and no DNS-API
credentials needed. Fly terminates TLS for you and issues certificates on request.
There is no line item here on any documented path.

---

## A worked example

**A personal relay on a Hetzner CX22, fronting a handful of home and office boxes.**

| Line | Cost (approximate — verify) |
|---|---|
| CX22 instance (2 vCPU, 4 GB) | ~€4/mo |
| IPv4 address | included |
| Bandwidth (20 TB-class allowance) | included; a personal fleet uses a fraction |
| TLS (Let's Encrypt via Caddy) | €0 |
| **Total** | **≈ €4/month** |

That single ~€4/mo instance comfortably serves **1–10 boxes** — the default agent cap
(64) is far above it, and the bundled traffic allowance is far above what a personal
fleet of syncs and the occasional call will move. Scaling to **10–50 boxes** typically
does not change the instance; you watch the *traffic* graph, not the CPU graph, and
only move up a tier if egress (not load) demands it.

For real redundancy, run this twice — a second relay on a **different provider in a
different region** (say a DigitalOcean Droplet in another continent) roughly doubles
the spend to well under €10/mo total, and now no single operator or region is
load-bearing. If you push heavy file or media traffic, choose the second host for its
**egress allowance**, not its core count.

---

## The honest third-party caveat

A VPS host is an **independent third party**. Two things follow, and both are worth
stating plainly rather than glossing:

- **The relay is content-blind to the extent the design allows — but it does terminate
  the public TLS.** It forwards traffic as ciphertext at the tunnel layer and cannot
  forge a signed changeset, a session, or an identity; the auth handshake your box
  speaks travels through it unmodified. But like *every* reverse proxy and tunnel
  service, the relay process terminates the public TLS and therefore sees the
  plaintext of the requests it forwards. This is not special to Vulos — it is a
  property of being the machine a public client connects to. The full account is in
  [What a relay can and cannot do](REACH.md#what-a-relay-can-and-cannot-do). **The
  mitigation is plurality, not trust:** run more than one relay under different
  operators, and no single one is load-bearing.
- **Availability and billing are the host's terms, not yours.** Uptime, egress pricing,
  IPv4 charges, and fair-use policies are set by the provider and *change*. Choose a
  reputable one, treat every figure in this document as "approximate, verify current",
  and do not build a fleet that falls over if one host has a bad day — which is, again,
  why the box holds tunnels to several relays at once.

**Running the relay on hardware you own removes the third party entirely.** A spare
box on a connection with a static IP or a port-forward is nobody's business but yours:
no external billing, no external availability terms, and the only egress limit is your
ISP's fair-use policy. The single requirement is a routable address — the one thing a
CGNAT'd residential line cannot give you, and the whole reason a relay exists.

---

## Pier — the supported alternative

[Pier](https://github.com/vul-os/pier) is a **supported alternative, not the
default.** It is a separate project that speaks the same rendezvous contract — the
protocol is byte-identical and pinned by a test, so you can mix Vulos and Pier
rendezvous nodes in one list. Prefer it when you *already* run Pier for its other
coordinator kinds, or as a deliberately different operator for your second relay.

For a first, plain reachability relay, the built-in `vulos relay serve` is the
recommended path: it comes from the repository you already have, it needs no second
project to track, and it gives you all three reachability facets (media ICE, HTTP
ingress, and rendezvous) from one process. Reach for Pier as the longer-term or
already-invested-in option, not the starting point.

---

## Using a tunnel service instead (Cloudflare Tunnel, ngrok)

Yes, you can point Cloudflare Tunnel or ngrok at your box instead of running
`vulos relay serve` — **but only in the mode that forwards raw TCP/TLS without
terminating it.** Both products' *default* mode does the opposite of that, so
this needs to be set up deliberately, not assumed:

- **ngrok.** `ngrok http` (the default, and the one every quick-start example
  uses) terminates TLS at ngrok's edge and hands your box decrypted plaintext
  — ngrok's infrastructure sees every request, same as any corporate proxy.
  `ngrok tls` is the mode that qualifies: it forwards the raw TLS byte stream
  straight through, unterminated, to a TLS listener on your box (Caddy, or
  Vulos's own cert). ngrok never holds the plaintext or the private key.
- **Cloudflare Tunnel.** A normal `cloudflared` HTTP/HTTPS ingress rule has
  the same shape as ngrok's default — Cloudflare's edge terminates TLS. A raw
  TCP ingress rule (`cloudflared tunnel run` with a TCP service, or Cloudflare
  Spectrum) forwards the byte stream untouched instead, which is the mode
  that qualifies. Cloudflare's ordinary HTTPS proxy path does not.

The general requirement, so it applies to whatever tunnel provider you're
actually evaluating: **it has to forward TCP/TLS without terminating it.**
That's the one property separating a relay from a man-in-the-middle — a
passthrough tunnel never holds the plaintext or the key, so it cannot read
your traffic even under compulsion or compromise. A provider whose only mode
decrypts at its own edge is the man-in-the-middle the sovereignty design
exists to avoid, regardless of how reputable the company is — see
[SECURITY.md](SECURITY.md) and, for what the built-in relay itself sees and
why, [REACH.md → What a relay can and cannot do](REACH.md#what-a-relay-can-and-cannot-do).

If you go this route, point the passthrough tunnel at your box's own TLS
listener, not at plain HTTP — terminating TLS anywhere before the tunnel
defeats the point of using passthrough mode in the first place.

Weigh it against `vulos relay serve`: a third-party tunnel needs no VPS of
your own, but its uptime, its terms, and whether it *stays* in
passthrough mode are entirely the provider's call, not yours. Running your
own relay keeps the same "someone terminates TLS" property the built-in
relay has (see [The honest third-party caveat](#the-honest-third-party-caveat)
above), but puts that someone under your control, or a deliberately chosen
ally's — not a tunnel company's product decision.

---

## See also

- [RELAY-SELF-HOST.md](RELAY-SELF-HOST.md) — the how-to: standing up `vulos relay serve` on Hetzner and Fly.io, grants, TLS, operations
- [REACH.md](REACH.md) — the architecture: why reachability is built this way, the security model, what a relay can and cannot do
- [NETWORKING.md](NETWORKING.md) — connection modes, direct mode, DNS, TLS, ports
- [CONFIGURATION.md](CONFIGURATION.md) — every environment variable
