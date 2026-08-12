# Identity & peering

Every Vulos instance has its own cryptographic identity — a Vula ID — and can talk to other instances directly: contact requests, messages, calls, AirDrop-style local Drop, and collaborative document sharing, all without a central account. This chapter explains what your identity is and where it lives, how devices pair and join your cluster, how Drop works on your LAN, how sharing falls back to a content-blind relay across the internet, and how key rotation, revocation, and the recovery phrase fit together.

The design premise is simple: **every Vulos instance is a server**. If you're running Vulos, you can receive — messages, files, and calls arrive at your box, gated by your explicit approval, without relay infrastructure or third-party accounts in the required path.

Related chapters: [USER-GUIDE.md](USER-GUIDE.md) for day-to-day use, [FILES.md](FILES.md) for file sharing specifics, [NETWORKING.md](NETWORKING.md) for how your box is reached, [SECURITY.md](SECURITY.md) for the wider security model, [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) for recovery procedures, and [COMMS.md](COMMS.md) for federated group chat/video (Matrix, Jitsi) when you want more than direct peer-to-peer messaging.

---

## The vula: your instance identity

Your box's peering identity is an **Ed25519 keypair**. The public key, base58-encoded with a prefix, is your **Vula ID**:

```
vulos:ed25519:9pJv3kQ8tW7xY2mN4rS6uA1bC5dE8fG2hJ4kL7nP9qR3
```

When combined with a server address for discovery and contact requests, it forms a **Vula address**:

```
vulos:ed25519:<base58-key>@your-box.example.com:443
```

The ID *is* the public key — anyone holding your Vula ID can verify signatures you make, with no key exchange or lookup step.

### Where identity lives on disk

The peering service owns `~/.vulos/peering/` (created on first boot, `backend/services/peering/`):

| Path | Contents |
|---|---|
| `~/.vulos/peering/identity/ed25519.priv` | Private key (raw 64 bytes, mode 0600) |
| `~/.vulos/peering/identity/ed25519.pub` | Public key (raw 32 bytes) |
| `~/.vulos/peering/identity/vulos_id` | Your Vula ID as text |
| `~/.vulos/peering/contacts.json` | Approved contact list |
| `~/.vulos/peering/inbox/`, `outbox/`, `media/`, `groups/`, `profile/` | Messages, pending deliveries, received files, groups, your profile |

If any identity file is missing on startup, a fresh keypair is generated. Separately, `~/.vulos/db/instance.json` holds the box's plain instance ULID and hostname — an identifier, not a key; the Vula ID is the cryptographic identity.

### Viewing and sharing your Vula ID

Open the **Peering** app — its **Identity** tab shows your display name, your Vula ID with a copy button, and a **Show QR** toggle that renders the ID as a QR code ("Scan to add this Vula instance"). The same data is available at `GET /api/peering/identity`.

### Backing up your identity (export / import)

You can export your identity key as an encrypted bundle and import it on a rebuilt box:

- `POST /api/peering/identity/export` with `{"passphrase": "..."}` returns a JSON bundle: the private key encrypted with ChaCha20-Poly1305 under an Argon2id-derived key (64 MiB memory cost — resistant to GPU cracking).
- `POST /api/peering/identity/import` with the bundle plus your passphrase restores the keypair, overwriting the current one.

This is a *manual* backup of the raw identity. The account-anchored recovery path below is the safety net when you didn't export in time.

---

## Contacts and the trust model

Peering is **closed by default**: *"No open inboxes. You don't receive anything from anyone until you approve them."* Every peer is in one of four states — Unknown, Pending, Approved, Blocked — shown as a legend in the Peering app.

- **Add someone**: Peering → Requests tab → enter their Vula ID or their server name (e.g. `alice.vulos.org`) plus an optional message. This sends a signed contact request (`POST /api/peering/contacts/request`).
- **Approve or block** incoming requests from the same tab. Only after mutual approval do messages, files, and calls flow.
- **Per-contact permissions**: each contact carries capability flags (message / media / call / video) you can edit from the Contacts tab. There is no ambient "any approved peer can do anything" authority.
- **Find people** by email or name through a directory you configure: `GET /api/peering/discover?email=...` proxies a lookup against whatever `VULOS_VERIFY_URL` names. There is **no default** (`discovery.go`'s `discoveryDefaultBaseURL` is `""`) — unset, the box logs "no directory configured — people discovery disabled" and every lookup returns empty without touching the network. Vulos the org runs no directory.

Under the hood, everything a peer sends arrives as a signed **envelope** — canonical JSON `{id, from, to, type, timestamp, payload, signature}` with an Ed25519 signature over the canonical bytes. Your box verifies the signature, checks the sender isn't revoked, and checks the approved-contacts list before anything is processed. The single exception is the first contact request itself, which is how an unknown peer introduces themselves.

### Messaging

Once approved, contacts can message each other from the Peering app. The mechanics are worth understanding because they explain the sovereignty properties of everything else in this chapter:

1. You send a message (`POST /api/peering/conversations/{conv_id}/send`).
2. Your box wraps it in a signed envelope and delivers it **directly to the recipient's box** at their `/api/peering/inbound/message` endpoint.
3. Their box verifies the signature and your contact status, stores the message in its local inbox, and shows it.

Conversations and history live entirely on the two boxes involved (`GET /api/peering/conversations`, `.../messages`). There is no server in the middle holding your chat log — the fallbacks below exist only for when the direct path is down.

### Worked example: Alice adds Bob

1. Bob opens Peering → Identity on his box and shows his QR (or copies `vulos:ed25519:...` and sends it to Alice out-of-band).
2. Alice opens Peering → Requests, pastes Bob's Vula ID (or just `bob.vulos.org` if his box has a public name), adds a note, and sends the request. Her box signs it with her identity key and delivers it to Bob's box.
3. Bob's Requests tab shows the pending request with Alice's identity. He approves it; his box records her as an approved contact with default permissions.
4. From now on their boxes talk directly: messages, Drop transfers, calls — each request signature-verified against the other's Vula ID, each capability gated by the contact permissions either side has granted.

No account was created anywhere, and no third party learned that Alice and Bob are contacts.

---

## Device pairing: joining your cluster

Peering connects *different people's* boxes. Pairing connects *your own* devices into one cluster that shares storage and syncs data (leaderless CRDT sync — see [ARCHITECTURE.md](ARCHITECTURE.md)).

### Join codes

An admin on an existing box can mint a short-lived join code:

```bash
curl -X POST -H "X-User-ID: <admin>" http://localhost:8080/api/cluster/join-code
# → {"short_code":"VULOS-7XK2-M4PQ-9RTV","qr_payload":"vulos://join/v1?...","expires_at":"..."}
```

- Format `VULOS-XXXX-XXXX-XXXX` (Crockford base32 — no confusable 0/O/1/I/L characters).
- Valid for **1 hour**, **single-use** — redemption deletes it.
- It carries scoped credentials for the cluster's shared storage bucket, so the new device never needs secrets copied by hand. The `qr_payload` encodes the same grant as a `vulos://join/v1?...` URL for QR transfer.
- The new device redeems it at `POST /api/setup/join-code` (unauthenticated setup path, rate-limited to 5 attempts/minute per IP). Expired codes return 410; used or unknown codes return 404.

### The join ceremony and what gets synced

In the setup wizard, choosing **Join Existing** asks for the cluster's storage details — bucket, region, access key, secret key — and the **cluster passphrase**. Then (`backend/services/joinsync/`):

1. The box verifies it can reach the bucket *and* that your passphrase is correct, by decrypting a well-known marker object in the bucket. A wrong passphrase fails before anything is written.
2. Storage credentials are saved to `~/.vulos/db/storage.json`. **The passphrase is never written to disk.**
3. The box enters *sync* boot mode and pulls the cluster state in the background: latest snapshot database plus the changeset tail. The wizard polls `GET /api/setup/join/status` and shows progress until sync completes.

After that, the device is a full peer of your cluster: it holds a complete, mergeable copy of the data and converges with your other boxes without a leader.

### The device key (TPM)

Independently of joining, every box maintains a **device key** in `~/.vulos/auth/tpm/` — backed by a hardware TPM when `/dev/tpmrm0` exists, otherwise a software-encrypted store. It seals secrets to *this machine* — passkey credentials, the device-PIN wrap, pairing and fleet-identity material, and integration credentials — and provides a stable device identity. Check what your box is using:

```bash
curl http://localhost:8080/api/auth/device/tpm/status
# → {"backend":"hardware","available":true,"device_path":"/dev/tpmrm0",...}
```

---

## Drop: AirDrop-style local sharing

Drop sends files to nearby Vulos instances — LAN-first, with an internet fallback.

### How discovery works

Your box advertises itself over **mDNS** as `_vula-drop._tcp.local` and continuously discovers peers doing the same. Requirements for the local path: both devices on the same LAN, with multicast DNS traffic allowed (some guest Wi-Fi networks block it — see [TROUBLESHOOTING.md](TROUBLESHOOTING.md)).

Who can see you is your choice — Drop tab → discoverability setting:

| Mode | Meaning |
|---|---|
| `everyone` | Any Vulos instance on the LAN sees you |
| `peers` *(default)* | Only approved contacts see you |
| `nobody` | Invisible to Drop discovery |

Two supplements cover the cases mDNS can't:

- **Proximity Code**: for devices on *different* networks, generate a 6-digit one-time code (valid 5 minutes or first use) on one device and type it on the other. The exchange goes through a rendezvous service you name with `VULOS_RENDEZVOUS_URL`. There is **no default** (`drop_proximity.go`'s `proxDefaultRendezvousBase` is `""`): unset, cross-network proximity redemption is disabled and only same-box/LAN redemption works.

### Sending and receiving

**To send**: Peering → Drop tab. Nearby peers appear as tiles (refreshed every 10 seconds); pick one, choose a file, send. The file uploads to your box's media store first, then transfers peer-to-peer.

**To receive**: the sender's box posts transfer metadata (name, size, type, sender identity) to yours; the transfer sits **pending** until you accept or decline from the Drop tab. Accepting pulls the file from the sender over a signed URL. One convenience: transfers from *approved contacts* can be auto-accepted if you enable that option — strangers always require an explicit accept.

---

## Calls and call history

Approved contacts (with the call permission) can call each other — WebRTC audio/video with signaling carried over the same signed-envelope channel, initiated from the Peering app's **Call** tab.

Your box keeps a local call log: direction, peer, outcome (completed / missed / rejected), start time, and duration, capped at the 500 most recent entries in `~/.vulos/peering/callhistory.json`. It's queryable at `GET /api/peering/call/history?limit=50` (newest first). The history stays on your box; no call metadata is reported anywhere.

---

## Cross-instance sharing and the relay

### Direct first, durable always

Delivery to a peer is a **direct, signed HTTPS request** to their box. When the peer is unreachable, your box does not lose the message:

- Every failed delivery lands in the **outbox** (`~/.vulos/peering/outbox/<peer>/…`) and retries on a widening schedule — 1s, 5s, 30s, 5 minutes, 1 hour, then hourly. Delivery acknowledgment removes it.
- When two boxes reconnect, they exchange anything missed since they last saw each other.

### The store-and-forward relay

For peers separated by NAT or long offline periods, Vulos supports an opt-in **relay**: any mutually trusted Vulos instance (yours or a friend's) willing to hold encrypted blobs until the recipient picks them up.

> **Your box can host one; it cannot yet use one.** Everything below describes the *serving* side, which is real and fully enforced. The **depositing** side is not wired: nothing in the repo POSTs to `/api/peering/relay/deposit` outside tests, no peering code reads a relay base URL to choose a relay for delivery, and the delivery ladder never falls back to store-and-forward. Treat this as a capability the box offers other implementations, not as a fallback your own messages take today.

The relay is deliberately **content-blind**:

- Deposits are opaque ciphertext, stored verbatim. The relay never holds a decryption key and never touches the crypto layer — it sees *who* is sending to *whom*, blob sizes, and timestamps, but not plaintext.
- Deposits are only accepted from Ed25519-verified senders who are mutually approved contacts of the recipient, with per-sender rate limits (100 deposits/hour) and nonce replay protection.
- Pickup requires the recipient to prove their identity with a fresh signed authorization; acknowledged blobs are deleted.

Running your own relay: it's off by default (`~/.vulos/peering/relay/config.json`, `enabled: false`). Limits when enabled: 25 MB per blob, 100 MB queued per recipient, 500 MB total (configurable), blobs expire after 72 hours by default (7-day hard cap).

**Trusting someone else's relay.** The intent is that before depositing with a relay you don't control, your box demands proof that the relay runs inside a verified trusted-execution enclave (AWS Nitro attestation, checked against pinned code measurements with a freshness window), with any verification failure a hard reject. The verifier is implemented (`services/peering/relay_attest.go`) but has **no non-test callers**, which follows from there being no depositing client at all. Even without attestation, the relay only ever holds ciphertext.

Large file shares ride the same principles — sealed, capability-scoped, resumable in bounded chunks through the relay. See [FILES.md](FILES.md) for the sharing UX.

### Shared documents (live collaboration)

Documents can be shared for real-time co-editing across instances (`backend/services/peering/collab_share.go`). The flow:

- **Share**: the owner sends a share invitation to an approved contact (`POST /api/peering/collab/share`), naming the document and the peer's permission: **edit** or **view**.
- **Receive**: the invitation arrives at the peer's box over the signed inbound channel and the document appears in their list with a *Shared* badge (owner's copy shows *Owned*). `GET /api/peering/collab/documents` lists both.
- **Permissions are enforced server-side on the receiving box**: a view-only peer receives live updates but any edit they attempt to push is rejected with 403. The owner can change a peer's level (`PUT /api/peering/collab/{doc_id}/perms`) at any time.
- **Leave or revoke**: a peer can leave a shared document; the owner can revoke access entirely (`DELETE /api/peering/collab/{doc_id}`).

Edits travel as CRDT operations between the participating boxes — concurrent edits merge without a coordinating server, and the inbound routes are gated by the same signature/contact checks as everything else.

---

## Key lifecycle and recovery

Long-lived identities need a plan for key hygiene, compromise, and loss. Vulos implements a full lifecycle: rotation, revocation, an account-anchored recovery path, and X3DH-style forward secrecy for message content.

### Rotation: planned key changes

While you still hold your current key, you can rotate to a fresh one. The box signs a **rotation certificate** — "old key X authorizes new key Y" — with the *old* key, and appends it to its lifecycle chain. Peers fetching your public profile (`GET /.well-known/vula-id`) see the chain, verify each hop's signature, and follow it to your current key. Their contact entry still names your original ID; the chain maps it forward. An invalid or out-of-order link stops the chain cold — peers never guess.

### Revocation: killing a key

A key can be revoked by a **revocation certificate**, signed either by the key itself (self-revocation) or by your recovery anchor (for a key you've lost control of). Revoke your own box's identity with:

```bash
curl -X POST -H "X-User-ID: <you>" \
  -d '{"reason":"compromised"}' \
  http://localhost:8080/api/peering/identity/revoke
```

This endpoint is owner-only by design — a remote peer cannot revoke your identity. Revocations are published in your well-known profile; peers that ingest them refuse *all* signatures from the revoked key thereafter, at every verification point (messages, prekeys, relay deposits). There is no active broadcast: peers learn on their next fetch of your profile.

### The recovery anchor: surviving a lost key

Rotation requires the old key. What if it's simply gone — disk dead, box stolen?

Vulos derives a **recovery anchor** — a second, independent Ed25519 key — deterministically from your account recovery phrase (via HKDF with a dedicated label, so the anchor is *not* derivable from anything stored on the box). When you generate your recovery kit, the box computes the anchor, immediately discards the private half, and keeps **only the anchor's public ID** on disk. Peers TOFU-pin your anchor the first time they see your profile and refuse to accept a different one later (anti-takeover).

If your identity key is lost:

1. From your 24-word recovery phrase — entirely offline if you wish — the anchor private key is re-derived.
2. It signs a **recovery certificate**: "identity X is succeeded by new identity Z, authorized by anchor A."
3. Peers who pinned your anchor verify and follow the succession to your new key. A lost key stops being terminal.

A thief who images your disk gets the identity key (revocable) and the anchor's *public* ID (harmless) — never the ability to forge a recovery.

### Forward secrecy: X3DH sessions

Message content between peers is encrypted with per-session keys established X3DH-style (the Signal pattern):

- Each identity publishes a **signed prekey** (medium-term X25519 key, signed by the identity key) and a pool of **one-time prekeys**. The signed prekey is served from your well-known profile; one-time prekeys are handed out singly via `POST /api/peering/prekeys/claim` and **deleted on hand-out** — never reused.
- A sender combines an ephemeral key with your identity key, signed prekey, and (when available) one one-time prekey through X25519 + HKDF-SHA256; content is sealed with XChaCha20-Poly1305.
- **What this buys you**: with a one-time prekey in play, even a later compromise of both long-term keys cannot retroactively decrypt a captured message — the required one-time and ephemeral secrets no longer exist. If the one-time pool is exhausted, sessions fall back to signed-prekey-only, where forward secrecy holds against identity-key compromise and the exposure window is bounded by prekey rotation. The pool replenishes automatically in the background (hourly).
- **Honest limitation**: this is session-establishment forward secrecy, not a per-message double ratchet.

Peers that only support the older static-key scheme still interoperate — the sender detects the peer's capability and uses the best mutually supported version.

### What the recovery phrase does — and does not — recover

Vulos shows you a **24-word recovery phrase** at signup and forces you to save it ("Without your recovery phrase, a forgotten password cannot be recovered"). Be precise about its powers:

**Recoverable with the phrase:**

- **Your master key**, and therefore all content encrypted under it (mail, files, anything using derived content keys). Forgot your password? `POST /api/auth/masterkey/recover` proves phrase possession, re-wraps the master key under a new password, and revokes all sessions — the server never sees the key.
- **Your account identity keys and keyring root** (via the recovery-kit restore path), and with them the **peering recovery anchor** — the lost-Vula-key succession described above.

**Not recoverable with the phrase:**

- **The password-manager vault** (`Vault` app, `backend/services/credvault/`): encrypted under its *own* master password (Argon2id + AES-256-GCM). No phrase escrow exists — lose that password and `vault.enc` is gone for good.
- **The authenticator's TOTP secrets** (`backend/services/authvault/`): encrypted under a random local keyfile (`~/.vulos/auth/totp/<user>/keyfile`). Lose the keyfile (and any export), lose the 2FA secrets.
- **A lost Vula identity key when no recovery kit was ever generated**: without a published anchor, peers have nothing to follow — you start a new identity and re-add contacts.
- **Past message session keys**: forward secrecy means even *you* cannot retroactively decrypt captured ciphertexts whose one-time keys are gone. That's the point.

**About "trusted devices":** there is no named trusted-device registry. Any device with an active signed-in session holds your unwrapped master key in browser memory and can reset a forgotten password without the phrase (the re-wrap happens client-side; the server checks only wire-format invariants and revokes your other sessions). Treat live sessions accordingly, and see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) for full recovery walkthroughs.

---

## Where everything lives: quick reference

| Path | What |
|---|---|
| `~/.vulos/peering/identity/` | Ed25519 keypair, Vula ID, lifecycle chain (`lifecycle.json`), anchor ID (`recovery_anchor.json`) |
| `~/.vulos/peering/contacts.json` | Approved contacts and permissions |
| `~/.vulos/peering/inbox/` · `outbox/` · `media/` · `groups/` | Conversations, pending deliveries, received files, groups |
| `~/.vulos/peering/callhistory.json` | Local call log (max 500 entries) |
| `~/.vulos/peering/relay/` | Relay config + held blobs (only if you run a relay) |
| `~/.vulos/db/instance.json` | Instance ULID and hostname |
| `~/.vulos/db/joincodes.json` · `storage.json` | Outstanding join codes; cluster storage credentials |
| `~/.vulos/auth/tpm/` | Device key (TPM-backed or software) |

| Env var | Purpose |
|---|---|
| `VULOS_RELAY_BASE_URL` | Store-and-forward relay base URL |
| `VULOS_RENDEZVOUS_URL` | Drop proximity-code rendezvous. **No default** — unset disables cross-network redemption (LAN/same-box only). |
| `VULOS_VERIFY_URL` | Directory for people lookup. **No default** — unset disables people discovery entirely. |

### API quick reference

| Area | Endpoints |
|---|---|
| Identity | `GET /api/peering/identity` · `POST /api/peering/identity/export` · `POST /api/peering/identity/import` · `POST /api/peering/identity/revoke` |
| Contacts | `POST /api/peering/contacts/request` · `GET /api/peering/contacts` · `GET /api/peering/contacts/requests` · `POST /api/peering/contacts/approve/{id}` · `POST /api/peering/contacts/block/{id}` · `DELETE /api/peering/contacts/{vulos_id}` |
| Discovery | `GET /api/peering/discover?email=…` (or `?name=…`) |
| Messaging | `GET /api/peering/conversations` · `GET /api/peering/conversations/{conv_id}/messages` · `POST /api/peering/conversations/{conv_id}/send` |
| Drop | `GET /api/peering/drop/nearby` · `POST /api/peering/drop/send` · `POST /api/peering/drop/decide` · `GET`/`PUT /api/peering/drop/settings` · `POST /api/peering/drop/code/generate` · `POST /api/peering/drop/code/redeem` |
| Calls | `POST /api/peering/call/initiate` (and `answer`/`reject`/`signal`/`hangup`) · `GET /api/peering/call/history` |
| Shared docs | `POST /api/peering/collab/share` · `GET /api/peering/collab/documents` · `GET`/`DELETE /api/peering/collab/{doc_id}` · `PUT /api/peering/collab/{doc_id}/perms` |
| Cluster join | `POST /api/cluster/join-code` (admin) · `POST /api/setup/join-code` · `POST /api/setup/join` · `GET /api/setup/join/status` |
| Device key | `GET /api/auth/device/identity` · `GET /api/auth/device/tpm/status` |
| Public profile | `GET /.well-known/vula-id` (Vula ID, lifecycle chain, revocations, signed prekey — public fields only) |

Everything under `/api/peering/inbound/*` is the box-to-box surface: it authenticates the *sending peer's signature*, not an OS login, and is not meant to be called by you directly.

Back up `~/.vulos/peering/identity/` (or use the encrypted export) and keep your recovery phrase offline — together they make your peering identity effectively indestructible.
