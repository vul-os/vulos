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
| **[Install to disk](#install-it-to-the-machines-disk)** | "This is my box now." Permanent, boots from its own drive. | Yes | Yes |

**The recommendation:** if you have a spare machine and you want to keep it,
flash the live USB, boot it, confirm the machine is happy, and then install to
disk from inside that live session. That is one sitting, and the live USB is
your fallback if the install goes badly. If you only want to look at Vulos
today, use Docker — you can throw it away with one command.

Two other paths exist and are covered at the end: [installing onto a
Linux server you already SSH into](#other-ways-in), and [running from
source](#other-ways-in) if you want to work on Vulos itself.

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

This status screen is what the **live USB** shows. A machine you have installed
to disk boots differently — see [After the install, what changes on
screen](#after-the-install-what-changes-on-screen).

### Reaching it from another device

Open `http://<the address shown>:8080` from a browser on **any other device on
the same network** — your laptop, your phone, whatever is nearest. You land in
exactly the same place the kiosk shows.

The `https://` address works too, but the certificate is self-signed, so your
browser will warn you once. Accepting it is expected on your own LAN.

---

## Install it to the machine's disk

This is what turns a live session into a box you keep: Vulos on the machine's
own drive, booting without the USB stick, with everything you create surviving a
reboot.

> **Read this before you run anything below.**
>
> `vulos-install --disk` **destroys everything on the target drive.** It writes a
> new partition table over it — every existing partition, operating system and
> file is gone, and there is no undo and no recovery. It does not inspect the
> disk first, so it will not warn you that Windows was on it.
>
> It also has not yet been run end-to-end against a physical disk on real
> hardware outside development testing. It is real code that runs today, not a
> placeholder — but keep the live USB handy, and do not point it at a machine
> holding anything you have not backed up.

### What you get, and what you don't

Honest about this, because the words "verified boot" get used loosely:

- **You get a persistent ext4 root** labelled `vulos-root`, and a signed release
  manifest is verified at **every** boot. If that signature check fails, the box
  halts rather than booting — deliberately.
- **You do not get dm-verity on this path.** The install writes a plain ext4
  root, so there is no block-level integrity device for the signed root hash to
  be bound to. The boot says so in the log rather than pretending otherwise.
- **You do not get A/B slot updates on this path.** The initramfs code that
  reads `boot-state.json` and boots the slot it names only runs for a root
  booted in live mode, and the boot entry this installer writes is not that.
  Staging an update from Settings on such a box therefore has nothing that will
  boot it. To move to a newer version, boot a newer live USB and install again.

Both of those exist and are proven by real reboots — but on the netboot-install
path, which this page does not walk you through and which has no UI yet.
[ARCHITECTURE.md → OS distribution](ARCHITECTURE.md#os-distribution-bare-metal)
has the exact bounds of that proof.

### 1. Boot the live USB and open Terminal

Boot the stick as above and go through the setup wizard. Use throwaway details —
nothing in this session survives the install.

On the desktop, open **Terminal** from the Launchpad, from the dock, or with
Cmd-K.

<picture>
  <img src="screenshots/terminal-light.png" alt="The Terminal app: a real shell on the machine you're running Vulos from" width="880" />
</picture>

### 2. Fetch the signed manifest for this release

The installer refuses to write a system that could not be verified at boot, so
it needs a **signed manifest** — `stable.json` plus its detached signature
`stable.json.sig` — matching the exact release you downloaded. These are release
assets. You cannot generate them: they only appear once the maintainer has signed
that version offline (the reasoning is in [decisions.md](decisions.md)).

```bash
curl -fsSLO https://github.com/vul-os/vulos/releases/download/vX.Y.Z/stable.json
curl -fsSLO https://github.com/vul-os/vulos/releases/download/vX.Y.Z/stable.json.sig
```

Download **both**, into the same directory. The installer always looks for the
signature at the manifest's path with `.sig` appended — there is no separate
flag for it.

**If a release does not list those two files among its assets, it has not been
signed, and a disk install cannot be verified for it.** The live USB is
unaffected; use a signed release, or stay on the live session for now.

### 3. Identify the target disk

```bash
lsblk
```

Pick the **disk**, not a partition — `/dev/sda`, never `/dev/sda1`. If you are
installing onto the same machine you booted the stick from, make very sure you
are choosing the internal drive and not the USB stick you are running from.

### 4. Install

```bash
sudo vulos-install --disk /dev/sdX --disk-manifest ./stable.json
```

It must run as root, and it stops to confirm before touching anything:

```
WARNING: All data on /dev/sdX will be overwritten.
Press Enter to continue, Ctrl-C to abort...
```

This is your last exit. Ctrl-C here changes nothing.

Once you continue, it verifies the manifest signature against the trust anchor
baked into the image **before** the disk is touched — a bad or missing signature
aborts with everything still intact. After that it partitions the drive (a FAT32
EFI System Partition, then an ext4 root labelled `vulos-root`), unpacks the
system, copies the manifest and signature into the new root's `/etc/vulos/`, and
installs the bootloader, printing a percentage as it goes.

The percentage sits at 20% for a while during the unpack. That is the longest
step and it reports no progress of its own; it is not stuck.

**What you should see at the end:** a line saying the install is complete and
that the disk now boots Vulos from its own drive. Anything else — particularly a
`FAILED:` line — means nothing usable was written, and the message names the
reason.

### 5. Reboot into it

Power off, remove the USB stick, and boot normally.

### After the install, what changes on screen

An installed disk boots into the kiosk browser and stays there. Two differences
from the live session are worth knowing:

- **The console status screen is not there.** It is a systemd unit, and an
  installed disk boots Vulos's own init directly rather than systemd. To check on
  the box, use a browser from another device.
- **The desktop starts even with no monitor attached.** The boot entry the
  installer writes tells it to, which is the opposite of the live USB's
  behaviour.

---

## First boot: the setup wizard

However you got here — Docker, live USB or an installed disk — the first time
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

**Installed to disk with `vulos-install --disk`.** Boot a newer live USB and run
the install again. **Settings → OS Update** will show you the running and latest
versions and will let you stage a release, but nothing on this disk layout will
boot a staged slot — see [What you get, and what you
don't](#what-you-get-and-what-you-dont) above.

Either way the update check itself is passive: Vulos verifies its release
channel in the background against the trust chain baked into the image, and that
check **never** downloads or stages anything on its own. Staging is always an
explicit **Download & stage** click, is admin-only, and requires a fresh
step-up re-authentication. Nothing ever reboots the box for you.

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

**`vulos-install --disk` fails signature verification.**
`stable.json` and `stable.json.sig` do not match the release you are installing,
or one of them was not found. The signature must be in the same directory as the
manifest with `.sig` appended. Re-download both from that exact release's assets.
Nothing was written to the disk.

**A disk install boots and then the screen stays on the splash.**
Report it. This was a real bug — the image shipped no kiosk browser, so the box
sat at a finished-looking splash forever with the explanation only in the
journal. It is fixed (the image now ships `cog`), and the failure is now loud
rather than silent, but the symptom is distinctive enough to be worth naming.

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

## Other ways in

**Onto a Linux server you already SSH into.** If you have a VPS or home server
running some other distro, you do not need the USB at all — the deploy script
pushes the whole stack (Go binary, frontend, GStreamer, and Caddy for TLS) over
SSH as root and wires up a systemd unit:

```bash
git clone https://github.com/vul-os/vulos.git
cd vulos
./build.sh --deploy YOUR_SERVER_IP
```

Note that `--domain` is not usable on its own: automatic TLS is issued over
DNS-01, so it requires DNS credentials in the same command
(`--domain os.example.com --dns-namecheap USER APIKEY`) or the script refuses to
start. Full detail in [DEPLOY.md](DEPLOY.md).

**From source, to work on Vulos itself.** Node 22+ and Go 1.25+:

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
