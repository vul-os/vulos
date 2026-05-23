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

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_ENV` | `prod` | Runtime environment: `local`, `dev`, `prod` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(empty)_ | OTel OTLP endpoint; if unset, tracing is no-op |
| `S3_ENDPOINT` | _(empty)_ | S3-compatible endpoint for backup vault |
| `S3_BUCKET` | _(empty)_ | Backup bucket name |
| `S3_ACCESS_KEY` | _(empty)_ | S3 credentials |
| `S3_SECRET_KEY` | _(empty)_ | S3 credentials |

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
