# Getting Started

By the end of this page you will have a Vulos box: a computer you own, running a
full desktop you reach from any browser on your network — Files, Mail, Calendar,
Contacts, a terminal, an assistant — with no account on anybody's cloud and no
sign-in to anything but your own machine.

**What it costs you**

| | |
|---|---|
| **Time** | About 2 minutes for the Docker route. About 20 minutes for a real machine, plus the download. |
| **Hardware** | A spare PC, mini-PC or laptop you are willing to wipe. Docker needs no hardware at all. |
| **A USB stick** | 4 GB or larger, for the two hardware routes. Its contents are erased. |
| **Money** | Nothing. Vulos is free software you run yourself. There is no Vulos account, no hosted tier, and nothing to pay for. |

Already running? [USER-GUIDE.md](USER-GUIDE.md) is the day-to-day manual.
Configuring it? [CONFIGURATION.md](CONFIGURATION.md). Curious how it works?
[ARCHITECTURE.md](ARCHITECTURE.md).

---

## Before you start

Read this section now rather than at step 6. Two of these will stop you dead
partway through if they are wrong.

**For either hardware route (live USB or disk install):**

- **UEFI firmware, not legacy BIOS.** The bootloader is `systemd-boot`, which
  only exists in the UEFI world. In your firmware settings, boot mode must be
  UEFI — not "Legacy", not "CSM".
- **Secure Boot must be OFF.** Nothing in the build signs the bootloader for
  Secure Boot, so a machine with it enabled will refuse to start Vulos. Turn it
  off in firmware settings before you begin.
- **x86_64 or arm64.** The published ARM64 image targets *generic UEFI-booting*
  ARM64 machines and VMs. It will **not** boot a Raspberry Pi or a PinePhone,
  whose firmware works differently — those need an image built for them
  (`sudo ./build.sh --arm64 --device rpi4`, or `--device pinephone`).
- **2 GB RAM minimum**, 4 GB or more if you want it to feel good.
- **4 GB or more if you are plugging in more than one monitor.** Vulos runs one
  browser per screen, and each screen after the first costs roughly 150–170 MB.
  Two or three screens do fit in 2 GB — that is measured, not guessed, and
  nothing refuses to start on a 2 GB box with three monitors — but what is left
  over is thin, and thin is where a box stops feeling good. Treat this as a
  recommendation for buying, not a floor for booting. The measurements are in
  [roadmap/SCREENS-COST.md](../roadmap/SCREENS-COST.md).
- **10 GB of disk**, 20 GB or more recommended, if you are installing to disk.
- **A monitor and keyboard** for the first boot — or none at all, if you would
  rather do the whole thing from another device's browser
  ([Running headless](#running-headless)).

**For Docker:** Docker 24 or newer (OrbStack works on macOS), and 2 GB of RAM.

---

## Pick how to install

This is your first real decision, so here is the honest comparison rather than a
list.

| | Who it's for | Survives a reboot? | Real hardware? |
|---|---|---|---|
| **[Docker](#docker-see-it-in-two-minutes)** | "Show me what this is." No hardware commitment, works on the laptop you're reading this on. | Yes — in a Docker volume | No |
| **[Live USB](#live-usb-run-it-on-real-hardware)** | "Will it run on that old machine?" The real desktop on real hardware, writing nothing to the machine's disk. | **No.** Everything is gone on reboot. | Yes |
| **[Deploy over SSH](#deploy-onto-a-linux-machine-you-already-run)** | "I already have a machine for this." A VPS, or any Linux box on your desk you can SSH into as root. | Yes | Yes |

**The recommendation:** if you just want to see Vulos today, use Docker — one
command, and one command to throw it away. If you have a spare machine, flash
the live USB and boot it: that is the real desktop on real hardware and it tells
you in ten minutes whether that machine is a good host. To then *keep* it, put
some Linux on that machine and use the deploy route.

**A fourth route needs a signed release.** Installing to a bare machine's own
disk from inside the live session — `vulos-install --disk` — is the path the
README and the release notes call primary. The command ships and finds the image
now; what it still requires is a release carrying a hand-signed `stable.json`,
because the release key deliberately never touches a build machine. The details
are in [Installing to the machine's own disk](#install-it-to-the-machines-disk)
below, including what the refusal looks like so you can recognise it.

[Building from source](#building-from-source) is at the end, if you want to work on
Vulos itself.

---

## Docker: see it in two minutes

This gives you the Vulos web desktop in a container. It is not the bare-metal
OS — there is no kiosk screen, no bootloader, no disk install — but the desktop,
apps, files and setup wizard are the same software.

```bash
docker run -d \
  --name vulos \
  -p 8080:8080 \
  --shm-size=1g \
  -v vulos-data:/root/.vulos \
  ghcr.io/vul-os/vulos:latest
```

Then open **http://localhost:8080**. You should land in the [setup
wizard](#first-boot-the-setup-wizard).

Your data lives on the `vulos-data` named volume, so it survives `docker stop` /
`docker start`, container removal and upgrades. It does not survive
`docker volume rm vulos-data` — that is the delete button.

`--shm-size=1g` is not optional if you intend to use streamed apps: the default
64 MB of shared memory is too small and sessions crash or render corrupt frames.

**Optional — hardware video encoding for streamed apps:**

```bash
# Intel/AMD (VA-API)
docker run -d --name vulos -p 8080:8080 --shm-size=1g --device /dev/dri \
  -v vulos-data:/root/.vulos ghcr.io/vul-os/vulos:latest

# NVIDIA (needs nvidia-container-toolkit on the host)
docker run -d --name vulos -p 8080:8080 --shm-size=1g --gpus all \
  -v vulos-data:/root/.vulos ghcr.io/vul-os/vulos:latest
```

Without a GPU passed through, streamed apps still work — they fall back to a
software encoder and are slower.

---

## Live USB: run it on real hardware

A **live session**: the real Vulos desktop, booted from the stick, with nothing
written to the machine's internal disk. It is the right way to find out whether
your hardware works before you commit it, and it is also a fine rescue or kiosk
environment because it is identical on every boot.

It is **not** an install. Everything you do — accounts, files, settings —
disappears when you reboot or pull the stick. To keep what you build, do the
[disk install](#install-it-to-the-machines-disk) afterwards.

### 1. Download

From the [Releases page](https://github.com/vul-os/vulos/releases):

| File | For |
|---|---|
| `vulos-vX.Y.Z-x86_64.img.gz` | Intel/AMD PCs, mini-PCs, laptops, most VMs |
| `vulos-vX.Y.Z-arm64.img.gz` | Generic UEFI-booting ARM64 machines and VMs (**not** Raspberry Pi — see [Before you start](#before-you-start)) |

Every release also ships a `SHA256SUMS` file covering both images. Check your
download before you write it to anything:

```bash
sha256sum -c SHA256SUMS --ignore-missing
```

You want to see `OK` beside the file you downloaded. Anything else means a bad
download — fetch it again rather than flashing it.

### 2. Write it to the USB stick

> **This erases the target drive completely, with no confirmation and no undo.**
> `dd` will write to whatever device you name, including the disk holding your
> own operating system. Find the right device name first, and read it twice.

```bash
lsblk            # Linux — look for the size that matches your stick
diskutil list    # macOS
```

Then, replacing `/dev/sdX` with your stick (the whole device — `/dev/sdb`, not
`/dev/sdb1`):

```bash
gunzip -c vulos-vX.Y.Z-x86_64.img.gz | sudo dd of=/dev/sdX bs=4M status=progress
```

`dd` prints bytes written as it goes and can sit at the end for a while flushing
to a slow stick. That is normal; let it finish.

**If you would rather not use `dd`,** [Balena Etcher](https://etcher.balena.io/)
does the same job on any OS and makes it much harder to pick the wrong drive —
it lists removable devices only. Drag the `.img.gz` in as-is; it decompresses
while flashing, so you do not need to `gunzip` first.

### 3. Boot the machine from it

Plug the stick in, power on, and open the firmware's boot-device menu — usually
F12, F10, Esc or Del at power-on, depending on the manufacturer. Pick the USB
stick, and make sure you are choosing its **UEFI** entry if the menu offers
both.

You will see a Vulos boot splash, then either the desktop or a status screen —
which one depends on whether a monitor is attached. That is the next section.

---

## What you'll see on screen

**With a monitor connected**, the machine boots into a fullscreen kiosk browser
showing Vulos itself. There is no login prompt and no terminal in the way: the
first boot goes straight to the [setup wizard](#first-boot-the-setup-wizard),
and later boots go straight to the desktop.

**With no monitor, or in the minute before the browser comes up**, the physical
console shows a plain status screen instead of a login prompt. There are no
console passwords configured on the image, so this screen exists to tell you
what is happening without ever offering a shell. It redraws every 15 seconds:

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

Those are real values, not placeholders. Read them like this:

| Field | Healthy | What else you might see |
|---|---|---|
| `Address` | Your LAN IP | `(no network yet)` — no DHCP lease yet |
| `Server` | `running` | `NOT running` — the backend hasn't started; give it a minute on first boot |
| `HTTP` | `up` | `down` — the box is not yet serving on port 8080 |
| `HTTPS` | `up` | `loopback-only` or `off`, with the reason printed underneath, taken from the box's own log |

The `https://` line only appears when HTTPS actually came up.

This status screen belongs to a systemd unit (`vulos-console.service`), so you
get it on the live USB and on a machine set up with the deploy route.

### Reaching it from another device

Open `http://<the address shown>:8080` from a browser on **any other device on
the same network** — your laptop, your phone, whatever is nearest. You land in
exactly the same place the kiosk shows.

The `https://` address works too, but the certificate is self-signed, so your
browser will warn you once. Accepting it is expected on your own LAN.

---

## Deploy onto a Linux machine you already run

If you have a VPS, or a spare machine with any Debian-family Linux already on
it, this is the way to get a Vulos box that keeps its state. The deploy script
builds Vulos on **your** machine and pushes the result onto **that** one over
SSH, then runs it as a systemd service.

**What you need:**

- The target reachable over SSH **as root with a key** — the script tests the
  connection with `BatchMode`, so a password prompt counts as a failure.
- A **Debian-family** target. First-run setup uses `apt-get` and edits
  `/etc/apt/sources.list.d/debian.sources`.
- **Go 1.25+ and Node 22+ on your own machine**, because the build happens
  locally before anything is copied.

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos
./build.sh --deploy YOUR_SERVER_IP
```

It verifies SSH first, installs system packages on the first run only (marked
by `/var/lib/vulos/.setup-complete`, so later deploys skip it), hardens sshd to
key-only, copies `vulos-server` and `vulos-init` to `/usr/local/bin/`, and
starts a `vulos.service` unit. Then open port 8080 on that host in a browser and
you are in the [setup wizard](#first-boot-the-setup-wizard).

A deployed box has no kiosk browser — the deploy package set deliberately omits
one, because a server does not need a screen. You drive it from a browser
elsewhere, which is how Vulos is meant to be used anyway.

**On the `--domain` flag:** it cannot be used alone. Automatic TLS is issued
over DNS-01, so `--domain` without DNS credentials in the same command exits
immediately:

```bash
./build.sh --deploy YOUR_SERVER_IP --domain os.example.com --dns-namecheap USER APIKEY
```

Full detail, including what Caddy is doing and how upgrades work, is in
[DEPLOY.md](DEPLOY.md).

---

## Install it to the machine's disk

This is the route the project README, the release notes and
[USER-GUIDE.md](USER-GUIDE.md) all call the primary one: Vulos on a bare
machine's own drive, booting without the USB stick, everything persisting.

**It needs one thing the project cannot ship for you, and this page would rather
say so than send you after it.** Two of the three things that used to be missing
are now in place:

1. ~~The `vulos-install` command is on no shipped image.~~ **Fixed.** `build.sh`
   now compiles `./cmd/installer` alongside the server, and the build fails if
   the binary is not present and executable in the rootfs the image is packed
   from. Boot the live USB, open Terminal, and `vulos-install` is there.
2. ~~The live image does not carry what the installer would read.~~ **Fixed.**
   The installer used to default to `/run/live/medium/vulos/os-core.squashfs`, a
   path no Vulos image creates — the OS is at `/image.squashfs` on the partition
   labelled `VULOS-LIVE-DATA`. It now searches the paths the image is legitimately
   reachable under and tells you every one it tried if it finds nothing.
3. **No release ships the signed manifest it requires — and this one is
   deliberate.** `stable.json` and `stable.json.sig` are produced by an offline
   signing step, not by CI, because the release private key never touches a build
   machine. They appear among a release's assets only when the maintainer has
   signed that version by hand. Without them the installer refuses to proceed
   rather than writing an image whose signature it could not check.

So the route works when a signed release exists, and refuses clearly when one
does not. If you hit that refusal, you have not done anything wrong: check the
release's assets for `stable.json` and `stable.json.sig`, and use one of the
routes above in the meantime.

### What it would give you, when it lands

Worth stating precisely, because "verified boot" gets used loosely and the
installer's own source is clear about its limits:

- **A persistent ext4 root** labelled `vulos-root`, with the signed release
  manifest verified at **every** boot. A failed signature check halts the boot
  rather than continuing — deliberately.
- **No dm-verity.** The installer writes a plain ext4 root, so there is no
  block-level integrity device for the signed root hash to be bound to. The
  boot logs that plainly rather than implying otherwise.
- **No A/B slot updates.** The initramfs code that reads `boot-state.json` and
  boots the slot it names exits early unless the kernel command line says
  `vulos.live`, and the boot entry this installer writes does not. Staging an
  update from Settings on such a disk would have nothing that boots it.
- **Destructive, with one confirmation.** It writes a new GPT over the target —
  every partition, OS and file gone, no undo — and it does not inspect the disk
  first, so it cannot warn you what was there. It prints
  `WARNING: All data on /dev/sdX will be overwritten.` and waits, and that is
  the only guard.
- **Unproven on real hardware.** It has not been run end-to-end against a
  physical disk outside development testing.

dm-verity and the A/B slot flip are both real and both proven by actual
reboots — but on the **netboot install** path, a different mechanism that has no
user interface yet and that this page does not cover. [ARCHITECTURE.md → OS
distribution](ARCHITECTURE.md#os-distribution-bare-metal) states the exact
bounds of those proofs.

### What to do instead

For a machine that keeps its state, use the [deploy
route](#deploy-onto-a-linux-machine-you-already-run): install any Linux you like
on the machine, then push Vulos onto it over SSH. You get a persistent box on
hardware you own today, without the bootloader work.

---

## First boot: the setup wizard

However you got here — Docker, live USB or a deployed server — the first time
you open Vulos it checks whether an account exists. If none does, you get the
setup wizard instead of a sign-in screen.

It opens with a fork:

- **New** — set this box up from scratch. Everything below.
- **Join existing** — attach this machine to a Vulos box you already run and sync
  into it. That is how you add a second box or restore one; see
  [ADD-DEVICE.md](ADD-DEVICE.md) and
  [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).

Choosing **New** walks you through, and most steps can be skipped:

1. **Welcome**, then **device type** — PC/tablet/mobile, TV, car or watch. This
   is not cosmetic: it reshapes the whole interface (a TV gets a 10-foot,
   remote-driven UI).
2. **Language**, **Timezone**, **Network** — scan and join WiFi here, or skip it
   if you are on Ethernet.
3. **Account** — display name, username, password. This is a **local account on
   this machine**. There is no cloud sign-in step anywhere in Vulos. It becomes
   the administrator account, including for `sudo` in the Terminal. You can also
   enrol a passkey (WebAuthn/FIDO2) here.
4. **Device PIN** — optional quick-unlock for the lock screen.
5. **Your apps** — two checkboxes, both pre-checked. Files, Calendar and
   Contacts are always included either way.
   - *"Claim your Vulos username — enables Mail"* works as described: unchecking
     it hides the Mail tile.
   - *"Install the productivity app — Diwan"* **currently does nothing.** Your
     choice is saved, but nothing reads it: the launcher's suite map has an entry
     only for Mail, so there is no tile for this checkbox to hide, and Diwan runs
     as its own service rather than as a launcher tile. Unchecking it does not
     stop that service being installed. See [APPS.md](APPS.md).
   - Neither box gives you a mailbox. Vulos hosts no email; Mail, Calendar and
     Contacts connect to accounts you already have
     ([MAIL-CALENDAR-CONTACTS.md](MAIL-CALENDAR-CONTACTS.md)).
6. **Appearance**, **Identity** (the box's hostname), **Storage** — optionally
   connect an S3-compatible bucket for encrypted backup. Skipping is fine; your
   data lives on the box's own disk regardless
   ([STORAGE-PROVIDERS.md](STORAGE-PROVIDERS.md)).
7. **SSH** and **Recovery kit** — generate an SSH keypair for the box, and take
   your account recovery material.
8. **Ready** — a summary of what you chose. Finishing creates the account.

> **The moment the account is created you are shown a 24-word recovery phrase,
> once.** You must tick a box confirming you have written it down before the
> wizard will continue, and it is never shown again. **A forgotten password
> cannot be recovered without it.** Write it on paper and store it somewhere
> other than the box. See [IDENTITY-KEYS.md](IDENTITY-KEYS.md) and
> [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).

Then you are on the desktop.

<picture>
  <img src="screenshots/hero-light.png" alt="The Vulos desktop after first-boot setup: menu bar, Home brief with agenda and assistant composer, and quick-launch tiles" width="880" />
</picture>

---

## Running headless

If the machine has no monitor, you can do all of the above from another device's
browser.

1. Boot it and give it a minute. On the live USB with no display connected, the
   kiosk step is skipped entirely — the log line is `no display connected,
   skipping kiosk (headless mode)`.
2. Find its address. Your router's DHCP client list is the usual way; the box
   also announces itself over mDNS as `vulos.local`. Attaching a monitor just
   long enough to read the [status screen](#what-youll-see-on-screen) works too.
3. Open `http://<that address>:8080` from any device on the same network and go
   through setup there.

---

## Check that it's working

From the box, or from any machine on the network with its address substituted
for `localhost`:

```bash
# Is the backend alive? No session needed.
# 200 with {"status":"ok","timestamp":...}, or 503 with "degraded".
curl http://localhost:8080/api/health
```

`degraded` means one of three checks failed: the data directory is not writable,
free disk has fallen below 500 MiB, or storage sync has fallen behind. Which one
is deliberately not shown to an anonymous caller — add your session cookie
(`-b "$COOKIE"`) to see the breakdown, or look at the Box Health panel in
Settings.

```bash
# Which video encoder was detected: software | vaapi | nvenc
curl http://localhost:8080/api/browser/status | jq .gpu_tier
```

`software` is a working answer, not an error — it means no hardware encoder was
found and streamed apps will use the CPU.

```bash
# Prometheus metrics. Owner-only: anything unauthenticated gets 403, not an
# empty page. Either pass the scrape token...
curl -H "Authorization: Bearer $VULOS_METRICS_TOKEN" http://localhost:8080/metrics | grep vulos_
# ...or an admin session cookie.
curl -b "$COOKIE" http://localhost:8080/metrics | grep vulos_
```

---

## Upgrading

**Live USB.** There is no upgrade step — it boots exactly the image on the stick
every time. Download a newer `.img.gz` and reflash.

**Deployed with `./build.sh --deploy`.** Re-run the same deploy command; it
skips the one-time package install and replaces the binaries. See
[DEPLOY.md → Upgrading](DEPLOY.md#upgrading).

**Settings → OS Update** shows the running version and the latest verified one
and offers **Download & stage**. Understand what it is before you rely on it:
the background check only ever *verifies* against the trust chain baked into the
image — it never downloads or stages anything by itself. Staging is admin-only
and needs a fresh step-up re-authentication. Nothing ever reboots the box for
you. And what boots a staged slot is the A/B mechanism described under
[Installing to the machine's own disk](#install-it-to-the-machines-disk), which
needs a signed release to install in the first place — so on a live USB or
a deployed server, staging still has nothing that will bring the new image up.

**Docker:**

```bash
docker pull ghcr.io/vul-os/vulos:latest
docker stop vulos && docker rm vulos
# Re-run your original `docker run` — your data is on the named volume
```

**Database schema.** Every upgrade path is safe for the local databases. The OS
applies schema migrations on boot, **forward-only** and **fail-closed** — a bad
migration aborts the boot rather than running against a half-migrated database.
You never run migrations by hand, though `vulos-server migrate up` and
`vulos-server migrate status` exist for out-of-band provisioning. See
[MIGRATIONS.md](MIGRATIONS.md).

---

## When something goes wrong

Headings here are what you *see*, not what is broken underneath.

**The machine won't boot the USB stick at all.**
Almost always firmware. Confirm boot mode is UEFI and not Legacy/CSM, and that
Secure Boot is disabled — Vulos's bootloader is not signed for it, so a
Secure-Boot machine refuses to start it. If the boot menu lists the stick twice,
pick the UEFI entry.

**`dd` seems to hang, or you are not certain it hit the right disk.**
`dd` reports progress and then pauses at the end while it flushes to the stick,
which can take a while. If you are unsure which device you named, stop and check
with `lsblk` / `diskutil list` before running it again — there is no undo. [Balena
Etcher](https://etcher.balena.io/) is the safer tool if this makes you nervous.

**The console says `Server: NOT running`.**
Give it a minute. The first boot applies database migrations before the backend
starts serving. If it does not clear, see
[TROUBLESHOOTING.md](TROUBLESHOOTING.md).

**The console says `HTTPS: off` or `loopback-only`.**
The reason is printed on the line underneath, straight from the box's own log.
Usually it means there is no LAN address yet because DHCP has not finished.

**The console shows `Address: (no network yet)`.**
No DHCP lease. Check the cable, or attach a monitor and join WiFi through the
setup wizard.

**`vulos-install: command not found` in the live session's Terminal.**
Expected, not a broken install. That binary is not compiled into any shipped
image — see [Installing to the machine's own
disk](#install-it-to-the-machines-disk).

**The boot splash reaches 100% and stays there forever.**
Worth reporting with the machine's details. This was a real bug: the image
shipped no kiosk browser at all, so the box sat on a finished-looking splash
with the only explanation in the journal. It is fixed — the image now ships
`cog` — and the failure is now announced on screen rather than in silence, but
the symptom is distinctive enough to be worth naming here.

**The desktop never appears, and the console says a kiosk browser is missing.**
That is the fixed failure above, being loud. The message names what it looked
for (`cog`, `chromium`, `chromium-browser`) and reminds you the server is still
running and reachable on port 8080 — so you can finish setup from another
device.

**The desktop hangs on first boot under Docker.**
`/dev/uinput` is not reachable in the container. Vulos tries to create it itself
and falls back to a much slower input path when it cannot. Add
`--device /dev/uinput` to your `docker run`.

**Streamed apps crash or render corrupt frames under Docker.**
Shared memory is too small. Run with `--shm-size=1g`.

**`/metrics` looks empty.**
It is not empty, it is refused: `/metrics` is owner-only and returns `403` to an
unauthorised scrape. Pass an admin session cookie or set `VULOS_METRICS_TOKEN`.

For deeper diagnosis — where the logs live, sign-in failures, reachability —
see [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

---

## Building from source

To work on Vulos itself. Node 22+ and Go 1.25+:

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos

# Terminal 1 — frontend, hot reload
cd frontend && npm install
npm run dev            # Vite on http://localhost:5173

# Terminal 2 — backend
cd backend && go run ./cmd/server -env local
```

Vite proxies `/api` and `/app` to port 8080. `local` mode needs no TLS and no
accounts anywhere. Or run both at once with `./scripts/dev.sh`, or
`./scripts/dev.sh deploy` for a full Docker build on `localhost:8080`. See
[DEVELOPMENT.md](DEVELOPMENT.md).

---

## Where to go next

You have a box. The obvious next step is [USER-GUIDE.md](USER-GUIDE.md) — the
daily-driver manual for windows and the dock, Files, the App Hub, the assistant,
and using Vulos from your phone.

After that, in the order most people need them:

- [Reaching your box from outside your home network](NETWORKING.md)
- [Connecting your existing mail, calendar and contacts](MAIL-CALENDAR-CONTACTS.md)
- [Backups, and what the recovery phrase can and cannot save](BACKUP-RECOVERY.md)
- [Running more than one box](MULTI-INSTANCE.md)
- [Hardening a box you intend to expose to the internet](SECURITY.md)
