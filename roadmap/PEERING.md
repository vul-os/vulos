# PEERING

Direct communication between Vula OS instances. Every Vula instance is a server — if you're running Vula, you can receive. No relay infrastructure, no third-party accounts, no federation bureaucracy. Your OS is your inbox.

---

## Core Concept

```
Alice (browser)                                    Bob (browser)
     │                                                  │
     ▼                                                  ▼
Alice's Vula Server ◄──── messages/media ────► Bob's Vula Server
     │                                                  │
     └──── calls/video: direct browser ◄──► browser ────┘
```

Two layers, separated by nature:

1. **Server-to-server** — messages, media, files, presence. Asynchronous. Durable. Your Vula instance stores incoming data and serves it when you connect. Works offline — messages queue until the recipient's server is reachable.

2. **Device-to-device** — voice calls, video calls, screen sharing. Real-time. Direct WebRTC between the actual browsers people are sitting at. NOT through the Vula servers. This matters because most users access Vula remotely — routing call media through two servers (device → server → server → device) adds hops, latency, and pointless transcoding. The servers only handle signaling (who's calling whom, ICE candidates, SDP exchange). The media flows peer-to-peer.

---

## Identity & Trust

No open inboxes. You don't receive anything from anyone until you approve them.

### Vula ID

Every Vula instance generates a keypair on first boot. The public key is the cryptographic identity. Email verification through vulos.org makes it human-readable and trustworthy.

```
Vula ID: vula:ed25519:<base58-encoded-public-key>
Display name: "Alice" (user-chosen, not unique)
Verified email: alice@gmail.com (verified via vulos.org, optional but recommended)
Slug: alice.vulos.org (from LANDING.md registration, if registered)
```

- Keypair stored in `~/.vulos/peering/identity/`
- All messages signed by sender's private key — unforgeable
- ID is portable: export keypair, import on new instance, same identity

### Email Verification

Uses the existing vulos.org infrastructure (see LANDING.md). Users already register a slug + email to get their `*.slug.vulos.org` domain. Peering piggybacks on this.

```
1. User registers on vulos.org: slug "alice", email "alice@gmail.com"
   (this already happens for DNS setup)
2. vulos.org sends a 6-digit code to alice@gmail.com
3. User enters code in their Vula instance
4. vulos.org stores: slug → Vula ID public key + verified email
5. Instance receives a signed verification token from vulos.org
6. Token is attached to the user's Vula ID — peers can verify it
```

The verification token proves "this Vula ID controls alice@gmail.com" without exposing the email to every peer. Peers see:
- **Verified badge** — email has been verified through vulos.org (email itself hidden by default)
- **Email visible** — only if the user explicitly shares it (per-contact or public)

Users who skip verification still work — they just show as unverified. Cryptographic identity still holds, you just can't look them up by email.

```
POST /api/peering/identity/verify    → initiate email verification via vulos.org
POST /api/peering/identity/confirm   → submit 6-digit code, receive verification token
```

vulos.org API addition:
```
POST /api/verify/send     → { email, vula_id } → sends 6-digit code
POST /api/verify/confirm  → { email, code, vula_id } → returns signed verification token
GET  /api/verify/lookup   → { email } → returns Vula ID + server (if user opted into discovery)
```

### Profile

Each user has a profile that travels with their Vula ID.

```json
{
  "vula_id": "vula:ed25519:5Hb7...",
  "display_name": "Alice",
  "image": "sha256:abc123...",
  "bio": "Building things",
  "verified_email": true,
  "slug": "alice.vulos.org",
  "visibility": {
    "image": "public",
    "bio": "peers",
    "email": "nobody"
  }
}
```

**Profile image:**
- Stored locally at `~/.vulos/peering/profile/avatar.webp`
- Auto-resized to 256×256, compressed to WebP (~10-30KB)
- Served at `/api/peering/profile/image` on the user's Vula instance
- Cached by peers after first fetch (ETag-based)

**Visibility controls — three levels per field:**

| Level | Who sees it |
|-------|------------|
| `public` | Anyone who visits your `/.well-known/vula-id` or finds you in the directory |
| `peers` | Only approved contacts |
| `nobody` | Hidden from everyone (still stored locally) |

Defaults: image = `public`, bio = `peers`, email = `nobody`

Users control this in Settings > Peering > Profile. Each field is independent — you might share your image publicly but keep your bio for peers only.

**Profile sync:** when you approve a contact, you fetch their profile (image, name, bio at the `peers` level). Profile updates are pushed to approved contacts via a lightweight notification — peers re-fetch on change.

```
GET  /api/peering/profile              → own profile
PUT  /api/peering/profile              → update profile fields
POST /api/peering/profile/image        → upload/update avatar
GET  /api/peering/profile/image        → serve avatar (respects visibility)
GET  /api/peering/profile/:vula_id     → fetch a peer's profile (they control what you see)
```

### Trust Model

```
Unknown ──► Pending ──► Approved ──► Blocked
                │                      ▲
                └──────────────────────┘
```

- **Unknown**: default state. Cannot send you anything. Your server rejects their requests at the door.
- **Pending**: they've sent a contact request (just their Vula ID + display name + optional message). You see it in a requests queue. No data transferred yet.
- **Approved**: you accepted. They're on your allow list. Now they can send messages, media, files, and initiate calls. Mutual — both sides must approve.
- **Blocked**: explicitly rejected. Their requests are silently dropped. They don't know they're blocked.

### Approved List

```json
{
  "contacts": [
    {
      "vula_id": "vula:ed25519:5Hb7...",
      "display_name": "Bob",
      "server": "bob.vulos.org:8080",
      "approved_at": "2026-04-01T12:00:00Z",
      "permissions": ["message", "media", "call", "video"]
    }
  ]
}
```

Stored in `~/.vulos/peering/contacts.json`. Each contact has granular permissions — you might approve someone for messages but not calls.

### Groups / Rooms

A group is just a list of approved Vula IDs with a shared name. No central server owns the group.

- Creator defines the group and member list
- Each member's server stores the group definition
- Messages to the group fan out server-to-server to each member
- Any member can add new members (if group policy allows)
- Group messages are signed by sender, verified by each recipient

---

## Server-to-Server: Messages & Media

Your Vula server is your mailbox. Others deliver to it. You read from it.

### Delivery

```
Sender's browser
  → Sender's Vula server (signs message, queues for delivery)
    → Recipient's Vula server (verifies signature, checks allow list)
      → Stored in recipient's inbox
        → Recipient's browser (fetched on connect, or pushed via WebSocket)
```

### Message Format

```json
{
  "id": "uuid-v7",
  "from": "vula:ed25519:5Hb7...",
  "to": "vula:ed25519:9Kx2...",
  "timestamp": "2026-04-02T14:30:00Z",
  "type": "text",
  "body": "hey, check this out",
  "signature": "<ed25519-signature-of-canonical-json>",
  "attachments": []
}
```

Types: `text`, `image`, `video`, `audio`, `file`, `location`, `contact`

### Media Transfer

Large files (images, video, documents) are transferred server-to-server over HTTPS.

- Sender's server stores the file, generates a signed URL
- Message contains a reference (hash + size + mime type)
- Recipient's server fetches the file from sender's server
- Once fetched, recipient has their own copy — no dependency on sender staying online
- Automatic thumbnail generation for images/video

### Offline Handling

- Messages queue on sender's server if recipient is unreachable
- Retry with exponential backoff (1s, 5s, 30s, 5m, 1h, then periodic)
- Recipient pulls missed messages on reconnect (last-seen timestamp sync)
- Messages stored on sender until delivery confirmed (ACK from recipient server)

### Storage

```
~/.vulos/peering/
  ├── identity/          (keypair, Vula ID, verification token)
  ├── profile/           (avatar.webp, profile.json, visibility settings)
  ├── contacts.json      (approved list)
  ├── inbox/             (received messages, indexed by conversation)
  ├── outbox/            (sent messages, pending delivery)
  ├── media/             (received files, images, video)
  └── groups/            (group definitions)
```

---

## Device-to-Device: Calls & Video

This is the critical separation. Vula already has WebRTC for app streaming — calls reuse the same infrastructure but the peers are different.

### Why Device-to-Device

```
App streaming:   browser ◄──WebRTC──► Vula server (server renders, client views)
Calls:           browser ◄──WebRTC──► browser     (both sides are real devices)
```

Most Vula users access their instance remotely. Their browser connects to a Vula server that might be in another room, another city, or a data centre. For app streaming, that's the whole point — the server does the rendering.

But for a call, both people are sitting at their browsers. Routing audio/video through two Vula servers means: microphone → browser → server A → server B → browser → speaker. That's two extra network hops, potential transcoding, and the servers are doing work they don't need to do.

Direct: microphone → browser → browser → speaker. One hop. Minimal latency.

### Signaling via Servers

The Vula servers still coordinate the call setup — they just don't carry the media.

```
1. Alice clicks "Call Bob"
2. Alice's browser → Alice's Vula server: "I want to call Bob"
3. Alice's server → Bob's server: call request (server-to-server)
4. Bob's server → Bob's browser: incoming call notification (WebSocket push)
5. Bob accepts
6. Both browsers exchange SDP offers/answers and ICE candidates
   (relayed through their Vula servers as signaling channel)
7. WebRTC peer connection established: browser ◄──► browser
8. Media flows directly. Servers are out of the loop.
```

### ICE / NAT Traversal

Direct browser-to-browser needs both peers to discover each other's network path.

- **STUN**: each browser discovers its public IP via STUN server. Vula can run its own or use public STUN servers.
- **TURN**: fallback relay when both peers are behind symmetric NATs. Vula servers can act as TURN servers for their own users — the media still goes through a server, but it's a known, controlled fallback, not the default path.
- **ICE candidates** exchanged via the signaling channel (server-to-server relay)

For users on the same LAN (e.g. two people in the same house with separate Vula instances), ICE discovers the local path — sub-millisecond latency, zero internet transit.

### Bandwidth Visibility

Before starting a group call, every participant's connection speed is visible. No guessing — you see the numbers and pick the right host.

Each Vula instance periodically measures its own bandwidth and reports it to peers on request:

```
GET /api/peering/bandwidth → { "upload_mbps": 48.2, "download_mbps": 210.5, "latency_ms": 12 }
```

- Measured by the instance itself (server-side speed test against a known endpoint, or measured from recent real traffic)
- Reported to the group when a call is being set up
- Displayed in the pre-call lobby next to each participant's name and avatar

```
┌─────────────────────────────────────────┐
│  Start Group Call                        │
│                                          │
│  👤 Alice    ▲ 48 Mbps  ▼ 210 Mbps  12ms │  ← Host
│  👤 Bob      ▲ 22 Mbps  ▼ 95 Mbps   18ms │
│  👤 Carol    ▲ 8 Mbps   ▼ 50 Mbps   34ms │
│  👤 Dave     ▲ 95 Mbps  ▼ 500 Mbps  4ms  │
│                                          │
│  SFU Host: [Alice ▼]    [Start Call]     │
│                                          │
│  Estimated capacity: ~15 video / ~50 audio│
└─────────────────────────────────────────┘
```

- Call initiator is default SFU host
- Dropdown lets anyone volunteer — you'd pick Dave here (95 Mbps up)
- Estimated capacity shown based on selected host's upload bandwidth
- During the call, host can be transferred if someone with better bandwidth joins or host degrades

### Rooms (Multi-Party Calls)

**1-on-1 calls: direct peer-to-peer**
- Browser ◄──► browser, no SFU needed
- Lowest possible latency

**Small groups (3–4 people): mesh**
- Each browser connects to every other browser directly
- N×(N-1)/2 connections. At 4 people that's 6 connections — manageable
- No central point of failure
- Falls back to SFU if any participant's bandwidth is too low

**Groups (5+): SFU on the host's Vula server**
- The host's Vula server runs the SFU (selected in pre-call lobby)
- Each browser sends one stream to the SFU, receives streams back
- SFU forwards packets — no transcoding, low CPU
- Built with [Pion](https://github.com/pion/webrtc) (Go, fits the stack natively)

### Efficiency: How to Handle Scale

The SFU uses the same tricks Google Meet and Discord use, just on personal hardware:

**Simulcast** — each sender's browser encodes 2 quality layers (high 720p + low 180p). The SFU picks which layer to forward to each receiver. Browser-native via WebRTC `addTransceiver` encodings — no extra work on our side.

**Last-N** — only forward video for participants visible on screen. If you're in a 15-person call but your grid shows 6 tiles, you receive 6 video streams, not 14. The rest are audio-only until you scroll or they speak.

**Dominant speaker** — detect who's talking (voice activity from audio levels). The active speaker always gets the high-quality simulcast layer. Everyone else gets low quality or audio-only.

**Audio mixing** — instead of forwarding N-1 individual audio streams, the SFU mixes the top 3 active speakers into a single stream per participant (excluding their own voice). Each person receives 3 audio streams max regardless of call size.

### Realistic Call Limits

These are real numbers based on the SFU host's upload bandwidth. Not theoretical — this is what you'd actually get on home/office internet.

| Host upload | Video (Last-N=6, 360p) | Audio-only |
|------------|----------------------|------------|
| 10 Mbps | 6-8 people | 20-30 people |
| 25 Mbps | 10-12 people | 40-50 people |
| 50 Mbps | 15-20 people | 50-80 people |
| 100 Mbps | 25-35 people | 100+ people |
| 1 Gbps (server) | 50+ people | 200+ people |

How the math works:
- Video at 360p with simulcast: ~500 kbps per stream forwarded
- Audio (Opus): ~32 kbps per stream, or ~96 kbps mixed (3 speakers)
- Last-N=6 means each participant receives at most 6 video streams = 3 Mbps download per person
- SFU upload = N participants × 6 streams × 500 kbps = N × 3 Mbps
- At 50 Mbps upload: 50 / 3 ≈ 16 people before upload saturates

**Hard cap: 50 participants per call.** Even if bandwidth allows more, the UX degrades — browser CPU for decoding, UI clutter, impossible to have a conversation. 50 is already more than most video platforms allow on free tiers.

### SFU Host Handoff

If the SFU host drops (connection dies, leaves the call):

1. Remaining participants detect the drop (WebSocket to SFU goes dead)
2. The participant with the highest reported upload bandwidth auto-becomes the new host
3. All browsers reconnect their WebRTC streams to the new SFU (2-4 second interruption)
4. Call resumes — brief freeze, not a full drop

No manual intervention needed. The pre-call bandwidth data makes the fallback choice automatic.

### Call Features

- [ ] Voice call (Opus codec, ~32kbps per direction)
- [ ] Video call (VP8/VP9/H.264, adaptive bitrate, simulcast 2 layers)
- [ ] Screen sharing (getDisplayMedia API → WebRTC track)
- [ ] Mute/unmute, camera on/off
- [ ] Pre-call lobby with bandwidth visibility and host selection
- [ ] Last-N video forwarding (configurable grid size: 4, 6, 9)
- [ ] Dominant speaker detection and highlight
- [ ] Audio mixing (top 3 speakers)
- [ ] SFU host handoff on disconnect
- [ ] Call quality indicator (RTT, packet loss, codec, host bandwidth)
- [ ] Picture-in-picture for video calls
- [ ] Call history (stored locally)
- [ ] Hard cap: 50 participants per call

---

## Real-Time Collaboration

Documents, sheets, slides, notes — any app that holds structured data can be collaboratively edited in real-time through the peering layer. No central server. Each participant's Vula instance holds the document, operations sync peer-to-peer.

### How It Works

Same pipes as messaging. A document edit is just a message with type `crdt-op` instead of `text`.

```
Alice types "Hello" at position 0
  → her browser generates CRDT operations
    → sent to her Vula server
      → fanned out to all peers who have this document open
        → each peer's browser applies the operations
          → everyone sees "Hello" appear
```

### CRDTs (Conflict-free Replicated Data Types)

CRDTs are data structures designed for exactly this — multiple people editing the same thing simultaneously with no central coordinator. Every edit merges automatically, no conflicts, no locking.

- **Text CRDT** — for Docs, Notes, Text Editor. Each character has a unique position ID. Insertions and deletions merge deterministically regardless of arrival order.
- **JSON CRDT** — for Sheets (cell values, formulas), Slides (object positions, properties), any structured data. Nested maps and arrays with per-field merge semantics.
- Library: [Yjs](https://github.com/yjs/yjs) — battle-tested, used by TipTap, Lexical, CodeMirror, Monaco. 10KB gzipped. Already integrates with the editors listed in DEFAULT-WEB-APPS.md.

### Transport

Yjs is transport-agnostic — it produces binary update blobs, you choose how to deliver them. Peering provides two channels:

**WebSocket (real-time, while both online):**
```
Alice's browser ──WebSocket──► Alice's Vula server ──server-to-server──► Bob's Vula server ──WebSocket──► Bob's browser
```
Sub-100ms latency for edits. Uses the same WebSocket connections already open for messaging and call signaling. Awareness protocol (cursor positions, selections, who's online) rides the same channel.

**Server-to-server sync (catch-up, when someone was offline):**
```
Bob comes online
  → Bob's server asks Alice's server: "what changed since my last state vector?"
    → Alice's server sends the diff (Yjs binary update)
      → Bob's document merges to current state
```

This is the same offline-then-sync pattern as messages. Yjs has built-in state vectors for efficient diff — you only send what the other side is missing.

### Document Sharing

A document becomes collaborative the moment you share it with a peer.

```
1. Alice opens a doc in Docs/Sheets/Notes/etc
2. Alice clicks "Share" → picks Bob from approved contacts
3. Alice's server sends Bob a share invitation (message type: "doc-share")
   Contains: document ID, document type (doc/sheet/slide), title, initial CRDT state
4. Bob accepts → document appears in his app with a "Shared" badge
5. Both can now edit. CRDT operations flow both directions via peering.
```

Permissions per shared document:
- **Edit** — full read/write, operations flow both ways
- **View** — read-only, receives operations but can't send. Cursor visible but greyed out.

### Awareness

While collaborating, you see who else is in the document:

- Cursor positions (coloured per person)
- Text selections
- Which cell someone is editing (Sheets)
- Which slide someone is on (Slides)
- Online/offline status
- Display name + avatar from their peering profile

Awareness data is ephemeral — not stored, not synced to S3, just broadcast over the live WebSocket. Disappears when someone disconnects.

### Which Apps Get Collaboration

Every app from DEFAULT-WEB-APPS.md that uses an editor Yjs already integrates with:

| App | Editor | Yjs binding | Collaborative data |
|-----|--------|------------|-------------------|
| Docs | TipTap / Lexical | `y-tiptap` / `y-lexical` | Rich text, headings, tables, images |
| Sheets | FortuneSheet / custom | `y-json` (custom binding) | Cell values, formulas, formatting |
| Slides | Custom + Reveal.js | `y-json` | Slide content, layout, order |
| Notes | TipTap | `y-tiptap` | Rich text, tags |
| Text Editor | CodeMirror 6 / Monaco | `y-codemirror.next` / `y-monaco` | Plain text, code |

### Group Collaboration

Same as group messaging — fan out to all participants. If 5 people are editing a document:

- Each person's operations go to their Vula server
- Their server fans out to the other 4 participants' servers
- Each server pushes to its connected browser via WebSocket
- CRDT handles merge — no coordination needed between servers

Works on the same group/room construct already defined for group messaging and group calls. A team can have a group where they message, call, AND co-edit documents — all through the same peering layer.

### Storage & Persistence

Collaborative documents are stored as Yjs documents (compact binary format) alongside the regular app data:

```
~/.vulos/peering/
  └── collab/
      ├── <doc-id>.yjs        (Yjs document binary — full CRDT state)
      ├── <doc-id>.meta.json  (title, type, owner, shared-with, permissions)
      └── ...
```

- Syncs across your own nodes via S3/MinIO (cluster layer) like everything else
- The Yjs binary includes full history — you can time-travel through edits
- Owner can revoke access — peer's copy becomes read-only, no further updates

### Server API

```
POST   /api/peering/collab/share           → share a document with a peer (sends invitation)
GET    /api/peering/collab/documents        → list all collaborative documents
GET    /api/peering/collab/:doc_id          → get document state + metadata
DELETE /api/peering/collab/:doc_id          → leave a collaborative document
PUT    /api/peering/collab/:doc_id/perms    → update permissions for a peer
WS     /api/peering/collab/:doc_id/sync     → WebSocket for real-time CRDT sync + awareness
```

Inbound (server-to-server):
```
POST /api/peering/inbound/collab-invite   → receive a document share invitation
POST /api/peering/inbound/collab-update   → receive CRDT operations from a peer
GET  /api/peering/inbound/collab-sync     → peer requesting diff since state vector
```

---

## Discovery

How do you find someone's Vula server to send them a contact request?

### Direct Exchange

Simplest. Share your Vula ID out-of-band (in person, text message, email, QR code).

```
vula:ed25519:5Hb7...@alice.vulos.org:8080
```

The ID includes the server address. Scan a QR code, your Vula instance sends a contact request to that address.

### Domain-Based Discovery

If Alice has `alice.vulos.org` pointing to her Vula instance:

```
GET https://alice.vulos.org/.well-known/vula-id
→ { "vula_id": "vula:ed25519:5Hb7...", "display_name": "Alice" }
```

Standard well-known URI. Type a domain, discover the identity.

### Vula Directory (Optional, Opt-In)

A public directory at `directory.vulos.org` where users can register their Vula ID + display name + optional metadata. Completely optional — you don't need to be listed to use peering.

- Search by display name, domain, or partial ID
- Users control what's visible (name only, name + domain, etc.)
- Directory only stores pointers — no messages, no media, no presence

---

## Drop

AirDrop-style sharing. Discover nearby Vula users, send them files, photos, links, documents — one tap. The transfer itself is just peering (server-to-server HTTPS, already built). Drop only solves discovery: who's near me and open to receive?

### How AirDrop Does It

Two radios: Bluetooth Low Energy (BLE) broadcasts "I'm here" to devices within ~10m. Once you pick a target, Wi-Fi Direct creates a point-to-point link for the actual transfer. The file never touches a router or the internet.

### How Vula Does It

Discovery needs to work in three contexts — Vula servers on a LAN, bare metal devices with Bluetooth, and browsers without either. Different discovery method for each, same transfer for all.

#### 1. LAN Discovery (mDNS) — primary path

Any Vula server on the local network advertises itself via mDNS (multicast DNS), the same protocol printers and Chromecasts use to be found without configuration.

```
Vula server broadcasts:
  _vula-drop._tcp.local  →  alice-vula.local:8080
  TXT: vula_id=vula:ed25519:5Hb7... display_name=Alice img=<hash>
```

Every Vula instance on the same network sees this. No internet, no central server, no pairing. Works in offices, homes, coffee shops — anywhere devices share a network.

- Go has `github.com/hashicorp/mdns` or `github.com/grandcat/zeroconf` — drop-in mDNS libraries
- Advertise when Drop is enabled (user toggles "Discoverable" in quick settings)
- Browse for other `_vula-drop._tcp` services to build the nearby list
- Show display name + avatar (fetched from the discovered server over LAN)

#### 2. Bluetooth Low Energy (BLE) — bare metal bonus

For bare metal Vula devices (phones, laptops, desktops with Bluetooth hardware). Discovery only — like AirDrop, BLE finds the target, the actual transfer goes over the network.

```
BLE advertisement:
  Service UUID: <vula-drop-uuid>
  Data: first 8 bytes of Vula ID hash (enough to identify, not enough to track)
```

- Range ~10m, works even without shared Wi-Fi
- Go backend uses BLE libraries (`tinygo.org/x/bluetooth` or `github.com/muka/go-bluetooth`)
- Once discovered via BLE, the browser shows the device. User taps to send.
- If both devices are on the same network, transfer goes over LAN (fast)
- If not on the same network, transfer goes through normal peering (internet)
- BLE rotates advertisement data periodically to prevent tracking

#### 3. Proximity Code — browser fallback

When there's no mDNS (remote browser) and no Bluetooth (not bare metal), users can still do quick nearby sharing with a short-lived code.

```
Alice opens Drop → sees a 6-digit code: 847 291
Bob opens Drop → enters 847 291
→ Bob's browser tells his Vula server "connect me to whoever has code 847291"
→ Matched via a lightweight rendezvous on vulos.org (or direct if both servers are reachable)
→ Transfer proceeds via normal peering
```

- Code expires after 5 minutes or first use
- No account needed on vulos.org — just a stateless code matcher
- Works from any browser, anywhere

### Discoverability Settings

Users control when they're visible to Drop:

| Setting | Meaning |
|---------|---------|
| **Everyone** | Any Vula instance on the network / in BLE range can see you |
| **Peers only** | Only approved contacts can see you (mDNS/BLE filtered by Vula ID) |
| **Nobody** | Drop discovery disabled, not advertised |

Default: **Peers only**. Changed in quick settings or Settings > Peering > Drop.

### UX Flow

```
┌──────────────────────────────────┐
│  Drop                            │
│                                  │
│  Nearby:                         │
│  ┌──────┐  ┌──────┐  ┌──────┐  │
│  │ 👤   │  │ 👤   │  │ 👤   │  │
│  │ Bob  │  │Carol │  │ Dave │  │
│  │  LAN │  │  BLE │  │  LAN │  │
│  └──────┘  └──────┘  └──────┘  │
│                                  │
│  Or enter code: [______]         │
│                                  │
│  Sending: vacation.jpg (4.2 MB)  │
│  To: Bob                         │
│  [Send]                          │
└──────────────────────────────────┘
```

- Drag a file onto the Drop window, or share from any app via the OS share menu
- Recipient gets a notification: "Alice wants to send you vacation.jpg (4.2 MB) — Accept / Decline"
- If recipient is an approved contact, option to auto-accept from them
- Transfer starts immediately on accept — goes over LAN if available, internet if not
- Progress bar, cancel button, done notification

### What Can Be Dropped

Anything that can be sent through peering:

- Files (any type, any size)
- Photos / videos (thumbnail preview in notification)
- Links / URLs
- Documents (opens in the relevant Vula app)
- Contact cards (Vula ID — quick way to add a peer)
- Text / clipboard content

### Server API

```
GET  /api/peering/drop/nearby         → list discovered nearby Vula instances (mDNS + BLE)
POST /api/peering/drop/send           → initiate a drop to a nearby peer
POST /api/peering/drop/code/generate  → generate a 6-digit proximity code
POST /api/peering/drop/code/redeem    → redeem a code to connect with a peer
PUT  /api/peering/drop/settings       → update discoverability (everyone/peers/nobody)
```

Inbound:
```
POST /api/peering/inbound/drop        → receive a drop request (shows notification to user)
```

---

## Server API

New endpoints on each Vula instance:

### Identity
```
GET  /api/peering/identity          → own Vula ID + public key
POST /api/peering/identity/export   → export keypair (encrypted)
POST /api/peering/identity/import   → import keypair
```

### Contacts
```
GET    /api/peering/contacts              → approved contact list
POST   /api/peering/contacts/request      → send contact request to another server
GET    /api/peering/contacts/requests      → pending incoming requests
POST   /api/peering/contacts/approve/:id  → approve a request
POST   /api/peering/contacts/block/:id    → block
DELETE /api/peering/contacts/:id          → remove contact
```

### Messages
```
GET  /api/peering/conversations                → list conversations
GET  /api/peering/conversations/:id/messages   → messages in conversation
POST /api/peering/conversations/:id/send       → send message
POST /api/peering/media/upload                 → upload media attachment
```

### Calls (Signaling)
```
POST /api/peering/call/initiate    → start a call (triggers signaling)
POST /api/peering/call/answer      → accept incoming call
POST /api/peering/call/reject      → reject
POST /api/peering/call/signal      → relay ICE/SDP between peers
POST /api/peering/call/hangup      → end call
```

### Inbound (Server-to-Server)
```
POST /api/peering/inbound/request   → receive contact request from another server
POST /api/peering/inbound/message   → receive message (verified against allow list)
POST /api/peering/inbound/signal    → receive call signaling
POST /api/peering/inbound/media     → receive media file
```

All inbound endpoints verify the sender's signature and check the allow list before accepting.

---

## Security

- **All server-to-server traffic over HTTPS** (TLS required, no plaintext)
- **Every message signed** with sender's Ed25519 key — tamper-proof, non-repuditable
- **End-to-end encryption** for messages: X25519 key exchange per conversation, messages encrypted with XChaCha20-Poly1305. Servers transport ciphertext — even if a server is compromised, message content is unreadable.
- **Perfect forward secrecy** for calls: new DTLS-SRTP keys per call (standard WebRTC)
- **Rate limiting** on inbound endpoints — prevent spam even from approved contacts
- **No metadata leakage to third parties** — no central server sees who talks to whom. Only the two Vula instances involved know about a conversation.

---

## Relationship to Cluster

The cluster system (CLUSTER.md) syncs state across your OWN nodes via S3/MinIO. Peering is communication BETWEEN different users' nodes. They complement each other:

- Your messages sync across your own nodes via S3 (you can read messages from any of your machines)
- Incoming messages arrive at whichever of your nodes the sender's server can reach
- Your Vula ID is the same across all your nodes (shared keypair via S3 sync)

---

## Implementation Order

1. **Identity** — keypair generation, Vula ID format, storage in `~/.vulos/peering/`
2. **Email verification** — integrate with vulos.org registration flow, 6-digit code, verification token
3. **Profile** — display name, avatar upload/resize, bio, visibility controls (public/peers/nobody)
4. **Contacts & trust** — allow list, contact requests with profile preview, approve/block flow
5. **Server-to-server messaging** — deliver text messages between two Vula instances
6. **Inbox UI** — conversation list, message thread view, send interface, contact profiles
7. **Media transfer** — images, files, thumbnails
8. **Bandwidth reporting** — server-side speed measurement, `/api/peering/bandwidth` endpoint
9. **Signaling** — call setup relay between servers
10. **Voice calls** — 1-on-1 browser-to-browser WebRTC audio
11. **Video calls** — add video track, 1-on-1
12. **Pre-call lobby** — bandwidth display, SFU host selection, capacity estimate
13. **SFU** — Pion-based SFU on host's server, simulcast, Last-N, audio mixing
14. **Group calls** — mesh for 3-4, SFU for 5-50, host handoff on disconnect
15. **Screen sharing** — getDisplayMedia integration
16. **Group messaging** — fan-out to multiple servers
17. **Document sharing** — share/accept flow, permissions, document metadata
18. **Real-time collab** — Yjs integration, WebSocket sync channel, awareness (cursors, selections)
19. **Collab in Docs** — TipTap + y-tiptap, first collaborative app
20. **Collab in Sheets/Notes/Text Editor** — extend to remaining apps
21. **Offline collab sync** — state vector diff on reconnect, catch-up via server-to-server
22. **Drop: LAN discovery** — mDNS advertisement/browsing, nearby list UI, send/accept flow
23. **Drop: proximity code** — 6-digit code generation/redemption, vulos.org rendezvous fallback
24. **Drop: BLE** — Bluetooth Low Energy advertisement/scanning for bare metal devices
25. **Discovery** — well-known URI, QR codes, email lookup, optional directory
26. **E2E encryption** — X25519 + XChaCha20-Poly1305 for message content and CRDT operations

---

# Extensions

Extensions are opt-in — the base system works without any of them.

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
