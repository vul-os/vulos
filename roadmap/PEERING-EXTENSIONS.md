# PEERING EXTENSIONS

Extensions to the peering system ([PEERING.md](PEERING.md)) that address its architectural limitations using emerging cryptographic primitives and protocols. These are opt-in layers — the base peering system works without any of them. Each extension adds capability without changing the core identity, trust, or messaging model.

---

## 1. Relay Peers (Offline Delivery)

**Problem:** if both sender and recipient servers are offline simultaneously, messages stall. Bare metal devices that sleep, mobile instances with intermittent connectivity, power outages — all cause delivery gaps with no third-party relay.

### How It Works

A relay peer is any Vula instance that both parties trust, willing to hold encrypted messages in transit. The relay never sees plaintext — messages are encrypted to the recipient's public key before leaving the sender.

```
Alice (offline) ──x──► Bob (offline)
                          
Alice (online) ──► Carol (relay) ──► Bob (comes online later)
```

Carol holds the ciphertext. When Bob's server comes back, Carol delivers. Carol cannot read the message — she sees an opaque blob addressed to Bob's Vula ID.

### Relay Selection

Automatic, not manual. Each contact relationship can designate relay peers:

```json
{
  "vula_id": "vula:ed25519:5Hb7...",
  "display_name": "Bob",
  "relay_peers": [
    {
      "vula_id": "vula:ed25519:7Cx3...",
      "display_name": "Carol",
      "server": "carol.vulos.org:8080",
      "capacity_mb": 100,
      "ttl_hours": 72
    }
  ]
}
```

**Selection rules:**
- Must be an approved contact of BOTH parties (mutual trust triangle)
- Both parties must explicitly enable relay through that peer
- Relay peer must opt into being a relay (not automatic — costs storage and bandwidth)
- Multiple relay peers for redundancy

### TEE-Backed Relays

For relays you don't personally trust (community relays, commercial relay services), **Trusted Execution Environments** provide cryptographic guarantees:

```
Sender ──► TEE Enclave on Relay ──► Recipient

The enclave:
  ✓ Receives ciphertext
  ✓ Stores until recipient is reachable
  ✓ Forwards to recipient
  ✗ Cannot read message content
  ✗ Cannot log metadata (sender/recipient pairs)
  ✗ Cannot modify the message
  
Remote attestation proves the enclave runs the expected code.
```

**Hardware support:**
- Intel SGX / TDX (server-grade)
- ARM TrustZone (mobile, embedded — relevant for bare metal Vula devices)
- AWS Nitro Enclaves (cloud Vula instances)
- AMD SEV-SNP (server-grade)

The relay operator can't tamper even if they want to. The sender verifies the TEE attestation before sending. If attestation fails, the relay is rejected.

### Relay Protocol

```
POST /api/peering/relay/deposit
  Body: { to: <vula_id>, blob: <encrypted>, ttl: 72h, signature: <sender_sig> }
  → Relay stores blob, indexed by recipient Vula ID

GET /api/peering/relay/pickup
  Headers: Authorization: <recipient_signature_of_timestamp>
  → Returns all pending blobs for this Vula ID, deletes after delivery ACK

POST /api/peering/relay/ack
  Body: { blob_ids: [...] }
  → Confirms receipt, relay deletes stored blobs
```

**Limits per relay peer:**
- Max stored per recipient: 100 MB (configurable by relay operator)
- Max TTL: 72 hours default, 7 days maximum
- Max blob size: 25 MB (larger files use chunked transfer)
- Rate limit: 100 deposits/hour per sender

### Storage on Relay

```
~/.vulos/peering/relay/
  ├── config.json          (relay settings: enabled, capacity, TTL, allowed peers)
  ├── store/
  │   ├── <recipient_id>/  (pending blobs per recipient)
  │   │   ├── <blob_id>.enc
  │   │   └── ...
  │   └── ...
  └── stats.json           (usage: stored bytes, deliveries, uptime)
```

---

## 2. Cluster Anycast (High Availability)

**Problem:** a popular peer's single instance becomes a bottleneck. If Alice has 300 contacts and her server goes down, she's completely unreachable. The cluster system (CLUSTER.md) already syncs state across nodes but doesn't solve routing.

### How It Works

Alice runs multiple Vula nodes (home server, cloud VPS, office machine). All share her identity via cluster sync. When Bob sends to Alice, his server tries all of Alice's known endpoints — first response wins.

```
Bob's server ──► Alice node 1 (home, 50ms)     ← winner
             ──► Alice node 2 (cloud, 120ms)
             ──► Alice node 3 (office, offline)  ← skip
```

### Discovery of Multiple Nodes

Alice's Vula ID resolves to multiple endpoints:

```json
GET https://alice.vulos.org/.well-known/vula-id
{
  "vula_id": "vula:ed25519:5Hb7...",
  "display_name": "Alice",
  "endpoints": [
    { "server": "home.alice.vulos.org:8080", "priority": 1 },
    { "server": "cloud.alice.vulos.org:8080", "priority": 2 },
    { "server": "office.alice.vulos.org:8080", "priority": 3 }
  ]
}
```

**DNS-level:** multiple A/AAAA records for `alice.vulos.org`, geo-weighted. Cloudflare/route53 health checks remove dead nodes automatically.

**Peer-level:** Bob's server caches Alice's endpoint list, pings periodically, sorts by latency. Failover is automatic — if the primary is unreachable, the next endpoint takes over within seconds.

### Consistency

All nodes share the same identity, contacts, inbox, and notification state via S3/MinIO (cluster layer). A message delivered to any node is visible from all nodes. The challenge is deduplication — if Bob's server hits two of Alice's nodes simultaneously:

- Messages carry UUIDv7 IDs — idempotent delivery
- Receiving node writes to S3, other nodes sync
- Duplicate detection on ID — second delivery is a no-op

### Endpoint Registration API

```
POST /api/peering/endpoints/register    → add a node to your endpoint list
DELETE /api/peering/endpoints/:id       → remove a node
GET /api/peering/endpoints              → list all your nodes + health status
PUT /api/peering/endpoints/:id/priority → change failover order
```

vulos.org API addition:
```
PUT /api/identity/endpoints  → update endpoint list for your slug (syncs to .well-known)
```

---

## 3. Signed Feeds (Asymmetric Publishing)

**Problem:** peering requires mutual approval. There's no way to publish content to followers who haven't been individually approved. Blogs, project updates, changelogs, community announcements — these are one-to-many, not bilateral.

### How It Works

A feed is a signed append-only log. The owner publishes entries, subscribers pull. No mutual approval needed — feeds are public or link-gated.

```
Alice publishes ──► signed entry added to her feed log
                        │
Bob (subscriber) ◄──── pulls new entries from Alice's server
Carol (subscriber) ◄── pulls new entries from Alice's server
Dave (not subscribed)   doesn't see anything
```

Each entry is signed with Alice's Ed25519 key — verifiable by anyone who knows her Vula ID, no trust relationship required.

### Feed Entry Format

```json
{
  "id": "uuid-v7",
  "feed_id": "vula:ed25519:5Hb7.../blog",
  "author": "vula:ed25519:5Hb7...",
  "sequence": 42,
  "timestamp": "2026-04-02T14:30:00Z",
  "type": "post",
  "body": {
    "title": "New feature: Drop sharing",
    "content": "We shipped LAN discovery today...",
    "media": []
  },
  "prev_hash": "<sha256 of entry 41>",
  "signature": "<ed25519-signature>"
}
```

**Append-only log:** each entry references the hash of the previous entry, forming a chain. Tamper-evident — you can't edit or delete past entries without breaking the chain. This is a feature, not a limitation (transparency). If you want to retract, you publish a retraction entry.

### Feed Types

| Type | Use Case | Access |
|------|----------|--------|
| `public` | Blog, project updates, announcements | Anyone with the feed URL |
| `peers` | Updates for approved contacts only | Approved contacts, pulled automatically |
| `link` | Shareable but not discoverable | Anyone with the direct feed link |

### Subscription

Subscribers don't need to be approved contacts. They just poll the feed:

```
GET /api/feeds/<feed_id>/entries?since=<sequence_number>
→ returns new entries since that sequence number
```

**Push option for approved contacts:** if the subscriber IS an approved contact, the publisher's server can push new entries via the existing peering notification system ([NOTIFICATIONS.md](NOTIFICATIONS.md), `event.feed_update`). Non-contacts poll.

**Peer-assisted distribution:** subscribers who have entries can serve them to other subscribers (see Section 4, Gossip). This offloads the publisher's server.

### Content-Addressing

Every feed entry gets a content hash: `sha256(<canonical-json>)`. This hash is the entry's permanent address. Any peer who has the entry can serve it — verifiable by hash + signature. The publisher's server is the origin, but not the only source.

This is the foundation for peer-assisted relay: if Bob already has Alice's feed entry #42, Carol can fetch it from Bob instead of Alice. The signature proves it's authentic regardless of who served it.

### Server API

```
POST   /api/feeds                        → create a new feed
GET    /api/feeds                        → list your feeds
GET    /api/feeds/:feed_id               → feed metadata
POST   /api/feeds/:feed_id/publish       → publish a new entry
GET    /api/feeds/:feed_id/entries       → list entries (paginated, filterable by since/type)
DELETE /api/feeds/:feed_id               → archive feed (entries remain, no new posts)
```

Public/link feeds are served without authentication:
```
GET /api/feeds/:feed_id/entries          → no auth required for public/link feeds
```

### Storage

```
~/.vulos/peering/
  └── feeds/
      ├── own/
      │   ├── <feed_id>/
      │   │   ├── meta.json          (title, type, description, access level)
      │   │   ├── entries/           (signed entry files, append-only)
      │   │   └── media/            (attached media)
      │   └── ...
      └── subscriptions/
          ├── <feed_id>/
          │   ├── meta.json          (publisher info, last synced sequence)
          │   ├── entries/           (cached entries)
          │   └── media/
          └── ...
```

---

## 4. Gossip Protocol (Scalable Group Delivery)

**Problem:** large groups require fan-out — every message sent to N servers individually. A 200-person group means the sender's server makes 200 HTTPS requests. This crushes bandwidth and doesn't scale.

### How It Works

Instead of the sender delivering to every member, messages propagate through the group epidemically. Each member who receives a message forwards it to a few peers who haven't seen it yet. Full propagation in O(log N) hops.

```
Alice sends message to 200-person group:
  Alice → Bob, Carol, Dave           (3 peers, chosen randomly)
  Bob → Eve, Frank, Grace            (3 more)
  Carol → Heidi, Ivan, Judy          (3 more)
  ...
  Full propagation in ~5 hops         (log₃(200) ≈ 5)
```

Total messages sent by Alice: 3 (not 200). Total messages across the network: ~600 (3× the group size, each message forwarded ~3 times on average). But the load is distributed — no single server is overwhelmed.

### Protocol

Each group member maintains a short list of "gossip peers" — other members they exchange updates with:

```json
{
  "group_id": "uuid-v7",
  "gossip_peers": [
    { "vula_id": "vula:ed25519:5Hb7...", "last_seen_seq": 1042 },
    { "vula_id": "vula:ed25519:9Kx2...", "last_seen_seq": 1041 },
    { "vula_id": "vula:ed25519:3Mn8...", "last_seen_seq": 1042 }
  ]
}
```

**Sync cycle (every few seconds while active, longer when idle):**

```
1. Pick a gossip peer at random
2. Exchange "state vectors" (what's the latest sequence you've seen?)
3. Send any entries the peer is missing
4. Receive any entries you're missing
```

This is the same state-vector pattern Yjs uses for CRDT sync (PEERING.md, Real-Time Collaboration) — applied to message delivery instead of document ops.

### Anti-Entropy

Gossip is probabilistic — a message might not reach everyone on the first wave. Anti-entropy repairs the gaps:

- Each member periodically does a full state-vector comparison with a random peer
- Missing entries are pulled
- Guaranteed eventual consistency — every member converges to the same state
- Convergence time for a 200-person group: <30 seconds typical, <2 minutes worst case

### Peer Selection

Not fully random — weighted for efficiency:

- Prefer peers with low latency (same region/network)
- Prefer peers that are frequently online (high uptime)
- Rotate gossip peers periodically to avoid cliques
- Each member gossips with 3-5 peers (tunable: higher = faster propagation, more bandwidth)

### When to Use Gossip vs. Fan-Out

| Group Size | Delivery Method | Reason |
|-----------|----------------|--------|
| 2-10 | Direct fan-out | Low overhead, simplest |
| 11-50 | Fan-out with backpressure | Manageable, latency matters for calls |
| 51+ | Gossip | Fan-out doesn't scale, gossip distributes load |

The threshold is configurable. Groups automatically switch delivery method as membership changes.

### Inbound

```
POST /api/peering/inbound/gossip-sync    → receive gossip state vector exchange
POST /api/peering/inbound/gossip-push    → receive gossip entries from a peer
```

---

## 5. MLS (Large Group Encryption)

**Problem:** the current peering spec uses pairwise encryption — each message encrypted separately for each recipient. For a 200-person group, that's 200 encryption operations per message and 200× the ciphertext size. Doesn't scale.

### What MLS Is

**Messaging Layer Security (RFC 9420, ratified 2023).** A protocol designed specifically for group end-to-end encryption. Instead of pairwise keys, the group shares a tree-based key structure where:

- Adding a member: O(log N) operations
- Removing a member: O(log N) operations
- Encrypting a message: O(1) — one encryption, one ciphertext, readable by all members
- Forward secrecy: keys rotate on every membership change
- Post-compromise security: a compromised member's past messages stay protected after they're removed

### How It Fits Vula

MLS replaces the encryption layer for groups, not the delivery layer. Messages still flow via gossip (Section 4) or fan-out (small groups). MLS handles who can read them.

```
Alice sends to 200-person group:
  1. Alice encrypts message once using the group's MLS key
  2. Single ciphertext delivered via gossip to all members
  3. Each member decrypts using their leaf key in the MLS tree
  
Without MLS: 200 encryptions, 200 different ciphertexts
With MLS: 1 encryption, 1 ciphertext
```

### MLS Tree Structure

```
                    [Root Key]
                   /          \
            [Node]              [Node]
           /      \            /      \
      [Node]    [Node]    [Node]    [Node]
      /    \    /    \    /    \    /    \
    Alice  Bob Carol Dave Eve  Frank Grace Heidi
    (leaf keys — each member holds their own)
```

Each member knows their path from leaf to root. They can decrypt messages encrypted to the root key. When a member is removed, the tree updates — new root key, old member's leaf pruned. O(log N) updates propagated to remaining members.

### Key Packages

Each Vula instance publishes an MLS key package (a bundle of public keys for joining groups):

```json
{
  "vula_id": "vula:ed25519:5Hb7...",
  "mls_key_package": "<base64-encoded-mls-key-package>",
  "cipher_suite": "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519",
  "created_at": "2026-04-02T00:00:00Z"
}
```

Key packages are pre-published so you can be added to a group even while offline. Your server stores a pool of single-use key packages:

```
POST /api/peering/mls/key-packages          → upload new key packages (batch)
GET  /api/peering/mls/key-packages/:vula_id → fetch a peer's key package (consumes one)
```

### Group Lifecycle

```
Create:
  1. Creator initializes MLS group with their own leaf
  2. For each member: fetch their key package, add to tree
  3. Distribute Welcome message (contains group state) to all members
  
Message:
  1. Sender encrypts with current epoch's key
  2. Single ciphertext distributed via gossip/fan-out
  3. All members decrypt with their leaf key

Add member:
  1. Fetch new member's key package
  2. Create Add proposal + Commit
  3. Distribute Commit to existing members, Welcome to new member
  4. Tree updates, new epoch key — O(log N)

Remove member:
  1. Create Remove proposal + Commit
  2. Distribute Commit to remaining members
  3. Tree updates, new epoch key — removed member can't decrypt future messages
```

### Library

[OpenMLS](https://github.com/openmls/openmls) — Rust implementation, mature, audited. Can be called from Go via CGO or compiled to WASM for browser-side operations. Alternatively, [mls-rs](https://github.com/AmazonCorr/mls-rs) from Amazon (also Rust, also audited).

### Relationship to Existing E2E

PEERING.md specifies X25519 + XChaCha20-Poly1305 for 1-on-1 message encryption. That stays. MLS is for groups only — it's overkill for two people. The boundary:

| Conversation | Encryption |
|-------------|-----------|
| 1-on-1 | X25519 + XChaCha20-Poly1305 (PEERING.md) |
| Group (3+) | MLS (this spec) |

---

## 6. Ring Signatures (Anonymous Group Participation)

**Problem:** every message in the peering system is signed by the sender's Ed25519 key — everyone knows who said what. Sometimes you need to say something without revealing which group member you are. Whistleblowing, anonymous feedback, sensitive topics, voting.

### What Ring Signatures Are

A ring signature proves "one of these N people signed this" without revealing which one. The verifier is convinced the signer is a group member, but can't identify them within the group.

```
Group members: Alice, Bob, Carol, Dave, Eve

Bob signs with ring signature using all 5 public keys.
Verifier sees: "signed by one of {Alice, Bob, Carol, Dave, Eve}"
Verifier cannot determine: it was Bob.
```

### How It Fits Vula

An opt-in mode for group conversations. The group creator or members can enable "anonymous mode" for specific threads or the entire group.

```json
{
  "id": "uuid-v7",
  "from": "ring:<group_id>",
  "to": "group:uuid-v7",
  "timestamp": "2026-04-02T14:30:00Z",
  "type": "text",
  "body": "I think we should reconsider this approach",
  "ring_signature": "<ring-signature-over-group-members>",
  "ring_members": ["vula:ed25519:5Hb7...", "vula:ed25519:9Kx2...", ...],
  "signature": null
}
```

The `from` field is the group ID, not a specific member. The `ring_signature` proves the sender is in `ring_members`. The individual `signature` field is null — replaced by the ring signature.

### Use Cases

| Use Case | How |
|----------|-----|
| Anonymous feedback | Post to a group thread in ring-signed mode |
| Voting / polling | Each vote is ring-signed — verifiable that only members voted, but not who voted for what |
| Whistleblowing | Report to a group where the reporter's identity is protected |
| Sensitive discussion | Enable anonymous mode for a thread about layoffs, compensation, etc. |

### Linkability Control

Two modes:

**Unlinkable:** each message is independently anonymous. Bob could post 5 messages and no one can tell if they're from the same person or 5 different people. Maximum privacy, but enables spam from a single member.

**Linkable (pseudonymous):** messages from the same signer are linked by a deterministic tag derived from their key + the group ID. Verifiers see "Person X posted 5 messages" but don't know X is Bob. Prevents one person from flooding while maintaining anonymity.

The group chooses which mode. Default: linkable (pseudonymous) to prevent abuse.

### Cryptographic Primitive

**Borromean ring signatures** or **LSAG (Linkable Spontaneous Anonymous Group) signatures** — both well-studied, efficient for groups up to ~100 members. Signature size scales linearly with group size (each member adds ~32 bytes), so a 50-person group produces a ~1.6KB signature. Acceptable for messages, too large for high-frequency events.

### Server API

```
POST /api/peering/groups/:id/anonymous    → toggle anonymous mode for a group
GET  /api/peering/groups/:id/anonymous    → check if anonymous mode is enabled
```

No new inbound endpoints needed — ring-signed messages use the same delivery path as regular messages, verified differently.

---

## 7. Zero-Knowledge Discovery

**Problem:** finding peers beyond your existing network requires either direct exchange (in person), domain lookup, or the opt-in directory. There's no way to discover peers by attributes ("people at my company", "people in my city") without a central directory seeing who's searching for whom.

### How It Works

Zero-knowledge proofs let a peer prove a property about themselves without revealing their identity or the underlying data.

```
Alice wants to find other Vula users at her company (acme.com).

Without ZK:
  Alice → Directory: "show me users with @acme.com emails"
  Directory learns: Alice works at Acme and is looking for coworkers

With ZK:
  Alice → Directory: "here's a proof that my email domain is X. Show me users who proved the same."
  Directory learns: someone proved domain membership. Cannot see the domain or who asked.
```

### ZK Proof of Email Domain

Built on the existing email verification flow (PEERING.md, Email Verification):

```
1. Alice verifies alice@acme.com through vulos.org (already happens)
2. Alice's instance generates a ZK proof:
   "I hold a valid verification token from vulos.org,
    and my email's domain is H(acme.com)"  (H = hash)
3. Alice publishes this proof to the discovery service
4. Bob does the same for bob@acme.com
5. Both proofs contain H(acme.com) — the service matches them
6. Alice and Bob are introduced (Vula IDs exchanged)
7. The service never saw "acme.com", only the hash
```

### ZK Proof of Location (Approximate)

For "people near me" without revealing exact location:

```
1. Alice's instance determines her geohash (precision: ~5km)
2. Generates ZK proof: "my geohash prefix is H(<geohash-prefix>)"
3. Published to discovery. Matched with others in the same cell.
4. Discovery service sees the hash, not the location.
```

Precision is user-controlled — share a coarse geohash (city-level) or fine (neighborhood-level).

### ZK Proof of Group Membership

"I'm a member of this community / organization / team" without revealing which member:

```
1. Group admin publishes a Merkle root of member Vula IDs
2. Alice generates a ZK proof of Merkle inclusion
3. Published to discovery: "I'm in group with root <hash>"
4. Others in the same group are matched
```

This enables "find people from my organization" without a centralized employee directory.

### Cryptographic Primitive

**ZK-SNARKs** (using Groth16 or PLONK) for email/location proofs — proof generation takes ~1 second on modern hardware, verification takes ~5ms. Proof size: ~200 bytes. Libraries: [gnark](https://github.com/ConsenSys/gnark) (Go-native, production-ready, fits the stack).

### Privacy Budget

Each ZK proof reveals a little information (hash of domain, approximate location). Multiple proofs from the same user could be correlated. Mitigations:

- Rate limit proof publications (1 per day per proof type)
- Proofs expire and must be regenerated (24-hour TTL)
- Different random salts per proof period — proofs from Monday can't be linked to proofs from Tuesday
- User controls which proof types to publish (domain only, location only, both, neither)

### Discovery Service

Runs on vulos.org as a lightweight match-maker:

```
POST /api/discovery/publish    → submit a ZK proof + proof type
GET  /api/discovery/matches    → retrieve matched Vula IDs (peers who proved the same attribute)
```

The service stores hashed attributes and proofs. It can verify proofs are valid but cannot extract the underlying data. Stateless by design — proofs expire, no long-term storage.

---

## 8. Compliance Extensions (Organizational Use)

**Problem:** E2E encryption + no central server = no way for an organization to audit communications. Enterprises need message retention, legal hold, and data loss prevention. Peering's architecture is the opposite of what compliance requires — by design.

### Approach: Transparent, User-Visible Compliance

Not surveillance. Not backdoors. Organizational compliance as an explicit, visible policy that users consent to when joining an org-managed group. The UI clearly shows "this conversation is subject to organizational audit."

### Threshold Key Escrow

The organization holds a key split across M-of-N administrators. Messages in org-managed groups are encrypted to the recipient AND to the org escrow key:

```
Message encryption:
  1. Encrypt to recipient (normal E2E)
  2. Also encrypt to org escrow key (threshold)
  
Org escrow key:
  Split across 5 administrators, threshold 3-of-5
  No single admin can decrypt
  Requires 3 admins acting together (auditable)
```

**Shamir's Secret Sharing** splits the org key. Reconstruction requires M shares from N administrators. The split happens once at org setup. Each admin stores their share securely (ideally in a hardware security module or TEE).

### Escrow Scope

Not all conversations — only org-managed groups and channels:

| Conversation Type | Escrow | Reason |
|-------------------|--------|--------|
| Personal 1-on-1 | Never | Private, not org business |
| Personal group | Never | User-created, not org business |
| Org channel | Yes | Created by org, subject to policy |
| Org direct message | Optional | Policy-dependent, user warned |

Users see a clear badge: `🔒 Org-auditable` on conversations where escrow is active. No hidden surveillance — the presence of escrow is visible to all participants.

### ZK Audit Proofs

For compliance checks that don't require reading message content — prove properties about messages without revealing them:

**"No sensitive data was shared":**
```
ZK proof that message body does not match regex patterns
(SSNs, credit card numbers, classified markers)
→ Auditor sees: PASS/FAIL
→ Auditor does not see: message content
```

**"All messages were from authorized senders":**
```
ZK proof that all message signatures in a time range
correspond to Vula IDs in the org's member list
→ Auditor sees: PASS/FAIL
→ Auditor does not see: individual messages
```

**"Retention policy was met":**
```
ZK proof that messages older than X days have been archived
and messages within retention period are intact
→ Auditor sees: PASS/FAIL
→ Auditor does not see: message content or metadata
```

These proofs are generated by the Vula instance automatically, submitted to the org's compliance system. The compliance system can verify without accessing message content.

### Legal Hold

When an org issues a legal hold on a user's communications:

1. Org admin sends a hold notice to the user's Vula instance (signed by org key)
2. Instance disables message expiry for the affected conversations
3. Instance acknowledges the hold (cryptographic receipt)
4. User is notified: "Your messages in [Channel] are under legal hold"
5. Messages are preserved until the hold is lifted

The instance cooperates because the user consented to org policies when joining. If the instance doesn't cooperate (user leaves org, device lost), the escrowed copies in the org's threshold-encrypted archive serve as backup.

### Server API

```
POST /api/org/escrow/setup         → initialize threshold escrow (admin only, M-of-N config)
POST /api/org/escrow/reconstruct   → submit key share for reconstruction (requires M shares)
POST /api/org/audit/zk-verify      → verify a ZK audit proof
POST /api/org/hold/issue           → issue legal hold on a conversation
POST /api/org/hold/release         → release legal hold
GET  /api/org/hold/status          → list active holds
GET  /api/org/compliance/report    → aggregate ZK audit results
```

---

## Relationships Between Extensions

These extensions are independent but compose naturally:

```
                    ┌─────────────────┐
                    │   Base Peering   │ ← PEERING.md
                    │  (Identity,      │
                    │   Trust, E2E)    │
                    └────────┬────────┘
                             │
          ┌──────────────────┼───────────────────┐
          │                  │                    │
    ┌─────▼─────┐    ┌──────▼──────┐    ┌───────▼───────┐
    │  Relay     │    │  Signed     │    │  Cluster      │
    │  Peers (1) │    │  Feeds (3)  │    │  Anycast (2)  │
    └─────┬─────┘    └──────┬──────┘    └───────────────┘
          │                  │
          │           ┌──────▼──────┐
          │           │  Gossip     │ ← feeds + groups use gossip for distribution
          │           │  Protocol(4)│
          │           └──────┬──────┘
          │                  │
          │           ┌──────▼──────┐
          │           │  MLS (5)    │ ← groups use MLS for encryption
          │           └──────┬──────┘
          │                  │
          │           ┌──────▼──────┐
          │           │  Ring       │ ← groups can enable anonymous mode
          │           │  Sigs (6)   │
          │           └─────────────┘
          │
    ┌─────▼──────────┐    ┌─────────────────┐
    │  ZK Discovery  │    │  Compliance (8)  │ ← org layer, sits alongside
    │  (7)           │    │  (threshold,     │    not on top of, other extensions
    └────────────────┘    │   ZK audit)      │
                          └─────────────────┘
```

**Dependency chain:**
- Gossip (4) depends on base peering (groups exist)
- MLS (5) depends on base peering (identity, groups) — optional upgrade to group encryption
- Ring Signatures (6) depend on groups + MLS (ring over MLS group members)
- Signed Feeds (3) can use Gossip (4) for distribution but works without it
- Relay Peers (1), Cluster Anycast (2), ZK Discovery (7), Compliance (8) are independent

---

## Implementation Order

Ordered by impact and dependency:

1. **Relay Peers** — highest impact, solves the most common real-world failure (offline delivery). No dependencies beyond base peering.
2. **Cluster Anycast** — builds on existing cluster infrastructure. Immediate reliability improvement.
3. **Signed Feeds** — enables new use case (publishing). Independent of other extensions.
4. **Gossip Protocol** — prerequisite for scaling groups beyond ~50. Needed before MLS matters.
5. **MLS** — group encryption at scale. Depends on groups existing and having enough members to justify it.
6. **Ring Signatures** — niche but unique. Depends on groups + MLS.
7. **ZK Discovery** — advanced discovery. Depends on email verification being widespread.
8. **Compliance** — enterprise feature. Depends on orgs adopting Vula, which depends on everything else working first.
