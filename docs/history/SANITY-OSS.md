# Vulos OSS — Deep Sanity Check

**Date:** 2026-05-21
**Branch:** audit/SANITY-OSS
**Scope:** Read-only cross-validation of tasks.md, roadmap/*.md, README.md, and actual backend/src code. Extends AUDIT-OSS.md; does not repeat findings already documented there.

---

## Confirmed Coherent

### Boot chain end-to-end
The boot sequence is correctly wired and fail-closed:

1. **USB/iPXE → kernel → initramfs**: `build.sh --live` produces a UEFI-bootable GPT image (ESP with systemd-boot, kernel, initrd; ext4 data partition with squashfs). The initramfs `scripts/initramfs/vulos-live` hook mounts squashfs + tmpfs overlayfs before `pivot_root`.
2. **vulos-init as PID1**: `backend/cmd/init/main.go` is a genuine Linux init. Live-boot (`vulos.live=1` in cmdline) correctly skips OSDIST-03 counter and VERITY-02 verification (the squashfs has no signed manifest in a live session — intentional).
3. **VERITY-02 gate on installed boots**: `verifyOSBeforeBoot()` is called before `startServices()`. Any failure calls `log.Fatalf(...)` which halts boot. Fail-closed is enforced.
4. **A/B boot counter**: `incrementBootCounter()` runs early; `markBootHealthy()` is called only after `localhost:8080` responds. Matches RAUC model.
5. **Signing canonical format**: consistent across `services/signing`, `services/osdist/manifest.go`, and `cmd/verify/verifier.go`. `vulos-sig-v1` text header consistent in all three layers.

### SSE-C encryption claim (README: "Encrypted at rest (SSE-C + Argon2id)")
`backend/services/cluster/s3.go` implements SSE-C with Argon2id (time=3, mem=64 MiB, threads=4, keyLen=32). Confirmed correct.

### Cloud-side naming (code URLs)
> Superseded: `cloudsignup.go` and its `defaultCloudAPIURL` / `api.vulos.org` constant no longer exist. Vulos is free, self-hosted software with no hosted control plane — there is no default cloud host, and an unconfigured box never dials out.

### Broker pubkey path
`backend/services/auth/cloudlogin.go:45` uses `/var/lib/vulos/cloud/broker.pub` with `VULOS_CLOUD_BROKER_PUBKEY` env override. Consistent with `cloudsignup.go`.

### Three deployment shapes in Setup.jsx
`NETB05_AccountChoiceStep` (Setup.jsx:1049) correctly branches to `local`, `cloud-login`, and `cloud-create`. All three paths through the wizard exist.

### S3/Restic backup
`backend/cmd/server/main.go:85-93` wires vault with restic — conditional on S3 configured and `vault.FindRestic()`. README claim is accurate.

### CONC-02 run-lease for singleton apps
`backend/services/appnet/launcher.go` has `runLease *lease.RunLease` and full singleton/replicated/collaborative branching. CONC-01 `Concurrency` field in manifest is validated. Both correctly implemented and wired.

### PEER-42 wiring (in-progress)
The peering sub-handlers ARE being wired in `backend/cmd/server/main.go:1452-1628`. This is in-progress, not blocked. Stores initialize at startup, handlers register onto `peeringMux`, and the mux is mounted at `/api/peering/`. The `contactStore != nil` guards are defensive, not silently broken.

### D94 tasks marked done
All 30 D94 track tasks (OSDIST-/SEED-/NETB-/SIGN-/VERITY-/LEASE-/SYNC-/CONC-/COLLAB-) are correctly marked `done` in their task entries. The at-a-glance table is accurate.

### Error handling on critical paths
- Boot verification: all errors call `log.Fatalf` → halt. Fail-closed confirmed.
- Lease acquire: returns `ErrLost` on 412. No fail-open pattern found.
- Signature check: returns non-nil error on any mismatch. Callers halt boot.

---

## Mismatches Found

### M1 — CRITICAL (already in AUDIT-OSS; confirmed still present)
**joinsync JSON field name mismatch**

`backend/services/joinsync/joinsync.go:95-99` expects `bucket`, `region`, `access`, `secret`.
`src/auth/Setup.tsx:557-561` posts `s3_bucket`, `s3_region`, `s3_access_key`, `s3_secret_key`.

Go's `encoding/json` silently ignores unknown fields. The join handler receives an empty `JoinRequest` and returns `ErrBadRequest("bucket")`. Setup wizard silently fails to join.

**Fix:** Either add json struct tags `json:"s3_bucket"` etc. to `JoinRequest`, or rename the JSX variables.

### M2 — CRITICAL (already in AUDIT-OSS; confirmed still present)
**`/api/setup/mode` missing from `publicPaths`**

`backend/services/auth/handlers.go:93-114` does not include `/api/setup/mode`. The setup wizard `IS09_SyncingStep` polls this endpoint unauthenticated at first-boot. Returns 401 on every poll; wizard hangs at syncing screen.

**Fix:** Add `"/api/setup/mode": true` to `publicPaths` in `backend/services/auth/handlers.go`.

### M3 — HIGH
**README claims runtime dm-verity block verification; initramfs does not set it up**

`README.md:154`: "dm-verity — Merkle tree over the squashfs verifies every block on read at runtime"
`roadmap/SIGNING.md:25`: "every block is verified on read at runtime"

The code verifies the verity root hash against the signed manifest BEFORE services start (`cmd/verify/verifier.go:325-334`) — this is correct. However, `scripts/initramfs/vulos-live:46` mounts the squashfs directly with `-o loop,ro`, not through a dm-verity device (`veritysetup open`). The kernel's dm-verity driver is never activated. Block-level runtime verification is NOT functioning.

The verifier comment (`verifier.go:256-258`) acknowledges this: *"This function is the pre-pivot gate; it does not invoke the kernel's dm-verity setup itself (that is done by the caller in the mount sequence)."* The caller (`scripts/initramfs/vulos-live`) does not do it.

**File:line:** `scripts/initramfs/vulos-live:46`
**Fix:** After mounting the squashfs as the lower layer, create a dm-verity device via `veritysetup open <squashfs> vulos-root <hashtree> <roothash>` and mount the resulting `/dev/mapper/vulos-root` instead of mounting the raw squashfs. Requires the `.hashtree` file on the data partition alongside `image.squashfs`.

### M4 — HIGH
**SMOKE-01 and SMOKE-02 marked `done` but smoke scripts are not in CI**

`tasks.md:1210-1212`: SMOKE-01 AC states "Failure blocks CI merge" and "Runs headless without external deps".
`tasks.md:1215-1217`: SMOKE-02 AC states "Runs headless in CI".
`.github/workflows/ci.yml`: CI runs frontend build, backend build/vet/test, gofmt check, Docker build — no smoke tests.

The scripts `scripts/smoke-peering.sh` and `scripts/smoke-liveusb.sh` exist and are correctly implemented, but they are not referenced in any CI workflow. The AC "Failure blocks CI merge" is not met.

Additionally, SMOKE-01's dependency is PEER-42 which is `in-progress`. The smoke test cannot currently pass (many routes return non-501 now but the smoke script was designed for when PEER-42 is complete). Marking SMOKE-01 `done` before its dep is `done` is premature.

**File:line:** `.github/workflows/ci.yml` (missing smoke steps); `tasks.md:1210,1215` (status should be `todo` or `in-progress`)
**Fix:** Add a CI job that runs `scripts/smoke-peering.sh` after PEER-42 completes. Re-open SMOKE-01 as `in-progress` until that job is in CI and PEER-42 is done.

### M5 — HIGH
**`services/installer` implemented and `done` in BMINIT-12 but not wired from any binary**

`backend/services/installer/installer.go:181-188` has `RegisterHandlers` for `/api/installer/disks`, `/status`, `/install`, `/progress`, `/live-session`.
`tasks.md:843`: `BMINIT-12` lists `main.go (routes)` as a key file and is marked `done`.
No import of `vulos/backend/services/installer` exists in `backend/cmd/server/main.go` or `backend/cmd/init/main.go`.

The installer UI (BMINIT-13) and the "Try Vulos / Install" flow (NETB-04) both depend on these endpoints being live. A user clicking "Install" in the live-USB wizard will get 404s for all installer routes.

**File:line:** `backend/cmd/server/main.go` (no `installer.RegisterHandlers` call)
**Fix:** Wire `installer.RegisterHandlers(mux, installer.New(home))` in `cmd/server/main.go`. Guard with `installer.IsLiveSession()` if the destructive routes should only be accessible in live-USB mode.

### M6 — MEDIUM
**tasks.md header text is stale: says "30 todo tasks" for D94 track**

`tasks.md:3`: *"New image-distribution track (D94): 30 `todo` tasks across OS Distribution..."*
`tasks.md:31-38` (at-a-glance): All D94 areas show 100% done.
All 30 D94 task entries are individually marked `done`.

The header was written when the tasks were added as todo. The tasks are now complete. The header text is misleading to a new reader who sees "30 todo tasks" but finds none.

**File:line:** `tasks.md:3`
**Fix:** Update the header status text to reflect current state: *"New image-distribution track (D94): 30 tasks — all done."*

### M7 — MEDIUM
**`VerifySquashfsBeforePivot` runs AFTER pivot, not before**

`backend/cmd/init/main.go:383`: `verifyOSBeforeBoot()` is called after `mountAll()`, which has already mounted the squashfs (the process is already running from it as PID1). The function name and comment say "before pivot" but the check happens after pivot_root — the squashfs is the active root at the point of verification.

This is not a security hole (the check still blocks services from starting if verification fails) but it is architecturally misleading. An attacker who can modify the squashfs in place between the initramfs mount and PID1 startup could bypass the check. The correct model would be to verify in the initramfs before mounting.

**File:line:** `backend/cmd/verify/verifier.go:254-260`, `backend/cmd/init/main.go:269-309`
**Fix (medium-term):** Move signature/hash verification into the initramfs (before squashfs mount), so verification gates the mount itself. Short-term, rename to `verifyOSAfterPivot` and update comments to accurately describe the timing.

### M8 — MEDIUM
**Widespread "Vula OS" naming; D90 rename not applied to 120+ files**

D90 (`decisions.md`) establishes: product is **Vulos**, not **Vula** / **Vula OS**. The rename was declared but not executed across the codebase.

Files using the old name (representative sample):
- `README.md:5` — heading `<h1 align="center">Vula OS</h1>`
- `README.md:35` — section heading "What is Vula OS?"
- `build.sh:2` — `# Vula OS — System Image Builder & Deployer`
- `build.sh:163` — banner text `Vula OS — Image Builder`
- `build.sh:425,832` — systemd unit `Description=Vula OS Server`
- `build.sh:999` — loader entry `title Vula OS Live`
- `build.sh:1094` — loader entry `title Vula OS`
- `scripts/initramfs/vulos-live:2,16,38,71` — all comments
- `index.html` — page title
- `Dockerfile` — comments
- `dev.sh`, `docker-compose.yml` — comments
- All `apps/*/app.json` and `apps/*/server.py` — 40+ app files
- All `roadmap/*.md` docs — 20+ roadmap files
- `tasks.md` — task descriptions (not status flags, so orchestration-safe)
- `LICENSE` — copyright line

Total: ~120 files. This affects branding externally visible to users and contributors.

**Fix:** Run a global `sed -i 's/Vula OS/Vulos/g; s/"Vula"/"Vulos"/g'` pass (with careful review to avoid breaking isiZulu etymology lines that are intentional, which have already been removed in most places).

### M9 — LOW
**README says "no relay required" for peering; relay IS required for NAT traversal**

`README.md:139`: *"Instances communicate directly, no relay required."*
`roadmap/PEERING.md:273-274`: explicitly describes TURN relay for NAT traversal and ICE exchange via signaling (server relay).

The README statement is an oversimplification that may mislead operators who deploy behind symmetric NAT and wonder why calls fail without a TURN server.

**Fix:** Change to: *"Instances communicate directly where network topology allows; TURN relay fallback for NAT traversal is built in."*

### M10 — LOW
**README references `landing/docs/desktop.png` which does not exist**

`README.md:30`: `<img src="landing/docs/desktop.png" width="720" alt="Vula OS Desktop" />`
The `landing/` directory does not exist in the repository. The screenshot renders as a broken image on GitHub.

**File:line:** `README.md:30`
**Fix:** Remove the img tag or replace with a real screenshot at a valid path.

### M11 — LOW
**`services/store` uses CGo (mattn/go-sqlite3) contrary to D23 (pure-Go, no CGO)**

D23 (`decisions.md`): *"keep SQLite pure-Go (modernc); CGO would break baremetal static-binary image."*
`backend/services/store/store.go:30`: `_ "github.com/mattn/go-sqlite3" // CGo SQLite driver with extension support`

The `store` package is currently not imported by any binary (it is orphaned), so it does not break the `go build ./...` output today. But if it is ever wired in (CLUSTER-01/CLUSTER-05/SYNC-* all depend on it), it will pull in CGo and break the static binary deploy.

**File:line:** `backend/services/store/store.go:30`
**Fix:** Replace `mattn/go-sqlite3` with `modernc.org/sqlite` (already in `go.mod`). Use the SQLite load-extension mechanism available in modernc or accept that cr-sqlite extension support requires a separate build tag.

### M12 — LOW (tasks.md data integrity)
**PEER tasks double-counted in "at-a-glance" summary vs header**

`tasks.md:3` header: *"Peering HTTP wiring (PEER-07, PEER-10 through PEER-41 minus PEER-20) ... reopened as todo"*
`tasks.md:13` at-a-glance: `Peering: 41/42 done — 32 tasks unwired (handler logic exists, not served)`

The header says these tasks were "reopened as todo" but individual task entries for PEER-07 through PEER-41 are all still marked `done`. The at-a-glance note clarifies the situation ("handler logic exists, not served") but the discrepancy between the header ("reopened as todo") and task entries ("done") is confusing. Anyone reading "41/42 done" would think the peering implementation is nearly complete, when in reality 32 routes currently only serve 501.

**Fix:** The current convention (mark handler-logic done, gate on PEER-42 for route wiring) is acceptable, but the header text "reopened as todo" should say "handler logic done, route wiring in-progress (PEER-42)".

---

## Recommended Actions

### Immediate (block shipped functionality)

1. **Fix joinsync field name mismatch** (M1): `src/auth/Setup.tsx:557-561` → rename to `bucket`, `region`, `access`, `secret` OR add json tags to `JoinRequest`. Every user who tries to join an existing cluster hits this silently.

2. **Add `/api/setup/mode` to `publicPaths`** (M2): `backend/services/auth/handlers.go:114` → add `"/api/setup/mode": true`. Every new-instance setup wizard hangs at the sync step.

3. **Wire `installer.RegisterHandlers`** (M5): `backend/cmd/server/main.go` → add `installer.RegisterHandlers(mux, installer.New(home))`. The BMINIT-13/NETB-04 "Try Vulos → Install" path 404s without this.

### Short-term (integrity and honesty)

4. **Add smoke tests to CI** (M4): `.github/workflows/ci.yml` → add a job that runs `scripts/smoke-peering.sh` (after PEER-42 completes) and re-open SMOKE-01 until done.

5. **Fix dm-verity runtime activation** (M3): `scripts/initramfs/vulos-live:46` → use `veritysetup open` to create a dm-verity device and mount `/dev/mapper/vulos-root` instead of the raw squashfs. README and SIGNING.md claims about runtime block verification are currently false.

6. **Update tasks.md header** (M6): `tasks.md:3` → change "30 `todo` tasks" to "30 tasks — all done" for the D94 track description.

### Medium-term (code quality and naming)

7. **Global rename "Vula OS" → "Vulos"** (M8): ~120 files. Priority targets: `README.md`, `build.sh` (especially systemd unit `Description=` and bootloader `title` entries which are user-visible), `scripts/initramfs/vulos-live`. App json/server.py files and roadmap docs are lower priority (internal only).

8. **Migrate `services/store` to modernc** (M11): `backend/services/store/store.go` → replace `mattn/go-sqlite3` with `modernc.org/sqlite` before this package is wired into any binary.

9. **Rename `VerifySquashfsBeforePivot`** (M7): Accurate name is `VerifyOSBeforeServices`. Update comments in `verifier.go` and `cmd/init/main.go` to reflect that verification happens post-pivot.

10. **Fix README "no relay required" claim** (M9): `README.md:139` → clarify that TURN relay is the NAT traversal fallback.

11. **Remove broken screenshot reference** (M10): `README.md:30` → remove or replace `landing/docs/desktop.png`.

---

## Appendix: Unwired Packages Status

Confirming AUDIT-OSS findings with additional context:

| Package | Gap | Disposition |
|---------|-----|-------------|
| `services/cluster` (`Cluster.Start()`) | Never called; S3 heartbeat + peer discovery non-functional | Intentional defer; wire when cluster features are needed |
| `services/sync` (hotpath, snapshot, bootstrap) | Never imported by any binary; CRDT sync does not run; `sync_lag` health check is hardcoded stub | Intentional defer (SYNC-01/02/03 done, need CLUSTER-05 + peering mesh first) |
| `services/installer` | `RegisterHandlers` never called; installer routes 404 | **Gap (M5 above)** — should be wired |
| `services/concurrency` | `policy.go` is a library; no HTTP endpoints to wire | Intentional — consumed by data stores, not directly by HTTP layer |
| `services/smsotp` | No wire-in | Intentional defer; AUTH-14 done, no deployment has Twilio configured |
| `services/telephony` | No wire-in | Intentional defer; MOBILE-* done, needs modem hardware |
| `services/naming` | No wire-in | Intentional defer; DNS naming for local LAN hostnames |
| `services/clientcerts` | No wire-in | Intentional defer; AUTH-11 done, mTLS cert management not yet exposed |
| `services/input` | No wire-in | Intentional defer; GAME-05 (uinput FF/rumble) works via stream service |
