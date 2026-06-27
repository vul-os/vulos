# Vulos Configuration Reference

This document consolidates all environment variables, config files, and runtime flags for the Vulos OS backend, frontend build, and self-hosted bundle.

---

## Backend: `--env` / `VULOS_ENV`

The most important flag. Controls the entire security and behaviour profile.

| Value | Who uses it | What changes |
|-------|-------------|--------------|
| `local` | Developer laptop | Binds `127.0.0.1`, skips TPM/fingerprint checks, allows self-signed certs, relaxes cookie flags, enables `/debug/env` endpoint |
| `dev` | CI / staging | Same as `local` but no debug endpoints; accepts staging cloud-broker pubkey alongside prod key |
| `prod` | Bare-metal / cloud | Binds all interfaces, full Secure cookies, hardware checks active, no debug endpoints |

```bash
# Flag
go run ./backend/cmd/server --env=local

# Environment variable (same effect)
VULOS_ENV=local go run ./backend/cmd/server
```

---

## Backend environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_ENV` | `prod` | Runtime environment: `local`, `dev`, `prod` |
| `PORT` | `8080` | HTTP server listen port |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(empty)_ | OTel OTLP endpoint; unset = tracing no-op |
| `S3_ENDPOINT` | _(empty)_ | S3-compatible endpoint for backup vault (Restic) |
| `S3_BUCKET` | _(empty)_ | Backup bucket name |
| `S3_ACCESS_KEY` | _(empty)_ | S3 access key |
| `S3_SECRET_KEY` | _(empty)_ | S3 secret key |
| `AI_PROVIDER` | `ollama` | AI backend: `ollama`, `openai`, `anthropic` |
| `AI_ENDPOINT` | `http://host.docker.internal:11434` | AI API endpoint |
| `DISPLAY` | `:99` | X11 display for app streaming (Xvfb) |
| `VULOS_MAIL_URL` | `http://localhost:3000` | URL of the LilMail service (proxied at `/api/mail/url`) |
| `VULOS_OS_BUCKET_URL` | `https://os.vulos.org` | OS update bucket URL (baked into seed at build time; override for forks) |

---

## GPU streaming

GPU tier is auto-detected at startup (`backend/services/gpu/gpu.go`). No configuration needed — the right encoder is chosen automatically.

| Tier | Hardware | Encoder | How to enable |
|------|----------|---------|---------------|
| 0 | None | VP8 (CPU) | Default — always available |
| 1 | Intel/AMD | H.264/AV1 via VA-API | Pass `--device /dev/dri` to Docker |
| 2 | NVIDIA | H.264/AV1 via NVENC | `--gpus all` + [NVIDIA Container Toolkit](GETTING-STARTED.md) |

Check the detected tier at runtime:

```bash
curl localhost:8080/api/browser/status | jq .gpu_tier
```

---

## Self-hosted bundle (`/etc/vulos/`)

The one-line installer (`curl -fsSL https://get.vulos.org | sudo bash`) writes these files. They are owned `root:vulos` mode 640 and never overwritten on re-run.

### `/etc/vulos/fabric.yaml`

Shared mesh identity, domain, TLS, and cloud endpoint settings. **Edit this first after install.**

```yaml
domain: os.yourdomain.com
acme_email: you@yourdomain.com
cloud_endpoint: https://app.vulos.org   # leave as-is for Vulos Cloud
```

### `/etc/vulos/storage.yaml`

S3/MinIO credentials. Choose one backend.

```yaml
# Tigris (default)
backend: tigris
access_key: YOUR_ACCESS_KEY
secret_key: YOUR_SECRET_KEY
bucket: your-bucket-name

# Local MinIO (--storage=minio)
backend: minio
endpoint: http://127.0.0.1:9000
access_key: vulos
secret_key: (read from /var/lib/vulos/minio/.minio_secret at start)
bucket: vulos
```

#### Per-app isolation (STS) — required for multi-app deployments

Each user gets their own bucket (`vulos-<userID>`), so cross-**user** isolation
always holds. Cross-**app** isolation **within a single user** is only enforced
when short-lived, prefix-scoped credentials are minted via STS.

Without STS, every storage-permitted app for a user receives the same **static,
full per-user-bucket credentials**. Those creds let an app read/write *any*
other app's data for that user when used directly against the object store (the
gateway-mediated `<userID>/<appID>/` prefix only scopes gateway-proxied access,
not direct use of the handed-out credentials). The backend logs a prominent
`[storage] WARNING` at startup whenever it runs in this mode.

To enforce per-app isolation, configure STS:

```bash
VULOS_STORAGE_STS_ENDPOINT=https://sts.example.com   # required to enable STS
VULOS_STORAGE_STS_ROLE_ARN=arn:...                   # optional
VULOS_STORAGE_STS_DURATION_SECONDS=900               # optional (default minter value)
```

When set, the gateway hands apps short-lived credentials scoped down to the
app's own `<userID>/<appID>/` prefix. **STS is REQUIRED for any deployment that
runs more than one storage-permitted app per user and needs them isolated.**

### `/etc/vulos/vulos.yaml`

OS backend config. Inherits from `fabric.yaml` and `storage.yaml`.

### `/etc/vulos/mail.yaml`

vulos-mail config. Inherits from `fabric.yaml` and `storage.yaml`.

### `/etc/vulos/office.yaml`

vulos-office config. Inherits from `fabric.yaml` and `storage.yaml`.

---

## Installer flags (`get.vulos.org`)

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | off | Print plan without making changes |
| `--storage=tigris` | on | Use Tigris hosted S3 storage |
| `--storage=minio` | off | Install and use local MinIO (`--storage=local` alias) |
| `--no-enable` | off | Install units but do not enable/start (CI/containers) |
| `--help` | — | Print usage |

```bash
# Dry run
curl -fsSL https://get.vulos.org | sudo bash -s -- --dry-run

# With local MinIO
curl -fsSL https://get.vulos.org | sudo bash -s -- --storage=minio
```

---

## Shared directory layout (self-hosted)

```
/etc/vulos/
  fabric.yaml       — mesh identity, domain, TLS, cloud endpoint
  storage.yaml      — S3/MinIO credentials and backend selector
  vulos.yaml        — OS backend config
  mail.yaml         — vulos-mail config
  office.yaml       — vulos-office config
  bundle.yaml       — installer metadata (arch, distro, storage mode)

/var/lib/vulos/
  fabric_public.pem   — shared fabric X25519 public key
  fabric_private.pem  — shared fabric X25519 private key (mode 600)
  vulos/              — OS backend data
  mail/
    x25519_public.pem
    x25519_private.pem (mode 600)
  office/
    uploads/
  minio/              — only when --storage=minio
    .minio_secret     — MinIO root password (mode 600)
```

---

## Vite / frontend build

The Vite dev server runs on `:5173` and proxies `/api` and `/app` to the backend on `:8080`.

| Command | What it does |
|---------|-------------|
| `npm run dev` | Vite dev server with HMR |
| `npm run build` | Production build into `dist/` |
| `npm run preview` | Preview the production build locally |
| `npm run test` | Vitest unit tests |
| `npm run lint` | ESLint |
| `npm run screenshots` | Playwright screenshotter → `docs/screenshots/` |

---

## Bare-metal seed variables

These are baked into the OS image at build time by `build.sh` and cannot be changed at runtime without a rebuild.

| Variable | Purpose |
|----------|---------|
| `VULOS_OS_BUCKET_URL` | OS update bucket URL (default `https://os.vulos.org`) |
| Trust anchor key | Ed25519 public key baked into `/etc/vulos/trust-anchor.pub` and the initramfs; controls which squashfs updates are accepted |

For forks, supply both your own trust anchor key and bucket URL at build time:

```bash
VULOS_OS_BUCKET_URL=https://os.myfork.example ./build.sh --live
```

See [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md) for the full signing and verification workflow.

---

## Observability

| Endpoint | Description |
|----------|-------------|
| `GET /metrics` | Prometheus textfile (`vulos_*` namespace) |
| OTel traces | Active when `OTEL_EXPORTER_OTLP_ENDPOINT` is set; uses `backend/internal/obs.Start()` |

---

## Service worker cache names

See [SW-CACHE-VERSIONS.md](SW-CACHE-VERSIONS.md) for the cross-repo coordination table. Current name for this repo: `vulos-os-shell-v1`.
