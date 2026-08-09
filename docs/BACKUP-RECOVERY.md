# Backup, restore & disaster recovery

What state your Vulos box holds and where, the four built-in backup mechanisms (the Restic vault, database snapshots, OS object-store snapshots, and the recovery kit), the data export, what the recovery phrase can and cannot save you from, and how to move a box to new hardware.

---

## The one-paragraph model

A Vulos box keeps everything under two roots: `~/.vulos` in the service user's home (databases, keys, app data, staged files) and your object-store bucket(s) (Drive documents and app storage — see [FILES.md](FILES.md)). A bare-metal image additionally carries `/etc/vulos/trust-anchor.pub` and release-verification files, which are part of the OS image itself, not your data. Backup is not one switch: the **Restic vault** snapshots user data to S3, the **database snapshot** tool backs up a SQLite database to encrypted S3, **OS Snapshots** capture point-in-time, restorable copies of your object-store bucket, and the **recovery kit** is a small JSON you download once and keep offline. Each uses a *different* secret (or none). Know all four before you need them.

## What state lives where

### `~/.vulos` — the box's home directory

Everything below is created by the Go backend (verified against the code, not aspirational). On the appliance/bare-metal image, a `LABEL=vulos-data` partition is mounted here at boot.

| Path | What it holds | Loss means |
|---|---|---|
| `~/.vulos/db/auth.db` | Users, password hashes, sessions, profiles, wrapped master keys, recovery blobs | Nobody can sign in; master keys gone |
| `~/.vulos/db/files.db` | The Drive index: folders, permissions, share links, versions | Drive structure and sharing gone (bytes may survive in the bucket, but unaddressed) |
| `~/.vulos/db/uploads.db` | Resumable-upload progress | In-flight uploads restart |
| `~/.vulos/db/storage.json` | Box-provisioned MinIO credentials (access/secret/SSE-C keys) | Box cannot reach its own storage |
| `~/.vulos/db/instance.json` | Instance ULID + hostname (the box's identity for the recovery kit) | Identity of this install |
| `~/.vulos/db/` (misc) | `recall.json`, `storagemode.db`, `suite-selection.json`, `notifications.json`, `push_subs.sqlite`, `vapid.json`, `dnd.json`, `turn.json`, `admin-token.json` | Assorted feature state (search index, push subscriptions, notification prefs…) |
| `~/.vulos/db/peer-received/` | Staged bytes redeemed from peer-shares, not yet saved to Drive | Unsaved received items |
| `~/.vulos/data/` | User data directory — **the Restic vault's backup scope** — including `data/uploads/` (upload staging) and per-app writable data | App/user working data |
| `~/.vulos/storage/` | **Standalone mode only:** the actual Drive bytes (no S3 configured) | Your documents |
| `~/.vulos/peering/` | Box identity and social state: `identity/` (the Ed25519 keypair behind your Vula ID), `contacts.json`, `groups/`, `inbox/`, `outbox/`, `media/`, `relay/` | Your box's peer identity — peers no longer recognize you; issued peer-share capabilities die |
| `~/.vulos/auth/vault/<userID>/` | Per-user credential vault (password manager), AES-256-GCM under its own master password | Saved passwords |
| `~/.vulos/auth/totp/` | Authenticator (TOTP) vault | 2FA codes you host for other sites |
| `~/.vulos/auth/tpm/` | Device key store. On a box with a TPM the private key lives *in the TPM*; this directory then holds only references. Software fallback keeps key material here | Device identity; passkeys are sealed with it |
| `~/.vulos/auth/passkeys/` | Server-side passkey (WebAuthn) credentials, sealed via the device key | Registered passkeys |
| `~/.vulos/auth/integrations/` | The box's device-identity material for any gateway integrations you've configured | Re-connect the integration to re-issue it |
| `~/.vulos/apps/`, `~/.vulos/ai-apps/`, `~/.vulos/models/`, `~/.vulos/os-cache/`, `~/.vulos/wine/`, `~/.vulos/lib/`, `~/.vulos/logs/`, `~/.vulos/tunnel/` | Installed apps, AI apps, downloaded LLM models, OS update slots, Wine prefixes, shared libs, app logs, coturn state | All re-downloadable or disposable |
| `~/.vulos/<appID>/` | Built-in-app sandboxes (the `appfs` API) | Per-app documents (e.g. Notes) |

### The object store

With S3/MinIO/Tigris configured, your actual documents live in per-user buckets (`vulos-<userID>`, keys `<userID>/drive/...` and `<userID>/<appID>/...`) and the OS's own cluster data in `vulos-cluster`. Bucket durability is primarily your storage provider's job. Of the box-side mechanisms below only **OS Snapshots** (mechanism 3) copies bucket contents — the Restic vault and the DB snapshot tool do not.

## Backup mechanism 1: the Restic vault (user data → S3)

The vault wraps [Restic](https://restic.net) for continuous, encrypted snapshots of **`~/.vulos/data`** — and only that directory.

**When it runs:** automatically every hour, if and only if (a) the `restic` binary is installed on the box and (b) S3 is configured. Otherwise the server logs `[vault] skipped` at startup and does nothing.

**Configuration** (verified variable names):

| Variable | Default | Meaning |
|---|---|---|
| `S3_ENDPOINT` | `s3.amazonaws.com` | S3-compatible endpoint for the backup repository |
| `S3_BUCKET` | `vulos-vault` | Repository bucket |
| `S3_REGION` | `us-east-1` | Region |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | _(empty — vault disabled until set)_ | Credentials |
| `S3_USE_SSL` | `true` | TLS |
| `VULOS_RESTIC_PASSWORD` (or `RESTIC_PASSWORD`) | dev-only default | **The repository encryption passphrase** |

Two things to internalize:

1. **The Restic passphrase is a real secret.** Snapshots are encrypted with it; without it they are unreadable, including by Vulos. In production (`VULOS_ENV=prod`) the server refuses to back up with the built-in dev default — set your own and store it with your recovery phrase.
2. **Scope is `~/.vulos/data` only.** Databases (`~/.vulos/db`), keys (`~/.vulos/auth`, `~/.vulos/peering`), and standalone Drive bytes (`~/.vulos/storage`) are *not* in the vault. See "Honest gaps" below.

**HTTP surface** (session-authenticated; backup and sync are admin-only):

| Endpoint | Purpose |
|---|---|
| `GET /api/vault/status` | Initialized? last backup? last error? |
| `POST /api/vault/backup` | Trigger a snapshot now (admin) |
| `GET /api/vault/snapshots` | List snapshots |
| `GET /api/vault/sync` | Sync status: latest snapshot, which hostnames have written snapshots |
| `POST /api/vault/sync` | **Restore** the latest snapshot into `~/.vulos/data` on *this* box (admin) — the "rebuild a new device" path |

Because it is a standard Restic repository, everything also works from any machine with plain `restic`:

```bash
export RESTIC_REPOSITORY="s3:https://<S3_ENDPOINT>/<S3_BUCKET>"
export AWS_ACCESS_KEY_ID=<S3_ACCESS_KEY> AWS_SECRET_ACCESS_KEY=<S3_SECRET_KEY>
export RESTIC_PASSWORD=<your passphrase>
restic snapshots
restic restore latest --target /restore/here
```

## Backup mechanism 2: database snapshots (SQLite → encrypted S3)

Separate from the vault, the OS can snapshot a SQLite database (a consistent `VACUUM INTO` image), encrypt it with a passphrase-derived key, and upload it to the cluster S3 bucket.

**CLI** (the `vulos` server binary doubles as the tool):

```bash
# snapshot the DB and upload
vulos backup

# download the latest snapshot and REPLACE the local DB (destructive; flag required)
vulos restore --confirm

# a different database
vulos backup --db ~/.vulos/db/files.db
```

**Configuration** (shared with the cluster subsystem):

| Variable | Default | Meaning |
|---|---|---|
| `VULOS_S3_ENDPOINT` | `localhost:9000` | Cluster S3 endpoint |
| `VULOS_S3_BUCKET` | `vulos-cluster` | Bucket |
| `VULOS_S3_ACCESS_KEY` / `VULOS_S3_SECRET_KEY` | _(required)_ | Credentials |
| `VULOS_S3_USE_SSL` | `false` | TLS |
| `VULOS_CLUSTER_PASSPHRASE` | _(required)_ | **Encrypts the snapshot** — the second secret to keep |
| `VULOS_BACKUP_DB` | `~/.vulos/db/auth.db` | Which database the default snapshot covers |
| `VULOS_NODE_ID` | hostname | Snapshot labeling |
| `VULOS_BACKUP_INTERVAL` | _(unset = off)_ | e.g. `1h` — periodic automatic snapshots |

**HTTP surface** (admin-only; registered only when the cluster S3 client is configured):

- `POST /api/admin/backup` — on-demand snapshot.
- `GET /api/admin/backup/status` — latest snapshot metadata.
- `POST /api/admin/restore` — destructive; requires the body `{"confirm":"RESTORE"}` in addition to admin auth.

Note the default covers **one** database — `auth.db`. If you care about your Drive index, back up `files.db` too (`--db` or a second periodic job with `VULOS_BACKUP_DB`).

## Backup mechanism 3: OS Snapshots (object-store point-in-time restore)

The mechanisms above copy `~/.vulos` state; **OS Snapshots** are the box-side way to protect the *bucket* itself — your Drive documents and per-app object storage. A snapshot is a point-in-time record of every live object under the box's data prefix, and a restore rolls the bucket back to it.

**How it works** (verified against `services/snapshot`):

- **Content-addressed & incremental.** Each object's bytes are stored once, keyed by SHA-256 and gzip-compressed; a new snapshot only uploads blobs whose content isn't already present, so repeated snapshots are cheap. Every snapshot still carries a *complete* manifest, so each one restores independently.
- **In-bucket, self-scoped.** Artifacts live under a reserved `…/_snapshots/` sub-prefix of the same bucket+prefix (and are excluded from capture, so snapshots never snapshot themselves). A snapshotter is bound to one box's (bucket, prefix) and cannot reach another account's data.
- **Fail-closed restore.** `Restore` first **verifies the entire snapshot** — manifest hash, every referenced blob present, every blob's decompressed content re-hashed to its recorded value, every key path-traversal-checked — and aborts **untouched** on any failure. Only then does it take an automatic **pre-restore safety snapshot** of the current state (so a bad restore is itself reversible) before writing the verified objects and reconciling deletions. An integrity failure surfaces as HTTP **422** with the box unmodified.
- **Retention + scheduling.** The default retention policy keeps the last **7 daily** and **4 weekly** snapshots; `POST …/prune` (and the scheduler) apply it. Automatic snapshots are **opt-in** via `VULOS_SNAPSHOT_INTERVAL` (e.g. `24h`); unset means manual-only.

**Availability:** the endpoints are wired to the box's own object store, so they exist only when an object store is configured; without one they return **503** (exactly like the DB backup routes). Standalone boxes with no S3 have no OS Snapshots.

**HTTP surface** (admin-only; restore is additionally confirm-gated):

| Endpoint | Purpose |
|---|---|
| `POST /api/admin/snapshots` | Snapshot the bucket now |
| `GET /api/admin/snapshots` | List snapshots (id, kind, object count, sizes) |
| `GET /api/admin/snapshots/usage` | Snapshot storage usage (the metered figure) |
| `POST /api/admin/snapshots/prune` | Apply the retention policy now |
| `POST /api/admin/snapshots/{id}/restore` | **DESTRUCTIVE** — roll the bucket back; requires body `{"confirm":"RESTORE"}` on top of admin auth |

Every create/prune/restore is written to the exec audit trail. OS Snapshots protect against accidental deletion or corruption *within* the bucket; they are not a substitute for your storage provider's own durability, and (like the other mechanisms) they do not cover `~/.vulos` databases and key stores.

## Backup mechanism 4: the recovery kit

`GET /api/recovery/kit` (admin session required) downloads `vulos-recovery-kit-<instance>-<date>.json`: the instance ULID, hostname, and — when the box provisioned its own storage — the storage endpoint, bucket, and access/secret keys from `storage.json`.

- It **never** contains passwords, password hashes, passphrases, or session tokens.
- It **does** contain storage credentials, so treat the file itself as a secret: store it offline (password manager, printed in a safe).
- Its job is bootstrap: after a total box loss, the kit tells you *which* instance you were and *where* its storage lives, so a rebuilt box can reattach.

Every issuance is written to the server audit log.

## "Export my data" — account-level portability

Distinct from disaster recovery: any signed-in user can download their own data as a single zip in standard formats — the anti-lock-in guarantee.

```
GET /api/export/data   →   vulos-export-<timestamp>.zip
```

Contents (each section is skipped gracefully and the gap recorded in the archive's `MANIFEST.txt`):

- `files/` — your entire Drive tree, real bytes, original names and paths (files over 256 MiB are skipped and counted in the manifest).
- `mail/` — messages from INBOX/Sent/Drafts/Archive as `.eml` plus a `messages.json` index per folder (up to 2000 per folder).
- `calendar.ics` / `contacts.vcf` — when the mail service exposes them.
- `settings.json` — your OS preferences, secret-scrubbed through a strict allowlist (no API keys, PINs, or password hashes can ride along).

Not covered, and the manifest says so: Diwan documents, per-app data, and content held only on a peer via a content-blind share (which the server cannot decrypt — that is the guarantee working, not a gap; export it from the instance holding the keys). Chat/video call history lives with your third-party comms apps (Cinny/Element, Jitsi Meet/Element Call) — Vulos never holds it, so there's nothing to export here by design.

## The recovery phrase and the master key

At account creation the box provisions a per-user **master key** and shows you a **24-word recovery phrase — once**. The master key is stored only as a doubly-wrapped envelope (wrapped under your password *and* under the phrase) inside `auth.db`; the plaintext key and the phrase are never persisted or logged.

What the master key is for: it protects your client-side content encryption — most visibly, opening **sealed (content-blind) shares** in the Files app ([FILES.md](FILES.md)). On normal login your browser unwraps it locally from the password envelope; the server does not see it.

### What the phrase CAN do

- **Reset a forgotten password without losing encrypted content.** `POST /api/auth/masterkey/recover` — deliberately usable without a session, since you are locked out when you need it; the OS's password-reset flow offers it as the phrase-based fallback. It takes your username, the phrase, and a new password; it verifies the phrase by actually unwrapping the master key, re-wraps the *same* key under the new password, resets the login credential, and revokes all sessions. Wrong phrase → nothing changes.
- Serve as the last-resort wrap of the master key if every device is gone.

There is also a middle tier: if you are still signed in on a trusted device, `POST /api/auth/masterkey/reset-active` lets that session reset the password using the device's in-memory master key — no phrase needed, and the server never sees the key.

### What the phrase CANNOT do

Be precise with yourself about this. The recovery phrase does **not** recover:

| Not recoverable by the phrase | What actually protects it |
|---|---|
| Restic vault backups | `VULOS_RESTIC_PASSWORD` |
| Database snapshots | `VULOS_CLUSTER_PASSPHRASE` |
| The credential vault (password manager, `~/.vulos/auth/vault/`) | Its own separate master password — forget that and the entries are gone |
| TPM-bound device keys and passkeys | The physical device |
| Any data that was never backed up | Nothing |

So a complete "keys drawer" for a self-hosted box is: **recovery phrase, Restic passphrase, cluster passphrase, credential-vault master password, recovery-kit JSON** — five things, kept offline.

## Disaster recovery: rebuilding a box

There is no single one-click restore; this is the honest, code-supported procedure.

1. **Reinstall Vulos** on the new machine — see [GETTING-STARTED.md](GETTING-STARTED.md).
2. **Reapply configuration**: environment variables, using your recovery kit for the storage endpoint, bucket, and credentials.
3. **Restore the auth database** (accounts, wrapped master keys):
   ```bash
   VULOS_S3_ENDPOINT=... VULOS_S3_BUCKET=... \
   VULOS_S3_ACCESS_KEY=... VULOS_S3_SECRET_KEY=... \
   VULOS_CLUSTER_PASSPHRASE=... vulos restore --confirm
   ```
   Repeat with `--db ~/.vulos/db/files.db` if you snapshotted the Drive index too.
4. **Restore user data**: sign in as admin and `POST /api/vault/sync` (pulls the latest Restic snapshot into `~/.vulos/data`), or use the plain `restic restore` shown above.
5. **Drive documents**: if your bytes live in an object store, they are already there — once `files.db` is restored, the Drive works again, because the index records bucket + key per file. In standalone mode, restore `~/.vulos/storage` from whatever copy you made (see gaps below).
6. **Re-enroll device-bound things**: re-register passkeys if the old box's device key was TPM-held (they were sealed to hardware that is gone), and re-pair any devices you had paired — `POST /api/pairing/issue` on the box, `POST /api/pairing/claim` from the device, `GET /api/pairing/devices` to check. Accounts are local to the box; there is **no** cloud enrollment step and no cloud console to approve anything in (the Vulos Cloud account/enrolment surface was removed from the server — `/api/auth/cloud/*` no longer exists). If you point the box at a control plane, that is configured separately (`VULOS_CP_URL` / `VULOS_CLOUD_URL`, or Settings), not re-enrolled here.

## Moving a box to new hardware

There is **no automated whole-box migration tool**. The supported manual move, implied directly by the state layout:

1. Stop the server on the old box (`sudo systemctl stop vulos.service` on bare metal, or `docker stop vulos` under Docker).
2. Copy the state, preserving permissions:
   ```bash
   rsync -aHAX ~/.vulos/  newbox:~/.vulos/
   ```
3. Reapply the same environment variables (they are not stored in `~/.vulos`).
4. Start the server on the new box.

What moves cleanly with the directory copy:

- Accounts, sessions, profiles, master-key envelopes (`db/auth.db`).
- The Drive index and all sharing state (`db/files.db`); bucket-backed bytes need no move at all, standalone bytes move with `~/.vulos/storage`.
- The **peering identity** (`~/.vulos/peering/identity/`) — your Vula ID, contacts, and issued capabilities keep working. If you prefer not to copy the whole tree, the OS also supports moving just the identity as a passphrase-encrypted bundle: `POST /api/peering/identity/export` on the old box, `POST /api/peering/identity/import` on the new one.
- Credential vault and TOTP vault files (still locked by their own passwords).

What does **not** move, and what to do:

| Item | Why | Fix |
|---|---|---|
| TPM-held device key (`auth/tpm/` on TPM hardware) | The private key physically lives in the old TPM | The new box generates its own; expect passkeys sealed under the old key to need re-registration |
| TLS/ACME material, IP-bound DNS | New host, new address | Update DNS; certificates re-issue — see [NETWORKING.md](NETWORKING.md) |

## Putting it together: a reference backup configuration

A self-hosted box with an S3-compatible store, everything above enabled (all variable names verified in the code):

```bash
# --- Restic vault: hourly snapshots of ~/.vulos/data ---
S3_ENDPOINT=s3.example.com
S3_BUCKET=mybox-vault
S3_ACCESS_KEY=...
S3_SECRET_KEY=...
VULOS_RESTIC_PASSWORD='<long unique passphrase #1>'

# --- DB snapshots: auth.db → encrypted object in the cluster bucket ---
VULOS_S3_ENDPOINT=s3.example.com
VULOS_S3_BUCKET=mybox-cluster
VULOS_S3_ACCESS_KEY=...
VULOS_S3_SECRET_KEY=...
VULOS_S3_USE_SSL=true
VULOS_CLUSTER_PASSPHRASE='<long unique passphrase #2>'
VULOS_BACKUP_INTERVAL=1h

# --- OS Snapshots: point-in-time restore points of the object-store bucket ---
VULOS_SNAPSHOT_INTERVAL=24h   # opt-in; unset = manual-only (POST /api/admin/snapshots)

VULOS_ENV=prod   # enforces "no dev-default Restic key" fail-closed
```

Then, once:

1. Download the recovery kit (`GET /api/recovery/kit`) and store it offline.
2. Record your recovery phrase (shown once at account creation) and both passphrases in the same offline place.
3. Snapshot the Drive index alongside the default job — e.g. a cron entry running `vulos backup --db ~/.vulos/db/files.db`.

### Restore drill (do this once per quarter)

- `GET /api/vault/status` shows `initialized: true` and a recent `last_backup`.
- `GET /api/vault/snapshots` lists snapshots; `restic restore latest --target /tmp/drill` on another machine succeeds with your recorded passphrase.
- `GET /api/admin/backup/status` reports `has_snapshot: true` with a recent `created_at`.
- On a scratch machine or VM: `vulos restore --confirm --db /tmp/drill-auth.db` succeeds with your recorded cluster passphrase.
- You can still find the recovery kit and read the phrase.

## Honest gaps (read before you rely on any of this)

These are properties of the current code, stated plainly so you can compensate:

- **The Restic vault does not back up `~/.vulos/db`** (your databases), `~/.vulos/auth` (key stores), `~/.vulos/peering` (identity), or `~/.vulos/storage` (standalone Drive bytes). Its scope is `~/.vulos/data` only.
- **The DB snapshot tool covers one database per run**, defaulting to `auth.db`. `files.db` and the rest are your responsibility.
- **Standalone boxes (no S3) therefore have no built-in offsite backup of Drive documents.** Both S3-based mechanisms are inert without an object store. Run your own copy of `~/.vulos` (restic, borg, rsync — while the server is stopped, or accept SQLite-snapshot fuzziness).
- Practical belt-and-braces for any box: a nightly stopped-service (or filesystem-snapshot) copy of the entire `~/.vulos` tree to somewhere else, plus the built-in mechanisms for their convenience.

Test your restore. A backup you have never restored is a hope, not a backup.

---

Related chapters: [FILES.md](FILES.md) (what the Drive stores and where), [SECURITY.md](SECURITY.md) (the trust model behind the master key), [PEERING.md](PEERING.md) (the box identity you are preserving), [CONFIGURATION.md](CONFIGURATION.md) (every variable named here), [TROUBLESHOOTING.md](TROUBLESHOOTING.md).
