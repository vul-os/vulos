# Connecting your box to Vulos Cloud

Vulos runs fully standalone — but you can optionally link your box to Vulos Cloud, the hosted control plane, to get one-account sign-in, brokered third-party integrations, push for sleeping cells, and a signed update channel. This chapter explains what connecting gives you, what it costs in sovereignty terms, exactly what the cloud can and cannot see, and how each seam works: unified sign-in, device enrollment, the integrations broker, push notifications, and OTA updates.

> **The short version:** your box stays the authority. Vulos Cloud never holds your files, messages, or notification content. Every cloud seam is opt-in, fail-closed, and designed so the control plane handles *coordination*, not *content*. If you never set a single cloud environment variable, no part of the OS talks to Vulos Cloud.

Related chapters: [GETTING-STARTED.md](GETTING-STARTED.md) for install, [CONFIGURATION.md](CONFIGURATION.md) for the full env-var reference, [PEERING.md](PEERING.md) for box-to-box identity and relay sharing, [SECURITY.md](SECURITY.md) for the broader security model, and [TROUBLESHOOTING.md](TROUBLESHOOTING.md) when something in this chapter fails.

### Where Vulos Cloud runs, and what it is

Vulos Cloud is deliberately small. Its whole job is **coordination**: it is the
Control Plane (CP) plus the relay and box provisioning. It is **not** where your
apps run — Ofisi and Files all run on your box (self-host at $0, or a
Vulos-managed box). **Mail is a connector** — the inbox talks to a mailbox you
already own (Gmail/Outlook/any IMAP/SMTP), so Vulos runs no mail server for you
by default. **Real-time chat and video are third-party** — Vulos Talk and Vulos
Meet are retired as first-party products; install Cinny/Element (Matrix chat)
or Jitsi Meet/Element Call (video) from the App Store instead (see
[COMMS.md](COMMS.md) for why, and how self-hosting the servers fits in).
Nothing about comms runs centrally in Vulos Cloud. The cloud's scope is exactly
three things:

- **Relay** — reachability fallback for boxes behind NAT.
- **Provisioning** — standing up a managed box for you.
- **Control Plane (CP)** — the coordination metadata described throughout this
  chapter (device identities, OAuth grant existence, push handles, billing).

The CP is a **single EU region** backed by **one Neon Postgres** database. There
is no multi-region CP fan-out and no per-region database sprawl — one region, one
control-plane database, content-blind by design. Billing is **per box** (self-host
is $0).

---

## What connecting gives you — and what it costs

| Capability | Without the cloud | With the cloud |
|---|---|---|
| Sign-in | Local accounts, passkeys, PIN, TOTP — all on-box | One Vulos account signs you in to the cloud console *and* to your OS |
| Third-party integrations (Google, …) | Bring your own OAuth app (self-host mode) | The cloud brokers OAuth; refresh tokens live on the control plane, your box mints short-lived access tokens |
| Web Push | Your box pushes directly to browser vendors (needs VAPID keys) | Same — plus, for managed cells that sleep, the cloud can send a content-blind "new mail" nudge on your behalf |
| OS updates | Signed public bucket, no account needed | Same — the update bucket is public-read and independent of your cloud account |
| Reachability across networks | Direct connections, LAN discovery | Relay fallback for boxes behind NAT (see [PEERING.md](PEERING.md) and [NETWORKING.md](NETWORKING.md)) |

The sovereignty cost is bounded and legible:

- **Your cloud account credentials are the cloud's.** When you sign in with a Vulos account, your email and password go to the control plane — that is what a hosted account is. The OS backend proxies the login so your *browser* never talks to the cloud, and it never persists your cloud session (the session cookie lives only in memory for the duration of one sign-in flow).
- **The control plane learns coordination metadata**, not content: your device's ULID, which account owns it, push subscription handles, and OAuth grant existence.
- **Everything below is grounded in the shipped code** — file references are given so you can verify the claims yourself.

---

## One switch: pointing your box at a control plane

The cloud seams activate when a control-plane URL is configured. Out of the box (self-host, no env vars), they are inert.

| Variable | Default | Used by |
|---|---|---|
| `VULOS_CLOUD_URL` | `https://api.vulos.org` | Integrations broker, device enrollment |
| `VULOS_CLOUD_API_URL` | `https://api.vulos.org` | Cloud signup proxy, unified sign-in, identity claim; enrollment falls back to this when `VULOS_CLOUD_URL` is unset |
| `VULOS_CP_URL` | _(empty)_ | Canonical control-plane selector; setting either this or `VULOS_CLOUD_URL` switches integrations from self-host mode to cloud-broker mode |
| `VULOS_CLOUD_ORIGIN` | _(derived)_ | Overrides the `Origin` header the box presents to the control plane's CSRF gate |

For managed Vulos Cloud instances these are pre-set. For a self-hosted box that wants cloud features, set `VULOS_CLOUD_URL` (and leave the rest at defaults).

---

## Unified sign-in: one Vulos account, cloud login, OS session

Since the unified sign-in release, one Vulos account signs you in to both the cloud console and your OS. You never create a second password for the box unless you want a local-only account.

### The user flow

**During first-boot setup** (`src/auth/Setup.jsx`): the wizard offers a choice between a *local-only* account and *Connect Vulos Cloud* — "Link this device to a Vulos Cloud account for remote access, 2FA, and optional cluster sync." You can create a new cloud account from the wizard or sign in with an existing one. Your cloud credentials become your OS login; a local user is created automatically.

**On the login screen** (`src/auth/LoginScreen.jsx`): once the box is enrolled, a **"Sign in with Vulos Cloud"** button appears. Clicking it shows a cloud email/password form. If your account has TOTP 2FA, you are prompted for the code; if your cloud account is passkey-only, the box cannot drive a WebAuthn ceremony against the cloud and will tell you to use a password-capable method or the cloud console directly.

If the box has never been enrolled with the cloud, the sign-in flow pauses on an **"Approve this device"** panel showing a short user code and a verification URL. Approve the device in the cloud console (see [Enrollment](#enrollment-approving-your-box-as-a-device) below); the login then completes automatically — the UI polls enrollment status every 3 seconds and re-submits your credentials once the device is approved.

On success you get a normal OS session cookie, exactly like a local login.

Two supporting endpoints round out the flow:

- `GET /api/auth/cloud/status` → `{"enrolled": true|false}` — the login screen and wizard use this to decide whether to offer the cloud option.
- `POST /api/auth/cloud/signup` — creates a cloud account from the box during setup. The box proxies to the cloud's signup endpoint (solving its proof-of-work gate for you); the cloud performs password validation server-side (NIST length rules plus a breach-database check) and returns structured errors the wizard can show. Vulos Cloud accounts are **handle-based**: you pick a handle and the cloud mints the identity address itself — external email addresses are not accepted as the account identity.

### What happens under the hood

The endpoint is `POST /api/auth/cloud/login` on your box (`backend/services/auth/cloudunified.go`), which drives the whole flow server-side so your browser never contacts the cloud:

1. **Cloud login.** The box's cloud client (`backend/services/cloudclient/client.go`) performs `POST /api/auth/login` (and `POST /api/auth/totp/verify` for 2FA) against the control plane. It carries a proper `Origin` header for the cloud's CSRF gate and transparently solves the cloud's proof-of-work anti-abuse challenge when asked (a SHA-256 hashcash puzzle fetched from `GET /api/captcha/challenge`, answered via the `X-Vulos-PoW` header).
2. **Device identity.** The box looks up its own enrolled device ULID. Not enrolled → the API answers `{"step":"enrollment_required"}` and the UI starts the approval flow.
3. **Broker key pinning.** Login tokens are verified against the cloud's *login-broker* Ed25519 public key. The box pins this key on first use (TOFU) to `/var/lib/vulos/cloud/broker.pub`; if the cloud ever serves a *different* key, the box **refuses to sign in** rather than silently accepting a swapped key. You can also pin the key explicitly via `VULOS_CLOUD_BROKER_PUBKEY` (inline base64 or a file path).
4. **Token mint.** Using the in-memory cloud session, the box asks the control plane (`POST /api/profile/login/issue`) to mint a **device-bound login token**: an Ed25519-signed JSON payload carrying your cloud account id, email, the *specific device ULID* it is valid for, and an expiry about 120 seconds out.
5. **Local verification.** The token is validated entirely on-box — signature, expiry, device binding, and single-use replay dedup — through the same verifier used by `POST /api/auth/cloudlogin`. Only then is the OS session minted.

Two consequences worth knowing:

- **The cloud is not a runtime login dependency.** After a successful online sign-in, a grace-period cache (`~/.vulos/auth/cloudtoken-cache.json`, default 72 hours, tunable via `VULOS_CLOUDTOKEN_GRACE`) lets you sign back in while the cloud is unreachable. Bad signatures always fail immediately — the cache is never consulted to excuse a bad token.
- **Fresh-session gate.** For accounts without 2FA, the cloud requires a *fresh* password login before minting a device token. If you see "The cloud needs a fresh sign-in — re-enter your password and try again", that gate fired; re-entering your password satisfies it.

### Sign-in errors you may encounter

| Message | Meaning |
|---|---|
| "Incorrect email or password for your Vulos Cloud account." | Cloud rejected the credentials |
| "That 2FA code is not valid…" | TOTP mismatch |
| "The cloud needs a fresh sign-in — re-enter your password and try again." | No-2FA fresh-session gate |
| "Could not reach Vulos Cloud — check your network connection" | Network / cloud outage; try local login instead |
| "cloud broker key mismatch — refusing to sign in (re-enroll this device or contact your admin)" | The cloud served a different signing key than the one pinned on this box — deliberate fail-closed behavior. Do not ignore this; see [TROUBLESHOOTING.md](TROUBLESHOOTING.md) |
| "cloud token is not valid for this device" / "has already been used" | Device-binding or replay protection fired |

---

## Enrollment: approving your box as a device

Before the cloud will mint login tokens or integration tokens for your box, the box must be **enrolled** — registered as a device that *you*, the account owner, explicitly approved. Enrollment is an RFC 8628 device-authorization flow (the same pattern TVs use to pair with streaming accounts), implemented in `backend/services/cloudenroll/`.

### The flow

1. The box generates a fresh Ed25519 device keypair. The private seed is sealed at rest via the device keystore — a TPM where the hardware has one, otherwise a software-encrypted store (`backend/services/devicekey/`).
2. The box calls `POST /enroll/start` on the control plane with its device public key and receives a `device_code`, a short human-readable `user_code`, and a `verification_uri`.
3. The UI shows you: **"Approve this device"** — visit the verification URL, signed in to your cloud account, and enter the user code.
4. The box polls `POST /enroll/poll` (respecting the server's advertised interval, default 5 seconds, and RFC 8628 `slow_down` backoff) until you approve.
5. On approval the control plane returns the box's assigned ULID and a **management-CA-signed device certificate** binding the device key to your account. Your in-browser approval *is* the attestation — there is no trust-on-first-use window.

You can drive enrollment from the box API directly:

```bash
# Kick off enrollment (unauthenticated by design — the grant only
# completes when the account OWNER approves the code in the cloud console)
curl -X POST http://localhost:8080/api/auth/cloud/enroll/start
# → {"user_code":"...","verification_uri":"https://..."}

# Watch progress
curl http://localhost:8080/api/auth/cloud/enroll/status
# → {"state":"pending","user_code":"...","verification_uri":"..."}
# → {"state":"approved","ulid":"01H..."}
```

One grant runs at a time; calling start again while a grant is pending returns the same user code.

### What is stored on the box after enrollment

| Path | Mode | Contents |
|---|---|---|
| `~/.vulos/auth/integrations/identity.json` | 0600 | Device ULID, owning account id, device public key, CA-signed device certificate |
| `~/.vulos/auth/integrations/enroll_key.sealed` | 0600 | The Ed25519 device private key seed, sealed by the TPM or software keystore |
| `/var/lib/vulos/cloud/broker.pub` | 0600 | The pinned login-broker public key (written on first unified sign-in) |
| `/var/lib/vulos/cloud/enrolled` | 0600 | Enrollment sentinel flag |

The device certificate is what the box later presents when minting integration tokens and reserving your account username — see the next sections.

### Reserving your Vulos account username

During setup, the wizard can check and reserve a unique **Vulos account username** for you (your account identity; sign-in itself uses your email/password or a linked Google/Microsoft account). The box proxies these calls to the control plane (`GET /api/identity/check?handle=...` and `POST /api/identity/claim`) so your browser only ever talks to your own box. Two details matter:

- The account performing the claim is derived by the cloud from *its* session — or, on a freshly enrolled box where no cloud session cookie exists yet, from the **device certificate** the box presents (the same enrollment cert as above). The account is never taken from the request body.
- If the cloud is unreachable, the availability check returns a soft `{"offline": true}` rather than failing the wizard — you can claim later.

### When enrollment fails

The box surfaces precise errors from the flow:

- **"enrollment denied by owner"** — someone (hopefully you) rejected the device in the cloud console.
- **"enrollment grant expired"** / **"enrollment grant expired before approval"** — the code timed out before approval; start again.
- **"could not start device enrollment: …"** — the control plane rejected or failed the start request itself. This covers network problems and any control-plane-side refusal; check connectivity to your configured cloud URL and see [TROUBLESHOOTING.md](TROUBLESHOOTING.md).
- **"timed out waiting for the cloud to issue a user code"** — the start call took longer than 20 seconds.

---

## What the cloud can and cannot see

Every claim here is checked against the box-side code that produces the traffic.

| Data | Does the cloud see it? | Grounding |
|---|---|---|
| Your files, documents, media | **No.** They never transit the control plane; cross-box sharing is end-to-end encrypted through the relay (see [PEERING.md](PEERING.md) and [FILES.md](FILES.md)) | `backend/services/peering/` |
| Notification content | **No.** Web Push payloads are RFC 8291 encrypted on your box to your browser's subscription keys; both browser vendors and any relay can route but not read them | `backend/services/notify/webpush.go` |
| Your box's VAPID push private key | **No.** It is generated and kept on-box (`~/.vulos/db/vapid.json`, mode 0600) and never leaves | `backend/services/notify/webpush.go` |
| Mail content for sleeping managed cells | **No.** The cloud can send only a generic, content-blind "new mail" nudge using its *own* push key; registration sends the cloud only your cell's ULID and a subscription handle | `backend/services/notify/cpregister.go` |
| Third-party OAuth refresh tokens | **Yes — by design.** In cloud-broker mode the control plane custodies refresh tokens (encrypted under its key-encryption key); your box receives only short-lived access tokens, cached in memory and never written to disk | `backend/services/integrations/client.go` |
| Your cloud account password | **Yes — it is the cloud's own account.** Sent only during sign-in/signup, proxied through the box; the cloud session cookie is held in memory only and never persisted on the box | `backend/services/cloudclient/client.go` |
| Which device ULIDs belong to your account | **Yes.** That is the enrollment registry — it is what device binding is made of | `backend/services/cloudenroll/enroll.go` |
| System telemetry | **No.** The telemetry service is a local WebSocket (`/api/telemetry`) streaming CPU/memory stats to your own browser; it does not post to the cloud | `backend/services/telemetry/` |

The inverse also holds: because the cloud is content-blind, **it cannot recover your data for you**. Your recovery phrase and on-box keys are the real root of trust — see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) and the key-lifecycle section of [PEERING.md](PEERING.md).

---

## The integrations broker

Integrations connect your box to third-party accounts (Google today via the cloud broker; Google, Microsoft, and Dropbox in self-host mode) so apps like Mail and Files can read your external calendar, contacts, or storage.

There are two mutually exclusive modes, selected by whether a control plane is configured (`VULOS_CP_URL` or `VULOS_CLOUD_URL` set):

### Cloud-broker mode

The control plane runs the OAuth consent flow and custodies the long-lived **refresh token**. Your box never sees it. When an app needs access, the box mints a **short-lived access token** from the broker:

```bash
# Is Google connected for the signed-in user?
curl -H "X-User-ID: <you>" http://localhost:8080/api/integrations/google/status
# → {"provider":"google","configured":true,"connected":true}

# Mint a short-lived access token (cached in memory, refreshed 60s before expiry)
curl -H "X-User-ID: <you>" http://localhost:8080/api/integrations/google/token
# → {"access_token":"ya29...","expires_at":"...","expires_in":3599,"scopes":[...]}
```

Access tokens are cached **in memory only** and never persisted; if the broker is unreachable and the cache is stale, the mint fails closed rather than serving a stale token.

**Per-device binding.** The mint request is authenticated as *this specific box*, not just "some fleet member", with a layered credential set (strongest first):

1. **Owner-attested device certificate** — the CA-signed cert from enrollment, presented via `X-Device-Cert` / `X-Device-Pubkey` plus an Ed25519 signature (`X-Device-Sig`) over the mint message. Preferred: your in-browser approval attested the key, so there is no first-use race.
2. **Registered device key (TOFU)** — the box's TPM/software device key, registered once with the broker (`POST /api/integrations/device/register`); the broker pins it and refuses a re-registration with a different key.
3. **Fleet HMAC fallback** — a shared-secret HMAC proving fleet membership, used until one of the stronger methods is in place.

### Self-host mode (no cloud at all)

With no control plane configured, the box runs the entire OAuth flow itself using **your own OAuth apps** (`backend/services/integrations/selfhost/`). You register an app with Google/Microsoft/Dropbox, then configure:

```bash
INTEGRATIONS_KEK=<base64 32-byte key>        # encrypts refresh tokens at rest (required in prod — fail-closed)
GOOGLE_OAUTH_CLIENT_ID=...     GOOGLE_OAUTH_CLIENT_SECRET=...
MICROSOFT_OAUTH_CLIENT_ID=...  MICROSOFT_OAUTH_CLIENT_SECRET=...
DROPBOX_OAUTH_CLIENT_ID=...    DROPBOX_OAUTH_CLIENT_SECRET=...
OAUTH_REDIRECT_BASE=https://os.yourdomain.com   # your box's external URL
```

The user flow is then fully on-box: `GET /api/integrations/{provider}/connect` redirects to the provider's consent screen (authorization-code + PKCE), the callback stores the refresh token AES-256-GCM-encrypted under your local `INTEGRATIONS_KEK`, and `GET /api/integrations/{provider}/token` mints access tokens locally. `GET /api/integrations` lists your connections; `DELETE /api/integrations/{provider}` disconnects.

In this mode nothing about your integrations ever touches Vulos Cloud — full sovereignty, at the cost of registering and maintaining your own OAuth apps.

---

## Push notifications

Vulos push is **sovereign by default**: your box sends Web Push messages *directly* to the push service of your browser's vendor (FCM for Chrome, Apple for Safari, Mozilla for Firefox). There is no Vulos server in the delivery path.

- **Enable it** by giving the box VAPID keys — or let it generate its own. With no explicit keys, the box load-or-generates a keypair at `~/.vulos/db/vapid.json` (mode 0600). Explicit configuration: `VULOS_PUSH_VAPID_PUBLIC`, `VULOS_PUSH_VAPID_PRIVATE`, `VULOS_PUSH_VAPID_SUBJECT`, or `VULOS_PUSH_VAPID_KEYFILE`.
- **Subscribe per device** under **Settings → Notifications**. The browser fetches the public key from `GET /api/notifications/push/vapid-public`, subscribes through the service worker, and posts the subscription to `POST /api/notifications/push/subscribe`. Up to 20 subscriptions per user; the oldest is evicted.
- **Payloads are end-to-end encrypted** per RFC 8291 (aes128gcm) to your subscription's keys before leaving the box. The vendor routes the message but cannot read it. Only notifications addressed to you are pushed, Do Not Disturb is honored on the box before anything is sent, and dead subscriptions (404/410) are pruned automatically.
- **Outbound-only**: this works behind NAT with no central dependency and no open inbound port.

### Cloud-relayed push (managed cells only)

A managed cell that scales to zero cannot push while asleep. For that case only, the box registers a *second*, cloud-keyed subscription with the control plane (`POST /api/notifications/push/cp-subscribe` locally; `POST /api/mail/push/register` on the control plane) so the always-on cloud can send a **generic "new mail" notice** on the sleeping cell's behalf.

What crosses this path: the cell's ULID and a subscription handle created with the *cloud's own* public push key. What never crosses it: your box's VAPID private key, and any mail content. The whole path is gated on managed-mode configuration (`VULOS_PUSH_CP_REGISTER_URL` + edge secret + cell ULID); on a self-hosted box it is inert — no self-host box ever talks to a control plane for push.

---

## OTA updates

Bare-metal Vulos updates itself from a signed OS bucket with A/B slots and automatic rollback (`backend/services/osdist/`). This is independent of your cloud account — the bucket is public-read and no credentials are ever sent.

### How updates arrive

- The updater checks every 4 hours (first check at boot) by fetching the manifest `os/stable.json` plus its detached signature from the OS bucket.
- Bucket URL resolution: `VULOS_OS_BUCKET_URL` env override → the baked-in default at `/etc/vulos/os-bucket-url` (Vulos ships `https://os.vulos.org`; forks bake their own) → configured mirrors, with failover.
- If the updater has no trust anchor (dev machines, containers), the update loop does not run at all.

### Verification — fail closed at every step

1. The manifest's Ed25519 signature is verified against the **trust anchor** public key baked into the image at `/etc/vulos/trust-anchor.pub` at build/flash time.
2. The manifest's `min_epoch` is enforced against the box's epoch floor — an attacker cannot serve you an old, signed-but-vulnerable manifest (anti-downgrade).
3. The downloaded squashfs must match the manifest's dm-verity root hash (SHA-256) **and** carry its own valid detached Ed25519 signature — both checked before the update is staged. Any failure deletes the download.

### Apply and rollback

Updates stage into the **inactive** slot of an A/B pair (`slot-a` / `slot-b` under `~/.vulos/os-cache`, tracked in `boot-state.json`). Staging never touches the running system or the writable data partition.

```bash
# What's running, what's staged
curl http://localhost:8080/api/os/update/status
# → {"running_version":"v07","available_version":"v08",
#    "slot_state":{"active":"a","pending":"b","last_known_good":"a"}}

# Flip to the staged slot (admin only; takes effect on reboot)
curl -X POST http://localhost:8080/api/os/update/apply
# → 202 {"status":"reboot to apply","new_active":"b"}
```

The same status/apply controls appear in **Settings** in the shell. There is currently one channel — the updater always follows `stable`.

**Rollback is automatic.** A boot counter increments early at every boot and only resets once services come up healthy. If a new image fails to boot cleanly `3` times (tunable via `VULOS_BOOT_THRESHOLD`), the bootloader flips back to the last known-good slot. See [ARCHITECTURE.md](ARCHITECTURE.md) for the dm-verity/A-B design and [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md) for verifying images from source.

A related but separate mechanism: `GET /api/setup/mode` reports the box's *boot mode* (`setup` on first run before an identity exists, `sync` while a cluster join is replaying data, `normal` otherwise). This gates the setup wizard, not OS images.

---

## Staying sovereign: running without the cloud

Nothing in this chapter is required. A box with no cloud configuration:

- signs in with local accounts, passkeys, PIN, and TOTP ([USER-GUIDE.md](USER-GUIDE.md));
- connects integrations through self-host OAuth with your own provider apps;
- sends Web Push directly from its own VAPID keys;
- pulls signed OS updates from the public bucket (or your fork's bucket);
- peers, shares, and syncs with your other boxes directly and over LAN ([PEERING.md](PEERING.md)).

Connecting to the cloud is a set of narrow, inspectable seams you can adopt one at a time — and each one fails closed back to the sovereign path when the cloud is unreachable.

---

## Quick reference

### Box-local endpoints

| Area | Endpoints |
|---|---|
| Unified sign-in | `POST /api/auth/cloud/login` · `GET /api/auth/cloud/status` · `POST /api/auth/cloud/signup` |
| Enrollment | `POST /api/auth/cloud/enroll/start` · `GET /api/auth/cloud/enroll/status` |
| Identity claim | `GET /api/identity/check?handle=…` · `POST /api/identity/claim` |
| Integrations (cloud mode) | `GET /api/integrations/google/status` · `GET /api/integrations/google/token` |
| Integrations (self-host) | `GET /api/integrations` · `GET /api/integrations/{provider}/connect` · `/callback` · `/status` · `/token` · `DELETE /api/integrations/{provider}` |
| Push | `GET /api/notifications/push/vapid-public` · `POST`/`DELETE /api/notifications/push/subscribe` · `POST`/`DELETE /api/notifications/push/cp-subscribe` |
| OS updates | `GET /api/os/update/status` · `POST /api/os/update/apply` (admin) |
| Boot mode | `GET /api/setup/mode` |

### Environment variables

| Variable | Purpose |
|---|---|
| `VULOS_CLOUD_URL` / `VULOS_CLOUD_API_URL` / `VULOS_CP_URL` | Control-plane base URLs (see [One switch](#one-switch-pointing-your-box-at-a-control-plane)) |
| `VULOS_CLOUD_ORIGIN` | Origin header override for the cloud's CSRF gate |
| `VULOS_CLOUD_BROKER_PUBKEY` | Pin the login-broker key explicitly (inline base64 or file path) |
| `VULOS_CLOUDTOKEN_CACHE` / `VULOS_CLOUDTOKEN_GRACE` | Offline sign-in cache location / grace window (default 72h) |
| `INTEGRATIONS_KEK`, `*_OAUTH_CLIENT_ID` / `*_OAUTH_CLIENT_SECRET`, `OAUTH_REDIRECT_BASE` | Self-host integrations |
| `VULOS_PUSH_VAPID_PUBLIC` / `_PRIVATE` / `_SUBJECT` / `_KEYFILE` | Web Push keys |
| `VULOS_PUSH_CP_REGISTER_URL`, `MAIL_EDGE_CP_SECRET` | Managed-cell push relay (self-host: leave unset) |
| `VULOS_OS_BUCKET_URL` | OS update bucket override |
| `VULOS_BOOT_THRESHOLD` | Failed-boot count before automatic rollback (default 3) |

### What lands on disk

| Path | What |
|---|---|
| `~/.vulos/auth/integrations/identity.json` + `enroll_key.sealed` | Enrolled device identity and sealed device key |
| `/var/lib/vulos/cloud/broker.pub` + `enrolled` | Pinned broker key; enrollment flag |
| `~/.vulos/auth/cloudtoken-cache.json` | Offline sign-in grace cache |
| `~/.vulos/db/vapid.json` + `push_subs.sqlite` | Web Push keypair and subscriptions |
| `~/.vulos/os-cache/` (`slot-a`, `slot-b`, `boot-state.json`) | Staged OS images and slot state |
| `/etc/vulos/trust-anchor.pub` + `os-bucket-url` | Update trust anchor and baked bucket URL |
