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

## 5a. What I could not run, and why

`vitest` could not run on this Mac for most of this work: `frontend/node_modules`
carried only the **linux**-arm64 rolldown native binding, so `npx vitest` failed
to start for **every** test, mine and pre-existing alike. I ran the suite in a
`node:22-bookworm` linux/arm64 container instead, and later — after another
agent reinstalled `node_modules` and it gained the darwin binding — natively on
the Mac. Both were used; the results quoted here are from the native run.

That binding set is shared, concurrently-mutated state. If `npx vitest` dies at
startup with a `@rolldown/binding-*` MODULE_NOT_FOUND, that is the platform of
the last `npm install`, not a broken test.

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
- ~~The install wizard UI~~ **DONE** (`af40e889`). The name field prefills with
  the box's per-instance `default_hostname` (it was EMPTY, and an empty field
  made the step skip its POST, which is what left every box on the shared
  name); it checks availability on a 400ms debounce while the owner types; and
  it shows `notice` and refuses to advance when the box answers
  `applied_live:false`. `saveToBox` had been discarding the success body, so
  no caller could tell a live rename from an inert one.
- ~~**Two mDNS responders run on a bare-metal box** — avahi-daemon (from
  `cmd/init`) and our in-process pion responder. They coexist (MEASURED) and
  agree on the address~~ — **BOTH HALVES OF THAT WERE WRONG. See §7.** They do
  not agree on the address; that disagreement was the defect. And avahi is not
  started by `cmd/init` on any shipping box: both shipped boot paths run systemd
  as PID 1, so avahi-daemon is started by its packaged unit and `cmd/init`'s
  `initnetAvahi` never runs. A sentence saying two responders "agree" is the
  reason nobody looked at the second one for three weeks. Consolidating on one
  responder is still worth a look.

---

## 7. The box published an app's address as its own name (2026-08-17)

### The measurement

On a booted arm64 box with one app running:

```
avahi-resolve -n vulos.local  ->  169.254.23.36
avahi-resolve -a 10.0.2.15    ->  vulos.local
```

The box was `10.0.2.15`. `169.254.23.36` was an IPv4LL address on `vh_bae456`
— the host side of **one application's** veth pair
(`backend/services/appnet/namespace.go:140`). Reverse resolution was right;
forward resolution was not.

That is worse than a routing failure. §2 made `internal/lan/names.go` the single
derivation feeding both the mDNS advertisement and the certificate's SANs
precisely so the two could never disagree — and the certificate carries the LAN
IP as a SAN (`cmd/server/lan_pairing.go`, `certIPs`). An address that is in
neither is a **TLS name mismatch on top of the routing failure**.

### Why nothing saw it

Everything in §2 and §3 tests **our** responder — the in-process pion one, which
answers with `lan.DetectLANIP()`. avahi-daemon is a **third publisher**: the
image installs it, systemd starts it, it derives its answer from its own view of
the interfaces, and no test in this repo had ever asked it a question. §6's
"they agree on the address" was an assertion, not a measurement.

### The mechanism, and which layer owns it

Reproduced end to end in a Debian trixie container, arm64, dhcpcd 10.1.0 and
avahi 0.8-16 (`scripts/smoke-lan-name.sh`). Two mechanisms combine.

**1. dhcpcd manages the app veths.** The image writes no `/etc/dhcpcd.conf`, so
Debian's default applies, and Debian's unit is literally
`Description=DHCP Client Daemon on all interfaces` with
`ExecStart=/usr/sbin/dhcpcd -q -b` — manager mode, no `denyinterfaces`.
Measured, with an appnet-shaped veth present:

```
vh_bae456: soliciting a DHCP lease
vh_bae456: probing for an IPv4LL address
vh_bae456: using IPv4LL address 169.254.69.120
vh_bae456: adding IP address 169.254.69.120/16 broadcast 169.254.255.255
vh_bae456: adding default route
```

The last line is why this half is not cosmetic. dhcpcd broadcasts DHCP DISCOVER
**into every application's network namespace** and will accept whatever answers.
An app that runs a DHCP server on its own side of the veth can hand the **host**
a default route and a set of resolvers. dhcpcd has no business on an app's link
at all: appnet already addresses both ends statically.

**2. avahi publishes the box's name on those links — and this half OWNS the
reported defect.** Fixing (1) alone does not fix it. Measured, with no
link-local anywhere and only appnet's own static addressing present:

```
vulos.local   10.200.23.1      (6 lookups, 6 identical answers)
```

Still not the box, still in no SAN list. An app-network interface acquiring an
address is arguably fine; publishing it as the box's identity is not.

### Why an allow-list and not a deny-list

`deny-interfaces` in `avahi-daemon.conf` takes **exact names only**. Measured,
same container, same scenario:

| config | `avahi-resolve -n vulos.local` |
|---|---|
| `deny-interfaces=vh_*` | `10.200.23.1` — **no effect, globs are not supported** |
| `deny-interfaces=vh_bae456` | `192.168.215.13` — works |

App veth names are derived from a hash of the app id and appear and vanish as
apps start and stop, so an exact-name deny-list cannot be written in advance.
The set of LAN interfaces, by contrast, is knowable when avahi starts. Hence
`scripts/vulos-lan-ifaces.sh` computes an **allow-list**, wired as an
`ExecStartPre` on `avahi-daemon.service` so it is recomputed on every start and
restart rather than once at image build (which would compute it on a developer
Mac).

It **fails open**: a box whose interfaces it cannot classify gets an
unrestricted avahi, not an empty allow-list. A box with no mDNS name at all is a
worse failure than the one being fixed, and it would only appear on hardware
nobody here owns. `TestLANIfacesFailsOpen` pins that, because "always write the
allow-list" is the natural hardening edit.

Both halves are configured from that one file: the dhcpcd `denyinterfaces`
stanza is **generated** from the same glob list the avahi exclusion uses, so the
two cannot disagree about what an app link is.

### What a LAN client actually saw — a correction to the brief

The brief for this work said "a LAN client that picks that record gets somewhere
unroutable." **Measured, that is not what avahi does.** Two containers on one
bridge, the box holding `169.254.23.36` on its app veth, a second host
resolving over the wire:

```
vulos.local  192.168.117.2
vulos.local  192.168.117.2      (5 of 5 identical)
```

avahi registers host address records **per interface** and answers a query
arriving on `eth0` with `eth0`'s records only. So over the wire avahi did not
leak the app address, and the `avahi-resolve` result above is what the box's
**own** resolver sees. That still matters — anything on the box, and anything in
an app namespace, resolving `vulos.local` got an address the LAN listener is not
bound to and the certificate does not name — but the failure is on-box, not
over-the-wire, and the fix should not be sold as more than it is.

The over-the-wire leak vector is our **pion** responder, which sets
`cfg.LocalAddress` once and answers with it on every interface. It is correct
today only because `detectLANIP()` happened to pick the right address; nothing
asserts it cannot pick an app-network one. See "Still open" below.

### The gate

`scripts/smoke-lan-name.sh` — **LANNAME-01**, wired into CI. It builds the box's
network shape in a container: a LAN interface with a **real DHCP server**
(dnsmasq in a network namespace), plus an appnet-shaped veth. Then it runs the
real daemons and asks the box what its own name resolves to.

The control is the point. The `stock` arm runs with no Vulos config and **must**
resolve the name incorrectly; if it ever resolves correctly the scenario has
stopped reproducing the defect and the gate fails rather than reporting a pass
it has not earned.

Observed:

```
arm stock: LAN address is 192.168.77.56, vulos.local answered: 169.254.122.114
control stock: went red as it must
arm fixed:  LAN address is 192.168.77.58, vulos.local answered: 192.168.77.58
every one of the 6 lookups answered with the LAN address
IPv4LL addresses on vh_bae456 — stock: 1, fixed: 0
PASS
```

Both mutations red:

| mutation | result |
|---|---|
| `--break avahi` | `fixed` answered `10.200.23.1` — exit 1 |
| `--break dhcpcd` | IPv4LL on `vh_bae456` — stock 1, **fixed 1** — exit 1 |

Note what `--break dhcpcd` shows: name resolution was still *correct*, because
the avahi allow-list already excluded the app link. Assertion C is what caught
it. That is the layering working as designed — avahi owns the name, dhcpcd owns
not being on an app's link — and it is why both assertions have to exist.

The first version of this gate was itself wrong, and in an instructive way: run
against docker's own `eth0` there is no DHCP server on the link, so dhcpcd
IPv4LL-addressed **the LAN interface itself** and the `fixed` arm failed for a
reason that had nothing to do with app veths. A box on a network with no DHCP
server taking an IPv4LL address on its LAN NIC is correct behaviour and must not
be "fixed". The scenario was wrong, not the config.

### What this does NOT prove

- **It runs in Docker.** There is no udevd, so dhcpcd is given `nodev` — without
  it dhcpcd prints "waiting for interface to initialise" for every interface,
  finds none, and exits, which would have made the gate green for the wrong
  reason. Nothing that depends on real udev ordering, real NIC drivers, wifi
  association, or systemd unit ordering on a real boot is covered.
- **No Vulos image is booted.** `TestLANIfacesReachesBothBuildPaths` reads
  `build.sh` and asserts both the image path and the ssh-deploy path install and
  wire the script; that is a source assertion, not a boot.
- **One app veth.** Nothing here says anything about many.
- **The `ExecStartPre` snapshot.** The allow-list is recomputed on every avahi
  start, but a NIC that appears *later* — a USB ethernet adapter hotplugged into
  a running box — is not in it until avahi restarts. Real gap, not addressed.
- **The fix has not been observed on a booted box.** The measurement that opened
  this section came from one; the measurement that closes it did not.

### Still open

- **`detectLANIP()` can return an app-network address.** Its fallback
  (`internal/lan/lan.go`) returns the first `IsPrivate()` IPv4 across all
  interfaces, and appnet's `10.200.0.0/16` is private. The primary path — a UDP
  `connect` toward `192.168.1.1` — masks it on any box with a default route, so
  this is latent rather than live. But that value is both what pion advertises
  **to the whole LAN** and what goes into the certificate's IP SAN, so unlike
  the avahi case it would be a genuine over-the-wire failure. It should select
  by interface (excluding the same app globs) rather than by address range, and
  nothing currently asserts it.
