# Mail (LilMail integration)

Vula OS's default mail client is **LilMail**
(github.com/exolutionza/lilmail) — a lightweight, database-free IMAP/SMTP
webmail client, part of the Vula OS suite. It replaces the previous bespoke
webmail and the Thunderbird desktop package.

LilMail lives in its own repository and is consumed by the OS, the same way
mail server (`vulos-mail`) and office (`vulos-office`) are kept separate. No
LilMail source is vendored into this repo.

## How it is wired

- **Service.** LilMail runs as a local service (Fiber web app, default port
  `3000`). It serves its own server-rendered UI and handles IMAP/SMTP login
  itself (email + password, or optional OAuth2 against an external IMAP
  provider — this is LilMail's own connection auth, unrelated to Vula OS
  sign-in).
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
