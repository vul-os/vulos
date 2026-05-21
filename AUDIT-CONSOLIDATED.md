# Vulos OSS — Audit + Deployment Readiness Consolidation

**Date:** 2026-05-21  
**Scope:** End-to-end audit, security hardening, peering verification, first-boot UX, env-mode tooling, comprehensive test coverage, sanity check vs roadmap/docs/code.  
**Repo:** vulos (OSS) — 236 / 236 tasks done. All branches merged to `main`, nothing pushed.

## What landed (by branch)

| Area | Branch | Outcome |
| --- | --- | --- |
| Code audit | `audit/AUDIT-OSS` | 237-line audit. Surfaced 2 criticals (joinsync field mismatch, `/api/setup/mode` missing from publicPaths), 3 highs (osdist/cluster/sync services never started). Report: `AUDIT-OSS.md`. |
| Audit fixes | `audit/AUDIT-FIX-OSS` | All 6 fixes applied. Wizard works end-to-end now. osdist/cluster/sync goroutines start. App stdout → per-app log file. |
| Security hardening | `audit/SEC-OSS` | 14 fixes. Login log scrubbed of cred prefixes. TPM fail-closed test added. 11 HTTP handlers gained `MaxBytesReader` body caps. Report: `SECURITY-OSS.md`. |
| Webapps security | `audit/SEC-WEBAPPS` | CSP on all 20 apps. XSS fix in notes. postMessage guard. weather app routed through host proxy. Permissions-Policy header. Report: `SECURITY-WEBAPPS.md`. |
| Peering e2e | `audit/TEST-PEERING` | 8 e2e tests + production bug fix in `collab.go` (body-double-read). 783 peering tests, 0 failures. Report: `PEERING-E2E.md`. |
| First-boot e2e | `audit/TEST-NETWORK` | 10 integration tests across Standalone / Self-hosted / Cloud-managed shapes. Report: `FIRSTBOOT-E2E.md`. |
| PEER-42 wiring | `audit/PEER42-RETRY` | 3 missing handler-sets wired (collab history, presence, collab invite). PEER-* now 42 / 42. No peering route returns 501. |
| Lint cleanup | `audit/OSS-LINT-AUTH` + `audit/OSS-LINT-CORE` | 5 fixes in `Setup.jsx`; 38 files cleaned in core/shell/apps; deferred report. |
| Sanity check | `audit/SANITY-OSS` | 12 mismatches. Confirmed M1+M2 already fixed by AUDIT-FIX-OSS. Report: `SANITY-OSS.md`. |
| Sanity fixes | `audit/SANITY-FIX-OSS` | dm-verity now actually active in initramfs; installer wired; smoke tests in CI; `services/store` migrated off mattn/go-sqlite3 to modernc.org/sqlite; "Vula OS" → "Vulos" in README + roadmap + apps + bootloader. |
| README | `audit/POLISH-README` | Rewrite reflecting image-based OS + peering + cloud-login + sync state. |
| `--env=local/dev/prod` flag | `audit/OSS-ENV-FLAG` | New `backend/services/env` package + 14 tests. cmd/server + cmd/init accept the flag. README documents it. |
| Comprehensive tests | `audit/OSS-TEST-SUITE` | 28 auth unit tests + 14 e2e tests (build-tag `e2e`, full first-boot lifecycle harness). `Makefile` + `scripts/test-all.sh`. Coverage report: `TEST-COVERAGE-OSS.md`. |
| Security hardening tests | `audit/OSS-SEC-HARDEN-TESTS` | 47 regression tests (services + peering) locking every SECURITY-OSS.md finding. Report: `SECURITY-HARDENING-TESTS-OSS.md`. |

## Verification

- `cd backend && go build ./...` — clean
- `cd backend && go test ./...` — all packages green
- `npm run build` — clean (chunk-size warning only)
- `make test-local` and `make test-dev` available

## Deployment shapes verified

1. **Standalone** — local OS account, no cloud, no bucket. `FIRSTBOOT-E2E.md` confirms.
2. **Self-hosted cluster** — BYO bucket + cluster passphrase, no cloud routing. joinsync wizard works (the field-name bug that previously broke this is now fixed).
3. **Cloud-managed** — cloud account + Tigris bucket + scale-to-zero managed instances. Cross-instance login broker wired.

## Boot chain (signed-payload primary, TLS opportunistic)

USB or iPXE → `imgverify` (Ed25519) → kernel + initramfs verified → squashfs sig + dm-verity root hash checked → **dm-verity `veritysetup open` activates at runtime** (now actually wired) → overlayfs mount → PID-1 services start.

## Remaining open work (intentional)

- Concurrency, smsotp, telephony, naming, clientcerts, input packages exist but are intentionally not yet wired into the live server (post-v1 features).
- E2E SFU group call + 1:1 call signaling not exercised by automated tests (require live WebRTC).
- File-drop BLE not exercised (hardware-dependent).

## Files of interest (root)

`README.md`, `AUDIT-OSS.md`, `SECURITY-OSS.md`, `SECURITY-WEBAPPS.md`, `PEERING-E2E.md`, `FIRSTBOOT-E2E.md`, `SANITY-OSS.md`, `TEST-COVERAGE-OSS.md`, `SECURITY-HARDENING-TESTS-OSS.md`, `Makefile`, `scripts/test-all.sh`, `tasks.md` (236 / 236 ✅).
