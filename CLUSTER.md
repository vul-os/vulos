# Vula OS — Multi-Node Cluster & Storage System

## Overview

Vula OS can run across multiple physical nodes (home, office, spare machines) that share state through a distributed storage layer. Each node is a full, independent Vula instance. There is **no primary node** — every node is equal. Nodes sync state through S3-compatible storage (MinIO), and tunnels provide remote access with automatic failover.

### Two Node Modes

| | Server Mode | Local Mode |
|---|---|---|
| **Use case** | Headless, serves remote users | Physical screen, someone sits in front |
| **Tunnel** | Yes — Vula Tunnel or direct domain | No — direct local use only |
| **S3 Sync** | Yes | Yes |
| **MinIO Storage Node** | Optional | Optional |
| **Display** | Xvfb (virtual) | Physical display |

Both modes run the exact same Vula OS. The only difference is whether a tunnel runs and whether the display is physical or virtual.

```
VULOS_MODE=server     # or "local"
VULOS_S3_SYNC=true    # both modes sync to S3
VULOS_TUNNEL=true     # only server mode
```

---

## Architecture

```
           Internet Users
                │
         Vula Tunnel / Direct Domain
         ┌──────┼──────┐
         ▼      ▼      ▼
      Home     Office   Laptop
      server   server   (local mode, no tunnel)
         │      │       │
         └──────┼───────┘
                ▼
           S3 / MinIO
       (shared state layer)
```

Every node:
- Stores everything locally on disk (fast reads, always available)
- Pushes changes to S3 asynchronously on write
- Pulls latest state from S3 on boot
- Works fully offline — sync catches up when connectivity returns

---

## MinIO as a Built-In App

MinIO runs as a standard Vula app from the registry. Enable it on any node to make that node a storage server. Nodes without MinIO just sync to a peer that has it.

### Registry Entry

```json
{
  "minio": {
    "name": "Storage Node",
    "type": "service",
    "description": "Distributed S3-compatible storage. Enable to make this node a storage server.",
    "category": "system",
    "icon": "storage",
    "versions": {
      "1.0": {
        "install": "curl -sSL https://dl.min.io/server/minio/release/linux-${ARCH}/minio -o /usr/local/bin/minio && chmod +x /usr/local/bin/minio",
        "command": "minio server /root/.vulos/minio-data --console-port ${PORT} --address :9000",
        "port": 9001,
        "env": {
          "MINIO_ROOT_USER": "${VULOS_STORAGE_ACCESS_KEY}",
          "MINIO_ROOT_PASSWORD": "${VULOS_STORAGE_SECRET_KEY}"
        },
        "singleton": true,
        "auto_start": true,
        "permissions": ["network", "filesystem"]
      }
    }
  }
}
```

### Storage Topologies

**Single MinIO node** (simplest — start here):
```
Home server: Vula + MinIO (storage node)
Office:      Vula (syncs to home MinIO)
Laptop:      Vula (syncs to home MinIO)
```

**Two MinIO nodes** (redundant — recommended):
```
Home server: Vula + MinIO ◄──► Office: Vula + MinIO
                                 (MinIO replication between both)
Laptop:      Vula (syncs to nearest MinIO)
```
MinIO site replication keeps both storage nodes in sync. If home goes down, office MinIO has everything.

**External S3** (no self-hosted storage):
```
All nodes sync directly to AWS S3 / Backblaze B2 / any S3-compatible provider
No MinIO needed — just set S3_ENDPOINT to the provider
```

### Settings UI

Storage nodes see:
```
Storage Node
├── Enabled: [toggle]
├── Allocated: 50 GB / 500 GB [slider]
├── Peers:
│   ├── home-server  (online, 120 GB used)
│   └── office-nuc   (online, 80 GB used)
├── Redundancy: 2 copies
└── Status: Healthy
```

Non-storage nodes see:
```
Storage
├── Sync: enabled
├── Connected to: home-server (MinIO)
├── Last sync: 12s ago
└── Local cache: 8 GB
```

---

## State Sync Layer

### The Problem

Multiple nodes read and write the same data. Nodes may be on different networks. The same user may be active on two nodes simultaneously. No node should be required to be online for others to work.

### Strategy Per Data Type

Different data needs different conflict resolution:

| Data | Storage | Sync Method | Conflict Strategy |
|------|---------|-------------|-------------------|
| **Auth DB** (users, sessions, profiles) | SQLite | cr-sqlite → S3 | CRDT auto-merge |
| **Settings / preferences** | SQLite | cr-sqlite → S3 | Last-write-wins per field |
| **App install list** | SQLite | cr-sqlite → S3 | Last-write-wins (union) |
| **User files** (documents, downloads) | Local disk | rclone bisync → S3 | Conflict copies |
| **Recall / embeddings** | Local | Rebuilt from synced files | Regenerated per node |
| **App runtime state** | Memory | Not synced | Ephemeral, per-node |
| **Chat history** | SQLite | cr-sqlite → S3 | CRDT merge (append-only) |
| **Browser profiles** | Local disk | rclone → S3 | Conflict copies |

### Why These Choices

**cr-sqlite (CRDT for SQLite)** — for structured data (auth, settings, app lists, chat). Each node has a local SQLite database. cr-sqlite tracks changes as conflict-free replicated data types. When two nodes sync, changes merge automatically without conflicts. This is how Apple Notes and Figma handle multi-device sync.

**Conflict copies** — for user files. If the same file is edited on two nodes before sync, the system keeps both versions:
```
notes.txt                                  ← latest version (winner)
notes.conflict-home-20260401-143022.txt    ← other version (preserved)
```
No data is ever lost. The user resolves manually if needed.

**Last-write-wins** — for settings and preferences. If you change your theme on two devices, the latest change wins. This matches user expectation — "I just changed this, it should be what I set."

**Not synced** — app processes and runtime state. Apps run locally on whichever node the user is on. If a node dies, apps restart fresh on the new node with their data intact (synced through S3).

---

## Concurrent Sessions — Same User, Multiple Nodes

This is the core challenge: a user is logged in at home (bare metal) AND at office (bare metal or remote) at the same time.

### What Doesn't Conflict (90% of usage)

- Opening different apps on different nodes — independent processes, no shared state
- Reading files — reads never conflict
- Installing apps — install list merges via CRDT (union)
- Changing different settings — cr-sqlite merges different fields cleanly

### What Could Conflict

**Same file edited on two nodes:**

1. **Presence hints** (prevention layer):
   - When a user opens a file for editing, the node writes a lightweight lease to S3:
     ```json
     {"node": "home", "user": "alice", "file": "notes.txt", "expires": "2026-04-01T14:35:00Z"}
     ```
   - Other nodes check before opening — see the hint and show:
     *"This file is open on Home — open read-only or edit anyway?"*
   - Leases expire automatically (60s heartbeat). No stale locks if a node crashes.
   - **This is advisory, not blocking.** The user can always override.

2. **Conflict copies** (safety net):
   - If both nodes edit anyway, sync detects the divergence (version vectors)
   - Both versions are kept, user is notified
   - A notification appears: *"notes.txt was edited on both Home and Office — review conflict"*

**Same DB field written on two nodes:**
- cr-sqlite handles this automatically
- For settings: last-write-wins per column (latest timestamp)
- For sessions: both sessions stay valid (different tokens, both merge into the table)
- For users: field-level merge (name changed on one node, email on another — both apply)

### Multiple Remote Sessions (Same User, Different Server Nodes)

Same user, two browser tabs hitting two different server nodes via tunnel load balancing. Handled identically:

- Sessions are synced via cr-sqlite — valid on all nodes
- Sticky sessions keep a tab on one node normally
- If that node dies, tunnel routes to another — session is valid there too because it's synced

### No Distributed Locks

Distributed locks across the internet are fragile, slow, and create single points of failure. If the lock holder dies, everyone waits. If the network partitions, deadlock.

Instead: **local-first writes + async sync + conflict resolution.** Every write succeeds locally and immediately. Conflicts are resolved during sync, not prevented with locks.

---

## Implementation Plan

### Phase 1: Foundation — SQLite Migration

Replace JSON file stores with SQLite + cr-sqlite.

**Current JSON stores to migrate:**
- `~/.vulos/db/auth.json` → `auth` table (users, sessions, profiles)
- `~/.vulos/db/recall.json` → `recall` table (embeddings, indexed files)
- Chat history (in-memory) → `chat` table
- App install state → `apps` table

**What changes in code:**

1. New package `backend/services/store/` — SQLite wrapper with cr-sqlite extension
2. `auth.Store` switches from JSON read/write to SQLite queries
3. `Flush()` becomes a no-op (SQLite handles persistence)
4. Other services that persist JSON follow the same pattern

**Schema example:**
```sql
-- cr-sqlite enabled: each table gets CRDT tracking
SELECT crsql_as_crr('users');
SELECT crsql_as_crr('sessions');
SELECT crsql_as_crr('profiles');
SELECT crsql_as_crr('settings');
SELECT crsql_as_crr('installed_apps');

CREATE TABLE users (
    id          TEXT PRIMARY KEY,
    username    TEXT UNIQUE NOT NULL,
    password_hash TEXT,
    email       TEXT,
    name        TEXT,
    picture     TEXT,
    providers   TEXT,  -- JSON
    created_at  TEXT,
    last_login  TEXT
);

CREATE TABLE sessions (
    token       TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    email       TEXT,
    name        TEXT,
    device_id   TEXT,
    expires_at  TEXT,
    created_at  TEXT
);

CREATE TABLE profiles (
    user_id     TEXT PRIMARY KEY,
    display_name TEXT,
    role        TEXT DEFAULT 'user',
    theme       TEXT DEFAULT 'dark',
    avatar      TEXT,
    timezone    TEXT,
    preferences TEXT  -- JSON
);
```

### Phase 2: S3 Sync Layer

**Database sync (cr-sqlite → S3):**

cr-sqlite exposes a changes table (`crsql_changes`) that captures every mutation as a CRDT changeset. The sync loop:

```
Every 5 seconds (or on write, debounced):
  1. SELECT changes FROM crsql_changes WHERE db_version > last_pushed_version
  2. Serialize changesets → upload to S3 as: nodes/{node_id}/changes/{version}.bin
  3. Update last_pushed_version

On pull (every 10 seconds, or on boot):
  1. List S3: nodes/*/changes/ — find changesets newer than last_pulled_version per peer
  2. Download new changesets
  3. Apply via crsql_changes INSERT (cr-sqlite merges automatically)
```

**File sync (rclone bisync → S3):**

```
New package: backend/services/sync/

Watches: ~/.vulos/data/, ~/.vulos/db/browser-profiles/
Ignores: ~/.vulos/apps/bin/ (reinstalled from registry, not synced)

On file change (inotify/fsnotify):
  1. Check presence leases for the file
  2. Upload to S3: files/{relative_path}
  3. Write version metadata: files/{relative_path}.meta (node_id, timestamp, hash)

On pull:
  1. List S3 files changed since last pull
  2. For each file:
     - If local is unchanged → download and overwrite
     - If local was also changed → create conflict copy, notify user
  3. Update last_pull_timestamp
```

**Presence leases:**

```
S3 key: leases/{user_id}/{file_path_hash}
Value:  {"node": "home", "user": "alice", "file": "notes.txt", "heartbeat": "2026-04-01T14:30:00Z"}
TTL:    Managed by heartbeat — if heartbeat > 60s old, lease is stale

On file open for edit:
  1. Check S3 for existing lease
  2. If exists and fresh → warn user, offer read-only or override
  3. Write/renew own lease
  4. Heartbeat every 30s while file is open
  5. Delete lease on file close
```

### Phase 3: Node Configuration

**New config fields:**
```env
# Node identity
VULOS_NODE_ID=home-server          # unique per node (defaults to hostname)
VULOS_MODE=server                  # "server" (headless + tunnel) or "local" (physical display)

# Storage
VULOS_S3_ENDPOINT=http://home:9000 # MinIO or external S3
VULOS_S3_BUCKET=vulos-cluster
VULOS_S3_ACCESS_KEY=...
VULOS_S3_SECRET_KEY=...
VULOS_SYNC_ENABLED=true
VULOS_SYNC_INTERVAL=5              # seconds between sync pushes

# Tunnel (server mode only)
VULOS_TUNNEL=true
VULOS_TUNNEL_TOKEN=<tunnel-api-key>
VULOS_DOMAIN=my-vulos.example.com
```

**New package `backend/services/cluster/`:**

```go
type Node struct {
    ID       string
    Mode     string // "server" or "local"
    Hostname string
    LastSeen time.Time
    Storage  bool   // running MinIO
}

type Cluster struct {
    nodeID    string
    s3client  *minio.Client
    db        *sql.DB       // local cr-sqlite
    syncLoop  *SyncLoop
    fileSync  *FileSync
    presence  *PresenceManager
}

// Register announces this node to the cluster via S3
func (c *Cluster) Register() error

// Peers returns known nodes from S3 metadata
func (c *Cluster) Peers() ([]Node, error)

// Health returns node health for tunnel health checks
func (c *Cluster) Health() map[string]any
```

### Phase 4: MinIO App Integration

1. Add MinIO to `registry.json` (see entry above)
2. Storage settings UI in `src/core/Settings.jsx`:
   - Toggle storage node on/off
   - Disk allocation slider
   - Peer list with status
   - Redundancy level selector
3. On MinIO enable: configure as S3 endpoint for this node and peers
4. Optional: MinIO site replication between two+ storage nodes for redundancy

### Phase 5: Tunnel Integration

Two options for remote access, chosen during init:

**Option A: Vula Tunnel (default — recommended)**

User registers on the Vula platform during init. The platform creates a Cloudflare Tunnel via API, gives the node a tunnel token, and assigns a subdomain (e.g., `alice.vula.dev`). The node runs `cloudflared` with that token.

> **TODO:** The tunnel platform is a separate project — lives in `landing/tunnel/` and will become its own repo. See "Tunnel Platform" section below for full scope.

During init, the OS only needs to:
1. Register/login to the Vula platform → get account
2. Platform creates a Cloudflare Tunnel → returns tunnel token + assigned URL
3. Install `cloudflared` on the node
4. Run `cloudflared tunnel run --token <token>`

```go
// backend/services/tunnel/cloudflare.go
func StartTunnel(token string) error {
    // Download cloudflared if not present
    // Run: cloudflared tunnel --no-autoupdate run --token <token>
    // Monitor process, reconnect on failure
}
```

**Option B: Direct Domain (manual — advanced)**

User brings their own domain, configures DNS and Caddy manually. This is the existing `build.sh --domain` flow.

```env
VULOS_DOMAIN=my.example.com
VULOS_TUNNEL_MODE=direct     # "vula" (default) or "direct"
# User configures Caddy + DNS externally (Namecheap, etc.)
```

**Health endpoint (both options):**
```go
// GET /api/health — platform or external LB checks this
func (c *Cluster) HealthHandler(w http.ResponseWriter, r *http.Request) {
    // Returns 200 if node is healthy, 503 if degraded
    // Checks: DB writable, disk space, sync lag
}
```

---

## Redundancy Model

```
Node A (home)        Node B (office)       Node C (laptop)
 local SSD            local SSD             local SSD
 full copy            full copy             full copy
    │                    │                     │
    └────────────┬───────┘─────────────────────┘
                 ▼
            S3 / MinIO
          (central sync copy)
```

**Every node has a full copy of all data.** S3 is the sync hub AND an additional redundancy copy. If any single node dies, its data exists on S3 and on every other node. If S3 goes down, every node keeps working with local data. If all nodes die except one, full recovery from that one node.

This gives **N+1 redundancy** where N is the number of nodes. S3/MinIO is the +1.

**No single point of failure:**
- No primary node
- No required coordinator
- No centralized database
- S3 going down = sync pauses, everything else continues
- Node going down = other nodes unaffected, tunnel reroutes

---

## Sync Conflict Resolution Summary

```
 Write event
      │
      ▼
 Write locally (always succeeds, never blocked)
      │
      ▼
 Push to S3 (async, debounced 5s)
      │
      ▼
 Other nodes pull
      │
      ├─ Database record? ──► cr-sqlite CRDT auto-merge
      │                       (field-level, no conflicts possible)
      │
      ├─ File, only one ───► Update local copy
      │  node edited?         (simple overwrite)
      │
      ├─ File, both ────────► Conflict copy created
      │  nodes edited?        user notified via toast
      │
      └─ Setting ───────────► Last-write-wins per key
                              (latest timestamp)
```

**Presence hints reduce conflicts but don't prevent them.** The safety net (conflict copies for files, CRDTs for DB) ensures no data loss regardless.

---

## App Registry Sync Across Nodes

Apps don't sync as binaries — each node installs natively from the registry. What syncs is the **intent** (which apps should be installed), and each node fulfills that intent using its own package manager and architecture.

### What Syncs: The Installed Apps List

The cr-sqlite database has an `installed_apps` table that replicates across all nodes:

```sql
CREATE TABLE installed_apps (
    app_id       TEXT PRIMARY KEY,
    version      TEXT NOT NULL,
    installed_by TEXT,           -- node that first installed it
    installed_at TEXT,
    status       TEXT DEFAULT 'active'  -- 'active', 'removed', 'pending'
);
SELECT crsql_as_crr('installed_apps');
```

When you install GIMP on home, this row syncs to all nodes via cr-sqlite. When laptop pulls that changeset, it sees a new app in `installed_apps` that isn't locally installed yet.

### The Reconciliation Loop

Each node runs a reconciler on boot and after every sync pull:

```go
func (c *Cluster) ReconcileApps() {
    // 1. Get desired state from synced DB
    desired := db.Query("SELECT app_id, version FROM installed_apps WHERE status = 'active'")

    // 2. Get actual state from local filesystem
    actual := appStore.Installed()  // scans ~/.vulos/apps/*/app.json

    // 3. Diff
    toInstall := desired - actual   // in DB but not on disk
    toRemove  := actual - desired   // on disk but removed from DB

    // 4. Act
    for _, app := range toInstall {
        go appStore.InstallFromRegistry(ctx, app.ID, app.Version)
    }
    for _, app := range toRemove {
        appStore.Uninstall(app.ID)
    }
}
```

This uses the existing `InstallFromRegistry()` which runs the install recipe from `registry.json` — `apt-get install`, `flatpak install`, or download binary. Each node installs natively for its own architecture and package manager state.

### Install Progress Tracking

Since installs take time (especially Flatpak), each node tracks its own install state locally:

```sql
-- Local only, NOT synced via cr-sqlite
CREATE TABLE local_app_status (
    app_id     TEXT PRIMARY KEY,
    state      TEXT,   -- 'installing', 'installed', 'failed', 'removing'
    progress   TEXT,   -- "downloading 45%", "unpacking", etc.
    error      TEXT,
    updated_at TEXT
);
```

The sync screen and Settings UI read from this to show per-app progress:

```
App installs
├── GIMP           ✓ Installed
├── Blender        ↓ Installing (apt: unpacking...)
├── LibreOffice    ◻ Queued
└── KiCad          ✗ Failed (retrying in 30s)
```

### What About App Data?

App data (`~/.vulos/data/{appID}/`) syncs via the file sync layer (rclone), separate from the install:

1. **Install recipe** syncs via DB → each node installs natively
2. **App data** (documents, saves, config) syncs via S3 file sync
3. **App binaries** never sync — each node has its own native install

### Uninstall Sync

User uninstalls GIMP on home → `installed_apps` row gets `status = 'removed'` → syncs to all nodes → reconciler on each node runs `Uninstall()` → apt/flatpak removal happens natively on each.

### Failure Handling

- **Install fails** (broken package, missing dep) → `local_app_status.state = 'failed'`, retry with exponential backoff (30s, 1m, 5m, 15m), then stop and surface in UI
- **App not in registry** (old version, removed app) → mark as `status = 'skipped'` locally, don't block other installs
- **Arch mismatch** (app only available for x86, node is arm64) → check registry recipe for arch support before attempting, mark `skipped` with reason
- **Disk full** → pause install queue, notify user, resume when space freed

### Full Flow: Install on One Node, Appears on All

```
Home (x86)                          Laptop (arm64)
───────────                         ──────────────
User installs GIMP
    │
    ▼
installed_apps: {gimp, "1.0", active}
    │
    ▼ cr-sqlite sync via S3
    │
    ├──────────────────────────────────► Reconciler sees new app
    │                                        │
    │                                        ▼
    │                                   registry.json lookup
    │                                        │
    │                                        ▼
    │                                   apt-get install gimp
    │                                   (arm64 native package)
    │                                        │
    │                                        ▼
    │                                   local_app_status: installed
    │
    ▼ file sync via S3
    │
    ├──────────────────────────────────► ~/.vulos/data/gimp/
                                        (user's GIMP configs, recent files)
```

---

## First Boot: New Instance or Join Cluster

On first boot (no `~/.vulos/db/` exists), the setup wizard presents a choice before anything else. This replaces the current `welcome` step.

### Setup Flow

```
┌─────────────────────────────────────────────┐
│                                             │
│              Welcome to Vula                │
│                                             │
│   ┌─────────────────┐ ┌──────────────────┐  │
│   │                 │ │                  │  │
│   │   New System    │ │  Join Existing   │  │
│   │                 │ │                  │  │
│   │  Set up a fresh │ │ Connect to your  │  │
│   │  Vula instance  │ │ existing cluster │  │
│   │                 │ │                  │  │
│   └─────────────────┘ └──────────────────┘  │
│                                             │
└─────────────────────────────────────────────┘
```

### Path A: New System

```
Welcome → New/Join → Language → Timezone → Network → Account → PIN → Storage → Tunnel → SSH → Recovery Kit → Appearance → Ready
```

The existing steps are unchanged. Three new steps are added after PIN:

**Step: Storage (MinIO)**

Recommend self-hosting MinIO on this node. This is the foundation for multi-node sync later.

```
┌─────────────────────────────────────────────┐
│                                             │
│           Set up storage                    │
│                                             │
│   Vula can self-host storage so your        │
│   files, apps, and settings sync across     │
│   all your devices automatically.           │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  [✓] Enable Storage (recommended)  │   │
│   │                                     │   │
│   │  Allocate: [====|------] 50 GB      │   │
│   │                                     │   │
│   │  Storage password                   │   │
│   │  ┌─────────────────────────────┐    │   │
│   │  │ ••••••••••••                │    │   │
│   │  └─────────────────────────────┘    │   │
│   │                                     │   │
│   │  Encryption passphrase              │   │
│   │  (encrypts all data before storing) │   │
│   │  ┌─────────────────────────────┐    │   │
│   │  │ ••••••••••••                │    │   │
│   │  └─────────────────────────────┘    │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Recommended for:                          │
│   • Home servers, always-on machines        │
│   • Any device with spare storage           │
│                                             │
│   Skip for:                                 │
│   • Laptops that won't always be on         │
│                                             │
│              [ Next ]  [ Skip ]             │
│                                             │
└─────────────────────────────────────────────┘
```

If enabled, the backend:
1. Installs MinIO from registry (`InstallFromRegistry("minio", "1.0")`)
2. Generates S3 access key + secret key
3. Creates the `vulos-cluster` bucket
4. Configures SSE-C encryption with the user's passphrase
5. Starts MinIO, sets this node as its own S3 endpoint

If skipped, no storage is configured. User can enable later in Settings.

**Step: SSH (Emergency Access)**

Generate an SSH key pair for emergency access.

```
┌─────────────────────────────────────────────┐
│                                             │
│           Emergency access                  │
│                                             │
│   An SSH key lets you access this machine   │
│   directly if the web interface is down.    │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  Generating SSH key...   ✓ Done     │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Your SSH private key:                     │
│   ┌─────────────────────────────────────┐   │
│   │ -----BEGIN OPENSSH PRIVATE KEY----- │   │
│   │ b3BlbnNzaC1rZXktdjEAAAAABG5vbmUA... │   │
│   │ ...                                 │   │
│   │ -----END OPENSSH PRIVATE KEY-----   │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Save this key somewhere safe.             │
│   You will NOT be able to see it again.     │
│                                             │
│   Usage:                                    │
│   ssh -i vula-ssh.key root@<this-ip>        │
│                                             │
│              [ Next ]                       │
│                                             │
└─────────────────────────────────────────────┘
```

The backend:
1. Generates Ed25519 key pair (`ssh-keygen -t ed25519`)
2. Installs public key to `/root/.ssh/authorized_keys`
3. Configures sshd for key-only auth (no password login)
4. Starts `sshd` service
5. Returns private key to the frontend (shown once, never stored on server)

**Step: Recovery Kit**

Shows ALL keys the user needs to save. Does not proceed until they confirm.

```
┌─────────────────────────────────────────────┐
│                                             │
│           Your recovery kit                 │
│                                             │
│   Save these credentials. You need them     │
│   to recover your system or add new nodes.  │
│                                             │
│   SSH Private Key                           │
│   ┌─────────────────────────────────────┐   │
│   │ -----BEGIN OPENSSH PRIVATE KEY----- │   │
│   │ b3BlbnNzaC1rZXktdjEAAAAABG5vbmUA... │   │
│   │ -----END OPENSSH PRIVATE KEY-----   │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Storage Access Key                        │
│   ┌─────────────────────────────────────┐   │
│   │ vula-ak-7f3a9b2c1d4e               │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Storage Secret Key                        │
│   ┌─────────────────────────────────────┐   │
│   │ vula-sk-8e2f1a9c7b3d6e4f0a5b...    │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Storage Endpoint                          │
│   ┌─────────────────────────────────────┐   │
│   │ http://192.168.1.50:9000            │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Tunnel API Key                            │
│   ┌─────────────────────────────────────┐   │
│   │ vtk_7f3a9b2c1d4e8f0a5b3c7d9e...    │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Tunnel URL                                │
│   ┌─────────────────────────────────────┐   │
│   │ https://alice.vula.dev              │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Encryption Passphrase                     │
│   ┌─────────────────────────────────────┐   │
│   │ (the passphrase you set above)      │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  [ Download JSON ]  [ QR Code ]    │   │
│   │  [ Email to myself ]               │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Type "confirm" to continue:               │
│   ┌─────────────────────────────────────┐   │
│   │                                     │   │
│   └─────────────────────────────────────┘   │
│                                             │
│              [ Next ]  (disabled until       │
│                         "confirm" typed)     │
│                                             │
└─────────────────────────────────────────────┘
```

### Recovery Kit Format

**JSON file** (`vula-recovery-kit.json`):

```json
{
  "version": 1,
  "created_at": "2026-04-01T10:00:00Z",
  "node_id": "home-server",
  "ssh": {
    "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn...\n-----END OPENSSH PRIVATE KEY-----",
    "public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5... vula@home-server",
    "port": 22
  },
  "storage": {
    "endpoint": "http://192.168.1.50:9000",
    "bucket": "vulos-cluster",
    "access_key": "vula-ak-7f3a9b2c1d4e",
    "secret_key": "vula-sk-8e2f1a9c7b3d6e4f0a5b..."
  },
  "tunnel": {
    "provider": "vula",
    "url": "https://alice.vula.dev",
    "api_key": "vtk_7f3a9b2c1d4e8f0a5b3c7d9e..."
  },
  "encryption": {
    "method": "AES-256-GCM",
    "passphrase_hint": "Set during setup — not stored here"
  }
}
```

**QR Code:**

QR version 40 holds ~2,953 bytes. The recovery kit JSON (with Ed25519 key, which is only ~400 bytes) fits comfortably:
- Ed25519 private key: ~400 bytes
- S3 credentials: ~200 bytes
- Metadata: ~150 bytes
- Total: ~750 bytes — well within QR capacity

The QR is generated as a data URL and displayed inline. User scans with phone camera to save.

If the kit exceeds QR capacity (e.g. RSA keys — we won't use these, but as a fallback), show a warning and disable QR, keeping the JSON download and email options.

**Email to myself** (requires Vula Platform account):

```
┌─────────────────────────────────────────────┐
│                                             │
│   Email recovery kit to:                    │
│   ┌─────────────────────────────────────┐   │
│   │ alice@example.com                   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│           [ Send ]                          │
│                                             │
│   Sent via Vula Platform. The email         │
│   contains your recovery kit as an          │
│   attachment. The encryption passphrase     │
│   is NOT included for security.             │
│                                             │
└─────────────────────────────────────────────┘
```

The email:
- OS sends kit to Vula Platform API: `POST /api/recovery/email`
- Platform sends email via Resend (email infrastructure is platform-side, not OS-side)
- Attaches `vula-recovery-kit.json`
- **Does NOT include the encryption passphrase** — user must remember this separately
- If user chose "Skip" for tunnel (no platform account), the "Email to myself" option is hidden — only JSON download and QR code are available

### Confirmation Gate

The "Next" button is disabled until the user types `confirm` in the text field. This is intentionally friction — the recovery kit contains everything needed to access their system. Losing it means losing emergency access.

The frontend validates:
```js
const canProceed = confirmText.toLowerCase().trim() === 'confirm'
```

No bypass, no skip. If storage was skipped (no MinIO), the recovery kit only contains the SSH key and the confirmation step is simpler but still required.

### Path B: Join Existing Cluster

```
Welcome → New/Join → Connect Storage → Syncing... → PIN → Ready
```

**Step 1: Connect Storage**

User enters S3/MinIO credentials to reach the cluster:

```
┌─────────────────────────────────────────────┐
│                                             │
│          Connect to your cluster            │
│                                             │
│   Storage endpoint                          │
│   ┌─────────────────────────────────────┐   │
│   │ http://home-server:9000             │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Bucket                                    │
│   ┌─────────────────────────────────────┐   │
│   │ vulos-cluster                       │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Access Key          Secret Key            │
│   ┌────────────────┐  ┌────────────────┐    │
│   │ ••••••••••••   │  │ ••••••••••••   │    │
│   └────────────────┘  └────────────────┘    │
│                                             │
│   ── or scan a join code from ──            │
│      another Vula node                      │
│                                             │
│          [ QR / Code ]                      │
│                                             │
│              [ Connect ]                    │
│                                             │
└─────────────────────────────────────────────┘
```

**Join codes:** An existing node can generate a join code (Settings → Cluster → "Add Node") that encodes the S3 endpoint, bucket, and a time-limited access token as a QR code or short alphanumeric code. This avoids the user needing to type S3 credentials manually.

On "Connect", the backend:
1. Validates S3 credentials (list bucket)
2. Checks for existing cluster data (looks for `nodes/` prefix in bucket)
3. If valid → proceeds to sync screen

**Step 2: Syncing Screen**

This is a dedicated sync UI that shows real-time progress. The user sees what's being pulled and can watch it happen. This screen is **reentrant** — if the node reboots mid-sync or sync is interrupted, it returns to this screen on next boot until sync is complete.

```
┌─────────────────────────────────────────────┐
│                                             │
│            Syncing your system              │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │          ████████████░░░░  74%      │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Database         ✓ Complete               │
│   Users & profiles ✓ Complete               │
│   Settings         ✓ Complete               │
│   User files       ↓ 312 / 847 files        │
│   App registry     ◻ Waiting                │
│   Install apps     ◻ Waiting                │
│                                             │
│   Currently: documents/project/report.pdf   │
│   Speed: 12.4 MB/s                          │
│   Remaining: ~3 min                         │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │ ▸ home-server  (source, online)     │   │
│   │ ▸ office-nuc   (online)             │   │
│   │ ★ this-node    (joining)            │   │
│   └─────────────────────────────────────┘   │
│                                             │
│          [ Continue in background ]         │
│                                             │
└─────────────────────────────────────────────┘
```

**Sync order (by priority):**

1. **Database** (cr-sqlite changesets) — users, sessions, profiles, settings. Small, fast. The node becomes usable for login after this step.
2. **User files** — documents, downloads, browser profiles. Largest payload, streamed in parallel.
3. **App registry** — pull the installed apps list from the cluster state.
4. **Install apps** — for each app in the synced install list, run the registry install recipe. This is the slowest step — apt/flatpak installs take time.

**"Continue in background"** — after the database is synced (step 1), the user can dismiss the sync screen and start using the system immediately. Files and apps continue syncing in the background with a subtle status indicator in the taskbar. The sync screen is accessible from Settings → Cluster → Sync Status at any time.

**Step 3: PIN + Ready**

After sync completes (or user clicks "Continue in background"), the node skips language/timezone/account/appearance (all synced from cluster) and goes straight to:

- **Set a device PIN** — local unlock PIN for this specific device (not synced, per-node security)
- **Ready** — node is live, registered in the cluster

### Reentrant Sync

The sync screen is not a one-time wizard step — it's a system state. The backend tracks sync progress in a local file (`~/.vulos/db/sync-state.json`):

```json
{
  "status": "syncing",
  "started_at": "2026-04-01T10:00:00Z",
  "phases": {
    "database": { "status": "complete", "finished_at": "..." },
    "files": { "status": "in_progress", "done": 312, "total": 847 },
    "registry": { "status": "pending" },
    "apps": { "status": "pending" }
  },
  "source_node": "home-server",
  "s3_endpoint": "http://home-server:9000",
  "s3_bucket": "vulos-cluster"
}
```

On every boot, the init process checks:
1. No `~/.vulos/db/` → show setup wizard (New/Join choice)
2. `sync-state.json` exists with `status: "syncing"` → resume sync screen
3. `sync-state.json` exists with `status: "complete"` → normal boot
4. No `sync-state.json` but DB exists → standalone node, normal boot

This means:
- Power loss during sync → reboots into sync screen, resumes where it left off
- Network drop during sync → sync screen shows "Reconnecting...", retries automatically
- User can always return to Settings → Cluster → Sync Status to see progress

### Join Code Generation

Existing nodes can generate join codes from Settings → Cluster → "Add a node":

```go
type JoinCode struct {
    Endpoint  string `json:"e"`
    Bucket    string `json:"b"`
    AccessKey string `json:"ak"`
    SecretKey string `json:"sk"`
    ExpiresAt int64  `json:"x"`  // unix timestamp, 1 hour TTL
    ClusterID string `json:"c"`  // to verify correct cluster
}
```

Encoded as:
- **QR code** — scan with phone camera or another Vula node's camera
- **Short code** — base32 alphanumeric, e.g. `VULA-K7MX-P2NR-9FTD` (typed manually)

The join code creates a temporary MinIO service account with limited permissions (read-only for initial sync, full access once node is registered). The generating node revokes the temporary credentials after the joining node registers itself with its own permanent credentials.

### Backend Init Flow

```go
// In backend/cmd/server/main.go startup sequence

func boot() {
    dbDir := filepath.Join(homeDir, ".vulos", "db")

    if !exists(dbDir) {
        // Fresh install — API serves setup wizard only
        // POST /api/setup/new → run normal setup flow
        // POST /api/setup/join → validate S3, start sync
        return startSetupMode()
    }

    if state := loadSyncState(dbDir); state != nil && state.Status == "syncing" {
        // Interrupted sync — resume
        // API serves sync status UI + continues sync in background
        return startSyncResumeMode(state)
    }

    // Normal boot
    return startNormal()
}
```

### API Endpoints

```
POST /api/setup/new              → proceed with fresh setup wizard
POST /api/setup/join             → { endpoint, bucket, access_key, secret_key }
                                    validates credentials, starts sync
POST /api/setup/join-code        → { code: "VULA-K7MX-..." }
                                    decodes join code, starts sync
GET  /api/setup/sync-status      → current sync progress (phases, counts, speed)
POST /api/setup/sync-background  → dismiss sync screen, continue in background
GET  /api/cluster/join-code      → generate join code (from existing node)
```

---

## SSH & Encryption

### SSH Server Setup

SSH must be available for emergency access on every node. Added to both Dockerfile and build.sh.

**Dockerfile addition** (in the apt-get install line):
```dockerfile
openssh-server \
```

**build.sh addition** (in the first-time setup package list):
```bash
openssh-server \
```

**sshd configuration** (`/etc/ssh/sshd_config.d/vulos.conf`):
```
# Key-only auth — no passwords
PasswordAuthentication no
ChallengeResponseAuthentication no
UsePAM no

# Root login only via key
PermitRootLogin prohibit-password

# Hardening
X11Forwarding no
MaxAuthTries 3
LoginGraceTime 30

# Keep alive (detect dead connections)
ClientAliveInterval 60
ClientAliveCountMax 3
```

**Init process** (in `vulos-init` or systemd service):
```go
// Generate host keys if missing (first boot)
if _, err := os.Stat("/etc/ssh/ssh_host_ed25519_key"); os.IsNotExist(err) {
    exec.Command("ssh-keygen", "-A").Run()  // generates all host key types
}

// Start sshd
exec.Command("/usr/sbin/sshd", "-D").Start()
```

**Docker-specific:** Expose port 22 in Dockerfile and document the mapping:
```dockerfile
EXPOSE 8080 22
```
```bash
docker run -p 8080:8080 -p 2222:22 --shm-size=1g vulos
```

### Encryption at Rest

**S3/MinIO encryption (SSE-C — client-side keys):**

All data is encrypted before leaving the node. MinIO never sees plaintext.

```go
// Derive encryption key from user's passphrase
func deriveEncryptionKey(passphrase string, salt []byte) []byte {
    // Argon2id: memory-hard, resistant to GPU/ASIC attacks
    return argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)
}

// Every S3 PUT includes the encryption key header
func putEncrypted(client *minio.Client, bucket, key string, data io.Reader, encKey []byte) error {
    sse, _ := encrypt.NewSSEC(encKey)
    _, err := client.PutObject(ctx, bucket, key, data, -1, minio.PutObjectOptions{
        ServerSideEncryption: sse,
    })
    return err
}
```

- Passphrase is set during init (Storage step)
- Key is derived via Argon2id and held in memory while the node runs
- All nodes in the cluster must use the same passphrase (entered during join)
- If the passphrase is lost, S3 data is unrecoverable — this is by design
- The passphrase itself is never stored on disk or in S3

**Encryption salt** is stored in S3 at `cluster/encryption-salt` (unencrypted, public metadata). This is safe — salt doesn't need to be secret, it just prevents rainbow table attacks.

---

## Tunnel & Remote Access

During init, the user chooses how this node is accessed remotely. Three options plus TURN configuration:

### Option A: Vula Platform (default — recommended)

Our managed platform. Handles tunnel provisioning, TURN relay, subdomain assignment, and usage monitoring. User registers during init, gets a URL and TURN credentials automatically. Uses Cloudflare Tunnels under the hood. We own `*.vula.dev` with wildcard DNS + wildcard TLS on Cloudflare (free).

**What's included:**
- Cloudflare Tunnel (assigned subdomain like `alice.vula.dev`)
- Managed coturn TURN server (for WebRTC relay when direct connection fails)
- Recovery kit cloud backup
- Usage dashboard

```
*.vula.dev (wildcard DNS + TLS on Cloudflare, free)
         │
         ▼
   Cloudflare Edge (routes by subdomain)
         │
         ├── bob.vula.dev    → bob's cloudflared    → bob's home server
         ├── alice.vula.dev  → alice's cloudflared  → alice's office NUC
         └── unknown         → 404 / signup page

   Vula TURN server (turn.vula.dev)
         │
         ├── WebRTC relay for bob's app streaming
         └── WebRTC relay for alice's app streaming
```

No traffic proxied through our application servers — Cloudflare connects directly to the user's node. TURN server only relays WebRTC media when peer-to-peer fails (symmetric NAT, corporate firewalls, etc.).

**Init flow:**

```
┌─────────────────────────────────────────────┐
│                                             │
│           Remote access                     │
│                                             │
│   Access your system from anywhere.         │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  (*) Vula Platform (recommended)    │   │
│   │      Free URL + TURN relay          │   │
│   │                                     │   │
│   │  ( ) Bring Your Own (advanced)      │   │
│   │      Own Cloudflare + own TURN      │   │
│   │                                     │   │
│   │  ( ) Direct Domain (advanced)       │   │
│   │      Own domain + Caddy + TURN      │   │
│   │                                     │   │
│   │  ( ) Skip — local only              │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   ── Vula Platform ──                       │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  [ Create account ]  [ I have one ] │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Email                                     │
│   ┌─────────────────────────────────────┐   │
│   │ alice@example.com                   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Password                                  │
│   ┌─────────────────────────────────────┐   │
│   │ ••••••••••••                        │   │
│   └─────────────────────────────────────┘   │
│                                             │
│              [ Connect ]                    │
│                                             │
└─────────────────────────────────────────────┘
```

After registration/login, the platform provisions everything:

```
┌─────────────────────────────────────────────┐
│                                             │
│           Remote access                     │
│                                             │
│   ✓ Connected to Vula Platform              │
│                                             │
│   Your URL:                                 │
│   ┌─────────────────────────────────────┐   │
│   │ https://alice.vula.dev              │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Tunnel Token:                             │
│   ┌─────────────────────────────────────┐   │
│   │ eyJhIjoiNzk2MjQ2ZDkwMzYxYTk4...    │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   TURN Server:  turn.vula.dev:3478          │
│   TURN credentials are managed              │
│   automatically by the platform.            │
│                                             │
│   Save this token — it's added to your      │
│   recovery kit automatically.               │
│                                             │
│              [ Next ]                       │
│                                             │
└─────────────────────────────────────────────┘
```

Recovery kit entry:
```json
{
  "tunnel": {
    "provider": "vula",
    "url": "https://alice.vula.dev",
    "tunnel_token": "eyJhIjoiNzk2MjQ2ZDkwMzYxYTk4..."
  },
  "turn": {
    "provider": "vula",
    "server": "turn:turn.vula.dev:3478",
    "note": "Credentials managed by platform — rotate automatically"
  }
}
```

**What happens under the hood:**
1. User registers on platform → platform creates Cloudflare Tunnel via Cloudflare API
2. Platform configures tunnel route: `alice.vula.dev` → tunnel UUID → user's `cloudflared`
3. Platform generates time-limited TURN credentials (HMAC-based, same as existing `network/turn.go`)
4. Platform returns tunnel token + TURN config to the node
5. Node installs `cloudflared` and runs `cloudflared tunnel --no-autoupdate run --token <token>`
6. Node configures WebRTC to use platform TURN server
7. `alice.vula.dev` is live immediately, app streaming works through TURN if needed

### Option B: Bring Your Own Cloudflare + TURN (advanced)

User has their own Cloudflare account and wants to manage their own tunnel. Optionally brings their own coturn server too.

```
┌─────────────────────────────────────────────┐
│                                             │
│           Bring your own                    │
│                                             │
│   ── Cloudflare Tunnel ──                   │
│                                             │
│   Tunnel Token                              │
│   ┌─────────────────────────────────────┐   │
│   │ eyJhIjoiNzk2MjQ2ZDkwMzYxYTk4...    │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Domain (routed to this tunnel)            │
│   ┌─────────────────────────────────────┐   │
│   │ my-vula.example.com                 │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   ── TURN Server (for WebRTC) ──            │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  (*) Use Vula TURN (free tier)      │   │
│   │  ( ) Own TURN server                │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   TURN Host                                 │
│   ┌─────────────────────────────────────┐   │
│   │ turn.myserver.com:3478              │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   TURN Secret                               │
│   ┌─────────────────────────────────────┐   │
│   │ ••••••••••••                        │   │
│   └─────────────────────────────────────┘   │
│                                             │
│              [ Next ]                       │
│                                             │
└─────────────────────────────────────────────┘
```

User creates their own Cloudflare Tunnel in the Cloudflare dashboard, gets the token, and pastes it here. For TURN, they can either use Vula's managed TURN (even without the full platform) or point to their own coturn instance.

### Option C: Direct Domain + Caddy (advanced)

User brings their own domain with no Cloudflare Tunnel. Server is directly exposed with Caddy handling TLS. This is the existing `build.sh --domain` flow.

```
┌─────────────────────────────────────────────┐
│                                             │
│           Direct domain setup               │
│                                             │
│   Domain                                    │
│   ┌─────────────────────────────────────┐   │
│   │ my-vula.example.com                 │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   DNS Provider                              │
│   ┌─────────────────────────────────────┐   │
│   │ Namecheap (default)            [▼]  │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   API User                                  │
│   ┌─────────────────────────────────────┐   │
│   │ myuser                              │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   API Key                                   │
│   ┌─────────────────────────────────────┐   │
│   │ ••••••••••••                        │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   ── TURN Server (for WebRTC) ──            │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  (*) Use Vula TURN (free tier)      │   │
│   │  ( ) Own TURN server                │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Caddy will be installed and configured    │
│   with wildcard TLS for your domain.        │
│                                             │
│              [ Next ]                       │
│                                             │
└─────────────────────────────────────────────┘
```

### TURN Server (coturn)

WebRTC app streaming needs a TURN relay when peer-to-peer connections fail (symmetric NAT, corporate firewalls, mobile networks). Three sources for TURN:

| Source | When | Config |
|--------|------|--------|
| **Vula Platform TURN** | Option A users, or Option B/C users who select "Use Vula TURN" | Auto-configured, credentials managed by platform |
| **User's own coturn** | Option B/C users who run their own | User provides host + shared secret |
| **No TURN** | Local-only mode, or user skips | WebRTC uses STUN only — works on simple NATs, fails on symmetric NAT |

**Platform-managed TURN** uses time-limited HMAC credentials (same mechanism as existing `network/turn.go`). The platform rotates credentials automatically. Users don't need to manage coturn at all.

**Self-hosted coturn** — user installs coturn on a VPS or their own server, provides the host and shared secret during init. The OS generates time-limited TURN credentials from the shared secret (already implemented in `backend/services/network/turn.go`).

### Tunnel Platform (Separate Project)

**TODO:** The Vula Platform is a separate codebase. Lives in `landing/tunnel/` for now, will become its own repo.

**What it does:**
- Landing page / marketing site for Vula
- User registration and login (account system)
- Subdomain slug registration and ownership
- Cloudflare Tunnel provisioning via Cloudflare API (create tunnel, configure routes, issue tokens)
- Managed coturn TURN server (shared across all platform users)
- TURN credential generation (time-limited HMAC, auto-rotation)
- Instance/node management (list nodes, health status, assigned URLs)
- Usage monitoring and throttling per user
- Billing (free tier + paid plans for more nodes, bandwidth, TURN usage, etc.)
- Recovery kit backup storage (encrypted, as a paid feature)

**Architecture:**
```
┌──────────────────────────────────┐
│   Vula Platform                  │
│   (landing/tunnel/)              │
│                                  │
│   Web UI:                        │
│   ├── Landing page               │
│   ├── Signup / Login             │
│   ├── Dashboard                  │
│   │   ├── My nodes               │
│   │   ├── Usage / billing        │
│   │   ├── TURN usage stats       │
│   │   └── Recovery kits          │
│   └── Admin panel                │
│       ├── User management        │
│       ├── Usage monitoring       │
│       └── Throttle controls      │
│                                  │
│   API:                           │
│   ├── Auth (register/login)      │
│   ├── Tunnel CRUD                │
│   ├── TURN credential issuance   │
│   ├── Usage metrics              │
│   └── Recovery backup            │
│                                  │
│   Infrastructure:                │
│   ├── Cloudflare API             │
│   │   (tunnel provisioning)      │
│   ├── coturn server              │
│   │   (TURN relay, UDP 3478)     │
│   ├── Stripe (billing)           │
│   └── Resend (email)             │
└──────────────────────────────────┘
```

**What the OS needs from it:**
- `POST /api/auth/register` → create account
- `POST /api/auth/login` → get session
- `POST /api/tunnel/provision` → create Cloudflare Tunnel, return token + assigned URL
- `DELETE /api/tunnel/:id` → tear down tunnel
- `GET /api/turn/credentials` → get time-limited TURN credentials (refreshed by OS periodically)
- `GET /api/tunnel/status` → node health, usage stats
- `POST /api/recovery/backup` → upload encrypted recovery kit (optional)
- `GET /api/recovery/restore` → retrieve recovery kit (optional)

**Slug/subdomain registration:**
- User picks slug during registration (e.g., "alice")
- Platform validates uniqueness, reserves `alice.vula.dev`
- Cloudflare wildcard DNS (`*.vula.dev`) means no DNS records to create per user
- Platform configures Cloudflare Tunnel route: `alice.vula.dev` → tunnel UUID
- If user has multiple nodes, platform can do `alice.vula.dev` with load balancing across tunnels

**Usage monitoring & throttling:**
- Platform tracks bandwidth per user via Cloudflare Analytics API
- Platform tracks TURN relay usage per user (bytes relayed, active sessions)
- Free tier: reasonable limits (e.g., 10GB/month tunnel, 5GB/month TURN, 1 node)
- Paid tiers: more nodes, higher bandwidth, priority routing, dedicated TURN capacity
- Platform can disable a tunnel token if abuse detected
- Future: per-user rate limiting at Cloudflare edge via Workers (if needed)

**Cost structure:**
- Cloudflare Tunnels: free, no per-tunnel cost
- Wildcard DNS + TLS: free on Cloudflare
- Cloudflare API calls: free tier covers provisioning volume
- coturn server: VPS cost (~$5-20/mo depending on bandwidth)
- Only other cost is the platform server itself + Stripe fees on paid plans

---

## Implementation Order

1. **SSH server** — add `openssh-server` to Dockerfile and build.sh, hardened sshd config, key generation on first boot. Standalone useful even without clustering.

2. **SQLite + cr-sqlite migration** — replace `auth.json` and other JSON stores. Prerequisite for everything else. No sync yet, just local SQLite.

3. **S3 sync layer** — push/pull cr-sqlite changesets and files to S3. SSE-C encryption with Argon2id key derivation. Basic sync between two nodes.

4. **MinIO registry app** — add MinIO to registry, install/configure during init, storage settings UI with allocation slider and peer list.

5. **First-boot New/Join flow** — setup wizard branching (New with Storage/SSH/Recovery Kit steps, Join with connect/sync). Join code generation. Sync screen with progress UI. Reentrant sync state. Type "confirm" gate for recovery kit.

6. **Recovery kit delivery** — JSON download, QR code generation. Email option available if connected to Vula Platform (platform handles email via Resend). Recovery kit viewer in Settings for existing nodes.

7. **`VULOS_MODE` config** — server vs local mode toggle. Controls tunnel and display behavior.

8. **Tunnel integration** — Three options: Vula Platform (Cloudflare tunnel + managed TURN, default), Bring Your Own (own Cloudflare + own coturn), Direct Domain (Caddy + TURN). Platform is a separate repo in `landing/tunnel/` (see TODO).

9. **Presence system** — file open/edit awareness across nodes. Advisory leases in S3.

10. **Conflict UI** — toast notifications for file conflicts, conflict resolution viewer.

Each phase is independently useful. Phase 1 gives emergency access. Phase 2 improves reliability (SQLite vs JSON). Phase 3-4 enable multi-device. Phase 5-6 make onboarding seamless. Phase 7+ adds the full cluster.

---

## Key Dependencies

| Component | Library / Tool | Purpose |
|-----------|---------------|---------|
| cr-sqlite | `go-crsqlite` or CGo bindings | CRDT-enabled SQLite |
| MinIO client | `github.com/minio/minio-go/v7` | S3 API for sync + storage |
| MinIO server | `minio` binary | Self-hosted S3 (optional app) |
| cloudflared | Cloudflare Tunnel client | Remote access, provisioned by Vula platform |
| coturn | TURN relay server | WebRTC relay for app streaming (managed by platform or self-hosted) |
| fsnotify | `github.com/fsnotify/fsnotify` | File change watching for sync |
| Litestream | `litestream` binary (alternative) | SQLite → S3 streaming (simpler alt to cr-sqlite for read-heavy workloads) |
| openssh-server | System package | Emergency SSH access |
| argon2 | `golang.org/x/crypto/argon2` | Encryption key derivation from passphrase |
| go-qrcode | `github.com/skip2/go-qrcode` | QR code generation for recovery kit + join codes |

**cr-sqlite vs Litestream:** cr-sqlite gives true multi-writer merge (both nodes can write simultaneously). Litestream is simpler but single-writer — only one node can write at a time, others are read replicas. For Vula's use case (multiple nodes active simultaneously), cr-sqlite is the right choice.
