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
