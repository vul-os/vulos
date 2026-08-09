# Vulos OS – Documentation Index

This directory contains architecture, deployment, and API documentation for the Vulos OS backend.

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](ARCHITECTURE.md) | System architecture overview (shell, assistant, auth, files, sync) |
| [GETTING-STARTED.md](GETTING-STARTED.md) | Install and first boot |
| **Using Vulos** | |
| [USER-GUIDE.md](USER-GUIDE.md) | Day-to-day desktop: shell, dock, Mission Control, Launchpad, settings |
| [APPS.md](APPS.md) | The app model — bundled apps, manifests, streamed native apps, the app gateway |
| [FILES.md](FILES.md) | Files: unified bucket, ACLs, sealed sharing, cross-box grants |
| [ASSISTANT.md](ASSISTANT.md) | The sovereign assistant — agent, ledger, egress Guard, LLM gateway seam |
| [TERMINAL.md](TERMINAL.md) | The built-in terminal |
| [SETTINGS.md](SETTINGS.md) | Settings surfaces (WiFi, display, audio, security, developer/API, webhooks) |
| [TROUBLESHOOTING.md](TROUBLESHOOTING.md) | Common problems and fixes across the stack |
| **Your account & devices** | |
| [IDENTITY-KEYS.md](IDENTITY-KEYS.md) | Identity is not e-mail: your local account, Ed25519 identity, and the Recovery Kit (24-word phrase vs. kit file) |
| [ADD-DEVICE.md](ADD-DEVICE.md) | Adding another device — the "Join existing" flow, join codes and passphrases |
| [REMOVE-DEVICE.md](REMOVE-DEVICE.md) | Removing/revoking a compromised device — owner + step-up, break-glass, propagation |
| [ACCOUNTS-ACCESS.md](ACCOUNTS-ACCESS.md) | Roles (admin/user/guest), what non-admins can't do, and step-up re-auth |
| [MAIL-CALENDAR-CONTACTS.md](MAIL-CALENDAR-CONTACTS.md) | Bring-your-own mail/calendar/contacts: the lilmail engine and the credential-brokering proxy |
| [MAIL-LILMAIL.md](MAIL-LILMAIL.md) | lilmail service wiring — ports, `VULOS_MAIL_URL`, `frame_ancestors` |
| [PEERING.md](PEERING.md) | Peer identity, contact requests, Drop file transfer, and the delivery ladder |
| [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) | Encrypted backup, restore, and the recovery kit |
| [CUSTOM-DOMAIN.md](CUSTOM-DOMAIN.md) | Point your own domain at the box or a published app (owner-gated) |
| [COMMS.md](COMMS.md) | Chat/video: why third-party (Element/Cinny, Jitsi Meet/Element Call), install + self-host |
| [CONFIGURATION.md](CONFIGURATION.md) | Environment variables and config files |
| [DEPLOY.md](DEPLOY.md) | Self-hosting guide |
| [MIGRATIONS.md](MIGRATIONS.md) | Database schema migrations — the forward-only, fail-closed runner |
| [REACH.md](REACH.md) | **Reachability**: Vulos's own reverse tunnel, multi-relay, discovery, security model |
| [RELAY-SELF-HOST.md](RELAY-SELF-HOST.md) | Running your own relay — step-by-step on Hetzner and Fly.io |
| [RELAY-PROVIDERS.md](RELAY-PROVIDERS.md) | Where to host a relay (Hetzner/Fly/DO/Vultr/home box) and where the costs come from |
| [NETWORKING.md](NETWORKING.md) | Connection modes, direct mode, DNS, TLS, ports, firewall |
| [STORAGE-PROVIDERS.md](STORAGE-PROVIDERS.md) | Choosing an S3-compatible bucket (R2/B2/Wasabi/AWS/Tigris/self-host) and where the costs come from |
| [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md) | Verifying image builds from source |
| [RELEASING.md](RELEASING.md) | Versioning and release policy |
| [security/](security/) | Security audits and hardening-test notes |
| [SLOs.md](SLOs.md) | Service level objectives |
| [../ROADMAP.md](../ROADMAP.md) | Feature roadmap |
| [SECURITY.md](SECURITY.md) | The box's overall security posture and hardening |
| [../SECURITY.md](../SECURITY.md) | Security policy (vulnerability reporting) |
| [THREAT-MODEL.md](THREAT-MODEL.md) | Threat model (incl. the sovereign assistant) |
| [KEY-CEREMONY.md](KEY-CEREMONY.md) | Release/signing key ceremony |
| [DEVELOPMENT.md](DEVELOPMENT.md) | Local development workflow |

## Quick links

- API: served from `GET /api/...` routes in `backend/cmd/server/`
- Sovereign assistant: `backend/services/assistant/` — agent, ledger, egress Guard
- Auth: `backend/services/auth/`, `backend/services/passkeys/`
- Files (ACL + sealed shares): `backend/services/files/`
- LLM gateway seam: `backend/internal/llmuxclient/` (`LLMUX_URL`)
- Reachability: `backend/services/reach/` — endpoint set, reverse tunnel, discovery role; `backend/cmd/vulos/relay.go` (the `vulos relay` command)
- Observability: `backend/internal/obs/` — `/metrics` endpoint, OTel traces
