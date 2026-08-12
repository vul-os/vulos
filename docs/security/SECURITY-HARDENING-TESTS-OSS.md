# Vulos OSS — Security Hardening Regression Tests

Audit date: 2026-05-21  
Branch: audit/OSS-SEC-HARDEN-TESTS  
Companion report: [SECURITY-OSS.md](SECURITY-OSS.md)

These tests fail when any of the hardening fixes from SECURITY-OSS.md are
accidentally reverted.  Each test is linked to a specific finding by section
number.

---

## Test files

| File | Package | Purpose |
|---|---|---|
| `backend/services/security_test.go` | `services_test` | System-wide hardening invariants |
| `backend/services/peering/security_test.go` | `peering` | Peering-subsystem regression tests |

---

## `backend/services/security_test.go`

### SEC-HARD-01 — Signing verification fails closed

**Tests:** `TestSigning_VerifyFailsClosedOnBadSig`, `TestSigning_VerifyRejectsZeroLengthSig`,
`TestSigning_VerifyRejectsWrongKey`, `TestSigning_SignVerifyRoundTrip`

**Guards:** SECURITY-OSS.md §1 (Signing Chain — signing.go)

Asserts that `signing.Verify` returns `false` — not a panic, not `true` — for:
corrupt signatures, zero-length signatures, and signatures by the wrong key.
The positive round-trip test ensures the negative tests are meaningful.

---

### SEC-HARD-02 — Epoch rollback refused

**Tests:** `TestEpoch_RollbackRefused`, `TestEpoch_FloorNeverDecreases`,
`TestEpoch_BumpFloor_RequiresValidRootSig`

**Guards:** SECURITY-OSS.md §1 (Signing Chain — epoch.go) and §8 (Boot Counter Rollback)

Asserts that:
- `AcceptEpoch` rejects any claimed epoch strictly below the current floor.
- `RaiseTo` is truly monotonic: calling it with a lower value is a no-op (floor never decreases).
- `BumpFloor` fails when the anchor file is missing (cannot be bumped without a root-signed message).

---

### SEC-HARD-03 — Trust anchor fails closed

**Tests:** `TestAnchor_FailsClosedOnMissingFile`, `TestAnchor_FailsClosedOnEmptyFile`,
`TestAnchor_FailsClosedOnWrongLengthKey`, `TestVerifyWithAnchor_FailsClosedOnBadSig`,
`TestVerifyWithAnchor_FailsClosedOnEmptyPayload`

**Guards:** SECURITY-OSS.md §1 (Signing Chain — anchor.go)

Asserts that `signing.LoadAnchor` returns a non-nil error for: missing file, empty file,
a key of the wrong length (16 bytes instead of 32).  Also asserts that
`VerifyWithAnchor` fails closed when the signature is wrong and when
`canonicalBytes` is nil.

---

### SEC-HARD-04 — dm-verity / boot verification fails closed

**Note:** The full pre-pivot verification gate (`verify.VerifySquashfsBeforePivot`) is tested
in `backend/cmd/verify/verifier_test.go`.  The invariants covered there include:
- Expired or unsigned release cert refused.
- Cert `min_epoch` below device floor refused.
- Squashfs image signature invalid → refused.
- Root hash mismatch between manifest pin and ImagePayload → refused.

The SEC-HARD-03 anchor tests (above) also directly exercise the boot-chain trust
anchor which is the root of the entire chain.

---

### SEC-HARD-05 — Cluster passphrase never logged

**Tests:** `TestClusterLog_PassphraseNotLeaked`, `TestAuthLog_NoCredentialMaterial`

**Guards:** SECURITY-OSS.md §2 (Cluster Passphrase), §7 (HTTP Handlers — auth.go fix)

`TestClusterLog_PassphraseNotLeaked` scans a representative set of log lines that
`cluster.go`, `joinsync.go`, and `relay.go` emit and verifies none contain the
passphrase string.  A self-check asserts the scan would catch a poisoned line.

`TestAuthLog_NoCredentialMaterial` checks that the auth event log (post-fix)
contains no `hash_prefix`, `pw_len`, `PasswordHash`, or `password=` fields —
guarding the HIGH-severity fix in SECURITY-OSS.md §7.

---

### SEC-HARD-06 — PIN: tpm_wrapped=true, no KeyStore → fail-closed

**Tests:** `TestPIN_TPMWrapped_NoKeyStore`

**Guards:** SECURITY-OSS.md §3 (TPM/PIN/Fingerprint — devicepin.go, HIGH, fixed)

Creates a `DevicePINService` with `ks=nil`, sets a PIN, then manually writes
`tpm_wrapped=true` into `pin.bin` (simulating a blob sealed on a device with a
hardware TPM).  Asserts that `ValidatePIN` returns an error rather than attempting
software decryption.  This is a cross-package gate: even if the dedicated unit
test in `auth/devicepin_test.go` were deleted, this test would catch the regression.

---

### SEC-HARD-07 — HTTP handlers cap request body via MaxBytesReader

**Tests:** `TestHTTP_MaxBytesReader_CriticalPOSTHandlers`

**Guards:** SECURITY-OSS.md §4 (WebAuthn, MED), §7 (HTTP Handlers, all MED/HIGH fixes)

**Read this test's limits honestly.**  It builds its *own* handler around
`http.MaxBytesReader` and asserts that a body 1 byte over each limit returns
HTTP 413.  It does **not** call the real routes — it pins the *limit values* the
audit chose and proves the pattern behaves as expected, so it would not notice
if a `MaxBytesReader` call were deleted from one of the handlers below.  Treat
the table as the reviewed inventory of limits, not as route coverage.

The 13 POST/PUT handlers hardened in the audit, with the limit each was given
(paths verified against the current route registrations):

| Handler route | Limit | Registered in |
|---|---|---|
| `POST /api/exec` | 64 KiB | `cmd/server/main.go` |
| `POST /api/network/configure` | 64 KiB | `cmd/server/main.go` |
| `POST /api/stream/launch-app` | 64 KiB | `cmd/server/main.go` |
| `POST /api/apps/launch` | 64 KiB | `cmd/server/main.go` |
| `PUT /api/device-profile` | 4 KiB | `cmd/server/main.go` |
| `POST /api/ai/chat` | 1 MiB | `internal/llmuxclient/handler.go` |
| `POST /api/ai-apps/{id}/update` | 10 MiB | `cmd/server/routes_aiapps.go` |
| `POST /api/sync/conflicts/resolve` | 64 KiB | `cmd/server/routes_conflicts.go` |
| `POST /api/ssh/authorized` | 16 KiB | `cmd/server/routes_sshkey.go` |
| `POST /api/open` | 8 KiB | `cmd/server/routes_open.go` |
| `POST /api/setup/join-code` | 4 KiB | `cmd/server/routes_joincode.go` |
| `POST /api/passkeys/register/finish` | 64 KiB | `cmd/server/routes_passkeys.go` |
| `POST /api/passkeys/assert/finish` | 64 KiB | `cmd/server/routes_passkeys.go` |

> Earlier revisions of this table named `POST /api/conflicts/*`,
> `POST /api/ssh-key/add`, `POST /api/passkeys/finish-register` and
> `POST /api/passkeys/finish-assert`.  None of those paths has ever been
> registered by the server; the rows above are the routes that actually exist.

---

### SEC-HARD-08 — publicPaths is the exhaustive allow-list

**Tests:** `TestPublicPaths_ExhaustiveAllowList`

**Guards:** SECURITY-OSS.md §7 — any new route added to the server must be
consciously reviewed before being added to `publicPaths`.

Pins the unauthenticated surface **exactly**, in both directions.  The test holds
a reviewed copy of the allow-list — currently **37 paths** and **4 prefixes**
(`/assets/`, `/__pubweb__/`, `/api/peering/inbound/`, `/api/v1/`) — each with the
reason it is safe, and compares it against `auth.PublicPaths()` /
`auth.PublicPrefixes()`:

- A path in `publicPaths` but not in the reviewed list → **REGRESSION** (auth was
  removed from a route without review).
- A path in the reviewed list but not in `publicPaths` → **STALE** (the list no
  longer describes the middleware).
- The prefix count must match exactly, because a prefix exempts a whole subtree.

It then stands up an in-process server and confirms a sentinel protected route
returns 401 without a session while `/api/auth/status` does not.

Adding a public route therefore *fails the build* until it is listed here with a
rationale.  Note that `/metrics` appears in the allow-list but is not public: the
handler runs its own owner-or-`VULOS_METRICS_TOKEN` gate and answers 403 without
one, so the session middleware has to defer to it.

---

### SEC-HARD-09 — Peering inbound: signature required, unknown sender rejected

**Tests:** `TestInboundMiddleware_SignatureRequired`

**Guards:** SECURITY-OSS.md §5 (Peering — inbound.go)

Builds a minimal HTTP server and verifies that:
- An empty request body returns non-200.
- A JSON body without a signature field returns non-200.

The dedicated `peering/security_test.go` tests (see below) cover the full
`InboundMiddleware` signature + allow-list enforcement at the HTTP level.

---

### SEC-HARD-10 — App sandbox: LD_PRELOAD / LD_LIBRARY_PATH blocked, env scrubbed

**Tests:** `TestAppSandbox_EnvScrubbing`

**Guards:** SECURITY-OSS.md §6 (App Sandbox — launcher.go)

Asserts that neither `LD_PRELOAD` nor `LD_LIBRARY_PATH` appears in the launcher's
minimal scrubbed environment (as defined in `appnet/launcher.go` lines 175–179).
This prevents untrusted app processes from preloading shared libraries into the
Vulos server process.

---

### SEC-HARD-11 — Security response headers present on every response

**Tests:** `TestSecurityHeaders_XContentTypeOptions`, `TestSecurityHeaders_PresentOnEveryServedResponse`

**Guards:** SECURITY-OSS.md §7 — `secHeadersMiddleware` in `cmd/server/main.go`

Replicates the `secHeadersMiddleware` closure from `main.go` and verifies that
`X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer` are present
on every response, including static-file responses.

---

### SEC-HARD-12 — Shell CSP keeps its structural directives

**Tests:** `TestShellCSP_ContentSecurityPolicyPresent`

**Guards:** the `shellCSP` const in `cmd/server/main.go`

Reconstructs the shell CSP string and asserts the structural directives survive
(`object-src 'none'`, `base-uri 'self'`, `form-action 'self'`,
`frame-ancestors 'self'`).  Like SEC-HARD-07 this test holds a *copy* of the
value rather than reading the const, so it pins the reviewed policy — it does not
detect a change made only in `main.go`.

---

### ORIGIN-01 — `allow-same-origin` only ever granted by an origin comparison

**Tests:** `TestSandbox_AllowSameOriginIsOnlyEverGrantedByOriginCheck`

**Guards:** `frontend/src/core/AppOrigins.*` — `iframeSandboxForURL()`

Walks `frontend/src` (`.js/.jsx/.ts/.tsx`) and fails if `allow-same-origin`
appears anywhere except inside the sanctioned origin gate, because an app frame
on the shell's own origin with that flag can read the shell's storage and script
`window.top`.  It also asserts the gate is still a comparison
(`isDistinctOrigin(url) ? … : BASE_SANDBOX`) rather than an unconditional grant,
and that the removed `needsSameOrigin()` allow-list has not come back.

The walk carries a **coverage floor of 150 files**: an extension filter that once
scanned only JavaScript covered 4 files instead of 227 after the TypeScript
migration, and passed while examining almost nothing.  The floor turns that
failure mode into a hard error.

---

## `backend/services/peering/security_test.go`

### PEER-SEC-01 — Envelope with bad signature rejected

**Tests:** `TestSEC_Envelope_BadSigRejected`

**Guards:** SECURITY-OSS.md §5 (Peering — relay.go, envelope.go)

A validly-signed envelope whose `Signature` field is then replaced with random
bytes of the correct length must cause `Verify` to return an error.

---

### PEER-SEC-07 — Absent signature rejected

**Tests:** `TestSEC_Envelope_AbsentSignatureRejected`

**Guards:** SECURITY-OSS.md §5

An `Envelope` that is never signed (empty `Signature`) must fail `Verify` with a
non-nil error.  An absent signature must never be treated as implicitly valid.

---

### PEER-SEC-08 — Tampered payload rejected

**Tests:** `TestSEC_Envelope_TamperedPayloadRejected`

**Guards:** SECURITY-OSS.md §5 — canonical-JSON signature covers all fields.

Modifying `Envelope.Payload` after signing must invalidate the signature.

---

### PEER-SEC-09 — Tampered type rejected

**Tests:** `TestSEC_Envelope_TamperedTypeRejected`

**Guards:** SECURITY-OSS.md §5 — type escalation (e.g. `message` → `signaling`) blocked.

Changing `Envelope.Type` after signing must invalidate the signature, preventing
a relayed message from being re-typed into a higher-privilege message class.

---

### PEER-SEC-02 — Relay deposit: forged signature rejected

**Tests:** `TestSEC_Relay_DepositForgeSigRejected`

**Guards:** SECURITY-OSS.md §5 (Peering — relay.go Deposit)

Even when the sender is in the approved contacts store, a deposit with a forged
(random 64-byte) signature must be refused.  This prevents an attacker from relaying
blobs on behalf of any identity.

---

### PEER-SEC-05 — Relay deposit: unknown sender rejected

**Tests:** `TestSEC_Relay_UnknownSenderRejected`

**Guards:** SECURITY-OSS.md §5 (Peering — relay.go mutual-trust check)

A deposit with a valid signature from a sender not in the contacts store must be
refused.  This enforces the mutual-trust requirement: a random internet client
cannot use an enabled relay as an open upload service.

---

### PEER-SEC-03 — Relay pickup: stale and future timestamps rejected

**Tests:** `TestSEC_Relay_StalePickupTimestampRejected`, `TestSEC_Relay_FutureTimestampRejected`

**Guards:** SECURITY-OSS.md §5 (Peering — relay.go `relayPickupTimestampTolerance = ±5 min`)

A pickup authentication token with a timestamp 10 minutes in the past (or future)
must be rejected, preventing indefinite replay of intercepted pickup tokens.

---

### PEER-SEC-04 — Relay pickup: audience mismatch rejected

**Tests:** `TestSEC_Relay_AudienceMismatchRejected`

**Guards:** SECURITY-OSS.md §5 (Peering — relay.go signature over `<vulosID>.<timestamp>`)

RecipientB attempting to authenticate as recipientA's Vulos ID while signing with
B's own key must fail: the relay derives the expected public key from the presented
Vulos ID, so a key-mismatch is caught as a signature failure.

---

### PEER-SEC-06 — InboundMiddleware: unapproved sender → 403; missing sig → 401

**Tests:** `TestSEC_InboundMiddleware_UnapprovedSenderReturns403`,
`TestSEC_InboundMiddleware_MissingSignatureReturns401`,
`TestSEC_InboundMiddleware_ApprovedSenderPasses`

**Guards:** SECURITY-OSS.md §5 (Peering — inbound.go)

Exercises the full `InboundMiddleware` HTTP stack:
- Valid sig from a sender not in contacts → **403 Forbidden**.
- Unsigned envelope → **401 Unauthorized**.
- Valid sig from approved contact → **200 OK** (positive path).

---

## Running the tests

```sh
# All security hardening tests
cd backend
go test ./services/ -run "^Test(Signing|Epoch|Anchor|Verify|Cluster|Auth|PIN|HTTP|Public|Inbound|App|Security)" -v
go test ./services/peering/ -run "^TestSEC_" -v

# Full backend test suite (includes all hardening tests)
go test ./...
```
