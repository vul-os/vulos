# webpush

Sovereign, self-hostable Web Push (VAPID / RFC 8291) — PUSH-CELL-01.

The box (cell) holds its own VAPID key pair and POSTs RFC 8291
(`aes128gcm`)-encrypted payloads **directly** to the browser vendor's push
service (FCM / Apple / Mozilla) named by each subscription's endpoint. This is
outbound-only, so it works behind NAT with no inbound reachability and no
central dependency. The vendor **routes** the payload but cannot **read** it —
Web Push end-to-end-encrypts to the subscription's own keys.

This package is generic: it has no dependency on any particular
notification/service model, so any part of the backend can send a push
without importing `backend/services/notify`. The `notify` service is the
current caller — see `backend/services/notify/webpush_service.go` for how it
wires this package in (`Service.SetPush`, `maybeWebPush`, `pushBinding`) — but
nothing here assumes that caller.

## What's here

- **`Config`** — VAPID key material + delivery knobs, loaded from the
  environment (`LoadConfig`) and resolved/generated-and-persisted from a key
  file (`ResolveVAPID`). Fail-safe-off: `Config.Enabled()` is `false` unless
  both halves of the key pair are present.
- **`Subscription`** — mirrors the browser's `PushSubscription.toJSON()`
  shape, scoped to an `OwnerID` the caller must stamp from a verified
  session (never trust the client body for it).
- **`Validate`** — bounds-checks a subscription and SSRF-screens its endpoint
  host (blocks loopback/private/link-local/metadata targets and common
  IP-literal obfuscations) before it is ever stored.
- **`Store`** — per-owner subscription persistence; `NewMemStore` (in-memory,
  tests/ephemeral) and `NewSQLiteStore` (durable, `modernc.org/sqlite`,
  CGO-free) implementations, each capped at `MaxSubsPerUser` per owner
  (oldest evicted).
- **`Payload`** / **`Sender`** / **`LiveSender`** — the wire payload a service
  worker receives, and the send transport. `LiveSender` uses an
  SSRF-guarded `http.Client` whose dialer re-screens the *resolved* IP at
  connect time (defeats DNS-rebind) and refuses redirects.

## Usage

```go
cfg := webpush.LoadConfig()
if err := webpush.ResolveVAPID(&cfg); err != nil {
    // key file unavailable — run without push rather than fail boot
}
store, err := webpush.NewSQLiteStore(dsn)
// ...
if err := webpush.Validate(sub); err != nil {
    // reject the subscribe request
}
_ = store.Save(ownerID, sub)

sender := webpush.LiveSender{}
status, err := sender.Send(sub, payloadJSON, cfg)
```

## Security notes

- The private VAPID key is a secret: never logged, never returned in any
  response, and written `0600` when persisted to a key file.
- `Validate` and `LiveSender.Send` both screen the subscription endpoint —
  once at parse time (form) and again at send time (resolved IP) — so a
  malicious owner can never point the box's outbound push POST at an
  internal/metadata service.
- A subscription the vendor reports as gone (`404`/`410`) should be pruned by
  the caller (see `notify`'s `pushBinding.pump` for the reference loop).

## Relationship to the DMTAP substrate's Wake capability (deliberate deviation, documented)

The [DMTAP substrate spec](https://github.com/vul-os/dmtap) (`substrate/ROLES.md`
§8, capability ⑤ Wake) defines wake pushes as **content-free**: a `WakePing`
carries nothing but an opaque "sync now" token, sealed with RFC 8291 to the
device's push key — no title, body, tag, or any other field, "the same fixed
shape" every time. The device is only ever *woken*; it then pulls the real
object over its own authenticated connection (wake-and-fetch, never
deliver-in-push). A conformant `WakePing` bearing any field beyond the opaque
token is rejected by spec (`ERR_WAKEPING_CONTENT_PRESENT`).

This package does **not** do that. `Payload` (above) carries real notification
content — `Title`, `Body`, `Tag`, `Source`, `URL` — and that whole struct is
what gets RFC 8291-sealed and POSTed to the vendor. That is an independent
design, not an implementation of the substrate's Wake role, and the deviation
is intentional:

- **What it gains.** The vendor still never sees plaintext — Web Push's
  `aes128gcm` encryption is end-to-end to the subscription's own keys, so a
  compromised or curious FCM/Apple/Mozilla can route the payload but not read
  it, exactly like a spec-conformant `WakePing`. Sending the real content
  saves a round trip: the client renders the notification straight from the
  push event with no need to wake, re-establish an authenticated connection
  back to the box, and fetch — which matters for a box that may itself be
  asleep, NAT'd, or rate-limited, and for a client on a metered/high-latency
  connection.
- **What it gives up.** DMTAP's opaque token is a *fixed* shape and size by
  design, specifically so a passive observer of the ciphertext (the vendor,
  or anyone downstream of it) learns nothing beyond "this device was pinged
  at this time." This package's payload size instead varies with the
  notification's title/body length, so ciphertext **size** leaks a rough
  signal about what kind of notification was sent (e.g., a one-line "new
  mail" nudge vs. a long chat preview) that the spec's fixed-shape design
  specifically avoids. **Timing** exposure is the same either way — the
  vendor always learns when a device was pushed, wake-only or not. **Content**
  itself is the one thing neither design ever exposes: both are sealed to the
  device's own keys end to end.
- **Interop cost.** A node that only speaks the substrate's Wake role (e.g.
  `vulos-relayd`, expecting a fixed-shape opaque ping followed by an
  out-of-band fetch) cannot interoperate with this box's push sender as-is —
  it would receive a real-content payload instead of an opaque token.

**Decision: keep the current behavior as the documented default (superset,
not a conformance mode).** No content-free "wake-only" mode has been added —
that would be a genuinely useful future addition if/when interop with
substrate-only Wake consumers (e.g. `vulos-relayd`'s wake origin role)
becomes a requirement, config-gated and off by default so existing push
behavior for every current box is never silently changed. Until then, this
divergence from `substrate/ROLES.md` §8 is intentional and accepted, not an
oversight.
