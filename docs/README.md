# Vulos OS – Documentation Index

This directory contains architecture, deployment, and API documentation for the Vulos OS backend.

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](ARCHITECTURE.md) | System architecture overview (shell, assistant, auth, files, sync) |
| [GETTING-STARTED.md](GETTING-STARTED.md) | Install and first boot |
| [COMMS.md](COMMS.md) | Chat/video: why third-party (Element/Cinny, Jitsi Meet/Element Call), install + self-host |
| [CONFIGURATION.md](CONFIGURATION.md) | Environment variables and config files |
| [DEPLOY.md](DEPLOY.md) | Self-hosting guide |
| [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md) | Forkable self-host bundle (trust anchor, bucket) |
| [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md) | Verifying image builds from source |
| [RELEASING.md](RELEASING.md) | Versioning and release policy |
| [security/](security/) | Security audits and hardening-test notes |
| [SLOs.md](SLOs.md) | Service level objectives |
| [../ROADMAP.md](../ROADMAP.md) | Feature roadmap |
| [../SECURITY.md](../SECURITY.md) | Security policy |
| [THREAT-MODEL.md](THREAT-MODEL.md) | Threat model (incl. the sovereign assistant) |

## Quick links

- API: served from `GET /api/...` routes in `backend/cmd/server/`
- Sovereign assistant: `backend/services/assistant/` — agent, ledger, egress Guard
- Auth: `backend/services/auth/`, `backend/services/passkeys/`
- Files (ACL + sealed shares): `backend/services/files/`
- LLM gateway seam: `backend/internal/llmuxclient/` (`LLMUX_URL`)
- Observability: `backend/internal/obs/` — `/metrics` endpoint, OTel traces
