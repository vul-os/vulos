# First Boot Setup

The software-level setup wizard that runs after the OS boots for the first time. Separate from BAREMETAL-INIT.md (which covers hardware boot, compositor, installer). This is what the user sees once the desktop is loaded and no `~/.vulos/db/` exists.

---

## New or Join

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

---

## Path A: New System

```
Welcome → New/Join → Language → Timezone → Network → Account → Instance Identity → PIN → Storage → SSH → Recovery Kit → Appearance → Ready
```

The existing steps (Language, Timezone, Network, Account, Appearance) are unchanged. New steps added:

### Step: Instance Identity

Each Vula instance gets two identifiers:

1. **Instance ID** — a ULID generated locally at first boot. Globally unique, no server check needed. This is the real identifier used for DNS routing (e.g., `*.01h5t3e8k2qj7r9xmvn4p.vulos.org`). Not user-chosen, not editable.
2. **Hostname** — human-readable name for LAN discovery via mDNS. Auto-generated from `{username}-{device}`, user can customize. Only matters on local network.

```
┌─────────────────────────────────────────────┐
│                                             │
│           Your instance                     │
│                                             │
│   Instance ID (auto-generated)              │
│   ┌─────────────────────────────────────┐   │
│   │ 01h5t3e8k2qj7r9xmvn4p             │   │
│   └─────────────────────────────────────┘   │
│   This is your unique address:              │
│   *.01h5t3e8k2qj7r9xmvn4p.vulos.org       │
│                                             │
│   Hostname (editable)                       │
│   ┌─────────────────────────────────────┐   │
│   │ alice-home                          │   │
│   └─────────────────────────────────────┘   │
│   On your local network:                    │
│   alice-home.local                          │
│                                             │
│              [ Next ]                       │
│                                             │
└─────────────────────────────────────────────┘
```

**Instance ID generation:** ULID (Universally Unique Lexicographically Sortable Identifier) — 128-bit, 26 characters, Crockford base32. Generated locally with no server round-trip. Time-ordered prefix means IDs sort by creation time. Uses Go `oklog/ulid` package.

```go
import "github.com/oklog/ulid/v2"

func generateInstanceID() string {
    return ulid.MustNew(ulid.Now(), rand.Reader).String()
}
```

**Hostname auto-generation:** `{username}-{device}` where device is guessed from hardware (e.g., "home", "laptop", "server", "desktop") or falls back to a random word. User can edit it. Does not need to be globally unique — only matters on LAN.

**What gets set:**
- `VULOS_INSTANCE_ID` → `01h5t3e8k2qj7r9xmvn4p` (ULID, immutable)
- `/etc/hostname` → `alice-home`
- mDNS via Avahi/systemd-resolved → `alice-home.local`

**When the instance comes online with internet:**
1. Registers `VULOS_INSTANCE_ID` + public IP with Vulos DNS API
2. DNS creates `*.01h5t3e8k2qj7r9xmvn4p.vulos.org` → instance IP
3. Wildcard cert issued via acme-dns
4. No uniqueness check needed — ULIDs don't collide

### Step: Storage (MinIO)

Recommend self-hosting MinIO on this node. This is the foundation for multi-node sync (see CLUSTER.md).

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

### Step: SSH (Emergency Access)

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

### SSH Server Setup

SSH must be available for emergency access on every node. Added to both Dockerfile and build.sh.

**Dockerfile addition** (in the apt-get install line):
```dockerfile
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
    exec.Command("ssh-keygen", "-A").Run()
}

// Start sshd
exec.Command("/usr/sbin/sshd", "-D").Start()
```

**Docker-specific:** Expose port 22 in Dockerfile:
```dockerfile
EXPOSE 8080 22
```
```bash
docker run -p 8080:8080 -p 2222:22 --shm-size=1g vulos
```

### Step: Recovery Kit

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
│   Encryption Passphrase                     │
│   ┌─────────────────────────────────────┐   │
│   │ (the passphrase you set above)      │   │
│   │                          [ Copy ]   │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  [ Download JSON ]  [ QR Code ]    │   │
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
  "instance_id": "01h5t3e8k2qj7r9xmvn4p",
  "hostname": "alice-home",
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

**Cloud backup** — users can back up their recovery kit to `vulos.org` (see LANDING.md). The user authenticates with their email, and the encrypted kit is stored. They can restore it later from any device.

### Confirmation Gate

The "Next" button is disabled until the user types `confirm` in the text field. This is intentionally friction — the recovery kit contains everything needed to access their system. Losing it means losing emergency access.

```js
const canProceed = confirmText.toLowerCase().trim() === 'confirm'
```

No bypass, no skip. If storage was skipped (no MinIO), the recovery kit only contains the SSH key and the confirmation step is simpler but still required.

---

## Path B: Join Existing Cluster

```
Welcome → New/Join → Connect Storage → Syncing... → PIN → Ready
```

### Step 1: Connect Storage

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

**Join codes:** An existing node can generate a join code (Settings → Cluster → "Add Node") that encodes the S3 endpoint, bucket, and a time-limited access token as a QR code or short alphanumeric code.

On "Connect", the backend:
1. Validates S3 credentials (list bucket)
2. Checks for existing cluster data (looks for `nodes/` prefix in bucket)
3. If valid → proceeds to sync screen

### Step 2: Syncing Screen

Dedicated sync UI with real-time progress. **Reentrant** — if the node reboots mid-sync or sync is interrupted, it returns to this screen on next boot until sync is complete.

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
4. **Install apps** — for each app in the synced install list, run the registry install recipe. Slowest step — apt/flatpak installs take time.

**"Continue in background"** — after the database is synced (step 1), the user can dismiss the sync screen and start using the system immediately. Files and apps continue syncing in the background with a subtle status indicator in the taskbar.

### Step 3: PIN + Ready

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

The join code creates a temporary MinIO service account with limited permissions (read-only for initial sync, full access once node is registered). The generating node revokes the temporary credentials after the joining node registers itself.

---

## Backend Init Flow

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

---

## API Endpoints

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

## Implementation Order

1. **Instance identity** — ULID generation at first boot, hostname auto-generation, mDNS setup, DNS registration on first internet connection
2. **SSH server** — add `openssh-server` to Dockerfile and build.sh, hardened sshd config, key generation
3. **Storage step** — MinIO install during init, S3 bucket creation, encryption setup
4. **Recovery kit** — JSON download, QR code generation, confirmation gate, cloud backup to vulos.org
5. **New system flow** — full wizard with all new steps integrated
6. **Join flow** — connect storage, sync screen with progress UI, reentrant sync state
7. **Join codes** — generation from existing nodes, QR + short code encoding, temporary credentials
