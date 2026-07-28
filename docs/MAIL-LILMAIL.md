# Mail (LilMail connector)

> This page is the wiring/config reference for the **Mail app** (the embedded
> lilmail iframe). For the user-facing overview of mail **and** the
> credential-brokering proxy behind **Calendar/Contacts**, see
> [MAIL-CALENDAR-CONTACTS.md](MAIL-CALENDAR-CONTACTS.md).

**Mail in Vulos is a connector, not a service Vulos runs.** The OS's built-in
inbox connects to whatever mailbox the user already has — Gmail, Outlook, or
any IMAP/SMTP account. There is no requirement to host mail on the box, and a
mailbox is not the account anchor — your identity is decoupled from mail.

The connector client is **LilMail** (github.com/exolutionza/lilmail) — a
lightweight, database-free IMAP/SMTP webmail client, paired with the
`@vulos/mail-ui` surface. It is the bundled inbox for Vulos; it points at an
external mail server (the one hosting your existing mailbox).

An on-box mail **engine** is dormant and experimental: resurrectable, but not a
primary mail server and not the default. Most people bring their own mailbox and
never run one.

LilMail lives in its own repository (github.com/vul-os/lilmail) and is consumed
by the OS as a service, the same way Diwan is kept separate. No LilMail source is
vendored into this repo.

## How it is wired

- **Service.** LilMail runs as a local service (Fiber web app, default port
  `3000`). It serves its own server-rendered UI and handles IMAP/SMTP login
  itself (email + password for the IMAP account — this is LilMail's own
  mail-server connection auth, unrelated to Vulos OS sign-in). Vulos OS
  authentication is email/password + 2FA/passkey/QR only; there is no
  Google OAuth or third-party identity provider at the OS level.
- **Embedding.** The OS shell embeds LilMail same-window via an `<iframe>`.
  The built-in **Mail** app (`src/apps/mail/App.jsx`) fetches the service URL
  from `GET /api/mail/url` and frames it.
- **Service URL.** Configurable via `VULOS_MAIL_URL` (default
  `http://localhost:3000`), served by `backend/cmd/server/routes_mail.go`.
- **Framing permission.** LilMail must allow the OS origin to embed it. Set
  `frame_ancestors` in LilMail's `config.toml`, e.g.:

  ```toml
  [server]
  frame_ancestors = "'self' http://localhost:8080"
  ```

  When set, LilMail emits `Content-Security-Policy: frame-ancestors …` instead
  of `X-Frame-Options: SAMEORIGIN`. (In non-SSL/dev mode LilMail sends no
  framing header, so embedding works out of the box.)

## Runtime requirement

The OS image / service supervisor must launch the LilMail binary and keep it
running. The Mail app shows "Connecting to Mail…" until the service responds.
