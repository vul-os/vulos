# Sessions: single-active-with-takeover (shipped) → multi-session coexistence (roadmap)

Status: **takeover shipped**; **multi-session is NOT implemented** — this doc is
the design for it and is referenced from the code comments in
`backend/services/auth/sessions.go` and `src/auth/AuthProvider.jsx`.

## What ships today — SESSION-TAKEOVER

Vulos does not forcibly limit a user to one session server-side (multiple
devices already coexist in the session table). What ships is the *choice*, made
visible at sign-in:

- On a successful sign-in, the shell calls `GET /api/auth/sessions`. The box
  returns the caller's active sessions as **redacted summaries** (id, device,
  provider, created/expiry, `current` flag — **never the token**) plus an
  `others` count.
- If `others > 0`, `SessionTakeoverModal` prompts:
  - **Use only here** → `POST /api/auth/sessions/revoke-others` → every other
    session for the user is revoked, this one is kept. The other devices learn
    of it on their next request (token no longer validates → 401 → "signed out:
    taken over on another device").
  - **Keep my other session** → this device signs itself out (`logout`), the
    other session stays. (Single-active semantics until multi-session lands.)
- Authorization is the caller's own valid session: you can only ever revoke your
  **own** other sessions, and `keepToken == ""` fails closed rather than
  revoking everything.

Endpoints (both session-required, not public):
- `GET  /api/auth/sessions` → `{ sessions: [SessionSummary], others: n }`
- `POST /api/auth/sessions/revoke-others` → `{ status: "took over", revoked: n }`

Store primitives: `ListSessionSummaries(userID, currentToken)`,
`RevokeOtherSessions(userID, keepToken)` (mirrors the revoke-others loop already
used by `ResetPasswordWithSessionKey`).

The prompt fires only after an **explicit sign-in**, never on passive app boot /
the 5-minute re-validation, so a legitimately multi-device user isn't nagged
every launch.

## The roadmap feature — MULTI-SESSION coexistence (do NOT build yet)

Goal: let a user keep several *named, visible, individually-revocable* sessions
(phone + laptop + desktop) at once, instead of the take-over-or-leave binary —
without weakening the security posture.

### How

1. **Enrich the Session record** (currently `id, user_id, token, device_id,
   provider, created, expires`). Add, captured at sign-in:
   - `last_seen_at` (touched by the auth middleware, throttled to ~1/min to
     avoid write amplification),
   - `client_ip_last` + coarse geo/city (privacy: store city-level, not raw IP,
     or hash the IP),
   - `user_agent` → parsed to a friendly `device_name` ("Chrome on macOS"),
   - `label` (user-editable, e.g. "Work laptop").
   Requires a small SQLite migration (add columns; back-fill nullable).

2. **A real Sessions surface in Settings → Security**: list every active session
   with device name, location, last-seen, "this device" badge, and a per-row
   **Sign out** button (calls a new `POST /api/auth/sessions/{id}/revoke` — revoke
   by session **id**, not token, so you can end a *specific* other device) plus
   **Sign out everywhere else** (the existing revoke-others).

3. **Replace the takeover prompt with a passive notice**: on a new sign-in,
   instead of forcing take-over/leave, show a dismissible "New sign-in on <device>
   from <location>" toast (and a web-push to the *other* devices — see §push).
   The user manages sessions from Settings at their leisure. This is the actual
   behavioral change that makes it "multi-session".

4. **Session ceiling + eviction policy** (optional, configurable): a soft cap
   (e.g. 10 active sessions) with oldest-`last_seen` eviction, so an abandoned
   fleet of stale tokens can't grow unbounded. Owner-configurable in Settings.

5. **Per-session scopes (stretch)**: mint sessions with a scope claim (full vs
   read-only vs a single-app audience), so a shared/loaner device can get a
   limited session. Ties into the existing app-audience-token seam
   (SECURITY-C1) rather than inventing a new one.

### §push — proactive "signed in elsewhere" / "signed out" notices

Today a revoked device only finds out on its next request. Multi-session should
web-push the affected device immediately, using the **already-present** sovereign
push stack (VAPID / `notify.Service`, targeted by `UserID`) — web push is keyed
to the device's push subscription, not the session token, so it still reaches a
device whose session was just revoked. Wire `RevokeOtherSessions` /
`revoke-by-id` to fire a targeted `notify.Notification` before deleting. (Left
out of the takeover MVP on purpose.)

### Security invariants to preserve (do not regress)

- Session summaries **never** include tokens.
- A caller can only ever list/revoke sessions for **their own** user id
  (X-User-ID is the auth-middleware-stamped identity, not client-supplied).
- Revoke-by-id must verify the target session's `user_id == caller`.
- No endpoint may become a user/session **existence oracle** (uniform responses).
- `last_seen`/IP capture must not turn the session table into a high-frequency
  write hotspot (throttle) or a precise location log (coarsen/hash).

### Why it's deferred

The takeover choice satisfies the immediate "logged in elsewhere" UX with a tiny,
auditable surface (two endpoints, no schema change). Full multi-session needs a
migration, a new Settings surface, UA/geo parsing, and the push wiring — a
larger, independently-testable unit best done as its own pass.
