# Vulos OSS Comprehensive Audit Report

**Date:** 2026-05-21  
**Branch:** audit/AUDIT-OSS  
**Scope:** Read-only audit of the OSS codebase. No code changes made.

---

## 1. Architecture Coherence

### Overall Assessment: Coherent, with two structural gaps

The image-based OS direction is internally consistent. The pieces fit together correctly at the design level, and the implementation follows the documented model closely. Two structural gaps exist: the A/B update loop is implemented but unstarted, and the CRDT sync engine is implemented but never imported by any entry point.

### Boot Chain

`cmd/init` is a proper Linux PID-1. The boot sequence is:

1. `verifyOSBeforeBoot()` calls `cmd/verify.VerifySquashfsBeforePivot()` before any service is started.
2. `VerifySquashfsBeforePivot()` performs the full chain: load anchor key from `/etc/vulos/trust-anchor.pub`, validate the release certificate at `/etc/vulos/release-cert.json` against the root anchor, enforce the epoch floor from `/var/lib/vulos/epoch-floor.json`, decode the release public key, verify the squashfs detached `.sig`, and check the dm-verity root hash against the manifest at `/etc/vulos/stable.json`.
3. Only after verification passes does `cmd/init` spawn `cmd/server`, start udev, Plymouth, avahi, the network phase, and the kiosk compositor.
4. `incrementBootCounter()` runs early (OSDIST-03); `markBootHealthy()` is called only after `localhost:8080` becomes responsive — matching the RAUC/Android A/B model exactly.

The signing canonical format (`Canonical()` → sorted keys, no whitespace) is used consistently in `services/signing`, `services/osdist/manifest.go` (`ParseAndVerify`), and `cmd/verify/verifier.go`. The `.sig` wire format (`vulos-sig-v1` text header) is also consistent across all three layers.

### A/B Slot Manager

`services/osdist` implements: `SlotManager`, `Updater` (OSDIST-04, 4-hour poll), `UpdateHandlers` (OSDIST-05: `GET/POST /api/os/update/*`), `StableManifest`, `Source` with mirror failover, and `ParseAndVerify`. The design matches OS-DISTRIBUTION.md exactly: download to inactive slot, verify hash + sig before committing, call `SetPending`, fire `RebootPrompt`. **Gap: `Updater.Run()` is never started (see §2).**

### Trust Model

The two-tier PKI (offline ROOT key → RELEASE key certificate → artifact signatures) is correctly implemented in `cmd/verify`. The broker pubkey path (`/var/lib/vulos/cloud/broker.pub`) is consistent between `services/auth/cloudlogin.go` and `services/auth/cloudsignup.go`. Public-bucket + signing-key trust separation (no per-device credentials) is correctly reflected in `services/osdist/source.go`.

### BMINIT / First-Boot

`cmd/init` implements the full BMINIT network phase (wired DHCP → WiFi fallback → `resolv.conf` → avahi). Boot mode detection (`services/bootmode`) correctly distinguishes "setup" / "sync" / "normal" by inspecting `instance.json` and `sync-state.json`. `joinsync.Join()` writes both files in the exact shape that `bootmode.Detect` reads (cross-package JSON shape contract tested).

### Peering / Relay Mesh

`services/peering` is imported and handlers are registered in `cmd/server/main.go`. The SFU sub-package is also wired. Relay, contacts, messaging, groups, feeds, calls, and collab handlers are all present (PEER-42 set).

### CRDT Sync

`services/sync` (hotpath, bootstrap, snapshot) is implemented but **never imported or started** from any entry point. This is the largest architectural gap: the codebase describes a leaderless CRDT sync layer, implements it, but no binary calls it.

### Cluster Heartbeat

`services/cluster` (`Cluster.Start()` — S3 heartbeat + peer discovery) is implemented but **never called** from `cmd/server/main.go`. Peer discovery is therefore non-functional at runtime.

---

## 2. Wire-In Completeness

### Entry Points

Only two real binaries exist under `backend/cmd/`:
- `cmd/server` — HTTP API server (the main runtime binary)
- `cmd/init` — Linux PID-1 / init process (Linux-only build tag)

The audit brief referenced `cmd/bm-init`, `cmd/joinsync`, `cmd/imgverify`, and `cmd/boot-verify` as separate binaries. **None of these exist.** Their functionality is embedded:
- `imgverify` / `boot-verify` → `cmd/init` calls `verifyOSBeforeBoot()` which calls `cmd/verify.VerifySquashfsBeforePivot()` inline
- `joinsync` as a binary → `cmd/server` via `routes_join.go`
- `bm-init` → `cmd/init` (the full BMINIT network phase is in main.go of that package)

### Per-Area Status

| Area | Status | Detail |
|------|--------|--------|
| CLOGIN | Wired | `cloudlogin.go`, `cloudsignup.go`, PIN, fingerprint — all registered; endpoints in `publicPaths` |
| PEER | Wired | All PEER-42 handlers registered via `routes_peer.go`; SFU imported |
| NETB | Wired | `bootmode.RegisterHandlers` called; joinsync via `routes_join.go` |
| SIGN | Wired (read path) | `signing` package used by `osdist`, `cmd/verify`; no sign operation in server (correct — signing is offline) |
| VERITY | Wired in `cmd/init` | `verifyOSBeforeBoot()` called before any service starts |
| BMINIT | Wired in `cmd/init` | Full network phase, udev, avahi, Plymouth |
| OSDIST (OSDIST-05) | Wired | `UpdateHandlers.RegisterHandlers(mux)` at line 1948 |
| OSDIST (OSDIST-04) | **NOT wired** | `Updater.Run()` never started; `StatusStore` passed as `nil` |
| LEASE | Wired | Lease/concurrency handlers present in routes |
| SMOKE | Partial | Health endpoint exists; `sync_lag` is a hardcoded stub (see §3) |

### Packages With No Entry-Point Wiring

| Package | What It Does | Gap |
|---------|-------------|-----|
| `services/cluster` | S3 heartbeat, peer discovery, `Cluster.Start()` | Never called from `cmd/server` |
| `services/sync` | CRDT sync engine (hotpath, bootstrap, snapshot) | Never imported by any binary |
| `services/concurrency` | Manifest concurrency modes | Implemented; no wire-in observed |
| `services/installer` | OS installer flow | Implemented; no wire-in in server |
| `services/smsotp` | SMS OTP auth | Implemented; no wire-in |
| `services/telephony` | Telephony backend | Implemented; no wire-in |
| `services/naming` | DNS naming | Implemented; no wire-in |
| `services/clientcerts` | Mutual TLS client certs | Implemented; no wire-in |
| `services/input` | Input device management | Implemented; no wire-in |

---

## 3. Stub / TODO Scan

### Go — Non-Test Files

Only one genuine `TODO` found in non-test Go source:

| File | Line | Text |
|------|------|------|
| `backend/services/appnet/launcher.go` | 181 | `cmd.Stdout = os.Stdout // TODO: capture to log file per app` |

One structural stub found (not marked TODO):

| File | Location | Issue |
|------|----------|-------|
| `backend/cmd/server/health.go` | `handleClusterHealth()` | `checks["sync_lag"] = "ok"` is a hardcoded placeholder. Comment reads: "Will be wired to real cluster sync once that layer is implemented" |

### JSX — Non-Test Files

No `TODO`, `FIXME`, or `XXX` markers were found in JSX source files.

---

## 4. Cross-Repo Contract

### Broker Pubkey Path

The OSS side reads the broker public key from `/var/lib/vulos/cloud/broker.pub` (constant in `services/auth/cloudlogin.go`). The env override `VULOS_CLOUD_BROKER_PUBKEY` allows test injection. This path must match what vulos-cloud writes during device enrollment. The constant is correct and consistent across `cloudlogin.go` and `cloudsignup.go`.

The cloud API base URL is `https://api.vulos.org` (constant in `services/auth/cloudsignup.go`), matching the canonical URL documented in `VULOS_NAMING.md`.

### joinsync Data-Bucket Protocol — CRITICAL MISMATCH

`services/joinsync.JoinRequest` defines these JSON field names:

```go
Bucket     string `json:"bucket"`
Region     string `json:"region"`
Access     string `json:"access"`
Secret     string `json:"secret"`
Passphrase string `json:"passphrase"`
```

`src/auth/Setup.jsx` (lines 557-560) posts:

```js
{
  s3_bucket: ...,
  s3_region: ...,
  s3_access_key: ...,
  s3_secret_key: ...,
  passphrase: ...
}
```

**The field names do not match.** `bucket` ≠ `s3_bucket`, `region` ≠ `s3_region`, `access` ≠ `s3_access_key`, `secret` ≠ `s3_secret_key`. Go's `encoding/json` silently ignores unknown fields; the handler will receive an empty `JoinRequest` and return `ErrBadRequest("bucket")`. The join flow silently fails on the first attempt. This bug exists end-to-end with no server-side error visible to the user unless they inspect network requests.

**Fix required:** Either rename the JSX fields to match the backend struct, or add `json:"s3_bucket"` etc. tags to `JoinRequest`.

---

## 5. First-Boot UX Coherence

### Three Deployment Shapes

`Setup.jsx` correctly implements all three shapes via `NETB05_AccountChoiceStep`:
- **`local`** — standalone, no cluster, no cloud account
- **`cloud-login`** — existing cloud account, cloud-managed deployment
- **`cloud-create`** — new cloud account (proxied via `/api/auth/cloud/signup` → `api.vulos.org`)

The STEPS and IS09_JOIN_STEPS wizard arrays cover: welcome, OS verification, network choice, account choice (three branches), sync progress, and landing. The flow maps to the three shapes documented in the roadmap.

### `/api/setup/mode` Missing from `publicPaths` — CRITICAL BUG

`IS09_SyncingStep` in `Setup.jsx` polls `/api/setup/mode` to detect when sync completes and the wizard should advance. This endpoint is served by `bootmode.RegisterHandlers()` and maps to `GET /api/setup/mode`.

**`/api/setup/mode` is NOT in `auth.publicPaths`** (lines 93-114 of `services/auth/handlers.go`). At first-boot the user has no session cookie. The auth middleware will return `401` for every poll, and the sync step will never advance. The setup wizard hangs indefinitely at the syncing screen.

**Fix required:** Add `"/api/setup/mode": true` to the `publicPaths` map in `services/auth/handlers.go`.

Present in `publicPaths` and correct:
- `/api/setup/status` ✓
- `/api/setup/join` ✓
- `/api/setup/join/status` ✓
- `/api/setup/join-code` ✓

### Static Assets Referenced in JSX

| Asset | Path | Exists? |
|-------|------|---------|
| App icon | `/icon-128.png` → `public/icon-128.png` | Yes ✓ |
| `PostSignupWizard` component | `./PostSignupWizard` | Present in `src/auth/` ✓ |

No broken SVG or image references were found in `Setup.jsx`.

---

## 6. Test Coverage Gaps

### Well-Covered Areas

All service packages that were audited have test files. Specifically confirmed:
- `services/signing` — canonical JSON, sign/verify, `.sig` marshal/parse
- `services/osdist` — manifest verify, slot manager, source failover, updater
- `services/joinsync` — join validation, progress polling, shape assertions
- `services/bootmode` — mode detection across all three states
- `services/auth` — login, cloud login, PIN, fingerprint
- `cmd/verify` — squashfs verification, release cert validation

### Gaps Worth Addressing

| Area | Gap |
|------|-----|
| `services/cluster` | No integration tests for `Cluster.Start()` lifecycle (start, heartbeat, shutdown) |
| `services/sync` | Test files exist but the package is never exercised from a running server — no smoke/integration path |
| `cmd/server/health.go` | `sync_lag` stub has no test asserting it returns the real value once wired |
| `osdist.Updater` | `Updater.Run()` is tested in isolation but the wiring gap (nil StatusStore, no goroutine start) is not caught by any integration test |
| `joinsync` ↔ `Setup.jsx` | The JSON field name mismatch (§4) has no contract test. A server-side test that sends the exact payload the JSX produces would have caught this. |
| `services/appnet/launcher.go:181` | Log capture for app stdout is untested — stdout goes to server process log, which is a risk in production |

---

## Summary of Actionable Findings

### Critical (break existing functionality)

1. **joinsync field name mismatch** — `Setup.jsx` sends `s3_bucket`/`s3_region`/`s3_access_key`/`s3_secret_key`; backend `JoinRequest` expects `bucket`/`region`/`access`/`secret`. Join silently fails.
2. **`/api/setup/mode` not in `publicPaths`** — setup wizard sync-polling step returns 401 at first-boot; wizard hangs.

### High (feature not functional)

3. **`osdist.Updater` never started** — OSDIST-04 background loop not started; `StatusStore` is `nil`; OS auto-updates are wired for HTTP control but never run. `cmd/server/main.go` line 1948: the `nil` needs to be a real `StatusStore` and `go updater.Run(ctx)` needs to be called.
4. **`services/cluster` never started** — `Cluster.Start()` not called from server; S3 heartbeat and peer discovery are non-functional.
5. **`services/sync` never imported** — CRDT sync engine does not run; sync_lag health check is a hardcoded stub.

### Low (tech debt)

6. `services/appnet/launcher.go:181` — `TODO: capture to log file per app` (app stdout goes to server process log).
7. Several service packages implemented but not wired: `concurrency`, `installer`, `smsotp`, `telephony`, `naming`, `clientcerts`, `input`.

### Build Status

`cd backend && go build ./...` — **passes cleanly, no errors.**
