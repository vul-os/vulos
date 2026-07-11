# Security model for admins

A practical guide to running a Vulos box securely: what the auth surface actually is, which defaults fail closed, which environment variables gate risky features, how verified boot works, and what to check before you expose the box to the internet.

This chapter is operational. For the formal analysis (STRIDE, trust boundaries, honest residual risks) read [../THREAT-MODEL.md](../THREAT-MODEL.md). To report a vulnerability, see the security policy at [../SECURITY.md](../SECURITY.md).

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
| Staging cloud-broker key | no | accepted alongside prod | no |

Two consequences worth spelling out. First, an unset `VULOS_ENV` means **prod**: you get the strict posture by default, including binding all interfaces — so a box on an untrusted network needs TLS and the checklist at the bottom of this page. Second, `local` mode exposes `/debug/env` and pprof; never point `--env=local` at a machine anyone else can reach.

---

## Signing in: the authentication surface

All of this lives in `backend/services/auth` and `backend/services/passkeys`. At a glance:

| Method | Strength | Phishing-resistant? | Notes |
|---|---|---|---|
| Passkey (WebAuthn) | Primary | Yes — origin-bound, key never leaves the authenticator | Requires `VULOS_RPID`/`VULOS_ORIGIN` in prod |
| Password | Fallback | No | Rate-limited with IP bans; pairs with the recovery phrase |
| Recovery phrase | Account recovery | n/a | Offline mnemonic; uniform failure responses |
| Device PIN | Unlock only | Device-local | Requires prior full auth on that device; hard lockout |
| QR / phone approval | Kiosk login | Yes — nothing typed on the untrusted device | Single-use, short-TTL challenge |
| Fingerprint | Hardware-dependent | Local biometric | Part of the prod hardware checks |
| Cloud unified sign-in | Optional | Inherits cloud account posture (incl. TOTP) | Verified locally against a pinned broker key |

The design principle behind this lineup (from the threat model): *isolate the credential, not the browsing*. There is deliberately no third-party OAuth at the OS level and no "streamed login screen" — a password typed on a compromised client is captured wherever the pixels render, so the answer is credentials that cannot be captured (passkeys) or never typed (QR approval).

### Passkeys (primary)

Passkeys (FIDO2/WebAuthn) are the primary login: the private key never leaves your authenticator, so nothing phishable or keyloggable is ever typed. Credentials are stored per-user and sealed with the device keystore (TPM-backed where the hardware has one).

Two env vars bind passkeys to your domain, and they **fail closed in prod**: if `VULOS_RPID` or `VULOS_ORIGIN` is unset or still a localhost value while `VULOS_ENV=prod`, the server refuses to start rather than run WebAuthn against the wrong relying party.

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

A short PIN (4–8 digits) can unlock the box on a device where you've already fully authenticated. It is device-local by construction: the PIN wraps a session credential via argon2id (64 MiB, per-device salt) + AES-256-GCM under `~/.vulos/auth/`, re-sealed by the TPM where one exists. Lockout is built in — 5 wrong PINs triggers a 15-minute lock; 3 locks total is permanent until you fully re-authenticate. Setting or changing the PIN requires a full-auth session, not a PIN session, so a stolen PIN can never mint a new PIN.

### QR / phone approval, fingerprint

- **QR login** lets a kiosk or shared screen be approved from an already-authenticated phone; the challenge is short-lived and single-use, so no reusable secret is typed on the untrusted device.
- **Fingerprint** enrollment/verify endpoints exist for hardware that supports it (`/api/auth/fingerprint/*`); the presence checks are part of the hardware checks that `prod` enforces and `local`/`dev` skip.

### TOTP

Two distinct things, easy to conflate:

- **Cloud sign-in TOTP**: when you attach the box to a Vulos Cloud account (`POST /api/auth/cloud/login`, enrollment via `/api/auth/cloud/enroll/*`), the cloud login flow handles the account's TOTP second factor as part of the sign-in conversation.
- **The TOTP vault** (`/api/auth/totp/*`): an on-box authenticator keychain for *your other accounts* (GitHub, etc.), with secrets encrypted at rest (AES-256-GCM) under `~/.vulos/auth/totp/`. It is a feature, not the box's own 2FA.

### Sessions

Sessions last **90 days**, extended when the same device logs in again, and are carried in a cookie that is always `HttpOnly`. Over HTTPS the cookie is `Secure` + `SameSite=None` (required for app subdomain iframes — which is exactly why the backend layers CSRF checks on state-changing requests); over plain-HTTP localhost dev it falls back to `SameSite=Lax`. Logout clears the cookie with matching attributes; password reset revokes the user's other sessions. Session expiry is checked server-side on every request — an expired cookie is just a 401.

To inspect your own session state:

```bash
curl -s https://yourbox/api/auth/status -b "$COOKIE" | jq   # auth state
curl -s https://yourbox/api/auth/me -b "$COOKIE" | jq       # who am I + session expiry
```

### Attaching the box to a Vulos Cloud account (optional)

Unified sign-in lets a cloud account open an OS session. The parts an admin should understand:

- The OS-side client (`backend/services/cloudclient`) speaks the cloud's browser-grade defenses — CSRF Origin allowlist and an adaptive proof-of-work gate — and keeps the cloud session cookie in an **in-memory jar only**: the OS never persists your cloud session. One client per flow, then it's dropped.
- What the box actually trusts is a **signed login token**: the cloud signs it with a login-broker Ed25519 key, and the box verifies locally against a pinned copy of that public key (`/var/lib/vulos/cloud/broker.pub`, or the `VULOS_CLOUD_BROKER_PUBKEY` env var holding a base64 key or a path). Verification is purely local — the cloud is not a runtime dependency for login, and no broker key configured simply means "cloud login not configured on this device".
- Box→cloud channels refuse plaintext URLs unless a dev-only escape hatch is set (see the env table below).

See [CLOUD.md](CLOUD.md) for what the cloud control plane does and does not see.

---

## Admin role, privileged routes, and audit logging

Vulos profiles carry roles; destructive and system-level routes require an authenticated **admin** session. Three properties to rely on:

- The auth middleware strips any client-supplied identity headers before setting its own — a forged `X-User-ID` header alone never confers admin.
- Privileged operations that shell out to system tools (Wi-Fi control, network configuration, and similar) write to an exec audit log, so "who changed the network config" has an answer.
- AI-app mutations (save/update/delete/snapshot/rollback) are admin-gated and audit-logged, with a fail-closed kill-switch (`DISABLE_AI_APP_EDIT=1`) that freezes all of them at once.

The assistant deserves its own mention because it *reads* private data and can be *asked* to act. The containment model (detailed in [ASSISTANT.md](ASSISTANT.md) and THREAT-MODEL Component 5): read-only tools run freely, but every side-effecting action becomes a server-stored **proposal** — single-use, 10-minute TTL, bound to the session user. Execution accepts only the opaque proposal id and re-reads the server-stored arguments, so a compromised browser cannot swap the recipient between what you saw and what runs. Tool results (email bodies!) are framed as untrusted content, and the egress Guard is the single choke point deciding whether any of it may reach an off-box model.

---

## Fail-closed by default

A recurring pattern in this codebase: when a security prerequisite is missing, the feature turns *off* (or the server refuses to start) instead of running open. Real, verified examples:

| Surface | Missing prerequisite | Behaviour |
|---|---|---|
| Passkeys in prod | `VULOS_RPID` / `VULOS_ORIGIN` unset or localhost | Server exits at startup with a clear error |
| Board (whiteboard) sync | `BOARD_AUTH_SECRET` unset in prod | Board proxy answers `503 board sync disabled` — no anonymous collab |
| Assistant egress | Endpoint classified `brokered`/`external`, `VULOS_ASSISTANT_ALLOW_EXTERNAL` not `1` | Request blocked before any mail content leaves the box; anything unclassifiable lands in the blocked `external` bucket |
| AI code execution | `VULOS_SANDBOX_ENABLED` unset | `Run()` errors immediately — no arbitrary Python execution |
| Netboot (`vulos.netboot=1`) | Verity/signature inputs absent in the initramfs | Boot **halts** rather than falling into an unverified loop mount |
| Netboot-to-install | Trust anchor missing, signature mismatch, or manifest epoch below the device floor | Refuses to write a single byte to the permanent slot |
| Same-LAN fabric sync | `VULOS_FABRIC_SECRET` unset | Handlers not registered — no unauthenticated peer exchange (and the fabric signing key must seal under `VULOS_FABRIC_KEY_HEX` in prod) |
| App subdomain provisioning in prod | `VULOS_DNS_API` or `VULOS_CADDY_DIR` unset | Routes not registered, so users are never told a domain is provisioned when nothing happened |
| Direct public listener | ACME mode without hostname, cleartext advertised endpoint, LAN-only connection mode | Refuses to construct/start; endpoint must be `https://` |
| Box→cloud channels | Plaintext control-plane URL | Refused unless the explicit dev-only escape hatch is set (`VULOS_CLOUD_ALLOW_INSECURE` / `VULOS_CP_ALLOW_INSECURE` / `VULOS_LANCERT_ALLOW_INSECURE`); billing likewise disables itself rather than leak its shared secret over HTTP |
| `/metrics` | No admin session, no scrape token | 403 — operational counters are owner-only |

One deliberate exception you should know about: the remote-input WebAuthn gate (AUTH-13) for streaming sessions **warns loudly instead of failing closed** when no verifier is available in prod. Set `VULOS_STREAM_STRICT_INPUT_GATE=1` to make it fail closed if you use streaming and cannot wire passkeys.

---

## Security-relevant environment variables

Every variable below is read by code in this repo. "Omitting" means leaving it unset.

| Variable | Setting it means | Omitting it means |
|---|---|---|
| `VULOS_ENV` | Chooses the posture table above | `prod` (strict) |
| `VULOS_RPID`, `VULOS_ORIGIN` | WebAuthn relying-party domain + origin | Prod refuses to start; dev uses localhost defaults |
| `BOARD_AUTH_SECRET` | Enables authenticated board sync (shared with the board sync server) | Board sync 503s in prod |
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
| `VULOS_MAIL_BROKER_SECRET` | Broker auth between the OS/assistant and vulos-mail (shared `LILMAIL_BROKER_SECRET`) | Mail integration falls back to per-request session-cookie auth |
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
| Device PIN material | argon2id-derived wrap + AES-256-GCM, optionally TPM-re-sealed; PIN never leaves the device |
| TOTP vault secrets | AES-256-GCM under `~/.vulos/auth/totp/` |
| Fabric signing key | AES-256-GCM sealed under `VULOS_FABRIC_KEY_HEX` (fail-closed in prod) |
| Bundle configs (`/etc/vulos/*.yaml`) | `root:vulos`, mode `640` — service can read, not write |
| Bundle private keys (`fabric_private.pem`, mail X25519 key, MinIO secret) | Mode `600` |

The self-host bundle additionally runs every service as a dedicated non-login `vulos` user under systemd hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`, `PrivateDevices`, empty capability bounding sets except mail's `CAP_NET_BIND_SERVICE`) — the full table is in [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md). Whole-disk encryption is your responsibility; Vulos protects specific secrets, not the entire filesystem.

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

(The board gate is a runtime one: with `BOARD_AUTH_SECRET` unset in prod there is no startup line — the token endpoint answers `503 board sync disabled: BOARD_AUTH_SECRET unset` when first used.)

A clean prod boot has none of the warnings above. This is the cheapest security audit you will ever run.

---

## Verified boot and the netboot signature chain

On bare metal, Vulos boots only what your trust anchor signed. The chain, end to end:

1. **A trust anchor is baked at build time.** `build.sh` embeds an Ed25519 public key at `/etc/vulos/trust-anchor.pub` (and into the initramfs). Trust is *signature-first, transport-second*: TLS success never authorizes a payload — only a signature chaining to this pinned key does. Forks supply their own key and update bucket at build time; see [CONFIGURATION.md](CONFIGURATION.md) and [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md).
2. **The root filesystem is a dm-verity squashfs.** The build produces `os-core.squashfs`, its verity root hash (`os-core.roothash`), and a detached signature binding that hash to the anchor (`os-core.roothash.sig`). The kernel refuses to read tampered blocks.
3. **A tiny fail-closed verifier checks the binding.** `vulos-verify-sig <anchor.pub> <file> <file.sig>` exits 0 only on a valid signature; *any* doubt — missing anchor, missing signature, mismatch — is a non-zero exit that callers treat as a hard halt. There is no CA-bundle fallback.
4. **Netboot halts without it.** In a netboot (`vulos.netboot=1`), the initramfs (`scripts/initramfs/vulos-live`) requires the verity/signature inputs to be present and valid; if they are absent the boot **fails closed** instead of degrading to an unverified mount. The iPXE stick likewise `imgverify`s the kernel and initramfs against the baked anchor before executing anything (`scripts/netboot/`).
5. **Install verifies before writing.** The netboot-to-install path verifies the squashfs signature against the pinned anchor *before a single byte* reaches the permanent slot, and the destructive endpoint is admin-gated.
6. **Rollback protection.** A signed `stable.json` manifest carries a `min_epoch`; the device persists a monotonically increasing epoch floor (`/var/lib/vulos/epoch-floor.json`) that can rise but never fall. An older *signed-but-vulnerable* image below the floor is refused.
7. **A/B slots.** Updates stage into the inactive slot only (`slot-a`/`slot-b` with an atomically-written `boot-state.json` tracking active/pending/last-known-good), so a bad update rolls back instead of bricking the box.

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

Updates stage into the slot that is *not* active (staging into the active slot is refused outright), the flip is an atomic file rename, and `last_known_good` is what a failed boot falls back to. The writable data partition is entirely separate from both slots — a rollback never touches your files.

If you build your own images: the netboot fail-closed gate needs `os-core.roothash.sig` and the `vulos-verify-sig` binary present in the initramfs — a build that omits them produces netboots that halt (correctly) rather than boot unverified.

Docker and plain-binary installs skip this machinery entirely; verified boot is a bare-metal property.

---

## The AI sandbox flag, honestly

`VULOS_SANDBOX_ENABLED=1` allows AI-generated Python to run as local processes bound to loopback. Be clear-eyed about what that is today: the old "dangerous code" substring blocklist is **not** a security boundary (trivially bypassed by obfuscation), and per THREAT-MODEL.md the seccomp syscall filter is **not currently applied** — a known gap. Until kernel-level isolation (namespaces + seccomp) wraps these processes, treat the flag as "I trust the code my assistant generates on this box" and leave it unset on any multi-user or internet-exposed machine. The rendered HTML side of AI apps is separately confined to an opaque-origin sandboxed iframe with a strict CSP. App installation and permissions are covered in [APPS.md](APPS.md); the assistant's own guardrails (proposal ledger, egress Guard) in [ASSISTANT.md](ASSISTANT.md).

---

## Observability endpoints

- `GET /healthz`, `GET /api/version`, `GET /api/health` — public liveness/version probes; no secrets.
- `GET /metrics` — **owner-only**: requires an admin session or the `VULOS_METRICS_TOKEN` bearer token. Metric names and labels never contain secret values, but the counters (Guard allow/block, proposal backlog, error totals) are operational intelligence you should not hand to strangers.
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
3. **Bind passkeys to your domain.** Set `VULOS_RPID` and `VULOS_ORIGIN` (prod refuses to start otherwise) and **enroll a passkey for the admin account** before exposure, so password login is your fallback rather than your front door.
4. **Set a device PIN policy consciously.** PIN unlock is device-local and lockout-protected, but only enroll it on physically-controlled devices.
5. **Check the fail-closed table above** for anything you actually use: `BOARD_AUTH_SECRET` if you collaborate on boards, `VULOS_FABRIC_SECRET` (+ `VULOS_FABRIC_KEY_HEX`) for multi-box LANs. `VULOS_STORAGE_STS_ENDPOINT` self-configures automatically against your own object store when one is configured — a storage-permitted app never gets a static credential either way; watch for the startup `[storage] ABORT` if you've explicitly disabled STS (`VULOS_STORAGE_STS_DISABLE=1`) while running a storage-permitted app against a configured object store.
6. **Audit the opt-ins.** `VULOS_ASSISTANT_ALLOW_EXTERNAL`, `VULOS_SANDBOX_ENABLED`, `VULOS_PEER_ALLOW_LAN`, and every `*_ALLOW_INSECURE` variable should be unset unless you can say why not.
7. **Probe your surface from outside.** `/metrics` must 403 without credentials; only the ports you chose in [NETWORKING.md](NETWORKING.md)'s firewall section should answer (`nmap` from a remote host, not from the LAN).
8. **Have a way back in.** Record the recovery phrase offline, and know the backup story before you need it — see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).
9. **Keep the update chain intact.** On bare metal, don't disable verified boot pieces to "fix" an update problem — a halting netboot is telling you signature material is missing, which is the system working. See [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

---

## See also

- [../THREAT-MODEL.md](../THREAT-MODEL.md) — formal threat model, including the sovereign assistant and AI-app builder
- [../SECURITY.md](../SECURITY.md) — vulnerability reporting, SLA, safe harbor
- [NETWORKING.md](NETWORKING.md) — ports, firewall modes, TLS termination
- [CONFIGURATION.md](CONFIGURATION.md) — full configuration reference
- [ASSISTANT.md](ASSISTANT.md) — assistant permissions and the proposal/execute gate
- [APPS.md](APPS.md) — app permissions and isolation
- [USER-GUIDE.md](USER-GUIDE.md) — day-to-day account and profile management
- [security/](security/) — audit notes and hardening test records
