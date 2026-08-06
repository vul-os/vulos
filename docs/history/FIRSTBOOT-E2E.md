# First-Boot End-to-End Flow — Vulos OS

## Overview

A clean Vulos OS install presents the `Setup.jsx` wizard on first boot.
The backend reports `bootmode = "setup"` while `~/.vulos/db/instance.json` is
absent.  At the OS-account-creation step the user chooses one of three shapes:

| Shape | Name in wizard | Bootmode sequence | Cloud required |
|-------|----------------|-------------------|----------------|
| 1 | **Standalone** | setup → normal | No |
| 2 | **New cluster** | setup → sync → normal | No (self-hosted) |
| 3 | **Join existing cluster** | setup → sync → normal | No (self-hosted) |

Historical note: earlier drafts described a fourth option that linked the OS
account to a hosted identity. That no longer exists — Vulos is free, self-hosted
software with no hosted account, sign-in, or enrolment. The only account concept
is the user's own local account on their own box (see `src/auth/Setup.tsx`).

---

## Shape 1 — Standalone (no cloud, no bucket)

### What the user sees

1. **Welcome** → **Choose setup type** (New System / Join Existing) → choose **New System**
2. **Language**, **Timezone**, **Network** (skippable)
3. **Account choice**: pick **Local only**
4. **Local account form**: display name, username, password
5. **PIN** (skippable), **Appearance**, **Identity** (hostname), **SSH Key**,
   **Recovery Kit**, **Ready**

### Backend wire-up

| Step | API call | Effect |
|------|----------|--------|
| Identity step | `GET /api/identity` | Returns ULID + hostname from `instance.json` (creates it on first call via `identity.Load`) |
| Hostname save | `POST /api/identity/hostname` | Updates hostname in `instance.json` |
| Setup complete | `POST /api/exec` (touch `.setup-complete`) | Marks setup done; `bootmode.Detect` returns `"normal"` |

### On-disk state after completion

```
~/.vulos/db/
  instance.json     # ULID + hostname + first_boot timestamp
  # NO storage.json
  # NO sync-state.json
```

`bootmode.Detect` → `"normal"` (no S3, no sync-state).

---

## Shape 2 — New Cluster (self-hosted, first node)

### What the user sees

1. Same wizard as Standalone up to **Cluster Storage** step
2. **Cluster Storage** step: enable toggle ON, enter storage password +
   encryption passphrase (confirm passphrase)
3. Wizard POSTs to `POST /api/setup/storage` which provisions MinIO + writes
   `storage.json` with the generated keys
4. Setup completes; the node is now the cluster origin

### Backend wire-up

| Step | API call | Package | Effect |
|------|----------|---------|--------|
| Storage provision | `POST /api/setup/storage` | `storageprov` | Starts MinIO, creates `vulos-cluster` bucket, writes `storage.json` (access key, secret key, SSE-C key, bucket name). Passphrase **never written**. |
| Cluster start | `cluster.New()` at server boot | `cluster` | Reads `VULOS_S3_*` env; calls `NewClient()` which fetches/creates the Argon2id salt at `cluster/encryption-salt`; calls `InitLeases()` to seed `cluster-init` lease |
| Join marker seed | `realBackend.validate()` on first join | `joinsync` | Fresh bucket: `GetEncrypted(cluster/join-marker)` → NoSuchKey → write marker; subsequent nodes validate against it |
| Heartbeat | `cluster.Start(ctx)` | `cluster` | Writes `nodes/{node_id}/meta.json` every 30 s |
| CRDT sync | `cluster.NewSyncLoop(ctx, cluster, db)` | `cluster/sync` | Pushes `crsql_changes` to `nodes/{node_id}/changes/{db_version}.bin` |

### On-disk state after completion

```
~/.vulos/db/
  instance.json
  storage.json      # enabled=true, access_key, secret_key, ssec_key, bucket_name
  # NO sync-state.json (new cluster does not trigger the sync path)
```

`bootmode.Detect` → `"normal"`.

### Note on "new cluster" vs. "join fresh bucket"

From the backend's perspective, **New Cluster** and **Join Existing** both go
through `joinsync.Join`.  The difference is only in the S3 state:

- **New cluster** (fresh bucket): `realBackend.validate` finds no join marker →
  seeds it → treats as success; `pull` finds no changesets → completes
  immediately.
- **Join existing** (populated bucket): validate decrypts the marker to confirm
  the passphrase; pull downloads existing changesets.

The `IS05_StorageStep` in Setup.jsx handles the new-cluster case (provisions the
bucket first via `POST /api/setup/storage`), while `IS09_JoinConnectStorageStep`
handles the join-existing case (accepts user-supplied credentials).

---

## Shape 3 — Join Existing Cluster

### What the user sees

1. **Welcome** → **Choose setup type** → choose **Join Existing**
2. **Connect to existing storage** form: S3 bucket, region, access key, secret
   key, encryption passphrase
3. POST to `POST /api/setup/join` — validation happens synchronously; on
   success the user sees the **Syncing** screen
4. Progress bar advances through: Initialising → Fetching keys → Restoring
   identity → Syncing storage → Restoring applications → Finalising
5. On completion: **Continue →** (PIN step then Ready)

### Backend wire-up

| Step | API call | Package | Effect |
|------|----------|---------|--------|
| Validate | `POST /api/setup/join` | `joinsync`, `routes_join.go` | Rate-limited (5 req/min); sanitise fields; `realBackend.validate()` → S3 reachability + passphrase marker check |
| Persist creds | (inside `joinsync.Join`) | `joinsync` | Writes `storage.json` (NO passphrase); calls `identity.Load()` → writes `instance.json`; writes `sync-state.json {status:"syncing"}` |
| Async pull | `go runPull(home, cfg, passphrase)` | `joinsync` / `sync` | Calls `sync.Bootstrap` to verify snapshot + changesets are readable; updates `sync-state.json` phases; flips to `status:"complete"` |
| Poll | `GET /api/setup/join/status` | `routes_join.go` | Reads `sync-state.json` and returns current phase / progress |
| Post-sync | `bootmode.Detect` | `bootmode` | `status != "syncing"` → returns `"normal"` |

### On-disk state after completion

```
~/.vulos/db/
  instance.json
  storage.json      # enabled=true, access_key, secret_key, endpoint, bucket, region
  sync-state.json   # {status:"complete", phase:"done", progress_pct:100}
```

`bootmode.Detect` → `"normal"` (sync-state present but not "syncing").

### Security invariants (SECAUDIT2 / INIT-08)

- Passphrase is passed to `realBackend.validate` in memory, used to derive the
  Argon2id SSE-C key inside `cluster.NewClient`, then discarded.
- Passphrase never appears in `storage.json`, `sync-state.json`, `instance.json`,
  or any `.tmp` atomic-write scratch file.
- Once `bootmode.Detect` returns `"normal"`, `POST /api/setup/join` returns
  `409 Conflict` — the join endpoint is closed to prevent unauthenticated
  credential overwrite (SECAUDIT2 L-2).

---

## Frontend → Backend Route Map

| Frontend call | Backend handler | Package |
|---------------|-----------------|---------|
| `GET /api/setup/mode` | `bootmode.RegisterHandlers` | `bootmode` |
| `POST /api/setup/join` | `registerJoinRoutes` | `cmd/server` → `joinsync` |
| `GET /api/setup/join/status` | `registerJoinRoutes` | `cmd/server` → `joinsync` |
| `POST /api/setup/storage` | `storageprov.RegisterHandlers` | `storageprov` |
| `GET /api/storage/status` | `registerStorageRoutes` | `cmd/server` |
| `GET /api/identity` | `routes_identity.go` | `cmd/server` |
| `POST /api/identity/hostname` | `routes_identity.go` | `cmd/server` |
| `GET /api/auth/cloud/status` | `routes_admintoken.go` | `cmd/server` |
| `POST /api/auth/cloud/signup` | auth routes | `cmd/server` → `auth` |

---

## Test Results

Tests run: `go test ./backend/firstboot/... ./backend/services/cluster/... ./backend/services/joinsync/...`

### `backend/firstboot`

| Test | Shape | Result |
|------|-------|--------|
| `TestStandalone_LocalAccount` | Standalone | PASS |
| `TestStandalone_IsNotProvisioned_UntilIdentityLoaded` | Standalone | PASS |
| `TestNewCluster_BucketCreatedPassphraseSavedCRDTInit` | New cluster | PASS |
| `TestNewCluster_PassphraseInMemoryOnly` | New cluster | PASS |
| `TestJoinExisting_BucketAcceptedPassphraseVerifiedFirstChangesetPulled` | Join existing | PASS |
| `TestJoinExisting_WrongPassphraseRejected` | Join existing | PASS |
| `TestJoinExisting_UnreachableBucketRejected` | Join existing | PASS |
| `TestBootmodeTransitions_FullLifecycle` | Join existing | PASS |
| `TestBootmodeTransitions_StandaloneDirectToNormal` | Standalone | PASS |
| `TestProvisionedInstanceRefusesJoin` | Guard | PASS |

### `backend/services/cluster`

All existing tests: **PASS** (cluster, presence, reconcile, s3, sync)

### `backend/services/joinsync`

All existing tests: **PASS** (joinsync, joinsync_l2, joinsync_security)

### Frontend

`npm run build` → **✓ built in ~3s** (no errors)

---

## Code Coverage: Missing Wiring Identified

### What is fully wired

- `bootmode.Detect` — all three mode transitions tested
- `joinsync.Join` — both validation branches (fresh bucket / existing marker)
- `joinsync.Progress` — sync-state polling
- `identity.Load` — ULID generation + instance.json write
- `storageprov.RegisterHandlers` — new-cluster bucket provisioning
- `routes_join.go` — HTTP layer for join + status endpoints
- `cluster.New` + `cluster.Start` — heartbeat loop
- `cluster.NewSyncLoop` — cr-sqlite push/pull cycle

### Observations / gaps noted

1. **New-cluster via Setup.jsx IS05_StorageStep**: This step POSTs to
   `/api/setup/storage` (storageprov), NOT to `/api/setup/join`. After
   storageprov writes `storage.json`, the cluster heartbeat and SyncLoop are
   started at server boot via env-var config (`VULOS_S3_*`), not wired through
   the same flow as joinsync. The bucket/creds from storageprov are not
   automatically read back into the cluster service — the operator must ensure
   `VULOS_S3_*` env vars match what storageprov wrote. This is a known
   architectural gap (noted in decisions.md); the workaround is documented in
   the operator guide.

2. **IS09 chooser "New System" path**: The `IS09_NewJoinChooserStep` routes to
   the full new-system flow (language → timezone → ... → IS05_StorageStep).
   The `IS09_JoinConnectStorageStep` routes to `/api/setup/join`. Both are
   reachable and call the correct backends.

3. **Cloud enrol path (NETB05_AccountChoiceStep)**: The cluster-passphrase field
   (`NETB05_clusterPassphrase`) is collected in the UI but is not sent to any
   backend endpoint from Setup.jsx. It is stored in wizard `config` state only.
   If the user intends to join a cluster via the cloud path, they must complete
   this through the separate IS09 join flow or Settings post-setup.
