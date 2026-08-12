# Vulos Documentation

These chapters are ordered the way a person actually needs them, not the way a
maintainer organizes source code: what Vulos is and how to get it running, how
to live in it day to day, how to keep a box healthy — and only after all of
that, how it's put together internally and where the APIs are. If you're
looking for the pitch (what Vulos is, why it exists), start at the
[project README](../README.md).

---

## Start here

| Guide | What's inside |
|---|---|
| [Getting Started](GETTING-STARTED.md) | The install guide: what you need before you start, choosing between Docker, a live USB and deploying over SSH, what a working first boot looks like, and what to do when it isn't |
| [User Guide](USER-GUIDE.md) | The daily-driver manual: windows and the dock, Files, the App Hub, the assistant, Calendar/Contacts/Notes, notifications, using it from your phone |

## Using Vulos day to day

| Guide | What's inside |
|---|---|
| [Settings](SETTINGS.md) | Every pane in the Settings app, in the order it groups them: appearance, devices, storage, network, accounts, updates |
| [Terminal](TERMINAL.md) | The built-in terminal — a real shell on the machine you're running |
| [Apps](APPS.md) | Installing from the App Hub, permissions and network isolation, publishing an app to a subdomain, API keys |
| [Files](FILES.md) | The Files app: uploads, the viewer/editor/owner sharing model, external drives, where your bytes live |
| [Assistant](ASSISTANT.md) | The sovereign AI assistant: what it can do, the proposal system, on-instance search, choosing where the model runs |
| [Mail, Calendar & Contacts](MAIL-CALENDAR-CONTACTS.md) | Bring your own mailbox — connecting existing mail/calendar/contacts accounts |
| [Comms](COMMS.md) | Chat and video calling — why third-party (Element/Cinny, Jitsi Meet), installing and self-hosting |
| [Peering](PEERING.md) | Your Vula ID, contact requests, LAN Drop file transfer, and reaching other people's boxes |
| [Running more than one box](MULTI-INSTANCE.md) | Setting up a fleet so your account, settings and reminders follow you — and what deliberately stays on one machine |

## Your account & devices

| Guide | What's inside |
|---|---|
| [Identity & Keys](IDENTITY-KEYS.md) | Your local account, your Ed25519 identity, and the recovery kit (24-word phrase vs. kit file) |
| [Adding Another Device](ADD-DEVICE.md) | The "Join existing" flow — join codes, passphrases, syncing a second box or laptop in |
| [Removing a Device](REMOVE-DEVICE.md) | Revoking a lost or compromised device — owner + step-up, break-glass, propagation |
| [Accounts & Access](ACCOUNTS-ACCESS.md) | Roles (admin/user/guest), what non-admins can't do, step-up re-authentication |

## Reaching your box from anywhere

| Guide | What's inside |
|---|---|
| [Networking](NETWORKING.md) | Connection modes, direct mode, DNS, TLS, ports, firewall |
| [Running Your Own Relay](RELAY-SELF-HOST.md) | Step-by-step: standing up `vulos relay serve` on Hetzner and Fly.io |
| [Relay-Hosting Providers](RELAY-PROVIDERS.md) | Where to host a relay and what it costs |
| [Custom Domain](CUSTOM-DOMAIN.md) | Pointing a domain you own at your box or a published app |
| [Storage Providers](STORAGE-PROVIDERS.md) | Choosing an S3-compatible bucket for Files/Drive storage and/or backup (R2/B2/Wasabi/AWS/Tigris/self-host) |
| [Deploy](DEPLOY.md) | Self-hosting reference: Docker, building from source, TLS termination, upgrading |

## Keeping it healthy

| Guide | What's inside |
|---|---|
| [Troubleshooting](TROUBLESHOOTING.md) | Symptom → cause → fix, with the exact log lines and endpoints to grep for |
| [Backup & Recovery](BACKUP-RECOVERY.md) | The backup mechanisms, restoring, what the recovery phrase can and can't save you from, moving to new hardware |
| [Security](SECURITY.md) | Running a box securely: the auth surface, fail-closed defaults, verified boot, what to check before exposing it to the internet |
| [Threat Model](THREAT-MODEL.md) | The formal analysis — STRIDE, trust boundaries, honest residual risks |
| [Vulnerability Reporting](../SECURITY.md) | The project's security policy and safe-harbor terms |
| [Service Level Objectives](SLOs.md) | Targets, measurement, and rollback triggers per surface |

---

## Under the hood — architecture, APIs & developer docs

| Guide | What's inside |
|---|---|
| [Architecture](ARCHITECTURE.md) | Component map, deployment modes, and the design decisions behind them |
| [Reach](REACH.md) | The reachability stack in depth: the tunnel protocol, discovery, the security model, what a relay can and cannot do |
| [Configuration Reference](CONFIGURATION.md) | Every environment variable, config file, and runtime flag |
| [Development](DEVELOPMENT.md) | Local dev workflow — running frontend/backend, tests, tooling |
| [Mail (LilMail wiring)](MAIL-LILMAIL.md) | Config/wiring reference for the embedded Mail app — ports, `VULOS_MAIL_URL`, `frame_ancestors` |
| [Migrations](MIGRATIONS.md) | The database schema runner — forward-only, fail-closed |
| [Key Ceremony](KEY-CEREMONY.md) | How release/signing keys are generated, custodied, rotated, and revoked |
| [Reproducible Builds](REPRODUCIBLE-BUILDS.md) | Verifying image builds from source |
| [Releasing](RELEASING.md) | Versioning scheme and release policy |
| [Service-Worker Cache Versions](SW-CACHE-VERSIONS.md) | The cache-bearing surfaces and their version registry |
| [security/](security/) | Security audits and hardening-test notes |
| [decisions.md](decisions.md) | Running log of design/operational decisions, dated |
| [../ROADMAP.md](../ROADMAP.md) | Feature roadmap |

### API & developer quick links

- API: served from `GET /api/...` routes in `backend/cmd/server/`
- Sovereign assistant: `backend/services/assistant/` — agent, ledger, egress Guard
- Auth: `backend/services/auth/`, `backend/services/passkeys/`
- Files (ACL + sealed shares): `backend/services/files/`
- AI gateway seam: `backend/internal/llmuxclient/` (`VULOS_AI_MODE`, `LLMUX_URL`) — see [Assistant → Choosing where your AI runs](ASSISTANT.md#choosing-where-your-ai-runs)
- Reachability: `backend/services/reach/` — endpoint set, reverse tunnel, discovery role; `backend/cmd/vulos/relay.go` (the `vulos relay` command)
- Observability: `backend/internal/obs/` — `/metrics` endpoint, OTel traces
