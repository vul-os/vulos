# Vulos Meet — Recording Architecture Plan (v1 stub, not yet implemented)

Recording is intentionally a stub in v1. This document describes the planned
architecture when recording is eventually implemented.

## Why recording is deferred

Recording has serious privacy and compliance implications:

- All participants must **explicitly consent** before recording starts (GDPR Art. 6, POPIA Sec. 11).
- Recordings require encrypted storage with clear, configurable retention.
- Participants have the right to request their face be blurred or the recording deleted.
- Recordings must not be stored server-side until proper audit infrastructure exists.

---

## Planned architecture

### Consent flow

1. Organizer enables recording for a scheduled meeting.
2. When the organizer clicks "Start recording", **every participant** receives an explicit
   consent banner: "The organizer has started recording this meeting. Do you consent? [Accept] [Leave]"
3. Participants who do not consent are redirected to a "you have left the recording session" screen.
4. Recording does not start until **all current participants** have consented.
5. Any participant who joins after recording starts sees the same consent banner immediately.

### Encryption

- Each recording is encrypted with a per-recording **age** key (https://age-encryption.org).
- The age key is wrapped with the organizer's public key.
- Recording bytes are **never stored in plaintext** on the application server.
- Encrypted chunks are streamed directly from the SFU / media server to object storage.

### Storage

- Default retention: **30 days** after the meeting ends.
- Organizer may extend retention up to 1 year or delete immediately.
- Retention is enforced by a scheduled deletion job (cronjob / Fly.io scheduler).
- Storage backend: S3-compatible (Fly Tigris or AWS S3), encrypted at rest.

### Metadata audit log

Every recording event is audit-logged:

```
recording_events(
  id          TEXT PRIMARY KEY,
  room_id     TEXT NOT NULL,
  event       TEXT,       -- 'started' | 'stopped' | 'consent-given' | 'consent-declined' | 'deleted'
  account_id  TEXT,
  ip          TEXT,
  at          TIMESTAMPTZ
)
```

### GDPR / POPIA compliance

- Data subjects (participants) may request:
  - **Face blurring** post-hoc (processed via video pipeline before serving).
  - **Recording deletion** (immediate; removes from object storage and audit-logs the deletion).
- Requests are fulfilled within **72 hours**.
- Recordings are included in the data export API for subject access requests.

### Per-room participant cap during recording

- Recording sessions are capped at **50 participants** per room (RAM budget for the SFU).
- A visible "Recording in progress" banner is shown on all participant tiles.

---

## Current state (v1)

The recording button is visible in the UI but is **disabled** with a "Coming soon" label.
No audio or video is ever captured server-side. No recording bytes are stored anywhere.
