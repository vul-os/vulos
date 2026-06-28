# Vulos OSS — Security Hardening Report

Audit date: 2026-05-21  
Branch: audit/SEC-OSS  
Scope: all packages under `backend/`  
Format: `location | severity | fix-applied | reasoning`

---

## 1. Signing Chain

| Location | Severity | Fix Applied | Reasoning |
|---|---|---|---|
| `backend/cmd/verify/verifier.go` | low | no | Verification is fully fail-closed: chain walks offline-root → release cert → artifact sig. Any mismatch, expired cert, or missing epoch floor causes `log.Fatalf`. No bypass path found. |
| `backend/services/signing/signing.go` | low | no | Ed25519 with canonical JSON (sorted keys, no floats). Detached `.sig` format. Non-malleable. |
| `backend/services/signing/epoch.go` | low | no | `BumpFloor` requires a valid root signature on the new epoch value. Floor stored atomically (temp+rename). Monotonically increasing — rollback to a lower epoch is rejected. |
| `backend/services/signing/anchor.go` | low | no | Trust anchor fails closed: missing or malformed anchor file causes immediate error, not a silent skip. |
| `backend/cmd/init/main.go` | low | no | `verifyOSBeforeBoot()` called before any pivot_root or service start. `log.Fatalf` on dm-verity mismatch. Boot counter incremented before VERITY check; `MarkHealthy` deferred until HTTP server is confirmed listening. |

---

## 2. Cluster Passphrase

| Location | Severity | Fix Applied | Reasoning |
|---|---|---|---|
| `backend/services/cluster/s3.go` | low | no | Passphrase never written to disk. Argon2id key derivation (time=3, mem=64MiB, threads=4, key=32B). SSE-C applied per-object. Salt is public (stored in cluster.json), passphrase is not. |
| `backend/services/cluster/cluster.go` | low | no | No passphrase in log output. Heartbeat and sync logs contain only node IDs, modes, and object counts — no secret material. |
| `backend/services/joinsync/joinsync.go` | low | no | Passphrase lives only in a closure passed to the pull goroutine. It is never written to `storage.json` nor any other file. `ErrAlreadyProvisioned` prevents double-provision replay. |

---

## 3. TPM / PIN / Fingerprint

| Location | Severity | Fix Applied | Reasoning |
|---|---|---|---|
| `backend/services/auth/devicepin.go` | low | no | PIN material never stored in plaintext. Argon2id wrap key (time=1, mem=64MiB, threads=4, key=32B). Ciphertext is AES-256-GCM. Optional TPM re-seal via `KeyStore.Seal`. Lockout at 5 wrong attempts; permanent lock after 3 lockouts. |
| `backend/services/auth/devicepin.go` (TPM-wrapped blob without KeyStore) | high | yes (test) | When `tpm_wrapped=true` in `pin.bin` but `ks==nil`, `ValidatePIN` must return an error rather than attempt software decryption. Code path confirmed fail-closed (line 438). Added `TestValidatePIN_TPMWrapped_NoKeyStore` to `devicepin_test.go` to assert this behaviour. |
| `backend/services/auth/fingerprint.go` | low | no | `IsAvailable()` checks for `gdbus` + `fprintd` presence at runtime. `Verify()` returns `ErrFingerprintUnavailable` (not nil) when hardware is absent. Fail-closed. PAM-level delegation means no credential material handled in process. |
| `backend/services/devicekey/tpm.go` | low | no | TPM2 wrap key derived from stable ECC P-256 public key DER. Seal/Unseal uses AES-256-GCM with the TPM-derived shared secret. Key never leaves the TPM boundary. |

---

## 4. WebAuthn / Passkeys

| Location | Severity | Fix Applied | Reasoning |
|---|---|---|---|
| `backend/services/passkeys/passkeys.go` | low | no | Challenge replay prevented by `au12ConsumeSession`: sessions are single-use tokens with expiry and `userID` binding checked before delete. RP-ID sourced from `PASSKEYS_RP_ID` environment variable. Device-bound via `KeyStore.Seal`. |
| `backend/cmd/server/routes_passkeys.go` (`finishRegister`) | med | yes | `AttestationResponse` decoder had no body size cap — a large POST could exhaust server memory. Added `http.MaxBytesReader(w, r.Body, 64<<10)`. |
| `backend/cmd/server/routes_passkeys.go` (`finishAssert`) | med | yes | Same issue for `AssertionResponse`. Added `http.MaxBytesReader(w, r.Body, 64<<10)`. |

---

## 5. Peering

| Location | Severity | Fix Applied | Reasoning |
|---|---|---|---|
| `backend/services/peering/relay.go` | low | no | Relay stores and forwards only the opaque `BlobB64` ciphertext. It never decrypts payload contents. Ed25519 sender signature verified before forwarding. Mutual-trust check enforced. Rate limiting per sender. Timestamp tolerance ±5 min prevents replay. Cluster passphrase never crosses the relay boundary. |

---

## 6. App Sandbox

| Location | Severity | Fix Applied | Reasoning |
|---|---|---|---|
| `backend/services/appnet/manifest.go` | low | no | Permission whitelist enforced against a static capability set. `..` rejected in `Command` and `IconPath`. Capabilities not in the whitelist cause manifest load to fail. |
| `backend/services/appnet/launcher.go` | low | no | Command taken from validated manifest only — no user-controlled shell execution. `os.Environ()` not inherited; environment is scrubbed. `LD_PRELOAD` and `LD_LIBRARY_PATH` explicitly blocked. Network namespace isolation applied via Linux namespaces + iptables. |

---

## 7. HTTP Handlers

| Location | Severity | Fix Applied | Reasoning |
|---|---|---|---|
| `backend/services/auth/auth.go` (login log line) | high | yes | Login failure log printed first 20 chars of `PasswordHash` and `len(password)`. This leaks credential material to log files and any log aggregation pipeline. Removed `hash_prefix` and `pw_len` from the log message. |
| `backend/cmd/server/main.go` (`POST /api/exec`) | high | yes | Admin-gated exec handler had no request body size cap. Added `http.MaxBytesReader(w, r.Body, 64<<10)`. |
| `backend/cmd/server/main.go` (`POST /api/network/configure`) | med | yes | No body size cap. Added `http.MaxBytesReader(w, r.Body, 64<<10)`. |
| `backend/cmd/server/main.go` (`POST /api/stream/launch-app`) | med | yes | No body size cap. Added `http.MaxBytesReader(w, r.Body, 64<<10)`. |
| `backend/cmd/server/main.go` (`POST /api/apps/launch`) | med | yes | No body size cap. Added `http.MaxBytesReader(w, r.Body, 64<<10)`. |
| `backend/cmd/server/main.go` (`PUT /api/device-profile`) | med | yes | No body size cap. Added `http.MaxBytesReader(w, r.Body, 4<<10)`. |
| `backend/cmd/server/main.go` (`POST /api/ai/chat`) | med | yes | No body size cap on unbounded LLM prompt input. Added `http.MaxBytesReader(w, r.Body, 1<<20)`. |
| `backend/cmd/server/routes_aiapps.go` (`POST /api/ai-apps/{id}/update`) | med | yes | Admin-gated handler that writes HTML/Python to disk had no body size cap. Added `http.MaxBytesReader(w, r.Body, 10<<20)`. |
| `backend/cmd/server/routes_conflicts.go` (conflict resolution handler) | med | yes | No body size cap. Added `http.MaxBytesReader(w, r.Body, 64<<10)`. |
| `backend/cmd/server/routes_sshkey.go` (authorized_keys add) | med | yes | No body size cap. Added `http.MaxBytesReader(w, r.Body, 16<<10)`. |
| `backend/cmd/server/routes_open.go` (`POST /api/open`) | med | yes | No body size cap on URL open handler. Added `http.MaxBytesReader(w, r.Body, 8<<10)`. SSRF prevention via `isRestrictedHost` is present and fail-closed on DNS resolution error. |
| `backend/cmd/server/routes_joincode.go` (`POST /api/setup/join-code`) | med | yes | No body size cap on unauthenticated join-code endpoint. Added `http.MaxBytesReader(w, r.Body, 4<<10)`. IP rate limiting (5 req/min) already present. |

---

## 8. Boot Counter Rollback

| Location | Severity | Fix Applied | Reasoning |
|---|---|---|---|
| `backend/services/osdist/slots.go` | low | no | `IncrementBootCounter` called before services are started (early in boot). `MarkHealthy` called only after the HTTP server is confirmed listening. `ShouldRollback` check gates A/B slot flip. Boot counter is monotonically increasing; epoch floor in `signing/epoch.go` enforces minimum and requires a root-signed bump to lower the floor. No rollback path found. |

---

## Summary

- **Critical**: 0
- **High**: 3 (all fixed inline)
  - Hash prefix + password length leaked in auth log (auth.go)
  - Admin exec handler missing body size cap (main.go)
  - TPM-wrapped credential fail-closed test missing (devicepin_test.go — test added)
- **Medium**: 11 (all fixed inline — missing `http.MaxBytesReader` across HTTP handlers)
- **Low**: 14 (no fixes required — design confirmed sound)

All fixes are backward-compatible and do not change any API surface or on-disk format.
