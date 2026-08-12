# Identity and Your Keys

In Vulos your identity is **not** an email address and it is **not** a cloud
account. There is nothing to sign in to on someone else's servers. When you set
up a box for the first time it creates a **local account** and generates a
cryptographic identity that lives on hardware you own.

This page explains what that identity is made of, what the Recovery Kit is, and
why you must keep it safe.

For the daily-driver walkthrough of first sign-in see
[USER-GUIDE.md](USER-GUIDE.md); for the deeper cryptography see
[KEY-CEREMONY.md](KEY-CEREMONY.md) and [SECURITY.md](SECURITY.md).

---

## What is created on first boot

The setup wizard (`frontend/src/auth/Setup.tsx`) collects three things that together make
up your account:

| Thing | What it is | Where it lives |
|---|---|---|
| **Username** | A short local handle (lowercase letters, numbers, `-` and `_`). Not an email address. | On the box |
| **Password** | Used to sign in, to `sudo` in the Terminal, and to re-prove yourself for dangerous actions (step-up). | Hashed on the box, never stored in the clear |
| **Device PIN** | An optional 4–8 digit quick-unlock for the lock screen. Skippable; you can set it later in Settings. | On the box |

The first account you create is the **administrator / owner** account. See
[ACCOUNTS-ACCESS.md](ACCOUNTS-ACCESS.md) for what that means.

There is no Vulos-hosted mailbox and no Vulos e-mail address — your identity is
deliberately decoupled from mail. Mail, Calendar and Contacts connect to an
account you already own (see [MAIL-CALENDAR-CONTACTS.md](MAIL-CALENDAR-CONTACTS.md)).

---

## Your identity keys

Two separate key spaces are generated. They are intentionally different keys for
different jobs — a compromise of one does not hand over the other.

### 1. Your account identity (Ed25519)

The box generates an **Ed25519** keypair that is your peering/network identity.
Its public form is encoded as a **Vulos ID**, and every box also has a **ULID**
(a stable instance identifier shown, read-only, on the Identity step of setup).
This is the key other boxes pin and trust when they talk to yours.

Crucially, **this key is generated independently on the box** — it is *not*
derived from your password and *not* derived from your recovery phrase. That is a
deliberate security boundary: your recovery phrase can *authorise* replacing this
key, but a copy of the phrase does not by itself sit on the box.

### 2. Your account master key

When your account is created the server also mints a per-user **master key**
that wraps your encrypted content. This is what your password protects. Losing
your password without your recovery phrase means the master key cannot be
unwrapped — which is exactly why the recovery phrase matters.

---

## The Recovery Kit

"Recovery Kit" refers to two distinct things. Keep them straight — only the first
one can actually reconstruct your identity.

### a) The 24-word recovery phrase — your identity anchor

At account-creation time the box derives **256 bits of entropy** and turns it into
a **24-word BIP39 mnemonic** (`backend/internal/auth/recovery.go`). From the seed
this phrase encodes, both your Ed25519 account keypair and an OS keyring root key
can be re-derived (HKDF-SHA256).

- The phrase is shown to you **exactly once**. You must confirm you have written
  it down before setup will continue.
- The plaintext phrase is **never** stored on the box. Only an encrypted copy is
  kept locally — sealed with XChaCha20-Poly1305 under a key derived from your
  password (Argon2id). If you forget your password, that local copy is of no use;
  the offline phrase is your only route back in.
- From the phrase the box can re-derive your **recovery anchor**. Only the
  anchor's *public* ID is ever written to disk (`recovery_anchor.json`); the
  private half — the actual power to sign a recovery and rebind your identity to a
  new key — exists only when you re-enter the phrase. A stolen box therefore
  yields the harmless public anchor ID but never the ability to forge a recovery
  (`backend/services/peering/recovery_anchor.go`).

This 24-word phrase is the thing that "reconstructs your identity anchor". Treat
it like the keys to your house: write it down, store it offline, never type it
into anything but a genuine Vulos recovery prompt.

### b) The downloadable Recovery Kit file + QR

The last steps of setup also let you **download a Recovery Kit JSON file** and
show an inline **QR code**. This is a *different* artefact from the phrase:

- The JSON (`GET /api/recovery/kit`, admin-only) contains **metadata and
  credentials** — your instance ULID, hostname, your S3 storage access key (if you
  connected storage), your SSH key fingerprint, an issue timestamp and a SHA-256
  checksum. Filename: `vulos-recovery-kit-<ulid>-<date>.json`.
- The **QR code encodes a verification token** of the form
  `vulos-recovery:v1:<ulid>:<checksum-prefix>` — it lets you *verify* which kit
  belongs to which box. **It does not contain your seed or private keys.**

> **Important:** the downloadable JSON file and its QR do **not** contain your
> 24-word phrase or any private key. They are convenience/verification material.
> The 24-word phrase from step (a) is the only thing that can rebuild your
> identity, and it is only ever shown on screen — never in the download.

---

## Keeping it safe — the short version

1. **Write down the 24 words** during setup and store them offline (paper, a
   metal backup plate, a hardware seed store). Anyone with the phrase can recover
   your account; nobody without it can — not even you.
2. Keep the downloaded Recovery Kit JSON somewhere private too — it holds your
   storage access key, which is worth protecting even though it can't rebuild
   your identity on its own.
3. If you also connected S3 storage, remember the **encryption passphrase** — you
   will need it to add another device (see [ADD-DEVICE.md](ADD-DEVICE.md)) or to
   restore from a backup (see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md)).

---

## Related pages

- [ADD-DEVICE.md](ADD-DEVICE.md) — bring a second device onto the same account.
- [REMOVE-DEVICE.md](REMOVE-DEVICE.md) — revoke a lost or compromised device.
- [ACCOUNTS-ACCESS.md](ACCOUNTS-ACCESS.md) — admin vs regular users, step-up.
- [KEY-CEREMONY.md](KEY-CEREMONY.md) — the full key-derivation ceremony.
- [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) — backups and restoring from the phrase.
