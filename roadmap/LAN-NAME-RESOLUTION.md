# LAN name resolution — what a browser on the LAN can actually reach

Status: **measured 2026-08-17**, fixes shipped. Everything below is either a
measurement made in this repo (marked MEASURED, with the command output) or a
published vendor claim (marked PUBLISHED — NOT measured here). Nothing is
presented as fact on reasoning alone.

Requirement driving this: *"It must be accessible from a browser without
installing the Android app — it must work through Chrome."*

---

## 1. What was broken

### 1.1 The box's hostname was literally `"vulos\n"` — MEASURED

`build.sh:392` / `build.sh:1257` write `/etc/hostname` with `echo "vulos" >`,
which is **correct**: `hostname(5)` specifies a newline-terminated file.
`cmd/init`'s `setHostname()` then read the whole file and passed it to
`sethostname(2)` untrimmed. The kernel does not validate hostname bytes.

Running this repo's real `setHostname()` in a privileged Debian trixie
container (docker, linux/arm64):

```
PROBE: /etc/hostname bytes = "vulos\n"
PROBE: os.Hostname() before = "062dd0d62ce1"
PROBE: os.Hostname() after  = "vulos\n" (err=<nil>)
PROBE: uname(2) nodename    = "vulos\n"
PROBE: len(after)=6 hex=76756c6f730a
```

Invisible under Docker/systemd, where systemd — not this code — sets the
hostname and trims. On bare metal `cmd/init` is PID 1 and `startSystemd()` only
logs, so this value stood. **Fourth instance of the "verified in Docker, absent
on bare metal" class.**

**It was NOT a `.local` resolution failure.** avahi-daemon normalises the stray
newline and still published a clean name — MEASURED:

```
Server startup complete. Host name is vulos.local. Local service cookie is 3289239450.
```

and from a second container on the same link:

```
$ getent hosts vulos.local
172.31.99.2     vulos.local
```

The damage was everywhere the value is **read back** rather than re-derived:
`os.Hostname()` → `config.Hostname` → `config.NodeID`, which reach the pairing
payload's box name, cluster node metadata, telemetry and kit backups.

Fixed by trimming and validating in the **reader**. `build.sh` is unchanged —
the file was right.

### 1.2 Two boxes on one LAN collided, and the client picked at random — MEASURED

Every box shipped `/etc/hostname = vulos`, and `internal/lan` advertised a
hard-coded `vulos.local`. **pion/mdns performs no RFC 6762 §8 probing at all** —
`Server()` claims every `LocalName` unconditionally.

Two containers running the real advertiser on one link, ten lookups of
`vulos.local` from a third host:

```
172.31.99.6 / .5 / .5 / .5 / .5 / .5 / .5 / .5 / .6 / .5
```

A coin flip. And because **both certificates carried `vulos.local`**, TLS
succeeded on the wrong box with no warning at all. Under the standing "every
instance is almost a direct clone" directive, the two boxes then look identical
to the user.

On the bare-metal path avahi-daemon runs too, and it *does* probe — MEASURED:

```
Withdrawing address record for 172.31.99.4 on eth0.
Host name conflict, retrying with vulos-2
Server startup complete. Host name is vulos-2.local.
```

`vulos-2.local` was in **nobody's SAN list**, so that path gave a name-mismatch
TLS error instead of a silent wrong box. Both names resolved:

```
172.31.99.2     vulos.local
172.31.99.4     vulos-2.local
```

Note both responders coexist on a bare-metal box: avahi binds `0.0.0.0:5353`
and pion binds the group address, and pion started successfully alongside it
(MEASURED).

### 1.3 Renaming the box would have broken TLS

`POST /api/identity/hostname` (wired to `Setup.tsx`'s `IS05_hostname` field)
validated a name, persisted it, wrote `/etc/hostname` — and stopped. It never
called `sethostname`, so the running box kept answering to its old name while
the response reported success. And the SAN list was the literal
`{"vulos.local", lanHost}`, so once the new name *did* take effect at the next
reboot, the box answered to a name its own certificate never mentioned.

A third writer, `cmd/server/main.go`, hard-coded `"vulos\n"` at startup and
wrote it back over the owner's chosen name on **every boot**.

### 1.4 The certificate carried no IP SANs at all

`lan.LoadCertSource(..., hosts, nil)` — the `nil` is the IP list. So
`https://192.168.1.50`, the universal fallback for any client that cannot
resolve `.local`, raised a **NAME MISMATCH on top of** the unknown-issuer
warning: two errors where there should be one.

---

## 2. What changed

`internal/lan/names.go` is now the **single derivation**. `NewNameSet(instanceID,
hostname)` produces the mDNS names **and** the certificate SANs, so they cannot
drift. Three hard-coded literals (a const in `lan.go`, a list in `main.go`, a
third copy in `lan_pairing.go`) are gone.

| Change | Effect |
|---|---|
| Default box name is per-instance (`vulos-<6 ULID chars>`) | Two boxes never want the same name. An owner who just clicks Next cannot collide. |
| mDNS conflict probe before claiming | First box keeps the friendly name; the second drops it and keeps its unique one. First-come-first-served, not random. |
| `Service.SetHostname` | Rename is live: re-derives names, restarts the advertisement. |
| Dynamic cert SANs (the `sync.Once` is gone) | Cert re-mints on rename **and** on a DHCP move. The persisted key is reused, so **every SPKI pin and browser exception survives** (guarded by `TestSelfSignedRemintKeepsSPKI`, which also asserts the serial *did* change so the check cannot pass vacuously). |
| IP + short-form SANs | `https://<lan-ip>`, `http://vulos-k3n7q2/`, `.lan`, `.home.arpa` all covered. |
| `GET /api/identity/hostname/available` | The wizard can say "taken by 192.168.1.9" **while the owner types**, instead of avahi silently renaming the loser hours later. |
| DHCP option 12 | The box now tells the router its name (see §4). |

### Verified end to end on a real link — MEASURED

Two boxes, second one booting into the conflict:

```
BOX 1: CLAIMED  = [vulos-k3n7q2.local vulos.local]
BOX 2: [lan] mDNS name vulos.local is already claimed on this link by
             172.31.99.2 — not advertising it from this box
BOX 2: CLAIMED  = [vulos-b4m8r3.local]
```

Twelve lookups from a third host, where it used to be a coin flip:

```
172.31.99.2  vulos.local     (x12, all identical)
172.31.99.2  vulos-k3n7q2.local
172.31.99.4  vulos-b4m8r3.local
```

Live rename, no restart:

```
[lan] renamed to "study"; now advertising [study.local vulos-k3n7q2.local vulos.local]
[lan] LAN SANs changed (...) — re-minting the self-signed certificate; the
      persisted key and therefore every SPKI pin is unchanged
AFTER RENAME cert DNS SANs = [study study.local study.lan study.home.arpa
      vulos-k3n7q2 ... vulos vulos.local ... box.01h....lan.vulos.org]
AFTER RENAME cert IP  SANs = [127.0.0.1 172.31.99.2]
```

And from the second host, dialling and checking the served leaf against each
name (`x509.VerifyHostname`):

```
CLIENT: study.local          cert NAME MATCHES (issuer still untrusted, as expected)
CLIENT: vulos-k3n7q2.local   cert NAME MATCHES
CLIENT: vulos.local          cert NAME MATCHES
CLIENT: 172.31.99.2          cert NAME MATCHES
```

All four would previously have been either a mismatch (`study.local`, the IP)
or a wrong-box success (`vulos.local`).

---

## 3. Which clients can resolve `.local`

| Platform | Resolves `.local`? | Evidence |
|---|---|---|
| macOS / iOS | Yes, Bonjour, built in | PUBLISHED — not measured here |
| Windows 10 1703+ | Yes, native mDNS resolver | PUBLISHED — not measured here |
| Linux + `nss-mdns` | Yes | **MEASURED** — `getent hosts vulos.local` → `172.31.99.2` on Debian trixie with `hosts: files mdns4_minimal [NOTFOUND=return] dns` |
| Linux, systemd-resolved with MulticastDNS | Yes | PUBLISHED — not measured here |
| **Android 12+** | **Yes** — see below | PUBLISHED (AOSP), corroborated by a MEASURED protocol test |
| Android 11 and earlier | No | PUBLISHED — AOSP says the code was not backported |
| Go programs built `CGO_ENABLED=0` | **No** | **MEASURED** — see below |

### 3.1 Android — the brief's premise was out of date

The task brief stated Chrome on Android "cannot resolve `.local`… Android's
system resolver does no mDNS for hostname lookups". **That is no longer true**,
and it changes the plan.

AOSP's DNS Resolver documentation states (PUBLISHED, quoted from
`source.android.com/docs/core/ota/modular-system/dns-resolver`): since
**November 2021** the Android resolver supports `.local` resolution,
implementing *"5.1 One-Shot multicast DNS Queries"* of RFC 6762 — calling
`getaddrinfo()` with a `*.local` hostname transparently sends a query to
`224.0.0.251:5353` / `[FF02::FB]:5353` and returns the local address. Stated
limitation: **"VPN and mobile data connections are excluded from `.local`
resolution."**

I have **no Android device here and did not test Chrome on Android.** What I
*could* test is whether our box answers the exact query shape AOSP describes. A
one-shot querier from an ephemeral unicast port, both without and with the QU
(unicast-response) bit — MEASURED:

```
ONESHOT: study.local        QM (no unicast-response bit)   REPLIED 172.31.99.2
ONESHOT: study.local        QU (unicast-response bit set)  REPLIED 172.31.99.2
ONESHOT: vulos-k3n7q2.local QM                             REPLIED 172.31.99.2
ONESHOT: vulos-k3n7q2.local QU                             REPLIED 172.31.99.2
ONESHOT: vulos.local        QM                             REPLIED 172.31.99.2
ONESHOT: vulos.local        QU                             REPLIED 172.31.99.2
```

So the box speaks the protocol Android documents. **Unverified:** that Chrome on
Android actually routes address-bar hostnames through `getaddrinfo()` rather
than its own resolver stack; whether the mainline `DnsResolver` module is
present on non-GMS devices; and real Wi-Fi AP behaviour (IGMP snooping,
multicast-to-unicast conversion and client isolation all differ from a Docker
bridge). **This needs one test on one real Android phone before anyone relies on
it.** That is the single highest-value open verification here.

### 3.2 Go clients do not resolve `.local` when built without cgo — MEASURED

```
CLIENT: study.local  DIAL FAILED: lookup study.local on 127.0.0.11:53: server misbehaving
```

Go's pure-Go resolver goes straight to `/etc/resolv.conf` and never consults
NSS, so `nss-mdns` is bypassed. `GODEBUG=netdns=cgo` does not help a binary
built `CGO_ENABLED=0` (which is what cross-compiling from macOS produces).
Relevant to `clients/core/` and any Go tooling that dials a box by name: dial
the IP, or resolve via mDNS explicitly. Not fixed here — recorded.

---

## 4. The router path (DHCP option 12)

If the router registers the box's DHCP hostname in its own resolver, then
`http://vulos-k3n7q2/` or `http://vulos-k3n7q2.lan/` works for **every** device
on the LAN with no mDNS, no app and no per-device configuration. That is the
one path that reaches a client whose resolver does not speak mDNS at all.

**It could not have worked before.** `initnetDHCPCmd` prefers busybox `udhcpc`,
which sends **no** hostname unless given `-x hostname:<name>`; `dhcpcd` (which
does send it) is looked up last. So the router learned nothing. The box now
sends option 12 with its derived name. `dhclient` is deliberately left alone —
its hostname comes from `dhclient.conf`, and guessing a flag that varies across
versions risks breaking DHCP outright, and a box with no lease is far worse than
a box whose name the router does not know.

The certificate carries `<name>`, `<name>.lan` and `<name>.home.arpa` so this
path costs one warning (unknown issuer) rather than two. `.lan` is OpenWrt's
historical default local domain; `.home.arpa` is the RFC 8375 reserved
residential name.

**UNVERIFIED, and it is router-dependent:** which consumer routers actually
publish option-12 names, and under which local domain. I have no router to test.
Sending the option is free and correct regardless; what a given router does with
it is the router's business. Do not present this as working until someone tests
it on real hardware.

---

## 5. Should the `:53` responder forward? — RECOMMENDATION: **NO**

### What it does today — MEASURED

```
DNS53: box.abc.lan.vulos.org  -> 192.168.1.50
DNS53: www.google.com         -> NXDOMAIN (no answer, no recursion, no upstream)
DNS53: github.com             -> NXDOMAIN
DNS53: vulos.local            -> NXDOMAIN
DNS53: example.org            -> NXDOMAIN
```

So the concern is real and worse than "returns nothing": it returns
**NXDOMAIN**, an authoritative *"this name does not exist"*, which clients
negative-cache. Pointing a phone — or a router's DHCP — at the box as its
resolver would break every other lookup on that device, stickily.

### Why not build the forwarder

1. **The premise it was meant to solve has moved.** It was the most promising
   route to Chrome-on-Android *while we believed Android could not do `.local`
   at all*. AOSP documents that it can (§3.1), and the box answers the exact
   query shape (MEASURED). The forwarder is now a large, risky answer to a
   question that may already be answered — and §3.1's one phone test settles
   that for a few minutes of work.
2. **It makes the box a household-wide single point of failure.** Box down or
   rebooting for an update = **no internet for anyone in the house**. For a
   product whose entire pitch is sovereignty and self-hosting, converting "my
   box is offline" into "my family's internet is broken" is the worst possible
   failure mode. It would also produce support load that has nothing to do with
   the box.
3. **It contradicts an existing, deliberate decision.** `VULOS_LAN_DNS_DISABLE`
   exists precisely because an unexpected `:53` on someone's home network is
   rude — port conflicts with a router or Pi-hole, a surprise
   authoritative-looking service on a LAN scan. Forwarding makes that listener
   far more consequential, not less.
4. **Attack surface.** A recursive/forwarding resolver that is reachable
   off-link is a DNS amplification reflector. The mitigation (strict source
   check against the local subnet) is easy to write and easy to get subtly
   wrong, and the blast radius of getting it wrong is other people's networks.
5. **Cheaper answers cover the same ground.** IP SANs make
   `https://<lan-ip>` a one-warning path on every client that exists. Option 12
   (§4) gives the router-served short name for free. Both are shipped.

### If it is ever built anyway

Non-negotiable constraints, recorded so nobody has to re-derive them:

- **Opt-in only** (a third env knob, never on by default, never implied by
  `VULOS_LAN_ENABLE`).
- **Refuse any query whose source is outside the box's own subnet**, before
  parsing.
- **Never the only resolver in a DHCP offer.** If the box is handed out as a
  resolver, a working secondary must be handed out alongside it.
- **Offline behaviour must be MEASURED, not assumed.** The entire point of this
  listener is that it keeps working with the internet down; a forwarder whose
  upstream is unreachable must still answer the authoritative name promptly and
  must not hang. Untested, that is exactly how "works offline" quietly becomes
  false.

---

## 6. Still open

- **Test one real Android phone** with Chrome against a box. Highest-value
  remaining verification; everything in §3.1 hangs on it.
- **Test a real consumer router** for §4 (option-12 registration, and which
  local domain it appends).
- **Real Wi-Fi, not a Docker bridge.** All mDNS measurements here were made on a
  docker bridge network. APs with IGMP snooping, multicast-to-unicast
  conversion, or client isolation can behave differently, and some deliberately
  drop mDNS between clients.
- **Two boxes booting simultaneously** can both pass the conflict probe before
  either answers, and both claim the generic name. Narrow window, benign
  outcome (the pre-existing behaviour), and it cannot affect the per-instance
  names, which differ by construction. Full RFC 6762 §8.2 tie-breaking would
  close it.
- **The install wizard UI** (`frontend/src/auth/Setup.tsx`) is not updated: it
  should call `GET /api/identity/hostname/available` while the user types, and
  it must surface `applied_live=false` + `notice` instead of a success tick.
  Held by another agent.
- **Two mDNS responders run on a bare-metal box** — avahi-daemon (from
  `cmd/init`) and our in-process pion responder. They coexist (MEASURED) and
  agree on the address, but it is duplicated machinery; consolidating on one is
  worth a look.
