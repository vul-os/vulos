# NOTIFICATIONS

System-level notifications for Vula OS, delivered through the peering layer. Only whitelisted (approved) contacts can push notifications to your instance. Same trust gate as messaging — unknown peers are rejected at the door.

This spec depends on the peering system defined in [PEERING.md](PEERING.md). Notifications use the same identity, trust model, server-to-server delivery, and cryptographic guarantees. Read PEERING.md first.

---

## Core Concept

```
Bob's Vula Server ──── notification ────► Alice's Vula Server
                                              │
                                              ▼
                                         Alice's browser
                                         (toast / badge / sound)
```

A notification is a lightweight, time-sensitive signal from a peer. It triggers system-level UI (toasts, badges, sounds) without requiring Alice to open a conversation or app. Notifications are not messages — they're events.

**The rule:** if you're not on my approved list (see PEERING.md, Trust Model), your notification is silently dropped. No exceptions.

---

## How It Differs from Messages

| Aspect | Messages | Notifications |
|--------|----------|---------------|
| Storage | Inbox, persistent, conversation-threaded | Ephemeral queue, TTL-based, auto-expire |
| Delivery | Store-and-forward, durable | Push-first, briefly queued if offline |
| UI | Conversation thread in messaging app | System-level toast, badge, sound |
| Purpose | Content (text, media, files) | Signal (something happened, action needed) |
| Interaction | Read, reply, react | Dismiss, tap to open context, act on inline action |
| Size | Variable (text + attachments) | Tiny (<1KB always) |

Messages are content you keep. Notifications are signals you act on.

---

## Notification Types

### `presence`

Peer availability changes. Delivered only if you've opted into presence notifications for that contact.

```json
{
  "type": "presence",
  "subtype": "online",
  "body": null
}
```

Subtypes: `online`, `offline`, `idle`, `busy`

No body needed — the subtype is the notification. Displayed as a subtle indicator (dot on avatar, status line) rather than a disruptive toast.

### `event`

Something happened that involves you. The most common notification type.

```json
{
  "type": "event",
  "subtype": "file_shared",
  "body": {
    "title": "Alice shared vacation.jpg",
    "doc_id": "uuid-v7",
    "preview": "image/jpeg, 4.2 MB"
  }
}
```

Subtypes:
- `file_shared` — peer shared a file or document with you
- `doc_edited` — peer made edits to a shared document
- `contact_request` — someone wants to add you (from pending, not yet approved — this is the one notification type that comes from non-approved peers, gated by rate limiting instead)
- `group_invite` — invited to a group/room
- `mention` — tagged in a group conversation
- `profile_updated` — peer changed their profile (triggers re-fetch, see PEERING.md Profile Sync)

### `call`

Incoming call or call-related events. High priority — these bypass Do Not Disturb unless explicitly silenced.

```json
{
  "type": "call",
  "subtype": "incoming",
  "body": {
    "call_id": "uuid-v7",
    "media": "video",
    "group": null
  }
}
```

Subtypes: `incoming`, `missed`, `ended`, `participant_joined`, `participant_left`

Call notifications are the bridge between the signaling layer (PEERING.md, Calls & Video) and the user's attention. `incoming` shows a full-screen overlay with accept/reject. `missed` shows a badge.

### `alert`

Custom peer-defined alerts. For automation, monitoring, bots — anything a peer's instance wants to tell you about.

```json
{
  "type": "alert",
  "subtype": "custom",
  "body": {
    "title": "Build failed",
    "detail": "main branch, commit abc123",
    "icon": "error",
    "url": "/apps/ci/builds/456"
  }
}
```

Subtypes: `custom`, `system`

`custom` — sent by a peer's automation (CI bots, monitoring, scripts). The peer must be on your approved list.
`system` — reserved for your own Vula instance (local events, not from peers). Not delivered via peering.

### `action`

Requires a response. Displayed with inline buttons.

```json
{
  "type": "action",
  "subtype": "accept_decline",
  "body": {
    "title": "Bob wants to send you vacation.jpg (4.2 MB)",
    "actions": [
      { "id": "accept", "label": "Accept" },
      { "id": "decline", "label": "Decline" }
    ],
    "context": {
      "ref_type": "drop",
      "ref_id": "uuid-v7"
    }
  }
}
```

Subtypes: `accept_decline`, `confirm`, `choose`

Action notifications are transient — they expire after their TTL (default 5 minutes). If not acted on, they auto-dismiss with the default action (usually decline/ignore). The response is sent back to the peer via a notification reply.

---

## Notification Format

Every notification is a signed, structured message delivered server-to-server through peering.

```json
{
  "id": "uuid-v7",
  "from": "vula:ed25519:5Hb7...",
  "to": "vula:ed25519:9Kx2...",
  "timestamp": "2026-04-02T14:30:00Z",
  "type": "event",
  "subtype": "file_shared",
  "body": { ... },
  "ttl": 3600,
  "priority": "normal",
  "signature": "<ed25519-signature-of-canonical-json>"
}
```

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | UUID v7 | yes | Unique notification ID, sortable by time |
| `from` | Vula ID | yes | Sender's identity |
| `to` | Vula ID | yes | Recipient's identity |
| `timestamp` | ISO 8601 | yes | When the notification was created |
| `type` | string | yes | `presence`, `event`, `call`, `alert`, `action` |
| `subtype` | string | yes | Type-specific subtype (see above) |
| `body` | object/null | no | Type-specific payload, always <1KB |
| `ttl` | integer | yes | Seconds until expiry. 0 = no expiry (presence only) |
| `priority` | string | yes | `low`, `normal`, `high`, `critical` |
| `signature` | string | yes | Ed25519 signature of canonical JSON (same as messages) |
| `group_id` | Vula ID | no | If notification relates to a group |

### Priority Levels

| Priority | UI Behavior | DND Behavior |
|----------|------------|--------------|
| `low` | Badge only, no toast | Suppressed entirely |
| `normal` | Toast + badge + sound | Queued, shown when DND ends |
| `high` | Persistent toast, louder sound | Shown after 2nd attempt in 5 min |
| `critical` | Full-screen overlay (calls only) | Always shown |

Only `call.incoming` should use `critical`. Peers cannot set `critical` on other types — the recipient's server downgrades it to `high`.

---

## Delivery

### Push Path (Online)

```
Peer's Vula server
  → POST /api/peering/inbound/notification (HTTPS, signed)
    → Recipient's server verifies signature + checks allow list
      → Stored in notification queue
        → Pushed to recipient's browser via WebSocket
          → System toast / badge / sound
```

Same delivery pipe as messages (PEERING.md, Server-to-Server), but notifications go to the notification queue instead of the inbox.

### Offline Handling

Notifications are not messages — they don't queue indefinitely.

- If recipient is offline, notification is stored on **recipient's server** (not sender's)
- On reconnect, pending notifications are delivered in order, filtered by TTL
- Expired notifications (past TTL) are silently dropped
- Maximum offline queue: 200 notifications per peer. Oldest dropped first (FIFO).
- Presence notifications are never queued — they're only meaningful in real-time

### Notification Replies (for Action Type)

When a user acts on an `action` notification, the response is sent back:

```json
{
  "id": "uuid-v7",
  "type": "notification_reply",
  "ref_id": "<original notification id>",
  "from": "vula:ed25519:9Kx2...",
  "to": "vula:ed25519:5Hb7...",
  "action": "accept",
  "signature": "<ed25519-signature>"
}
```

Delivered via `POST /api/peering/inbound/notification-reply`. Same trust rules apply.

---

## Browser Delivery

The browser maintains a WebSocket connection to its Vula server (same one used for messaging and call signaling). Notifications ride this connection.

### WebSocket Channel

```
Browser ◄──WebSocket──► Vula Server

Server pushes:
{
  "channel": "notification",
  "payload": { <notification object> }
}
```

No separate connection needed. The existing peering WebSocket multiplexes messages, call signaling, collab updates, and now notifications — distinguished by `channel`.

### System UI

Notifications render at the OS level, not inside any specific app:

```
┌─────────────────────────────────────────────────────┐
│  ┌─────────────────────────────────────────────┐    │
│  │ 👤 Bob                            just now   │    │
│  │ Shared vacation.jpg (4.2 MB)                 │    │
│  │                          [View]  [Dismiss]   │    │
│  └─────────────────────────────────────────────┘    │
│                                                      │
│  Desktop / App                                       │
│                                                      │
└─────────────────────────────────────────────────────┘
```

- Toast appears top-right, auto-dismisses after 5 seconds (configurable)
- Click toast to open relevant context (conversation, file, call screen)
- Action notifications show inline buttons — no need to navigate away
- Badge count on the system tray / taskbar icon
- Sound per priority level (none for low, chime for normal, ring for high/critical)

### Notification Center

A pull-down panel (like Android/iOS notification shade) that collects recent notifications:

```
┌──────────────────────────────────────┐
│  Notifications                  ✕    │
│                                      │
│  Today                               │
│  ────────────────────────────────    │
│  👤 Bob — Shared vacation.jpg   2m   │
│  👤 Carol — Mentioned you in    15m  │
│     "Project Alpha"                  │
│  👤 Bob — Missed video call     1h   │
│                                      │
│  Earlier                             │
│  ────────────────────────────────    │
│  👤 Alice — Document edited     3h   │
│  👤 Dave — Build succeeded      5h   │
│                                      │
│  [Clear all]                         │
└──────────────────────────────────────┘
```

- Grouped by time (today, earlier, this week)
- Each entry tappable — opens the relevant context
- Swipe to dismiss individual, "Clear all" for bulk
- Unread count badge persists until cleared
- Notification center is accessible from the system tray at all times

---

## Whitelisting & Permissions

Notifications inherit the peering trust model entirely. The allow list is `contacts.json` (PEERING.md, Approved List).

### Per-Contact Notification Permissions

Extend the existing contact permissions:

```json
{
  "vula_id": "vula:ed25519:5Hb7...",
  "display_name": "Bob",
  "server": "bob.vulos.org:8080",
  "approved_at": "2026-04-01T12:00:00Z",
  "permissions": ["message", "media", "call", "video"],
  "notification_permissions": {
    "presence": true,
    "event": true,
    "call": true,
    "alert": true,
    "action": true
  }
}
```

You can approve Bob for messaging but disable his alert notifications (e.g., his CI bot is noisy). Each notification type is independently toggleable per contact.

### Rate Limiting

Even approved contacts are rate-limited to prevent notification spam:

| Type | Limit | Window |
|------|-------|--------|
| `presence` | 10 | per minute |
| `event` | 30 | per minute |
| `call` | 5 | per minute |
| `alert` | 20 | per minute |
| `action` | 10 | per minute |

Exceeding the limit: notifications are silently dropped for the remainder of the window. No error returned to sender (prevents probing).

### The contact_request Exception

`event.contact_request` is the only notification accepted from non-approved peers. This is how someone reaches you in the first place. Gated by:

- Aggressive rate limiting: 3 requests per hour per source IP
- Signature verification still required (must be a valid Vula ID)
- Queued in the requests list, not shown as a system toast (lower disruption)
- If blocked, all future requests from that Vula ID are silently dropped

---

## Do Not Disturb

System-wide mode that suppresses notifications:

| DND Level | Behavior |
|-----------|----------|
| **Off** | All notifications delivered normally |
| **On** | `low` suppressed, `normal` queued, `high` shown after 2nd attempt, `critical` always shown |
| **Total silence** | Everything suppressed except `critical` (incoming calls from starred contacts only) |

- DND is a local setting — peers don't know you're in DND
- Queued notifications deliver when DND ends, in order, filtered by TTL
- Schedule support: auto-enable DND during certain hours (e.g., 22:00–08:00)
- Per-contact override: "Always notify" for specific contacts, bypasses DND for their `high` priority notifications

---

## Storage

```
~/.vulos/peering/
  └── notifications/
      ├── queue.json        (pending notifications, delivered on browser connect)
      ├── history.json      (recent notifications, last 7 days, max 1000)
      └── settings.json     (DND schedule, per-contact overrides, sound preferences)
```

- `queue.json` — notifications waiting to be pushed to the browser. Cleared as they're delivered. TTL-filtered on each delivery attempt.
- `history.json` — delivered notifications kept for the notification center UI. Auto-pruned: older than 7 days or beyond 1000 entries, whichever comes first.
- `settings.json` — user preferences (DND, schedules, sounds, per-contact overrides).
- Syncs across your own nodes via S3/MinIO (cluster layer) like everything else in `~/.vulos/peering/`.

---

## Server API

### Local (Browser to Own Server)

```
GET    /api/notifications                    → list recent notifications (paginated)
GET    /api/notifications/unread             → unread count + badge number
POST   /api/notifications/:id/read          → mark as read
POST   /api/notifications/:id/action        → respond to action notification
POST   /api/notifications/read-all          → mark all as read
DELETE /api/notifications/:id               → dismiss
DELETE /api/notifications                    → clear all
GET    /api/notifications/settings           → notification preferences
PUT    /api/notifications/settings           → update preferences (DND, sounds, per-contact)
WS     /api/notifications/stream            → WebSocket for real-time push (or multiplexed on existing peering WS)
```

### Inbound (Server-to-Server, via Peering)

```
POST /api/peering/inbound/notification       → receive notification from approved peer
POST /api/peering/inbound/notification-reply  → receive action response from peer
```

Both endpoints: verify Ed25519 signature, check allow list, check rate limits, check per-contact notification permissions. Reject everything else.

---

## Implementation Order

Builds on peering infrastructure. Prerequisites: Identity, Contacts & Trust, Server-to-Server messaging (PEERING.md steps 1–5).

1. **Notification format & storage** — define types, queue.json, history.json, TTL expiry logic
2. **Inbound endpoint** — `/api/peering/inbound/notification` with signature verification, allow list check, rate limiting
3. **WebSocket push** — multiplex notifications on existing peering WebSocket, `channel: "notification"` 
4. **Toast UI** — system-level toast rendering, priority-based sound/display, auto-dismiss
5. **Notification center** — pull-down panel, history view, grouped by time, clear/dismiss
6. **Action notifications** — inline buttons, reply delivery via `/api/peering/inbound/notification-reply`
7. **Per-contact permissions** — extend contacts.json, settings UI per contact
8. **Do Not Disturb** — DND modes, scheduling, per-contact overrides
9. **Presence notifications** — online/offline/idle detection, subtle UI indicators
10. **Alert notifications** — custom alerts from peer automation, icon/url support
