# Accounts and Access Control

Vulos boxes have a simple, deliberate permission model. The person who sets the
box up is its **owner**, and the most dangerous actions require re-proving who you
are even when you're already signed in. This page explains the roles, what each
can and cannot do, and what "step-up" means.

For the identity behind an account see [IDENTITY-KEYS.md](IDENTITY-KEYS.md).

---

## The roles

There are three roles (`backend/services/auth/profiles.go`):

| Role | Who gets it |
|---|---|
| **admin** (owner) | The **first account** created during setup. Full management of the box and fleet. |
| **user** | Additional accounts. This is the default role for anyone created after the owner. |
| **guest** | Reserved for limited/guest access. |

The first user is always the administrator — the same credentials you use for
`sudo` in the Terminal. There is always at least one admin (the box tracks the
admin count so you can't accidentally strip the last one).

Only an admin can change another account's role
(`Store.SetRole`), and a role change is audit-logged with the real client IP and
user-agent.

---

## What an admin/owner can do

Management and sensitive settings are **admin/owner-only**. Among them:

- **Fleet management** — rename, mark store-only, or remove instances
  (`/api/instances/{ulid}/…`, admin-gated; `401` if unauthenticated, `403` if not
  admin).
- **Issue join codes** to add new devices (`GET /api/cluster/join-code`,
  admin-only) — see [ADD-DEVICE.md](ADD-DEVICE.md).
- **Download the Recovery Kit** file (`GET /api/recovery/kit`, admin-only).
- **Device-key rotation and revocation** (`/api/auth/device/rotate`,
  `/api/auth/device/revoke`) — owner **and** step-up — see
  [REMOVE-DEVICE.md](REMOVE-DEVICE.md).
- **Custom domains and CDN** configuration — see
  [CUSTOM-DOMAIN.md](CUSTOM-DOMAIN.md).
- Backups, app versions/updates, and other box-wide settings.

Whenever a management route refuses a request it fails **closed**: a missing
session is `401`, and a non-admin (or a degraded boot with no auth store at all)
is `403` — never "everyone is admin".

---

## What a regular (non-admin) user can and can't do

A `user` account is a full desktop account for day-to-day work, but it has **no
management access**. Concretely, a non-admin:

**Can:**

- Sign in and use the desktop shell, apps, Files, the assistant, Mail/Calendar/
  Contacts, and their own data.
- Change their own personal settings and profile.

**Cannot:**

- Manage the fleet (rename/remove instances) — `403`.
- Issue join codes to add devices — `403`.
- Download the Recovery Kit — `403`.
- Rotate or revoke device keys — `403`.
- Configure custom domains / CDN, or other box-wide settings.
- Change anyone's role (including their own).

In short: a regular user drives the box; only the owner administers it.

---

## Step-up: re-proving yourself for dangerous actions

Some actions are dangerous enough that a valid session cookie should not be
enough on its own — removing a fleet member, rotating a recovery key, exporting
secrets. These require **step-up re-authentication**
(`backend/services/stepup`).

How it works:

1. You re-prove your **password** via `POST /api/auth/stepup/verify`.
2. That mints a short-lived **elevated token** bound to your user, valid for
   **5 minutes** (carried in the `vulos_stepup` cookie, or an
   `X-Stepup-Token` header).
3. Privileged handlers call `stepup.Require`, which passes **only** if the request
   carries a currently-valid elevated token for the same verified user. No token,
   an expired token, or a token minted for a different user all fail.

Step-up is **fail-closed**: any error, missing input, or ambiguous state is
treated as "not elevated". The elevated token is domain-separated from your
session token, so a leaked step-up token cannot be replayed as a session (or vice
versa). It is deliberately short — a re-proof for the next few privileged clicks,
not a second session.

> The current step-up factor is your account password. The design leaves a clean
> seam to swap in WebAuthn/TOTP later without changing any caller — so a future
> release can require a hardware key or authenticator for step-up.

---

## Related pages

- [IDENTITY-KEYS.md](IDENTITY-KEYS.md) — the account and its keys.
- [ADD-DEVICE.md](ADD-DEVICE.md) / [REMOVE-DEVICE.md](REMOVE-DEVICE.md) — device
  lifecycle, both owner-gated.
- [CUSTOM-DOMAIN.md](CUSTOM-DOMAIN.md) — owner-gated domain setup.
- [SETTINGS.md](SETTINGS.md) — the settings surfaces themselves.
