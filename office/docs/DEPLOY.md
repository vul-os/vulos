# Vulos Office – Deployment Guide

## Requirements

- Linux host or container runtime
- Go 1.21+
- Node.js 20+ (for frontend build)
- PostgreSQL (optional; defaults to SQLite)

## Quick Start (Docker)

```sh
docker run -d \
  --name vulos-office \
  -p 8080:8080 \
  -v office-data:/data \
  ghcr.io/vul-os/vulos-office:latest
```

## Building from Source

```sh
git clone https://github.com/vul-os/vulos-office.git
cd vulos-office

# Build frontend
npm ci && npm run build

# Build backend (no CGO required)
CGO_ENABLED=0 go build -trimpath -o vulos-office .
./vulos-office
```

## Configuration

Copy `config.yaml.example` to `config.yaml` and edit:

```yaml
server:
  addr: ":8080"
  uploads_dir: "/data/uploads"
auth:
  enabled: true
  jwt_secret: "<secret>"
database:
  driver: sqlite   # or postgres
  dsn: "/data/office.db"
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTel OTLP endpoint (optional) |

## Observability

- `GET /metrics` — Prometheus `vulos_office_*` metrics.
- OTel traces when `OTEL_EXPORTER_OTLP_ENDPOINT` is set.

## Upgrading

Stop, replace binary, restart. SQLite schema is versioned; auto-migrated on startup.
