# A padlock on your box's LAN address

This is about one narrow thing: making `https://vulos.local` show a **normal
padlock in a normal browser**, with no warning to click through, while your box
has no internet connection.

If you use the native client, you do not need any of this. The native client
pins your box's key at pairing and is unaffected by everything below. This page
is for browsers.

**Status: built, and unmeasured on phones.** The certificate authority, the
operator tool (`vulos-lanca`), the box-side certificate selection, and the
Settings panel that hands you the root are all implemented and tested. What is
**not** done is a measurement: the install instructions below are transcribed
from vendor documentation and have **not been performed on a real Android or
iOS device** by anyone on this project. Chrome, Firefox, Android and iOS are
also absent from the name-constraint enforcement table further down. See
[decisions.md D101](decisions.md#d101-2026-08-17--a-name-constrained-private-root-for-the-browser-surface-d96-ds-pinning-stands-for-native-clients).

---

## Why there is no free option

You cannot buy, or get free from Let's Encrypt, a certificate for
`vulos.local` — or for `192.168.1.50`, or for any name that is not globally
unique in the public DNS. This is not a gap someone forgot to fill; it is a
prohibition.

The CA/Browser Forum's [Internal Names guidance](https://cabforum.org/working-groups/server/internal-names/)
says "the issuance of certificates with a reserved IP address or internal server
name is prohibited", and that on 1 October 2016 every remaining such certificate
was "revoked and/or blocked by browser software". An *internal name* is one that
"cannot be verified as globally unique within the public DNS … because it does
not end with a Top Level Domain registered in IANA's Root Zone Database".
`.local` is exactly that, and RFC 6762 reserves it besides.

A public CA also could not verify you control `vulos.local` — nobody does; it
means a different machine on every network in the world.

So there are three options, and only three:

| Option | Encrypted | Works offline | Padlock | Cost |
|---|---|---|---|---|
| Self-signed certificate | yes | yes | **no** — one warning to click, per browser | none |
| A real domain + Let's Encrypt | yes | **no** — needs DNS and renewal | yes | a domain, and internet |
| **Your own root, installed once per device** | yes | yes | **yes** | one install per device |

The third is what this page describes. It is the only one that gets all three.

---

## What you are actually installing

A **root certificate you generated**, that lives only on your own machine, and
that is **cryptographically limited** in what it can vouch for.

That limit is the whole point. Your device already trusts roughly 150 root
certificate authorities, and every one of them can issue a certificate for any
name on earth — `google.com`, your bank, anything. That is how the public web
works, and it has gone wrong in public more than once.

The root you install here carries an X.509 *name constraint* (`permittedSubtrees`,
RFC 5280 §4.2.1.10) restricting it to:

- `.local`, `.lan`, `.home.arpa` — names that only exist on a local network
- `lan.vulos.org` — the box-specific subdomain
- private and link-local IP ranges (`10/8`, `172.16/12`, `192.168/16`,
  `169.254/16`, `fc00::/7`, `fe80::/10`)

**None of those resolve on the public internet.** If someone steals the root's
private key, they still cannot mint a working certificate for `google.com`. The
worst they can do is impersonate a machine on a local network they are already
on.

You can check this yourself at any time:

```
vulos-lanca inspect ~/.vulos-lanca/root.crt
```

```
permitted DNS  local, lan, home.arpa, lan.vulos.org
permitted IP   10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, fc00::/7, fe80::/10
path length    0 (cannot sign a subordinate CA)
```

Two more limits worth knowing:

- **The private key never goes on the box.** It stays on the machine where you
  run `vulos-lanca`. If it lived on the box, anyone who stole the box would get
  it — and the tool refuses to write it to a box path for exactly that reason.
- **Your box's own private key never moves either.** The box signs a request
  with the key it already has; the CA signs over that same key. So issuing or
  renewing a certificate **never changes your box's fingerprint**, and native
  clients that already paired do not have to pair again.

### Which browsers were actually tested

Name constraints are only useful if the software enforces them. This was
measured, not assumed:

| Verifier | Enforces the constraint? |
|---|---|
| Go `crypto/x509` | **yes**, measured |
| OpenSSL 3.6.3 | **yes**, measured |
| NSS (`vfychain`) | **yes**, measured |
| Apple Security.framework (macOS) | **yes**, measured |
| **Chrome** | **not tested** |
| **Firefox** | **not tested** (it uses mozilla::pkix, not the NSS verifier above) |
| **Android** | **not tested** |
| **iOS** | **not tested** |

Apple's Security.framework is shared between macOS and iOS, which makes the
macOS result the most transferable of the four — but it is still a macOS
measurement, not an iOS device.

---

## Setting it up

### Once, on your own computer

```
vulos-lanca init -label "home"
vulos-lanca root > vulos-root.crt
```

`vulos-root.crt` is the file you install on your devices. It is public and
contains no secret. The private key stays at `~/.vulos-lanca/root.key` and
should never leave that machine.

### Once per box

The box generates a signing request **from the key it already has**. There is no
dedicated command for this yet, so use `openssl` against the key the box already
persists:

```
# on the box — this key already exists; do NOT generate a new one
openssl req -new -key /var/lib/vulos/tls/lan-selfsigned.key \
  -subj "/CN=vulos.local" -out box.csr
```

Using the *existing* key is the point. It is what keeps your box's fingerprint —
and every native client's pin — unchanged when the certificate is issued or
renewed. Generating a fresh key here would silently break every paired client.

Then, on your computer, sign it for the names the box actually advertises:

```
vulos-lanca issue -csr box.csr \
  -dns vulos.local,vulos.lan,vulos-k3n7q2.local \
  -ip 192.168.1.50 \
  -out lan.crt
```

> **Use the names your box really advertises**, which Settings → Network shows.
> Do not guess. If two boxes are on one network, the second is renamed (to
> something like `vulos-2.local`), and a certificate for a name your box does not
> answer to produces a *worse* error than no certificate at all.

Copy `lan.crt` to `/var/lib/vulos/tls/lan.crt` on the box, and copy the box's
existing key to `/var/lib/vulos/tls/lan.key` (they must be the same key pair).
`LoadCertSource` watches those paths and picks up a new certificate on the next
connection, with no restart.

**Also copy the root itself to the box**, at
`/var/lib/vulos/tls/lan-root.crt`:

```
vulos-lanca root > /tmp/vulos-root.crt   # on your computer
# then get that file to the box at /var/lib/vulos/tls/lan-root.crt
```

That is the file the box hands out under **Settings → Network → Browser
Trust**, so every device in the house can fetch it instead of you carrying it
to each one. It is the certificate only — no key, and the tool refuses to write
the key to a box path.

The box checks it before offering it to anyone. Two things it will refuse: a
file that is not a certificate authority (a leaf copied here by mistake — it
installs cleanly on a device and validates nothing), and a CA that carries **no
name constraints**, which would give every device you installed it on authority
over any name on earth. Both refusals appear in the panel with the reason.

The box chooses between the CA-issued certificate and the self-signed one per
connection: the CA leaf is served when it is currently valid and covers the
name the browser asked for, and the self-signed one is served otherwise. A
device *without* your root installed still gets exactly the one click it would
have had — never a hard failure. The box cannot tell which devices have your
root, because the TLS handshake carries no such signal, and it does not need
to.

Some names cannot be covered, and the tool tells you which:

```
SKIPPED vulos — outside this CA's permitted subtrees, so it will NOT be on the certificate.
```

Bare names with no dot (`https://vulos/`) can never be on a constrained
certificate — the X.509 format has no way to express "any single-word name".
Those keep showing the one-click warning. Everything with a dot in it gets the
padlock.

---

## Installing the root on each device

The box will hand you the file: **Settings → Network → Browser Trust**. That
page shows the root's SHA-256 fingerprint, a download button, and a QR code
pointing a phone at the same download (by the box's IP address, not
`vulos.local`, because a phone that cannot resolve `.local` is exactly the case
this whole thing exists for). It carries the per-OS steps below as well, so you
do not have to read this file on a phone.

**Chicken-and-egg, stated plainly: that first download is not authenticated.**
You are fetching the root over a connection your device does not trust yet —
that is the situation by definition, and the box cannot fix it from its end. A
network attacker could serve you a different authority.

What makes it acceptable is that the root is a *public* certificate whose
fingerprint you can check somewhere else. Before you install it, compare the
SHA-256 the panel shows against the machine that actually holds the CA:

```
vulos-lanca inspect ~/.vulos-lanca/root.crt
openssl x509 -in ~/.vulos-lanca/root.crt -noout -fingerprint -sha256
```

This is the same check `vulos -print-pairing` asks you to make of the box's key
fingerprint, for the same reason: a first-contact decision that becomes
permanent is the one worth verifying. If you would rather not rely on it at
all, copy the file across by USB, AirDrop or email instead — it contains no
secret and travels fine.

**Everything below is transcribed from vendor documentation.** Nobody on this
project has performed any of it on a device.

### Windows

1. Double-click `vulos-root.crt` → **Install Certificate**.
2. Choose **Local Machine** (needs admin) or **Current User**.
3. Choose **Place all certificates in the following store** → **Browse** →
   **Trusted Root Certification Authorities**. The default ("automatically
   select") puts it in the wrong store and it will not work.
4. Restart the browser.

Firefox does **not** use the Windows store by default; see the Firefox note
below.

### macOS

1. Double-click `vulos-root.crt` — Keychain Access opens.
2. Add it to the **login** keychain (or **System**, for all users).
3. Find it in the list, open it, expand **Trust**, and set **When using this
   certificate** to **Always Trust**. **Importing is not enough on its own** —
   without this step macOS still will not trust it.
4. Restart the browser.

### iOS and iPadOS — two steps, and the second is easy to miss

1. Email or AirDrop `vulos-root.crt` to the device and open it. iOS downloads it
   as a *profile*.
2. **Settings → General → VPN & Device Management** → tap the downloaded profile
   → **Install**.
3. **This is where most people stop, and it does not work yet.** You must also
   go to **Settings → General → About → Certificate Trust Settings** and turn
   **on** the switch next to your certificate. Until you do, iOS has the
   certificate but does not trust it for TLS, and Safari still shows a warning.

iOS shows a warning dialog when you enable full trust. That warning is accurate:
you are granting the certificate real authority on the device. What it does not
say is that this particular root is name-constrained and cannot be used against
public sites.

### Android — expect a permanent warning, and know why

1. Copy `vulos-root.crt` to the device.
2. **Settings → Security → Encryption & credentials → Install a certificate →
   CA certificate**. Android shows a full-screen scary warning here; continue.
3. Select the file.

Two things you should know before you do this, because neither is obvious:

- **Android will keep telling you your network may be monitored.** Once any
  user CA is installed, Android shows a standing notification and/or a "Network
  may be monitored by an unknown third party" entry in security settings. It is
  not detecting a problem, and it will not go away. It appears because
  user-installed CAs are most commonly installed by workplace monitoring tools,
  so Android warns unconditionally. It cannot distinguish your constrained root
  from a corporate interception proxy.
- **Apps will not trust it — only browsers will.** Since Android 7 (Nougat),
  apps do not trust user-installed CAs unless they explicitly opt in via a
  network security config. Chrome and other browsers do. So this fixes the
  browser and does nothing for a third-party app talking to your box.

Installing to the *system* store instead would avoid both, but requires root and
is not something to recommend.

### Firefox (any platform)

Firefox ships its own trust store and ignores the OS one by default.

- Either: **Settings → Privacy & Security → Certificates → View Certificates →
  Authorities → Import**, tick **Trust this CA to identify websites**.
- Or: set `security.enterprise_roots.enabled` to `true` in `about:config` to
  make Firefox read the OS store.

---

## If you would rather not

Nothing here is mandatory, and skipping it costs less than it sounds like.

- **Click through the warning once.** The connection is encrypted either way;
  the warning is about *identity*, not secrecy. The browser remembers per site.
- **Use the native client.** It pins your box's key and needs no CA at all.

If you install the root and later change your mind, delete it from each device's
certificate store. Your box keeps working — it falls back to the self-signed
certificate and the one-click warning.

## If the certificate expires

Nothing breaks. Certificates last 397 days, and if one expires before you get
around to renewing it, the box falls back to its self-signed certificate — the
one-click warning comes back, and everything keeps working. **An expired
certificate never locks you out of your own box.** That is deliberate: renewal
needs you to run a tool, and a box that became unreachable because its owner was
away for a year would be a much worse failure than a warning.
