# Chat and video calling: third-party by design

Vulos ships **no first-party chat or video-calling product**. There is no Vulos
Talk, no Vulos Meet. This chapter explains why, what the OS offers instead, how
to install it, and how self-hosting the servers behind it fits the sovereign-box
story. For direct peer-to-peer messaging between your own approved contacts —
which *is* first-party — see [PEERING.md](PEERING.md).

---

## Why no first-party comms product

Real-time chat and video are the one product area where reinventing the wheel
buys nothing and costs a great deal:

- **Established open protocols already solve this.** Matrix (chat, an open
  federated protocol with dozens of independent server and client
  implementations) and the WebRTC/Jitsi/SFU stack for video conferencing are
  mature, audited, and widely deployed. A first-party Vulos protocol would be a
  strictly worse, unfederated island next to them.
- **Federated and self-hostable, so there's no lock-in.** A Matrix homeserver
  talks to every other Matrix homeserver and to matrix.org itself; a Jitsi
  deployment talks WebRTC to any Jitsi-compatible client. Your account, your
  contacts, and your call history aren't trapped behind a Vulos-only wire
  protocol — you can point the client at any homeserver or Jitsi instance,
  including one you run yourself, and switch without losing anything.
- **It keeps the OS's own security promises honest.** Vulos's actual
  differentiator — a sovereign box that is the authority for *your* data — is
  better spent on Files, Mail's IMAP/CalDAV connector model, and the
  peer-to-peer **Messages** builtin (see below) than on maintaining a
  from-scratch chat/video server that would inevitably lag Matrix and WebRTC on
  security review and interoperability.

So the App Store's answer to "I want group chat and video calls" is: install a
best-in-class open client, and optionally self-host the server it talks to.

---

## What the App Store offers

Four registry entries, installed the same way as any other App Hub app (see
[APPS.md § Installing apps from the App Hub](APPS.md#installing-apps-from-the-app-hub)):

| Registry ID | App | What it is | Install type |
|-------------|-----|------------|---------------|
| `element` | **Element** | Full-featured Matrix client — chat, voice, and video calls, works with any Matrix homeserver | Flatpak (`im.riot.Riot`), streamed as a native app window |
| `cinny` | **Cinny** | Lighter, web-based Matrix client for the same protocol — a faster, simpler alternative to Element for chat | Static web bundle, pinned download + checksum |
| `jitsi-meet` | **Jitsi Meet** | Video conferencing client — join any Jitsi deployment (including the public `meet.jit.si`) with no account required | Flatpak (`org.jitsi.jitsi-meet`), streamed as a native app window |
| `element-call` | **Element Call** | Native Matrix group video calling (MSC3401) — a static web client that plugs into a Matrix homeserver + a LiveKit SFU for group calls; can also run embedded as a widget inside Element/Cinny | Static web bundle, pinned download + checksum |

All four are `vetted: true` and Ed25519-signed by the release key, like every
other registry entry — see [APPS.md § Supply-chain rules](APPS.md#supply-chain-rules)
and [KEY-CEREMONY.md](KEY-CEREMONY.md) for what that signature actually
guarantees. Element and Jitsi Meet install and run like any other Flatpak app
on the box; Cinny and Element Call install as static web bundles served over
HTTP, matching the "hosted-web experience the user configures" shape.

### Installing one

From the shell: open **App Hub**, find the app, click install. From the API:

```bash
# List the registry entry (confirms it's present and shows available versions)
curl https://os.example.com/api/store/registry/element

# Install the latest version
curl -X POST https://os.example.com/api/store/registry/install \
  -H "Content-Type: application/json" \
  -d '{"app_id":"element","version":"latest"}'
```

Install is admin-gated, same as any other registry install.

### Element Call needs two things pointed at it

Element Call is a static frontend only — it has no backend of its own. After
install, edit its `static/config.json` (in the app's data directory) to point
at:

1. **A Matrix homeserver** that supports MSC3401 group calls.
2. **A LiveKit SFU** (or another compatible focus) that actually mixes/relays
   the media.

Until you do, the client has nothing to connect to. This is the same shape as
Cinny needing a homeserver URL — the App Store installs the *client*; you
configure which server it talks to.

---

## Self-hosting the servers: the sovereign-box story

Installing a client from the App Store gets you *access* to Matrix or Jitsi.
Whether the server side is also self-hosted (on this box, another box you own,
or a third party) is a separate, independent choice — exactly the choice you'd
make for any federated protocol.

### Matrix homeserver

The registry carries a **Conduit** entry (`conduit`, Rust, single binary,
`admin_only: true` since a homeserver is infrastructure, not a personal app)
as the self-host path, and it's enabled.

Famedly's own `famedly/conduit` GitLab releases remain source-archives-only
(no prebuilt binary, so no sha256 to pin) — that project stalling on
distributable binaries is exactly why the entry shipped `_disabled: true`
for a while. The entry now tracks **[Continuwuity](https://continuwuity.org)**
instead: the actively-maintained community continuation of Conduit/conduwuit,
which does publish signed prebuilt Linux binaries. The shipped version
(`0.5.9`, `conduwuit-linux-amd64`) was downloaded directly and its sha256
computed locally — no vendor-published digest exists to compare against, so
this is a first-party-verified checksum, not a copied one — and boot-tested
in a container matching the box's runtime image (fresh RocksDB store, admin
room created, listening on `127.0.0.1:6167`) before being enabled. Same
supply-chain rule as everything else in the registry
([APPS.md § Supply-chain rules](APPS.md#supply-chain-rules)): a pinned
checksum is mandatory, and this one is real.

#### Running your own homeserver

Install `conduit` from the App Hub (or `POST /api/store/registry/install`
with `{"app_id":"conduit","version":"0.5.9"}`) like any other registry app.
Its `post_install` step writes a minimal `conduit.toml` (server name
`localhost` by default, RocksDB storage under the app's `data/` directory,
registration disabled) and it listens on `127.0.0.1:6167`. Then point
Element's or Cinny's homeserver field at it. Federation, TLS termination,
and exposing `6167` outside `localhost` (see [NETWORKING.md](NETWORKING.md)
for the box's reverse-proxy/ingress options) are yours to configure — the
registry entry installs and runs the binary; it doesn't make routing
decisions for you.

If you'd rather not run a homeserver on this box at all, self-hosting a
homeserver still doesn't require the App Store:

- Running Conduit/Continuwuity (or Synapse, Dendrite, etc.) yourself outside
  the App Store flow — any Docker host or VM works, since Matrix federation
  doesn't care where the homeserver runs — and pointing Element/Cinny's
  homeserver field at it, or
- Using an existing homeserver you already trust (matrix.org, or one your
  organization runs).

Either way, Element and Cinny are homeserver-agnostic: nothing in the client
install ties you to a specific server.

### Jitsi instance

There's no Jitsi *server* registry entry (`jitsi-meet` is the client only,
installed via Flatpak). Jitsi Meet's client can join **any** Jitsi deployment
without an account, so the default is simply joining `meet.jit.si` or another
public instance. To self-host the server side, run the official
[docker-jitsi-meet](https://github.com/jitsi/docker-jitsi-meet) stack on this
box or another one, then point the client at your instance's URL when starting
a call. Because Jitsi speaks standard WebRTC, self-hosting the server doesn't
require a matching client version or a Vulos-specific integration.

### Why this is still "sovereign"

The sovereign-box property here isn't "the OS ships its own protocol" — it's
"nothing about your chat or video traffic is *forced* through Vulos
infrastructure." Real-time chat and video are third-party end to end, and nothing about comms
runs through Vulos infrastructure. Whether you point Element at
matrix.org or at a homeserver you run yourself, the box is never relaying your
messages through a Vulos-operated chat backend, because there isn't one.

---

## The sovereign alternative: peer-to-peer Messages

If you don't want a Matrix homeserver at all, the OS's own **Messages**
builtin (Vula peering — see [PEERING.md](PEERING.md)) is first-party,
box-to-box, and requires no server: messages are signed envelopes delivered
directly between your box and an approved contact's box (`/api/peering/inbound/message`),
with a relay fallback only when direct delivery is unreachable, and group
calls carried by an in-process Pion SFU (`backend/services/peering/sfu`) with
no host-registry escalation. It has no group chat / group video the way Matrix
or Jitsi do — it's aimed at direct, sovereign contact-to-contact communication,
not a Matrix/Jitsi replacement. Use it for that; use Element/Cinny + Jitsi
Meet/Element Call from the App Store when you want federated group chat or
conferencing.

---

## Quick reference

| I want... | Use |
|-----------|-----|
| Group chat with people outside my Vulos boxes | Element or Cinny (Matrix), any homeserver |
| Ad-hoc video call, no account | Jitsi Meet, join a public instance |
| Native Matrix group video calls | Element Call, configured against a homeserver + LiveKit SFU |
| Direct encrypted messaging/calls with approved contacts, no server | Messages (Peering), see [PEERING.md](PEERING.md) |
| To self-host the homeserver | Install `conduit` from the App Hub (Continuwuity), or run Synapse/Dendrite/etc. yourself, and point Element/Cinny at it |
| To self-host the video conferencing server | Run [docker-jitsi-meet](https://github.com/jitsi/docker-jitsi-meet) yourself and point Jitsi Meet at it |
