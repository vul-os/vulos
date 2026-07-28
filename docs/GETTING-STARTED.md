# Getting Started with Vulos

This guide covers how to install Vulos, run it for the first time, and get through first-boot setup. For deeper configuration options see [CONFIGURATION.md](CONFIGURATION.md). For the full architecture see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## Requirements

| Component | Minimum | Recommended |
|-----------|---------|-------------|
| OS | Linux x86_64 or arm64 (Debian/Ubuntu) | Debian 13 Trixie |
| RAM | 2 GB | 4 GB |
| Disk | 10 GB | 20 GB+ |
| Node.js | 22+ | latest LTS |
| Go | 1.25+ | latest stable |
| Docker | 24+ | latest (OrbStack on macOS) |

---

## Installation options

### Option 1 — Docker (quickest, for development or trying it out)

```bash
docker run -d \
  --name vulos \
  -p 8080:8080 \
  --shm-size=1g \
  -v vulos-data:/root/.vulos \
  ghcr.io/vul-os/vulos:latest
```

Open **http://localhost:8080** to complete first-boot setup.

For GPU-accelerated streaming:

```bash
# Intel/AMD (VA-API)
docker run -d --name vulos -p 8080:8080 --shm-size=1g --device /dev/dri \
  -v vulos-data:/root/.vulos ghcr.io/vul-os/vulos:latest

# NVIDIA (requires nvidia-container-toolkit on host)
docker run -d --name vulos -p 8080:8080 --shm-size=1g --gpus all \
  -v vulos-data:/root/.vulos ghcr.io/vul-os/vulos:latest
```

### Option 2 — Deploy to a cloud/VPS server

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos
./build.sh --deploy YOUR_SERVER_IP --domain os.yourdomain.com
```

This pushes the full stack (Go binary, frontend, GStreamer, Caddy) over SSH and wires up systemd units.

### Option 3 — Bare-metal USB flash

Download the `.img.gz` from [Releases](https://github.com/vul-os/vulos/releases) and flash:

```bash
gunzip -c vulos-vX.X.X-x86_64.img.gz | sudo dd of=/dev/sdX bs=4M status=progress
```

Or use [Balena Etcher](https://etcher.balena.io/) — drag and drop the `.img.gz`.

| Platform | File |
|----------|------|
| x86_64 | `vulos-vX.X.X-x86_64.img.gz` |
| ARM64 | `vulos-vX.X.X-arm64.img.gz` |

Vulos boots into a fullscreen kiosk browser running the React shell. Native Linux apps stream into browser windows via WebRTC — no VNC, no remote desktop.

### Option 4 — Full self-hosted bundle (OS + office)

```bash
curl -fsSL https://get.vulos.org | sudo bash
```

Installs `vulos`, `lilmail`, and `vulos-office` (Ofisi, the office suite) as systemd services with shared config under `/etc/vulos/`. LilMail is a mail/calendar/contacts client for a mailbox you already own (Gmail/Outlook/any IMAP/SMTP) — it hosts no mail; point its config at your own account. See [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md) for full setup and post-install steps.

### Option 5 — Build from source (development)

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos

# Terminal 1 — frontend (hot reload)
npm install
npm run dev           # Vite dev server on http://localhost:5173

# Terminal 2 — backend
go run ./backend/cmd/server --env=local
```

Vite proxies `/api` and `/app` to `:8080`. No TLS, no cloud account needed in `local` mode.

Or use the dev script:

```bash
./dev.sh              # Go + Vite HMR together
./dev.sh deploy       # Full Docker build on localhost:8080
```

---

## First boot

When you open Vulos for the first time you will be taken through a multi-step
setup wizard (`src/auth/Setup.jsx`). Choosing **New** at the first fork takes
you through:

1. **Welcome** and **New vs. Join** — start a fresh install, or join an existing Vulos box/cluster.
2. **Device** — pick a device profile (PC/tablet, TV, car, watch) that tunes the shell layout.
3. **Language**, **Timezone**, **Network** (WiFi, if applicable).
4. **Account** — create your admin account (display name, `@vulos` username, password). This is a local account only — Vulos has no cloud sign-in step. Optionally enroll a passkey (WebAuthn/FIDO2) for phishing-resistant login.
5. **Device PIN** — optional quick-unlock PIN for the lock screen.
6. **Intent** — a few questions about how you'll use Vulos, used only to tune defaults.
7. **Apps** — the default-everything bundle (the owned productivity app **Ofisi** — Docs/Sheets/Slides/PDF/whiteboards — plus the PIM apps) is pre-checked; opt out of anything you don't want. Files, Calendar and Contacts are always included. Calendar/Contacts and mail connect to a mailbox you already own (via lilmail) — there is no Vulos-hosted mailbox and no Vulos-hosted email address.
8. **Appearance**, **Identity** (display name), **Storage** — optionally connect an S3 bucket (Tigris recommended, or local MinIO) for encrypted backup with Restic.
9. **SSH** and **Recovery kit** — download your account recovery material.
10. **Ready** — setup completes and you land on the desktop.

Choosing **Join** instead skips straight to picking up an existing installation's storage and syncing, then the shared PIN + Ready steps.

After setup you land on the desktop shell. The dock at the bottom holds your running apps. Press **F3** for Mission Control (overview of all windows). Open **Settings** to configure WiFi, display, audio, and more.

<picture>
  <img src="screenshots/hero-light.png" alt="The Vulos desktop after first-boot setup: menu bar, Home brief with agenda and assistant composer, and quick-launch tiles" width="880" />
</picture>

---

## Verifying the install

```bash
# Check backend is responding
curl http://localhost:8080/api/health

# Check GPU tier detected (0=software, 1=VA-API, 2=NVENC)
curl http://localhost:8080/api/browser/status | jq .gpu_tier

# Prometheus metrics
curl http://localhost:8080/metrics | grep vulos_
```

---

## Upgrading

### Docker

```bash
docker pull ghcr.io/vul-os/vulos:latest
docker stop vulos && docker rm vulos
# Re-run the original docker run command — data is on the named volume
```

### Self-hosted bundle

```bash
curl -fsSL https://get.vulos.org | sudo bash   # idempotent — re-downloads and re-verifies
sudo systemctl restart vulos-bundle.target
```

### Bare metal

Vulos uses A/B slots with auto-rollback. A signed update is fetched from `os.vulos.org` and staged to the inactive slot. On next boot the slot activates; if services do not come up cleanly within the threshold the bootloader flips back automatically.

### Database schema

Every upgrade path is safe for the local databases: the OS applies schema
migrations automatically on boot, **forward-only** and **fail-closed** (a bad
migration aborts boot rather than running on a half-migrated database). Self-host
and Vulos-managed boxes use the identical runner. You never run migrations by
hand — but `vulos migrate up` / `vulos migrate status` are available for
out-of-band provisioning. See [MIGRATIONS.md](MIGRATIONS.md) for the upgrade model
and how to add the next migration.

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| First boot hangs | Ensure `/dev/uinput` is accessible: `--device /dev/uinput` |
| App launch fails | `appnet` needs `CAP_NET_ADMIN`: add `--cap-add NET_ADMIN` |
| Metrics empty | Verify `/metrics` is reachable; check `obs.Init()` was called in `main()` |
| Port 25 blocked | Use a VPS with port 25 open (Hetzner, OVH) or configure a mail relay |
| Checksum mismatch | Re-run the installer; it re-downloads from scratch |

For more, see [DEPLOY.md](DEPLOY.md) and [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md).
