# Vulos OS – Self-Hosting Guide

## Requirements

- Linux host (amd64 or arm64) — Raspberry Pi 5, x86_64 server, or VM
- Docker ≥ 24 (or Podman)
- 2 GB RAM minimum; 4 GB recommended
- 10 GB disk for base install; more for data/apps

## Quick Start (Docker)

```sh
docker run -d \
  --name vulos \
  -p 8080:8080 \
  -v vulos-data:/root/.vulos \
  ghcr.io/vul-os/vulos:latest
```

Open `http://localhost:8080` to complete first-boot setup.

## Building from Source

```sh
git clone https://github.com/vul-os/vulos.git
cd vulos

# Build the frontend
npm ci && npm run build

# Build the Go backend (requires Go 1.21+)
cd backend
go build -trimpath -ldflags="-s -w" -o ../dist/vulos ./cmd/server/

cd ..
./dist/vulos
```

## Environment Variables

This guide covers the **self-host** deployment shapes — a sovereign box on your
own hardware or a VPS you rent. That box runs as `DEPLOY_MODE=standalone`
(default: no control plane, every app open) or, once you point it at an
external control plane, as `DEPLOY_MODE=os`. Both run the identical OS image —
only the entitlement-gating and object-storage seams differ. See
[ARCHITECTURE.md → Deployment modes](ARCHITECTURE.md#deployment-modes) for the
canonical breakdown.

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_ENV` | `prod` | Runtime environment: `local`, `dev`, `prod` |
| `DEPLOY_MODE` | `standalone` (unset) | Deployment shape: `standalone` (sovereign, all apps open) or `os` (linked to a control plane, entitlement gating enforced). See [CONFIGURATION.md](CONFIGURATION.md) and the note above. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(empty)_ | OTel OTLP endpoint; if unset, tracing is no-op |
| `S3_ENDPOINT` | `s3.amazonaws.com` | S3-compatible endpoint for the backup vault (Restic) |
| `S3_BUCKET` | `vulos-vault` | Backup bucket name |
| `S3_ACCESS_KEY` | _(empty)_ | S3 credentials |
| `S3_SECRET_KEY` | _(empty)_ | S3 credentials |

See [CONFIGURATION.md](CONFIGURATION.md) for the full environment variable
reference, including the separate per-user storage-gateway (`VULOS_STORAGE_*`),
cloud, relay, push, and integrations variables.

## TLS termination with Caddy (SSH deploy path)

`./build.sh --deploy <host> --domain <yourdomain> --dns-namecheap <user> <key>`
(the scripted SSH deploy path, distinct from the plain Docker/source-build
steps above) provisions [Caddy](https://caddyserver.com/) in front of the
Vulos backend:

- Builds a custom `caddy` binary via `xcaddy` with the `caddy-dns/namecheap`
  plugin, so it can complete a wildcard cert via DNS-01.
- Creates a dedicated `caddy` system user and `/etc/caddy`, `/var/lib/caddy`.
- Writes `/etc/caddy/Caddyfile` with an `acme_dns namecheap` block and two
  site blocks — `$DOMAIN` and `*.$DOMAIN` — both `reverse_proxy localhost:8080`.
  Caddy terminates TLS and forwards everything to the Go backend on `:8080`;
  the backend itself never needs its own certificate.
- Writes Namecheap API credentials to `/etc/caddy/env` (mode 600):
  `NAMECHEAP_API_USER` / `NAMECHEAP_API_KEY`.
- Installs and enables a `caddy.service` systemd unit
  (`caddy run --config /etc/caddy/Caddyfile --adapter caddyfile`).
- Subsequent deploys restart `caddy.service` automatically when
  `/etc/caddy/Caddyfile` already exists.

If you deploy via plain Docker or a manual source build (no `--domain`), Caddy
is not involved — put your own reverse proxy or load balancer in front of
`:8080` and terminate TLS there instead.

## dm-verity / Verified Boot (optional)

For bare-metal installs requiring a verified rootfs, see `docs/REPRODUCIBLE-BUILDS.md`.

## Upgrading

```sh
docker pull ghcr.io/vul-os/vulos:latest
docker stop vulos && docker rm vulos
# Re-run the docker run command above — data is on the volume
```

## Troubleshooting

- **First boot hangs**: ensure `/dev/uinput` is accessible inside the container (`--device /dev/uinput`).
- **App launch fails**: `appnet` requires `iproute2` and CAP_NET_ADMIN. Use `--cap-add NET_ADMIN`.
- **Metrics not appearing**: check that `obs.Init()` was called (it is, in `main()`). Verify `/metrics` is reachable.
