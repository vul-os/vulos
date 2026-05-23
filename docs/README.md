# Vulos OS – Documentation Index

This directory contains architecture, deployment, and API documentation for the Vulos OS backend.

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](ARCHITECTURE.md) | System architecture overview |
| [DEPLOY.md](DEPLOY.md) | Self-hosting guide |
| [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md) | Verifying image builds from source |
| [RELEASING.md](RELEASING.md) | Versioning and release policy |
| [../SLOs.md](../SLOs.md) | Service level objectives |
| [../ROADMAP.md](../ROADMAP.md) | Feature roadmap |
| [../SECURITY.md](../SECURITY.md) | Security policy |
| [../THREAT-MODEL.md](../THREAT-MODEL.md) | Threat model |

## Quick links

- API: served from `GET /api/...` routes in `backend/cmd/server/main.go`
- Auth: `backend/services/auth/`
- Storage: `backend/internal/storage/`
- Observability: `backend/internal/obs/` — `/metrics` endpoint, OTel traces
