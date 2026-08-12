# Adding Another Device

Vulos lets you run more than one box on the same account — a second box at home,
a laptop, a cloud VPS — and have them share the same encrypted storage and sync
with each other. You do this with the **"Join existing"** flow in the setup
wizard.

This page describes, in plain language, how the join flow actually works today,
and the more secure join-code pairing where it is available.

For identity and keys see [IDENTITY-KEYS.md](IDENTITY-KEYS.md); for removing a
device again see [REMOVE-DEVICE.md](REMOVE-DEVICE.md).

---

## How joining works, in one sentence

A new device joins by **connecting to the same encrypted S3-compatible storage**
that your existing box already uses, proving it with the cluster **encryption
passphrase**, and then syncing that storage down in the background.

Everything in the shared bucket is encrypted, so what a joining device really
needs is two things:

1. the **storage credentials** (bucket, region, access key, secret, endpoint), and
2. the **encryption passphrase** that unlocks the encrypted contents.

There are two ways to hand those over: a **join code** (the easier, more secure
path) or **typing the details in by hand**.

---

## Option A — Join with a join code (recommended)

This is the "approve from an existing device" path. You start it on the box you
already trust, then finish it on the new device.

### On your existing box (the owner does this)

<picture>
  <img src="screenshots/instances-light.png" alt="The Dashboard's Instances panel, listing every box and cloud node on the account" width="880" />
</picture>

1. Open the **Dashboard → Instances** panel (or Settings, depending on your
   build).
2. Generate a **join code**. This calls `POST /api/cluster/join-code`, which is
   **admin/owner-only** — a regular user cannot mint one. (It is a POST because
   it mints and stores a live credential; as a GET, any page an admin visited
   could have triggered it.)
3. You get a short code in the form `VULOS-XXXX-XXXX-XXXX` and a matching **QR
   code**. The code is **single-use** and **expires after one hour**.

The join code carries the scoped storage credentials for the cluster, so the new
device doesn't need you to copy secrets around by hand
(`backend/services/joincode/joincode.go`).

### On the new device

1. Boot Vulos and open its address. Because no account exists yet, you land in
   the setup wizard.
2. At the first fork choose **Join Existing** instead of New.
3. Enter the short code (or scan the QR). The device redeems it via
   `POST /api/setup/join-code`, which returns the storage credentials. The code
   is consumed on first use — a second attempt with the same code fails.
4. You still supply the **encryption passphrase** for the cluster — this is what
   actually decrypts the shared storage, and it is never carried inside the join
   code.
5. The device validates the storage and passphrase (`POST /api/setup/join`),
   then begins syncing in the background. You can watch progress on the syncing
   step; poll is via `GET /api/setup/join/status`.
6. When sync finishes it asks only for a lock-screen **PIN**, then lands on the
   desktop.

> **Why this is the safer path:** the join code is minted **only by an
> admin/owner on an already-trusted box**, it is single-use, and it self-destructs
> after an hour. A leaked code is far less dangerous than long-lived storage keys
> pasted into chat.

---

## Option B — Join by typing the details in

If you don't have a join code (for example you're restoring a box and only have
your backup details), you can enter the storage details directly on the **Join
Existing → Connect Storage** step:

- **Bucket**, **Region**, **Access key**, **Secret key**
- **Endpoint** (optional — for self-hosted / non-AWS object stores such as MinIO
  or Tigris) and whether it uses **SSL/TLS**
- The cluster **encryption passphrase**

The device posts these to `POST /api/setup/join` and syncs exactly as above. This
endpoint is unauthenticated by necessity (there is no account yet), so it is
**rate-limited to five attempts per IP per minute** and a **wrong passphrase is
rejected** with a clear error. Once a box is fully provisioned the join endpoint
refuses further attempts (it returns `409`).

---

## What "joined" means and what it doesn't

- **Shared storage, real sync.** Joined boxes read and write the same encrypted
  bucket and sync to each other. Adding a box this way is also how you *restore*
  a box — see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).
- **Same account.** You sign in with the same local account credentials.
- **Each box keeps its own network identity.** Every box has its own independently
  generated Ed25519 identity / Vulos ID and its own ULID — joining does not clone
  keys between boxes (see [IDENTITY-KEYS.md](IDENTITY-KEYS.md)).

### Honest note on the current mechanism

Today, joining is fundamentally **"prove you hold the storage passphrase (or a
valid join code), then sync the encrypted storage."** The join code makes that
pairing owner-gated, single-use and short-lived, which is a real improvement over
hand-copied keys.

A stronger, fully cryptographic **per-device enrolment** — where an existing
device must *vouch* for the new one before it is trusted, rather than the new
device simply presenting storage credentials — exists as a primitive in the fleet
(the `device-enroll` quorum action in `backend/services/fleetid`) and is used for
break-glass recovery, but the **first-boot join wizard does not yet require a
peer vouch to complete a join**. Treat the encryption passphrase and any join code
as sensitive material accordingly.

---

## Related pages

- [REMOVE-DEVICE.md](REMOVE-DEVICE.md) — revoke a device you no longer trust.
- [IDENTITY-KEYS.md](IDENTITY-KEYS.md) — what identity each box holds.
- [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) — restoring a box from storage.
- [PEERING.md](PEERING.md) — how boxes talk to each other once joined.
