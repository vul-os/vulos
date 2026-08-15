# Security model for admins

A practical guide to running a Vulos box securely: what the auth surface actually is, which defaults fail closed, which environment variables gate risky features, how verified boot works, and what to check before you expose the box to the internet.

This chapter is operational. For the formal analysis (STRIDE, trust boundaries, honest residual risks) read [THREAT-MODEL.md](THREAT-MODEL.md). To report a vulnerability, see the security policy at [../SECURITY.md](../SECURITY.md).

Vulos is the OS. There is no Vulos-operated cloud, managed hosting, billing, or control plane standing behind your box — you run the same binary on your own hardware or on any VPS you rent, and it protects itself the same way either way. The two positions that fall out of that, stated plainly:

- **Nobody but you holds your keys.** Not Vulos, not a hosted service, not the reachability broker your box talks to. See [Key custody](#key-custody-yours-only) below and [KEY-CEREMONY.md](KEY-CEREMONY.md) for the full key model.
- **We do not collect your data.** There is no telemetry or analytics SDK anywhere in this codebase; the only outbound connection an unconfigured box makes by default is a read-only, signed check for OS updates (see [Verified boot and the netboot signature chain](#verified-boot-and-the-netboot-signature-chain)) — no usage data rides along. **That check is opt-out:** set `VULOS_OS_AUTOUPDATE=0` for a box with zero default egress, and pull updates by hand from Settings → OS Update instead. Note the fail-safe direction — an unrecognised value leaves updates enabled, so verify rather than assume.

---

## Reachability: one authenticated handler, no bypass

However a client reaches your box — through your own `vulos relay serve`, through a Pier instance (yours or another operator's), or direct over TLS to a public IP — every path lands on the **same** authenticated HTTP handler. There is no "trusted because it came from the relay", "trusted because it's direct", or "trusted because it's on the LAN" shortcut anywhere in the routing. An unauthenticated request gets the same 401 no matter which door it came through, and the direct listener's own unauthenticated probe path carries no user data and answers only a caller-supplied nonce. The full reachability model — the three connection options, how Pier is "hired, not depended on" (it only ever forwards ciphertext), TLS termination, and the ports involved — lives in [NETWORKING.md](NETWORKING.md); this page assumes that model and covers what happens once a request actually arrives.

---

## The one flag that matters: `VULOS_ENV`

Everything security-relevant keys off the runtime environment, set with `--env` or `VULOS_ENV` (`local`, `dev`, or `prod` — **default `prod`**):

| | `local` | `dev` | `prod` |
|---|---|---|---|
| HTTP bind | `127.0.0.1` only | `127.0.0.1` only | all interfaces |
| Hardware checks (TPM/fingerprint) | skipped | skipped | enforced where present |
| Self-signed upstream certs | accepted | accepted | rejected |
| Debug endpoints (`/debug/env`, pprof) | **enabled** | off | off |
| Strict cookies | per-request TLS state | per-request TLS state | enforced |

Two consequences worth spelling out. First, an unset `VULOS_ENV` means **prod**: you get the strict posture by default, including binding all interfaces — so a box on an untrusted network needs TLS and the checklist at the bottom of this page. Second, `local` mode exposes `/debug/env` and pprof; never point `--env=local` at a machine anyone else can reach.

---

## Signing in: the authentication surface

All of this lives in `backend/services/auth` and `backend/services/passkeys`. At a glance:

| Method | Strength | Phishing-resistant? | Notes |
|---|---|---|---|
| Passkey (WebAuthn) | Primary | Yes — origin-bound, key never leaves the authenticator | Requires `VULOS_RPID`/`VULOS_ORIGIN` in prod |
| Password | Fallback | No | Rate-limited with IP bans; pairs with the recovery phrase |
| Recovery phrase | Account recovery | n/a | Offline mnemonic; uniform failure responses |
| Device PIN | Unlock only | Device-local | **The hardened implementation is not wired up** — the live PIN is salted SHA-256 with no lockout ladder. It is stored in `profile_secrets`, a table that is deliberately excluded from multi-instance replication, so a PIN set on one box does not unlock another. See below |
| QR / phone approval | Kiosk login | Yes — nothing typed on the untrusted device | Single-use, short-TTL challenge |
| Fingerprint | Hardware-dependent | Local biometric | Part of the prod hardware checks |

The design principle behind this lineup (from the threat model): *isolate the credential, not the browsing*. There is deliberately no third-party OAuth at the OS level and no "streamed login screen" — a password typed on a compromised client is captured wherever the pixels render, so the answer is credentials that cannot be captured (passkeys) or never typed (QR approval).

### Passkeys (primary)

Passkeys (FIDO2/WebAuthn) are the primary login: the private key never leaves your authenticator, so nothing phishable or keyloggable is ever typed. Credentials are stored per-user and sealed with the device keystore (TPM-backed where the hardware has one).

Two env vars bind passkeys to your domain, and they **fail closed in prod — by disabling the feature, not by halting the box**: if `VULOS_RPID` or `VULOS_ORIGIN` is unset or still a localhost value while `VULOS_ENV=prod`, the passkey routes are never registered and the server logs `##### PASSKEYS DISABLED #####` (`backend/cmd/server/main.go:1292-1301`). It keeps booting, deliberately — a freshly flashed box has no production domain yet, and bricking it would be worse. **The consequence to plan around: until you set these, password login is your only front door, not your fallback.**

```bash
VULOS_RPID=os.example.com
VULOS_ORIGIN=https://os.example.com
```

### Password + recovery phrase

Password login remains as fallback (`POST /api/auth/login`). The account master key is zero-access: the server stores only wrapped key slots, never the key or your recovery phrase. Two recovery tiers exist:

- **Recovery phrase** — recover the account and set a new password with the mnemonic; failures are rate-limited and deliberately uniform (the API never reveals whether the username or the phrase was wrong).
- **Active-session reset** — a logged-in session holding the master key in memory can rotate the password without knowing the old one; doing so **revokes the user's other sessions**. The code is explicit about the honest boundary: a hijacked live session could do this too — the same risk as any logged-in session anywhere.

Failed logins are rate-limited with escalating IP bans; admins can inspect and lift bans via `GET /api/auth/security/bans`, `POST /api/auth/security/unban`, and `GET /api/auth/security/stats`.

### Device PIN

> **Read this before relying on the PIN.** Two PIN implementations exist in the
> tree and **the strong one is not wired up**. `DevicePINService`
> (`backend/services/auth/devicepin.go`) does everything described in the next
> paragraph, but `Handler.DevicePIN` is never assigned outside tests — so
> `/api/auth/pin/device/set`, `/api/auth/pin/unlock` and `/api/auth/pin/status`
> all return **503** on a shipped box.
>
> What actually runs is the per-user profile PIN (`POST /api/auth/pin/set`,
> `/validate` → `Store.SetPIN`/`ValidatePIN`, `backend/services/auth/profiles.go:131-176`).
> That is a **single-round salted SHA-256** stored in `profile_secrets` (moved
> out of the profile row when profiles began replicating between a person's
> boxes — a PIN belongs to the machine you are standing at, not to the account,
> so it must not travel): no
> argon2id, no AES-GCM, no TPM, and **no PIN lockout ladder** — the only backoff
> is the generic per-IP limiter (5 failures per 10 minutes). It also does no
> server-side length or digit validation, and `ValidatePIN` returns **true when
> no PIN is set**. Treat the PIN as a convenience lock on a device you already
> trust, not as a second factor.

The design below is what `DevicePINService` implements, and is accurate about
that code — it is simply not the code path a request reaches today. A short PIN
(4–8 digits) unlocks the box on a device where you have already fully
authenticated. It is device-local by construction: the PIN wraps a session
credential via argon2id (64 MiB, per-device salt) + AES-256-GCM under
`~/.vulos/auth/`, re-sealed by the TPM where one exists. Lockout is built in — 5
wrong PINs triggers a 15-minute lock; 3 locks total is permanent until you fully
re-authenticate. Setting or changing the PIN requires a full-auth session, not a
PIN session, so a stolen PIN can never mint a new PIN.

### QR / phone approval, fingerprint

- **QR login** lets a kiosk or shared screen be approved from an already-authenticated phone; the challenge is short-lived and single-use, so no reusable secret is typed on the untrusted device. `POST /api/auth/qr/begin` also sets a **browser-binding cookie** (`vulos_qr_bind`, `HttpOnly`, `SameSite=Strict`, path-scoped to `/api/auth/qr/`), and `GET /api/auth/qr/poll` refuses with 403 unless that cookie matches the challenge id (QRSEC-02). This closes a login-CSRF/session-fixation hole: poll is public, is a GET, and on success sets a 90-day session cookie that is `SameSite=None` over HTTPS — so without the bind, an attacker could begin and approve their *own* challenge and then get a victim's browser to adopt that session with nothing more than an `<img src=…/api/auth/qr/poll?id=…>` tag. The bind cookie is not a credential; it only asserts "this browser is the one that asked", and the check runs *before* the poll so a cross-site probe cannot consume the challenge either.
- **Fingerprint** enrollment/verify endpoints exist for hardware that supports it (`/api/auth/fingerprint/*`); the presence checks are part of the hardware checks that `prod` enforces and `local`/`dev` skip.

### TOTP

**The TOTP vault** (`/api/auth/totp/*`) is an on-box authenticator keychain for *your other accounts* (GitHub, etc.), with secrets encrypted at rest (AES-256-GCM) under `~/.vulos/auth/totp/`. It is a feature — a place to keep 2FA codes for services you use elsewhere — not the box's own second factor.

### Sessions

Sessions last **90 days**, extended when the same device logs in again, and are carried in a cookie that is always `HttpOnly`. Over HTTPS the cookie is `Secure` + `SameSite=None` (required for app subdomain iframes — which is exactly why the backend layers CSRF checks on state-changing requests); over plain-HTTP localhost dev it falls back to `SameSite=Lax`. Logout clears the cookie with matching attributes; password reset revokes the user's other sessions. Session expiry is checked server-side on every request — an expired cookie is just a 401.

To inspect your own session state:

```bash
curl -s https://yourbox/api/auth/status -b "$COOKIE" | jq   # auth state
curl -s https://yourbox/api/auth/me -b "$COOKIE" | jq       # who am I + session expiry
```

---

## Admin role, privileged routes, and audit logging

Vulos profiles carry roles; destructive and system-level routes require an authenticated **admin** session. Three properties to rely on:

- The auth middleware strips any client-supplied identity headers before setting its own — a forged `X-User-ID` header alone never confers admin.
- Privileged operations that shell out to system tools (Wi-Fi control, network configuration, and similar) write to an exec audit log, so "who changed the network config" has an answer.
- AI-app mutations (save/update/delete/snapshot/rollback) are admin-gated and audit-logged, with a fail-closed kill-switch (`DISABLE_AI_APP_EDIT=1`) that freezes all of them at once.

The assistant deserves its own mention because it *reads* private data and can be *asked* to act. The containment model (detailed in [ASSISTANT.md](ASSISTANT.md) and THREAT-MODEL Component 5): read-only tools run freely, but every side-effecting action becomes a server-stored **proposal** — single-use, 10-minute TTL, bound to the session user. Execution accepts only the opaque proposal id and re-reads the server-stored arguments, so a compromised browser cannot swap the recipient between what you saw and what runs. Tool results (email bodies!) are framed as untrusted content, and the egress Guard is the single choke point deciding whether any of it may reach an off-box model.

---

## Key custody: yours only

Two key systems live on a box, and both are held entirely by you:

- **Your account master key.** A random 256-bit key generated at signup, wrapped under your password *and* under a 24-word recovery phrase shown to you once — the server persists only the doubly-wrapped, opaque envelope (`master_key_blobs` in `auth.db`) and never the plaintext key or the phrase. On a normal login the browser unwraps it client-side; the server never sees it. Recovery is entirely in your hands: `POST /api/auth/masterkey/recover` reconstructs the key from your own phrase, and a wrong phrase fails closed and changes nothing. Nobody — not Vulos, not whichever Pier instance your box happens to be dialing out to — holds a copy or an escrow of this key, and there is no backdoor unwrap path in the code. See [KEY-CEREMONY.md](KEY-CEREMONY.md) for the full key model and [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) for what the phrase can and cannot do.
- **Your box's peering identity (Vulos ID).** An Ed25519 keypair generated on first use and stored on disk under `~/.vulos/peering/identity/`; it is what other boxes verify you by. It never leaves the box except as a passphrase-encrypted export bundle you explicitly request (`POST /api/peering/identity/export`) — the passphrase is yours, chosen at export time, and nothing about the export touches any third party.

The software-signing keys covered in [KEY-CEREMONY.md](KEY-CEREMONY.md) (the root/release keys that sign OS images and the App Hub registry) are a separate system with a separate purpose — they decide what *code* a box will trust, not what *your data* is encrypted with. Neither system is escrowed anywhere.

---

## Fail-closed by default

A recurring pattern in this codebase: when a security prerequisite is missing, the feature turns *off* instead of running open. Usually that means the routes are never registered, so the surface 404s rather than 403s; only a few checks abort startup. Real, verified examples:

| Surface | Missing prerequisite | Behaviour |
|---|---|---|
| Passkeys in prod | `VULOS_RPID` / `VULOS_ORIGIN` unset or localhost | Passkey routes are **not registered** (404) and startup logs `##### PASSKEYS DISABLED #####`. The server still boots — password login is then the only front door |
| Assistant egress | Endpoint classified `brokered`/`external`, `VULOS_ASSISTANT_ALLOW_EXTERNAL` not `1` | Request blocked before any mail content leaves the box; anything unclassifiable lands in the blocked `external` bucket |
| AI code execution | `VULOS_SANDBOX_ENABLED` unset | `Run()` errors immediately — no arbitrary Python execution |
| Netboot (`vulos.netboot=1`) | Verity/signature inputs absent in the initramfs | Boot **halts** rather than falling into an unverified loop mount |
| Netboot-to-install | Trust anchor missing, signature mismatch, or manifest epoch below the device floor | Refuses to write a single byte to the permanent slot |
| Same-LAN fabric sync | `VULOS_FABRIC_SECRET` unset | Handlers not registered — no unauthenticated peer exchange (and the fabric signing key must seal under `VULOS_FABRIC_KEY_HEX` in prod) |
| App subdomain provisioning in prod | `VULOS_DNS_API` or `VULOS_CADDY_DIR` unset | Routes not registered, so users are never told a domain is provisioned when nothing happened |
| Direct public listener | ACME mode without hostname, cleartext advertised endpoint, LAN-only connection mode | Refuses to construct/start; endpoint must be `https://` |
| Box→gateway channels | Plaintext gateway URL | Refused unless the explicit dev-only escape hatch is set (`VULOS_CLOUD_ALLOW_INSECURE` / `VULOS_CP_ALLOW_INSECURE` / `VULOS_LANCERT_ALLOW_INSECURE`) |
| `/metrics` | No admin session, no scrape token | 403 — operational counters are owner-only |

One deliberate exception you should know about: the remote-input WebAuthn gate (AUTH-13) for streaming sessions **warns loudly instead of failing closed** when no verifier is available in prod. Set `VULOS_STREAM_STRICT_INPUT_GATE=1` to make it fail closed if you use streaming and cannot wire passkeys.

---

## Security-relevant environment variables

Every variable below is read by code in this repo. "Omitting" means leaving it unset.

| Variable | Setting it means | Omitting it means |
|---|---|---|
| `VULOS_ENV` | Chooses the posture table above | `prod` (strict) |
| `VULOS_RPID`, `VULOS_ORIGIN` | WebAuthn relying-party domain + origin | Prod disables passkeys entirely if unset/localhost (boot continues); dev uses localhost defaults |
| `VULOS_ASSISTANT_ALLOW_EXTERNAL=1` | Authorizes brokered/external LLM endpoints — mail content may genuinely leave the box | Assistant egress limited to local/sovereign tiers |
| `VULOS_SANDBOX_ENABLED=1` | Allows AI-generated Python to execute (see sandbox section) | Execution disabled |
| `VULOS_DISABLE_EXEC` (any value) | Kill-switch: disables routes that shell out (Wi-Fi control, network mode, etc.) | Exec-backed routes available (audited) |
| `DISABLE_AI_APP_EDIT=1` | Freezes all mutating AI-app routes (save/update/delete/snapshot/rollback); saved apps stay viewable | AI-app editing available to admins |
| `VULOS_STREAM_STRICT_INPUT_GATE=1` | Remote input injection fails closed when no WebAuthn verifier is wired | Prod logs a prominent warning instead |
| `VULOS_METRICS_TOKEN` | Lets a Prometheus scraper hit `/metrics` with `Authorization: Bearer <token>` | `/metrics` requires an admin session |
| `VULOS_FABRIC_SECRET` | Enables same-LAN multi-box sync (set identically on every sibling) | Fabric sync off |
| `VULOS_FABRIC_KEY_HEX` | Seals the fabric signing key at rest (AES-256-GCM); required in prod | Prod fails closed; dev derives a loud dev key |
| `VULOS_STORAGE_STS_ENDPOINT` (+ `_ROLE_ARN`, `_DURATION_SECONDS`) | Apps get short-lived credentials scoped to their own `<user>/<app>/` prefix. Self-host **defaults this automatically** to the box's own object-store endpoint when one is configured — set `VULOS_STORAGE_STS_DISABLE=1` to opt out | No fallback to a static credential: a storage-permitted app gets **no** injected credential at all (fail-closed) and must use `POST /api/storage/presign` instead. If an object store is statically configured with a storage-permitted app installed and STS ends up unavailable anyway, the server **aborts at boot**. |
| `VULOS_S3_*` + `VULOS_CLUSTER_PASSPHRASE` | Enables encrypted cluster backup/sync to S3 (passphrase encrypts what leaves the box) | Cluster backup disabled |
| `VULOS_MAIL_BROKER_SECRET` | Broker auth between the OS/assistant and LilMail (shared `LILMAIL_BROKER_SECRET`) | Mail integration falls back to per-request session-cookie auth |
| `VULOS_PEER_ALLOW_LAN=1` | SSRF guard admits private/LAN peer addresses for cross-box shares | LAN peer addresses blocked |
| `VULOS_LANCERT_ALLOW_INSECURE`, `VULOS_CLOUD_ALLOW_INSECURE`, `VULOS_CP_ALLOW_INSECURE` | **Dev-only escape hatches** that permit plaintext HTTP to cloud endpoints | Plaintext refused (fail-closed) — leave these unset in production |

See [CONFIGURATION.md](CONFIGURATION.md) for the non-security remainder and [NETWORKING.md](NETWORKING.md) for the `VULOS_DIRECT_*` / `VULOS_LAN_*` families.

---

## Data at rest

What is actually protected on disk, so you can judge what a stolen disk (without full-disk encryption) exposes:

| Data | Protection |
|---|---|
| Identity/auth SQLite stores | File mode `0600`, owned by the backend process user; pure-Go SQLite (no CGO C library in the attack surface) |
| Account master key | Never stored — only password/phrase-wrapped slots; the server cannot decrypt your content-blind data even under subpoena of the disk |
| Passkey credentials | Sealed with the device keystore (TPM-backed where present) before hitting disk |
| Device PIN material | *(design, not wired)* argon2id-derived wrap + AES-256-GCM, optionally TPM-re-sealed. **What is stored today** is a salted SHA-256 PIN hash in `profile_secrets`, which never replicates to another box |
| TOTP vault secrets | AES-256-GCM under `~/.vulos/auth/totp/` |
| Fabric signing key | AES-256-GCM sealed under `VULOS_FABRIC_KEY_HEX` (fail-closed in prod) |
| Bare-metal trust anchor (`/etc/vulos/trust-anchor.pub`) | Baked into the OS image; controls which signed updates VERITY-02 accepts |

A bare-metal box (live USB or installed to disk) runs `vulos.service` under systemd, unsandboxed (no `NoNewPrivileges`/`ProtectSystem`/user-namespace hardening on that unit today — it's a single-user appliance image, not a multi-tenant host). Whole-disk encryption is your responsibility; Vulos protects specific secrets, not the entire filesystem.

---

## SSRF and egress guards

Several features accept URLs or addresses that ultimately cause the *box* to make a request. Each is guarded:

- **Peer shares / cross-instance fetches** go through a safe dialer that blocks private and LAN address ranges by default; `VULOS_PEER_ALLOW_LAN=1` is the explicit opt-in for genuinely-local peers.
- **The assistant's egress Guard** classifies every model endpoint (local / sovereign / brokered / external) before mail content can flow to it; private-range IPs are never silently treated as "local", and unclassifiable endpoints land in the blocked bucket.
- **The QR-login egress proxy** rejects private/SSRF ranges.
- **The direct listener's self-probe** only ever fetches the box's *own* validated https endpoint, never a caller-supplied URL, and refuses redirects.

---

## Read your startup log

The server is deliberately loud at startup about anything running in a degraded or dangerous configuration. After changing deployment config, skim the log for these markers before calling it done:

```
[storage] STS not configured ...                 → storage-permitted apps get NO credential (fail-closed); they must use POST /api/storage/presign
[storage] ABORT: app(s) declare "storage" ...    → boot refused: an object store is configured with a storage-permitted app but no STS is available
[stream/webauthn] WARNING: AUTH-13 NOT ENFORCED  → remote input gate is open (see above)
[appnet/subdomain] DNS provisioning DISABLED ... → prod refused to fake domain provisioning
[fabric] disabled: VULOS_FABRIC_SECRET unset     → multi-box sync off (fail-closed)
[direct] disabled / self-reachability check ...  → direct fast path not actually reachable
[env] debug endpoints enabled ...                → you are in local mode; do not expose this
```

A clean prod boot has none of the warnings above. This is the cheapest security audit you will ever run.

---

## Verified boot and the netboot signature chain

On bare metal, Vulos boots only what your trust anchor signed. The chain, end to end:

1. **A trust anchor is baked at build time.** `build.sh` embeds an Ed25519 public key at `/etc/vulos/trust-anchor.pub` (and into the initramfs). Trust is *signature-first, transport-second*: TLS success never authorizes a payload — only a signature chaining to this pinned key does. Forks supply their own key and update bucket at build time; see [CONFIGURATION.md](CONFIGURATION.md) and [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md).
2. **The root filesystem is a dm-verity squashfs — active on a newly installed disk, proven by a real boot (VERITY-03).** The build produces `os-core.squashfs`, its Merkle tree (`os-core.hashtree`) and its verity root hash (`os-core.roothash`); the installer stages those beside the image, the initramfs opens the verity device, and the kernel then refuses to read tampered blocks. `scripts/netboot-install-smoke.sh` Phase 5 proves it end to end, requiring the boot to have recorded `verity=active` and not merely that the files are present.

   Two limits. **The roothash is not signed on this path.** `build.sh` writes no `os-core.roothash.sig` — dm-verity binds the image to a roothash, but nothing yet binds that roothash to the release key here, so this is tamper detection without provenance (see step 3). And **it is a property of new installs, not of every box**: a pre-existing disk with no verity siblings still takes the documented unverified loop-mount fallback with a console warning, recording `verity=inactive`. On a netboot (step 4) that fallback is refused outright instead. Full detail, from the code, in [ARCHITECTURE.md → OS distribution](ARCHITECTURE.md#os-distribution-bare-metal).
3. **A tiny fail-closed verifier checks the binding — where it is present.** `vulos-verify-sig <anchor.pub> <file> <file.sig>` exits 0 only on a valid signature; *any* doubt — missing anchor, missing signature, mismatch — is a non-zero exit that callers treat as a hard halt. There is no CA-bundle fallback. **It is not on a shipped box today:** `build.sh:182-183` compiles only `vulos-server` and `vulos-init`, and `backend/cmd/vulos-verify-sig/` exists as source only. The command below is therefore something you can run from a source checkout, not from an installed system.
4. **Netboot halts without it.** In a netboot (`vulos.netboot=1`), the initramfs (`scripts/initramfs/vulos-live`) requires the verity/signature inputs to be present and valid; if they are absent the boot **fails closed** instead of degrading to an unverified mount. The iPXE stick likewise `imgverify`s the kernel and initramfs against the baked anchor before executing anything (`scripts/netboot/`).
5. **Install verifies before writing.** The netboot-to-install path verifies the squashfs signature against the pinned anchor *before a single byte* reaches the permanent slot, and the destructive endpoint is admin-gated.
6. **Rollback protection, and key revocation.** The device persists a monotonically increasing epoch floor (`/var/lib/vulos/epoch-floor.json`) that can rise but never fall; an older *signed-but-vulnerable* image below it is refused. The floor is raised **only from the root-signed release cert** (`signing.RaiseFromReleaseCert`, which re-validates the root signature, so a cert nobody proved the root signed can never move it). The `stable.json` manifest also carries a `min_epoch`, but it is only ever *checked* against the floor — a release-key-signed artifact must not be able to move the defence that retires a compromised release key. This is what makes `issue-release-cert -min-epoch N` an actual revocation: a box refuses a retired cert and then raises its floor past it. A box learns a new floor when it checks for an update, so between a revocation and that check the old cert's `not-after` is the remaining bound.
7. **A/B slots — staging and the flip both work.** Updates stage into the inactive slot only (`slot-a`/`slot-b` with an atomically-written `boot-state.json` tracking active/pending/last-known-good), and the initramfs boots the slot `boot-state.json` names. See the note below for how far the reboot proof goes and for one stale log line to ignore.

You can run the same check the initramfs runs, by hand:

```bash
vulos-verify-sig /etc/vulos/trust-anchor.pub os-core.roothash os-core.roothash.sig
echo $?   # 0 = signature chains to the pinned anchor; anything else = do not trust
```

The verifier is intentionally tiny and binary in its answer — exit 0 on a valid signature, exit 1 on *any* doubt (missing or malformed anchor, missing signature, mismatch), exit 2 on usage error. There is no verbose mode to misread.

The A/B state is a single JSON file per box:

```json
{
  "active": "a",
  "pending": "b",
  "boot_counter": 0,
  "last_known_good": "a"
}
```

Updates stage into the slot that is *not* active (staging into the active slot is refused outright), every mutation of this file is an atomic write-then-rename, and `last_known_good` records the slot a failed boot *should* fall back to. The writable data partition is entirely separate from both slots — a rollback would never touch your files.

> **The slot flip works (OSDIST-FLIP-01).** This page previously recorded it as
> a known gap. It was closed by the second of the two options named here: the
> initramfs (`scripts/initramfs/vulos-live`) reads `boot-state.json` at
> init-bottom and boots the slot it names, treating the kernel cmdline as a
> default rather than the answer. `writeSlotABootEntry` still writes the
> systemd-boot entry once at install time and it still says slot-a — that entry
> is simply no longer what decides. It was proven the way this page demanded, by
> an actual reboot: `scripts/netboot-install-smoke.sh` Phase 4 stages slot-b,
> flips `active`, reboots the same disk, and requires both a serving HTTP
> endpoint *and* a `/var/cache/vulos/booted-slot` marker reading
> `slot=b via=boot-state` — HTTP alone would have passed had it quietly booted
> slot-a.
>
> So: a staged OS update **does** become active on the next reboot, and a boot
> counter that passes `VULOS_BOOT_THRESHOLD` **does** roll the box back.
>
> One caveat remains:
>
> - The reboot proof hardlinks slot-b to slot-a's image, so what is proven is
>   *which slot the firmware and initramfs choose*, not that a genuinely
>   different image boots. See
>   [ARCHITECTURE.md → OS distribution](ARCHITECTURE.md#os-distribution-bare-metal).
>
> **Retired 2026-08-15:** this page used to carry a second caveat — that `init`'s
> rollback log line still said the flip "HAS NO EFFECT on the next boot", the
> opposite of the truth, so an operator reading it mid-incident would conclude
> the box had not protected itself when it had. The line is fixed
> (`backend/cmd/init/main.go`), and the boot it describes is observable: a
> non-quiet serial log shows `boot-state.json selects slot b`.
>
> Everything below the flip — signature verification, the epoch floor, staging
> into the inactive slot, the boot counter — is real and does what this page
> says.

If you build your own images: the netboot fail-closed gate needs `os-core.roothash.sig` and the `vulos-verify-sig` binary present in the initramfs — a build that omits them produces netboots that halt (correctly) rather than boot unverified.

Docker and plain-binary installs skip this machinery entirely; verified boot is a bare-metal property.

---

## The AI sandbox flag, honestly

`VULOS_SANDBOX_ENABLED=1` allows AI-generated Python to run as local processes bound to loopback. Be clear-eyed about what that is today: the old "dangerous code" substring blocklist is **not** a security boundary (trivially bypassed by obfuscation), and per THREAT-MODEL.md the seccomp syscall filter is **not currently applied** — a known gap. Until kernel-level isolation (namespaces + seccomp) wraps these processes, treat the flag as "I trust the code my assistant generates on this box" and leave it unset on any multi-user or internet-exposed machine. The rendered HTML side of AI apps is separately confined to an opaque-origin sandboxed iframe with a strict CSP. App installation and permissions are covered in [APPS.md](APPS.md); the assistant's own guardrails (proposal ledger, egress Guard) in [ASSISTANT.md](ASSISTANT.md).

---

## Observability endpoints

- `GET /healthz`, `GET /api/version` — public liveness/version probes; no secrets.
- `GET /api/health` — **verdict public, detail gated.** Anonymous callers get `{"status","timestamp"}` and the real `200`/`503`; the `checks` map is withheld unless the request carries a session. The checks still run either way, so the verdict is never a guess — only the output is redacted. The detail is gated because each field discloses something on a box that is already failing: the absolute data-dir path and raw OS error (`data_dir_writable`), exact free capacity, which fingerprints the deployment and tells an attacker how much to write to force a `503` (`disk_space`), and whether S3 cluster sync exists at all (`sync_lag`).
- `GET /metrics` — **owner-only**: requires an admin session or the `VULOS_METRICS_TOKEN` bearer token. Metric names and labels never contain secret values, but the counters that are actually populated — Guard allow/block, proposal backlog, RAG mode — are operational intelligence you should not hand to strangers. (The generic request/error/queue counters are registered but never written; see [ARCHITECTURE.md → Observability](ARCHITECTURE.md#observability).)
- `local` mode only: `/debug/env` and pprof are enabled. They exist for developers; never run `--env=local` on a reachable box.

---

## What this model does not protect

Honesty is a design value here, so the limits are stated in the code and the threat model, not discovered later:

- **A compromised client keeps its session.** Passkeys make the *credential* uncapturable; they do not sanitize the device. A live session on an infected browser can do what you can do until it expires or is revoked — including the active-session password reset.
- **Opting into external AI egress is real egress.** With `VULOS_ASSISTANT_ALLOW_EXTERNAL=1`, mail content genuinely leaves the box. The Guard enforces your *choice*; it cannot make a third-party provider private.
- **The confirmation gate stops unapproved side effects, not bad approvals.** A well-crafted social-engineering proposal that you approve still executes; the `FromContent` flag on targets sourced from email is a nudge, not a block.
- **App sandboxing has a known gap.** Seccomp filtering is not currently applied to installed apps (tracked in THREAT-MODEL Component 3) — one more reason `VULOS_SANDBOX_ENABLED` should stay off on exposed boxes.
- **No runtime intrusion detection.** Verified boot protects the image at rest and at boot; there is no post-boot integrity monitoring yet.
- **Physical access and full-disk protection are yours.** Vulos encrypts specific secrets at rest, not the whole disk, and the dm-verity key-rotation protocol for a compromised *build* key is an open design question (a re-image is the current answer).

---

## Before you expose this box to the internet

Work through this before forwarding a port or pointing public DNS at the box:

1. **Confirm the environment.** `VULOS_ENV` unset or `prod` — never `local`. Verify debug endpoints are dead: `curl -s https://yourbox/debug/env` should 404.
2. **Terminate TLS.** Either the direct listener with ACME (`VULOS_DIRECT_ENABLE=1` + `VULOS_DIRECT_HOSTNAME`), certs at `/etc/vulos/tls/cert.pem|key.pem`, or your own reverse proxy. Session cookies are only `Secure` when the request actually arrives over HTTPS.
3. **Bind passkeys to your domain.** Set `VULOS_RPID` and `VULOS_ORIGIN` — without them prod silently disables passkeys rather than refusing to boot, so check the startup log for `PASSKEYS DISABLED` — and **enroll a passkey for the admin account** before exposure, so password login is your fallback rather than your front door.
4. **Set a device PIN policy consciously.** PIN unlock is device-local and lockout-protected, but only enroll it on physically-controlled devices.
5. **Check the fail-closed table above** for anything you actually use: `VULOS_FABRIC_SECRET` (+ `VULOS_FABRIC_KEY_HEX`) for multi-box LANs. `VULOS_STORAGE_STS_ENDPOINT` self-configures automatically against your own object store when one is configured — a storage-permitted app never gets a static credential either way; watch for the startup `[storage] ABORT` if you've explicitly disabled STS (`VULOS_STORAGE_STS_DISABLE=1`) while running a storage-permitted app against a configured object store.
6. **Audit the opt-ins.** `VULOS_ASSISTANT_ALLOW_EXTERNAL`, `VULOS_SANDBOX_ENABLED`, `VULOS_PEER_ALLOW_LAN`, and every `*_ALLOW_INSECURE` variable should be unset unless you can say why not.
7. **Probe your surface from outside.** `/metrics` must 403 without credentials; only the ports you chose in [NETWORKING.md](NETWORKING.md)'s firewall section should answer (`nmap` from a remote host, not from the LAN).
8. **Have a way back in.** Record the recovery phrase offline, and know the backup story before you need it — see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).
9. **Keep the update chain intact.** On bare metal, don't disable verified boot pieces to "fix" an update problem — a halting netboot is telling you signature material is missing, which is the system working. See [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

---

## See also

- [THREAT-MODEL.md](THREAT-MODEL.md) — formal threat model, including the sovereign assistant and AI-app builder
- [../SECURITY.md](../SECURITY.md) — vulnerability reporting, SLA, safe harbor
- [NETWORKING.md](NETWORKING.md) — ports, firewall modes, TLS termination
- [CONFIGURATION.md](CONFIGURATION.md) — full configuration reference
- [ASSISTANT.md](ASSISTANT.md) — assistant permissions and the proposal/execute gate
- [APPS.md](APPS.md) — app permissions and isolation
- [USER-GUIDE.md](USER-GUIDE.md) — day-to-day account and profile management
- [security/](security/) — audit notes and hardening test records
