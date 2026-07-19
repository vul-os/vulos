# Remote Assist — co-presence, screen share, and delegated profile access

Status: **roadmap / designed, not built.** Part of the OS (`vulos`), not a separate
repo — every primitive it needs already lives here.

A TeamViewer-class capability, done the Vulos way: one user can *see* another
user's session (cursor, windows, file activity), multiple people can share a
live view with visible cursors, and a user can grant another user **temporary,
scoped, revocable** access to their profile. The hard part is not the pixels —
it is making the trust model impossible to get wrong.

---

## Why it lives in the OS, not a new repo

Everything it composes is already OS-level and exists nowhere else:

- **WebRTC session streaming.** The CPU-app-stream lane (Xvfb + encode + WebRTC)
  and "remote access" deployment mode already stream a session to a browser.
  Remote *viewing* extends that transport; it does not invent one.
- **Profile isolation.** Per-profile network namespaces, session-scoped cookies,
  and the `{app}--{profile}.{ulid}.vulos.org` scheme already isolate profiles.
  "Temp access to another profile" is a delegated capability over that model.
- **Capability grants + gateway auth + app-token seam.** Input injection, session
  ownership, and admission are already gated here. Remote control is a new
  capability *shape*, not a new auth system.
- **Multi-instance fabric.** Cross-box assist (help someone on their Pi from your
  laptop) rides the existing fabric/relay reachability, not a new tunnel.

A separate repo would re-import all of the above and add a network hop for
nothing. The only genuinely reusable sub-primitive — a co-presence/cursor layer —
already exists at the **document** level (Ofisi's Yjs awareness, including its
whiteboard document type); the new work is the **shell**-level version, which
is OS.

---

## Capabilities, in increasing privilege (each a distinct grant)

1. **Co-presence (view cursors).** Two+ users viewing a *shared surface* see each
   other's cursors + selection, labelled by identity. Already real in Ofisi
   (Yjs awareness); the roadmap item is lifting it to the OS shell
   (desktop-level co-presence) and to streamed app windows.
2. **Screen view (read-only).** User B watches User A's live session (or a single
   app window) over WebRTC. No input. A always sees B is watching.
3. **Remote control.** B's input (pointer/keyboard) is injected into A's session.
   The sharpest privilege — every safeguard below exists for this.
4. **Delegated profile access.** A grants B a *time-boxed, revocable* session on
   A's profile (e.g. "drive my machine for 30 min"), scoped to whole-session or a
   single app. Distinct from control: B gets their own authed session against A's
   profile, not a mirror of A's screen.

Each is a separate capability in the grant — holding one never implies a higher
one.

---

## The trust model (this is the whole feature)

Remote assist is where a convenience feature becomes a full account-takeover
vector if the trust model is loose (see TeamViewer's own breach history). Vulos's
posture: capability-first, consent-visible, fail-closed.

- **Explicit, per-session consent.** No standing "always allow." A grant is
  created by the profile owner (or an admin under a policy the owner opted into),
  for a named grantee, with an explicit scope and a hard expiry.
- **Time-boxed + revocable.** Every grant has a TTL and a one-click kill from the
  owner. Revocation evicts within one heartbeat — the same fail-closed cadence as
  the rest of the capability model. Closing the lid / logging out revokes.
- **Un-hideable "someone is here" indicator.** Whenever a session is being viewed
  or controlled, a persistent, server-authored banner + participant list is shown
  in the owner's shell. It is rendered by the OS chrome, not by the assist app, so
  a malicious grantee cannot suppress it (the same discipline used for any
  server-authored, admin-gated presence indicator — client-rendered "you're
  being watched" banners are trivially spoofable or suppressible).
- **Scoped, not all-or-nothing.** view-only vs control, and single-app vs
  whole-session, are independent axes. "Look at my spreadsheet" never implies
  "type into my terminal."
- **Full audit.** Every grant issue/accept/revoke and (optionally) an input-event
  log is written to the audit trail, owner-visible. A control session is
  attributable to a specific identity for its whole duration.
- **Fail-closed everywhere.** Any ambiguity — expired grant, revoked mid-session,
  unrecognised scope, store error — ends the session rather than degrading to
  "allow." Cloud and self-host identical.

---

## Deployment shapes (consistent with the canonical three)

- **Self-host / OS:** assist runs entirely on the owner's box(es); the grantee
  reaches it over the existing fabric/relay/LAN reachability. Nothing transits a
  third party — the sovereign path.
- **Cross-box within one account:** driving your own Pi from your laptop is a
  same-account capability, no external grant needed.
- **Cross-account (helping someone else):** an out-of-band, expiring invite
  (unguessable code, hash-stored, bounded, revocable) that the helper redeems
  for a scoped, time-boxed grant.
- **Cloud/managed:** same capability model; the WebRTC media may transit a Vulos
  POP only if the owner is on a placement that discloses it — never silently.

---

## Build order (when scheduled)

1. Shell-level co-presence (cursors/labels on the desktop) — reuses the Yjs
   awareness pattern; lowest privilege, proves the identity plumbing.
2. Read-only screen view over the existing WebRTC lane + the un-hideable
   indicator + audit.
3. Delegated profile access (time-boxed scoped grant → own authed session).
4. Remote control (input injection) last — it depends on every safeguard above
   being real and tested first.

Each stage is independently shippable and independently useful; control is
deliberately last because it is the one that must never ship ahead of its
guardrails.
