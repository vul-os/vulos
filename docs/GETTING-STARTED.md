# Getting Started with Vulos

This is the install guide: what to download, how to get Vulos running on hardware you own, what you'll see on screen the first time, and how to get through first-boot setup. For day-to-day use once it's running, see [USER-GUIDE.md](USER-GUIDE.md). For configuration options, see [CONFIGURATION.md](CONFIGURATION.md). For the full architecture, see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## Which path is right for you

| You want to... | Do this |
|---|---|
| Put Vulos on a spare PC, mini-PC, or laptop and keep it there | [Try it on a USB stick](#try-it-on-a-usb-stick), then [install to disk](#installing-to-disk-the-primary-path) |
| Try the real desktop in two minutes, no hardware commitment | [Docker](#docker-the-fastest-way-to-try-it) |
| Put Vulos on a VPS or home server you already run and SSH into | [Deploy to a server you already run](#deploy-to-a-server-you-already-run) |
| Hack on Vulos itself | [Build from source](#build-from-source-development) |

If you're not sure: **Docker is the fastest way to see it**, and **installing to disk on a spare machine is the way to actually keep it**. Both are covered below.

---

## Requirements

**Real hardware (USB stick or disk install):**

- x86_64 or arm64, with **UEFI firmware** (not legacy BIOS) — the bootloader is `systemd-boot`, which requires UEFI. **Disable Secure Boot** in your firmware settings first; Vulos's bootloader isn't signed for Secure Boot today, so a Secure-Boot-enabled machine will refuse to boot it.
- 2 GB RAM minimum, 4 GB+ recommended
- 10 GB free disk minimum, 20 GB+ recommended, if you're installing to disk
- A USB stick, 4 GB or larger, to flash the live image onto
- A monitor and keyboard for first boot (or see [Running headless](#running-headless) below if you don't have one)
- Raspberry Pi 4/5 and PinePhone are supported but need a separate build — the CI-published ARM64 image is a **generic** UEFI-booting ARM64 target and will not boot Pi/PinePhone firmware. Build one yourself: `sudo ./build.sh --arm64 --device rpi4` (or `--device pinephone`).

**Docker:**

- Docker 24+ (or OrbStack on macOS)
- 2 GB RAM minimum, 4 GB recommended

**Deploying to a server you already run:**

- A Linux server (VPS or your own box) reachable over SSH as root
- Same RAM/disk minimums as above

**Building from source:**

- Node.js 22+ and Go 1.25+
- Docker, if you want the full containerized build (`./scripts/dev.sh deploy`)

---

## Try it on a USB stick

This is a **live session** — the fastest way to see the real Vulos desktop on real hardware, with nothing written to the machine's internal disk. It's the right choice for a first look, a demo, or a rescue/kiosk environment that's always clean on boot. It is **not** an install: everything you do disappears the moment you reboot or pull the drive. To keep what you build, see [Installing to disk](#installing-to-disk-the-primary-path) below.

### 1. Download

Grab the image for your CPU architecture from the [Releases page](https://github.com/vul-os/vulos/releases):

| File | For |
|---|---|
| `vulos-vX.Y.Z-x86_64.img.gz` | Intel/AMD PCs, mini-PCs, laptops, most VMs |
| `vulos-vX.Y.Z-arm64.img.gz` | Generic UEFI-booting ARM64 hardware and VMs (not Raspberry Pi — see Requirements above) |

Each release also ships a `SHA256SUMS` file. Verify your download before flashing anything to a drive:

```bash
sha256sum -c SHA256SUMS --ignore-missing
```

### 2. Flash it to the USB stick

**On Linux/macOS**, find your USB stick's device name first (`lsblk` on Linux, `diskutil list` on macOS) — get this wrong and `dd` will happily overwrite the wrong disk:

```bash
gunzip -c vulos-vX.Y.Z-x86_64.img.gz | sudo dd of=/dev/sdX bs=4M status=progress
```

**On any OS**, [Balena Etcher](https://etcher.balena.io/) is the safer option if you're not comfortable with `dd` — it lets you pick the target drive from a list and won't let you write over your main disk by accident. Drag and drop the `.img.gz` file; it flashes directly, no need to `gunzip` first.

### 3. Boot it

Plug the USB stick into the machine, power it on, and get into its boot-device menu (usually F12, F10, Esc, or Del at power-on — varies by manufacturer) and pick the USB stick. Make sure UEFI boot is selected, not legacy/CSM boot.

What happens next depends on whether a display is connected — see [What you'll see on screen](#what-youll-see-on-screen) below.

---

## What you'll see on screen

**With a monitor connected:** the machine boots into a fullscreen kiosk browser showing the Vulos desktop — no login prompt, no terminal, just the setup wizard (see [First boot: the setup wizard](#first-boot-the-setup-wizard) below) the first time, or the desktop itself on later boots.

**Without a monitor, or before the kiosk has come up:** the physical console shows a plain status screen instead of a login prompt — there are deliberately no console credentials configured on the image, so this screen exists to tell you what's happening without ever offering a shell. It refreshes every 15 seconds:

```
  Vulos

  Open in a browser:   http://192.168.1.42:8080
                       https://192.168.1.42   (self-signed - accept once)

  Address:   192.168.1.42
  Server:    running
  HTTP:      up
  HTTPS:     up

  This console is status-only. Manage the box from the browser.
```

That's a real IP address and real status — not a placeholder. If `Server` says `NOT running`, the backend hasn't come up yet (give it a minute on first boot). If `HTTPS` says `off`, the screen prints the reason underneath, straight from the box's own logs — usually "no LAN address yet" on a machine that's still getting a DHCP lease.

### Reaching it from another device

Open `http://<the address shown>:8080` in a browser on **any other device on the same network** — your laptop, your phone, whatever's handy. That lands you in the same setup wizard the kiosk shows locally. The `https://` address works too, but it's a self-signed certificate for now, so your browser will warn you once; accepting it is expected and safe on your own LAN.

---

## Installing to disk (the primary path)

Installing to the machine's own disk is the way to actually keep a Vulos box — this is what "your own personal server on hardware you own" means in practice. It turns the live session above into a permanent install: a persistent ext4 root, signature-verified at every boot, with A/B slots that updates stage into. (The slots exist and staging works; the automatic *flip* between them does not yet — see the OS-update note below.)

> **Honesty check:** `vulos-install --disk` is real code that runs today — it isn't a placeholder — but it has not yet been run end-to-end against a physical disk on real hardware outside development testing. Keep the USB stick handy the first time you try it, and don't run it against a machine holding data you haven't backed up.

### 1. Boot the live USB and get a Terminal

Boot the USB stick as described above and go through the setup wizard (it's fine to use throwaway details here — nothing about this session persists once you install). Once you're on the desktop, open **Terminal** from the Launchpad, Cmd-K, or the dock.

<picture>
  <img src="screenshots/terminal-light.png" alt="The Terminal app: a real shell on the machine you're running Vulos from" width="880" />
</picture>

### 2. Get the signed manifest for this release

The installer refuses to write a system it can't cryptographically verify at boot, so it needs a **signed manifest** — `stable.json` and `stable.json.sig` — matching the exact release you downloaded. These are release assets, not something you generate yourself; they only exist on the [Releases page](https://github.com/vul-os/vulos/releases) once the maintainer has signed them offline for that version (see [decisions.md § D99](decisions.md) for why signing is a separate, deliberately offline step).

```bash
curl -fsSLO https://github.com/vul-os/vulos/releases/download/vX.Y.Z/stable.json
curl -fsSLO https://github.com/vul-os/vulos/releases/download/vX.Y.Z/stable.json.sig
```

**If those two files aren't listed among a release's assets, that release hasn't been signed yet and disk installs can't be verified for it.** The live-USB path above is unaffected either way — try a different (signed) release, or stick with the live session for now.

### 3. Find your target disk

```bash
lsblk
```

Identify the disk (not a partition — `/dev/sda`, not `/dev/sda1`) you want Vulos installed onto. **Every existing partition and file on it will be destroyed.** If you're installing onto the same machine you booted the USB stick from, make sure you're picking the internal disk, not the USB stick itself.

### 4. Install

```bash
sudo vulos-install --disk /dev/sdX --disk-manifest ./stable.json
```

The installer prints a warning and waits for you to press Enter before touching anything:

```
WARNING: All data on /dev/sdX will be overwritten.
Press Enter to continue, Ctrl-C to abort...
```

Once confirmed, it verifies the manifest's signature against the trust anchor baked into the image, partitions the disk (a FAT32 EFI System Partition plus an ext4 root labelled `vulos-root`), unpacks the verified system onto it, writes the manifest to the new root's `/etc/vulos/`, and installs the bootloader — printing a percentage as it goes.

### 5. Reboot into the installed system

Power off, remove the USB stick, and boot the machine normally. It comes up exactly like the live session did — kiosk browser or console status screen, depending on whether a monitor's attached — except this time, what you build survives a reboot.

---

## Docker (the fastest way to try it)

No hardware commitment, no USB stick — runs on any machine with Docker:

```bash
docker run -d \
  --name vulos \
  -p 8080:8080 \
  --shm-size=1g \
  -v vulos-data:/root/.vulos \
  ghcr.io/vul-os/vulos:latest
```

Open **http://localhost:8080** to land in the same setup wizard described below. Your data lives on the `vulos-data` named volume and survives container restarts (`docker stop`/`docker start`), rebuilds, and upgrades — it does not survive `docker volume rm`.

For GPU-accelerated app streaming:

```bash
# Intel/AMD (VA-API)
docker run -d --name vulos -p 8080:8080 --shm-size=1g --device /dev/dri \
  -v vulos-data:/root/.vulos ghcr.io/vul-os/vulos:latest

# NVIDIA (requires nvidia-container-toolkit on the host)
docker run -d --name vulos -p 8080:8080 --shm-size=1g --gpus all \
  -v vulos-data:/root/.vulos ghcr.io/vul-os/vulos:latest
```

Without a GPU passed through, streamed apps still work — they fall back to a software (CPU) encoder, just slower.

---

## Deploy to a server you already run

If you already have a VPS or a home server running some Linux distro and reachable over SSH, you don't need the live-USB/disk-install path at all — the deploy script installs Vulos onto that machine directly:

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos
./build.sh --deploy YOUR_SERVER_IP --domain os.yourdomain.com
```

This pushes the full stack (Go binary, frontend, GStreamer, Caddy for TLS) over SSH and wires up a systemd unit. Full detail, including DNS-01 automatic TLS, is in [DEPLOY.md](DEPLOY.md).

---

## Build from source (development)

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos

# Terminal 1 — frontend (hot reload)
cd frontend && npm install
npm run dev           # Vite dev server on http://localhost:5173

# Terminal 2 — backend
cd backend && go run ./cmd/server --env=local
```

Vite proxies `/api` and `/app` to `:8080`. No TLS, no cloud account needed in `local` mode.

Or use the dev script to run both together:

```bash
./scripts/dev.sh              # Go + Vite HMR together
./scripts/dev.sh deploy       # Full Docker build on localhost:8080
```

---

## First boot: the setup wizard

However you got here — USB, disk install, Docker, or a fresh deploy — the very first time you open Vulos in a browser, it checks whether an account already exists. If not, you land in the setup wizard instead of a sign-in screen.

The wizard opens with a fork:

- **New** — set this box up from scratch. Walks through the steps below.
- **Join existing** — connect this device to storage from a Vulos box you already run, and sync into it (adding a second box, or restoring one). See [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).

Choosing **New** takes you through, most steps skippable:

1. **Welcome** and device type — PC/tablet, TV, car, or watch, auto-detected, and it reshapes the whole UI (a TV gets a 10-foot remote-driven interface, for instance).
2. **Language**, **Timezone**, **Network** (WiFi scan-and-join, skip if you're on Ethernet).
3. **Account** — display name, `@vulos` username, password. This is a **local account only** — there is no cloud sign-in step, and this becomes the administrator account you'll use for `sudo` in the Terminal. Optionally enroll a passkey (WebAuthn/FIDO2) for phishing-resistant login.
4. **Device PIN** — optional quick-unlock PIN for the lock screen.
6. **Apps** — the default bundle (the built-in productivity app **Diwan** — Docs/Sheets/Slides/PDF/whiteboards — plus the PIM apps) is pre-checked; opt out of anything you don't want. Files, Calendar, and Contacts are always included. Calendar/Contacts and mail connect to a mailbox you already own — there is no Vulos-hosted mailbox and no Vulos-hosted email address.
7. **Appearance**, **Identity** (hostname), **Storage** — optionally connect an S3-compatible bucket (Tigris, or a MinIO instance you run) for encrypted backup. Skipping this is fine; your data still lives on the box's own disk.
8. **SSH** and **Recovery kit** — generate an SSH keypair for the box, and download your account recovery material.
9. **Ready** — a summary of everything chosen; finishing creates the account.

**The moment your account is created, you're shown a 24-word recovery phrase — exactly once.** You have to tick a box confirming you've written it down before the wizard lets you continue. This is not optional and not re-shown later: **a forgotten password cannot be recovered without this phrase.** Store it offline. See [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) and [SECURITY.md](SECURITY.md).

After setup you land on the desktop. The dock at the bottom holds your running apps; press **F3** for Mission Control (an overview of every window); open **Settings** to configure WiFi, display, audio, and more. For the full day-to-day manual — Files, the App Hub, the assistant, notifications, using it from your phone — see [USER-GUIDE.md](USER-GUIDE.md).

<picture>
  <img src="screenshots/hero-light.png" alt="The Vulos desktop after first-boot setup: menu bar, Home brief with agenda and assistant composer, and quick-launch tiles" width="880" />
</picture>

---

## Running headless

If the machine you're installing on has no monitor attached, you can still do the entire setup from another device's browser:

1. Boot it and give it a minute — with no display connected, the kiosk step is skipped entirely (you'll see `no display connected, skipping kiosk (headless mode)` in the logs if you're watching them over a serial console).
2. Find its IP address another way — check your router's DHCP client list, or attach a monitor just long enough to read the [console status screen](#what-youll-see-on-screen).
3. Open `http://<that address>:8080` from a browser on any other device on the same network and go through setup there.

---

## Verifying the install

```bash
# Check the backend is responding
curl http://localhost:8080/api/health

# Check GPU tier detected — a NAME, not a number: software | vaapi | nvenc
curl http://localhost:8080/api/browser/status | jq .gpu_tier

# Prometheus metrics — owner-only: this returns 403 without credentials.
# Either pass the scrape token...
curl -H "Authorization: Bearer $VULOS_METRICS_TOKEN" http://localhost:8080/metrics | grep vulos_
# ...or send an admin session cookie:
curl -b "$COOKIE" http://localhost:8080/metrics | grep vulos_
```

(Substitute your box's address for `localhost` if you're checking it from another device.)

---

## Upgrading

**Bare metal — live USB or installed to disk.** Vulos checks its release channel in the background automatically; that check only ever verifies, it never downloads or stages anything on its own. To actually upgrade: **Settings → OS Update** shows the running version and the latest verified one, and lets you explicitly **Download & stage** it — staging downloads, verifies, and writes the release to the inactive A/B slot. There is no upgrade step for the **live USB session** — it boots the exact image on the stick every time, so you get a newer version by downloading a newer `.img.gz` and reflashing.

> **A staged image activates on the next reboot (OSDIST-FLIP-01).** This
> section previously said it did not. The initramfs
> (`scripts/initramfs/vulos-live`) now reads `boot-state.json` at init-bottom
> and boots the slot it names, treating the kernel cmdline as the default
> rather than the answer. The systemd-boot entry is still written once at
> install time and still says slot-a; it is no longer what decides. The
> failed-boot rollback works by the same route, in the other direction.
>
> Two things to know before you rely on it. Nothing reboots the box for you —
> staging marks the slot pending and you choose when. And what has been proven
> by an actual reboot is *which slot boots*, not that a wholly different image
> boots, because the test harness hardlinks the two slots' images; see
> [ARCHITECTURE.md → OS distribution](ARCHITECTURE.md#os-distribution-bare-metal)
> for the exact limits of that proof.
>
> The selection fails safe: a missing, unreadable or truncated
> `boot-state.json`, an `active` value outside `a|b`, or a chosen slot whose
> squashfs is absent all keep the current image rather than booting nothing.

**Docker:**

```bash
docker pull ghcr.io/vul-os/vulos:latest
docker stop vulos && docker rm vulos
# Re-run the original docker run command — data is on the named volume
```

**Deployed with `./build.sh --deploy`:** re-run the same deploy command; see [DEPLOY.md → Upgrading](DEPLOY.md#upgrading).

**Database schema.** Every upgrade path is safe for the local databases: the OS applies schema migrations automatically on boot, **forward-only** and **fail-closed** (a bad migration aborts boot rather than running on a half-migrated database). You never run migrations by hand — but `vulos-server migrate up` / `vulos-server migrate status` (subcommands of the server binary itself) are available for out-of-band provisioning. See [MIGRATIONS.md](MIGRATIONS.md).

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `dd` seems to hang, or you're not sure it's writing the right disk | Check the device name again (`lsblk`/`diskutil list`) before running it — there's no undo. Consider [Balena Etcher](https://etcher.balena.io/) instead. |
| Machine won't boot the USB stick at all | Confirm UEFI boot mode (not legacy/CSM) in firmware settings, and that Secure Boot is disabled. |
| First boot hangs on the desktop (Docker) | Ensure `/dev/uinput` is accessible: add `--device /dev/uinput` |
| Streamed apps crash or render corrupt frames (Docker) | Run with `--shm-size=1g` |
| Console shows `Server: NOT running` | Give it a minute — first boot runs database migrations before the backend starts serving. If it doesn't clear, see [TROUBLESHOOTING.md](TROUBLESHOOTING.md). |
| Console shows `HTTPS: off` or `loopback-only` | The screen prints the reason underneath, straight from the box's `[lan]` log line — usually no LAN address yet (DHCP still pending) |
| `vulos-install --disk` fails signature verification | `stable.json`/`stable.json.sig` don't match the release you downloaded, or weren't found — re-download both from the exact release's assets page |
| Metrics endpoint looks empty | `/metrics` is owner-only — an unauthorized scrape returns `403`, not an empty page. Pass a session or set `VULOS_METRICS_TOKEN`. |

For deeper diagnosis — where logs live, health-check internals, sign-in failures, reachability — see [TROUBLESHOOTING.md](TROUBLESHOOTING.md).
