# Does an app installed with `apt-get` survive a reboot?

**Short answer: it depends entirely on the boot path, and the three paths do not
agree.**

| boot path | written by | `/` at runtime | apt install survives a reboot? |
|---|---|---|---|
| live-USB | `build.sh --live` | overlay: squashfs (ro) + **tmpfs upper in RAM** | **NO** |
| live ESP re-flash | `backend/internal/installer/esp.go` | overlay: squashfs (ro) + **tmpfs upper in RAM** | **NO** |
| netboot-installed disk | `backend/services/installer/netboot_install.go` | overlay: slot squashfs (ro) + **tmpfs upper in RAM** | **NO** |
| plain `--disk` install | `backend/internal/installer/disk.go` | the real ext4, mounted `rw` | **YES** |
| plain `--disk` image | `build.sh --disk` | the real ext4, mounted `rw` | **YES** |

**And the crucial correction to the framing: apt is not the variable.** On the
three overlay paths flatpak installs, static-download installs and apt installs
are all equally volatile. On the two `--disk` paths all three equally persist.
Removing the apt entries from `registry.json` would not change one line of this
table. See "What this means for the 29 entries" below before sizing any
migration.

---

## 1. Where an `apt-get` install actually writes

`backend/services/appnet/registry.go` runs an entry's `install` string with
`cmd.Dir = appDir` and `APP_DIR=<appDir>` in the environment. **`apt-get`
ignores both.** dpkg unpacks to the paths recorded in the `.deb`, so an install
writes to `/usr`, `/var/lib/dpkg`, `/var/lib/apt`, `/etc` and `/opt` — the
system tree — on every boot path without exception. Nothing about the working
directory changes that on any path.

So "does it survive a reboot" reduces exactly to "is the system tree on this box
persistent", and that is decided by one token on the kernel command line.

### The token, and why it splits the paths

`cmdline_has` in `scripts/initramfs/vulos-live` matches **both** the bare token
and the `KEY=` form:

```sh
case " $_cl " in
    *" $1 "*|*" $1="*) return 0 ;;
esac
```

The five command lines this repository writes, read out of their own sources
(not restated — `loaderEntryCmdlines` in the guard reads them at test time):

```
build.sh --live      root=LABEL=VULOS-LIVE-DATA ro vulos.live=1 quiet splash …
esp.go               root=LABEL=vulos-root ro quiet splash vulos.live=1 toram vulos.squashfs=/EFI/vulos/os-core.squashfs
netboot_install.go   root=LABEL=vulos-root ro quiet splash vulos.live=0 vulos.slot=a vulos.squashfs=/var/cache/vulos/slot-a/os-core.squashfs
build.sh --disk      root=LABEL=vulos-root rw init=/sbin/vulos-init quiet splash …
disk.go              root=LABEL=vulos-root rw init=/sbin/vulos-init quiet splash …
```

Three carry `vulos.live` (including `vulos.live=0`, which **activates** the hook
— that is deliberate and necessary, see `roadmap/BOOT-FOUR-ERRORS.md`); two do
not. That is the whole split.

On the three that do, the hook builds:

```
lower  = the squashfs, through dm-verity where available   (read-only, sealed)
upper  = /run/vulos/rw/upper   ← on `mount -t tmpfs -o mode=0755 tmpfs-rw`
work   = /run/vulos/rw/work    ← same tmpfs
merged = overlay(lower, upper) → bound over $rootmnt
```

Every byte apt writes lands in `upper`. `upper` is RAM. It is destroyed at
power-off. The lower layer cannot absorb the write even in principle: it is a
read-only squashfs, and on a verity-sealed image any modification would break
the root hash and halt the next boot.

On the two that do not, the hook exits at its gate having done nothing at all,
`/` is the ext4 the installer formatted and labelled `vulos-root`, the kernel
mounted it `rw`, and apt behaves exactly as on any Debian machine.

## 2. Does the `/var/cache/vulos` fix cover `/usr`, `/var/lib/dpkg` or `/opt`?

**No. It was scoped narrowly and it stayed narrow — verified, not inherited.**

The NETB-03 fix captures `${rootmnt}/var/cache/vulos` before the overlay rebind
and re-mounts it on top afterwards, gated on `case "$SQUASHFS_PATH" in
/var/cache/vulos/slot-*)`. Driving the hook to completion and reading its mount
log, the set of subtrees pulled back out of the overlay is:

- live-USB boot: **none**
- netboot-installed boot: **`/var/cache/vulos`, and only that**

**UPDATED 2026-08-17 (OWNSTATE-01).** That netboot list is now three entries,
not one: `/var/cache/vulos`, `/root/.vulos` and `/var/lib/vulos`. An owner
account was measured *not surviving a reboot* on a real installed box, and the
same capture-before-rebind mechanism was applied to the box's data directory —
see `roadmap/OWNER-STATE-PERSISTENCE.md`. The live-USB list is still **none**.

**None of that changes this section's answer, and the answer is now measured
rather than asserted.** `apt-get` writes to `/usr`, `/var/lib/dpkg`,
`/var/lib/apt`, `/etc` and `/opt`; none of them is a subtree of any of the three,
and `/var/lib/vulos` is not a parent of `/var/lib/dpkg` or `/var/lib/apt`.
`TestOnlyVarCacheVulosIsRescuedFromTheOverlay` computes the rescued set from the
hook's own mount log and then checks every apt write target against it, so an
edit that did start persisting one of them fails there with a pointer back to
this note.

One thing the same commit is careful *not* to do, and it matters here: the
netboot boot also mounts a **tmpfs back over `/root/.vulos/apps`**, deliberately
returning the app directory to RAM after its parent is persisted. That test now
pins that set too. So the manifests an installed app leaves behind still die
with the Flatpak/apt payload they point at, exactly as §3 below describes —
`roadmap/APP-DIR-PERSISTENCE.md` measured why anything else is worse.

### Nothing outside the initramfs covers them either

Three facts close it, each checked rather than assumed:

1. **No fstab.** The built rootfs in `output/rootfs` has **no `/etc/fstab` at
   all**. (The one `writeFstabNetboot` produces is written onto the *shadowed*
   partition and is inert — `roadmap/BOOT-FOUR-ERRORS.md` covers why.)
2. **No `.mount` unit** anywhere in the shipped tree.
3. **`mountAll()`** in `backend/cmd/init/main.go` mounts the pseudo-filesystems
   plus `LABEL=vulos-data` at the data dir — and nothing else. It never touches
   `/usr`, `/opt` or `/var`.

## 3. What the user actually experiences

**On today's images: the app disappears from the App Hub entirely. It does not
linger as a listed-but-unstartable ghost.** That is the better of the two
failures, and it is true for a reason worth writing down, because it is one
config change away from being false.

`AppStore.Installed()` is a directory scan of `appsDir`, and
`appsDir = datadir.Join("apps")` — i.e. `$VULOS_DATA_DIR`, or `$HOME/.vulos`
when unset. The `vulos-server` unit `build.sh` writes sets `HOME=/root` and does
**not** set `VULOS_DATA_DIR`. So the manifests live at `/root/.vulos/apps`,
inside `/`, on exactly the same filesystem as everything apt writes. Both are in
the tmpfs; both die together.

**CORRECTED 2026-08-17 for the netboot-installed path.** This paragraph used to
add "along with every other installed app and the rest of the box's state", and
that half is no longer true there. OWNSTATE-01 persists `/root/.vulos` and
`/var/lib/vulos` onto the disk, because the owner's ACCOUNT was dying on every
reboot too — see `roadmap/OWNER-STATE-PERSISTENCE.md`. The app directory was
deliberately excluded from that: the initramfs mounts a **tmpfs back over
`${rootmnt}/root/.vulos/apps`** immediately after the state bind, precisely so
the sentence above keeps holding. Manifests and payloads still die together;
what no longer dies with them is the user's account. The two states are now
pinned separately and exactly by
`TestOnlyVarCacheVulosIsRescuedFromTheOverlay`, and dropping that tmpfs fails
both it and `TestNetbootInstalledDiskKeepsTheOwnerAccountOnDisk`.

The bundled apps in `/opt/vulos/apps` are unaffected — they ship inside the
squashfs and are read-only-persistent. **None of the 29 apt entries has a
bundled counterpart** (the intersection of the registry ids and
`frontend/apps/*` is empty), so there is no entry whose manifest ships in the
image while its binary comes from apt. That is the one configuration that would
produce the ghost-app shape today, and it does not occur.

### The mechanism that was supposed to prevent this is doubly inert

`mountDataPartition()` is the only code in the image that could put
`$HOME/.vulos` on storage independent of the root filesystem. It does not work,
for two independent reasons:

- **It looks up `LABEL=vulos-data`, and nothing in this repository ever creates
  a filesystem with that label.** `build.sh` makes `VULOS-LIVE-DATA` and
  `vulos-root`; `disk.go` makes `vulos-root`; `esp.go` makes `EFI` and
  `vulos-live-data` (a *different* string — `blkid -L` is exact); the netboot
  installer makes `vulos-root` only. `blkid -L vulos-data` returns nothing on
  every image this project builds, so the function returns early every time.
- **It lives in `vulos-init`, which is PID 1 only when the command line says
  `init=/sbin/vulos-init` — and that token appears on exactly the two command
  lines that do NOT activate the overlay.** On all three overlay boots systemd
  is PID 1 and `vulos-init` never executes at all.

The invariant is a clean biconditional and it is now guarded: **a boot runs
`vulos-init` if and only if its root is already persistent.** The data-partition
mount only ever runs where it is not needed.

### The shape that would be worse, and how it becomes reachable

If `$HOME/.vulos` ever becomes persistent while `/` stays an overlay — an
operator setting `VULOS_DATA_DIR` to a mounted volume (README documents this),
or someone finally creating a `vulos-data` partition, or adding
`init=/sbin/vulos-init` to a live entry — then the manifest survives and the
apt-installed binary does not. The App Hub lists the app, the launch fails, and
that is strictly worse than the app being gone. All three of those triggers are
now guarded (`TestNothingCreatesTheDataPartitionLabel`,
`TestVulosInitRunsOnlyWhereTheRootIsAlreadyPersistent`,
`TestInstalledAppManifestsShareTheRootFilesystemsFate`) and each fails loudly
with a pointer back to this note.

**A FOURTH trigger has since been pulled, on purpose, and defused rather than
guarded away.** OWNSTATE-01 makes `$HOME/.vulos` persistent while `/` stays an
overlay on the netboot-installed path — exactly the condition this section
warns about — because the alternative was a box that cannot keep an owner
across a reboot. The ghost shape is prevented not by refusing the trigger but
by mounting a tmpfs back over `/root/.vulos/apps` afterwards, so the manifest
directory is the one thing under the data dir that stays in RAM with its
payload. That is the narrowest available answer: it buys the owner's account
and changes nothing about apps.

It is a *worse* protection than the three above in one specific way, and that
should be said. Those are "the trigger cannot happen"; this is "the trigger
happens and is compensated". If the compensating tmpfs is ever dropped, the
ghost shape is immediately real on every netboot-installed box. Two tests fail
if it is (`TestOnlyVarCacheVulosIsRescuedFromTheOverlay`, which pins the
put-back-in-RAM set exactly, and
`TestNetbootInstalledDiskKeepsTheOwnerAccountOnDisk`), and both were
mutation-verified against exactly that deletion. The durable fix remains
option 2 in `roadmap/APP-DIR-PERSISTENCE.md`: teach
`AppStore.RealisedVersions()` the flatpak liveness check `Registry.ListEntries`
already performs, after which a surviving manifest is merely useless instead of
harmful.

## 4. The image build is a different thing and is not in question

`build.sh` uses debootstrap and apt to **construct** the rootfs, in a chroot,
before `mksquashfs` runs. Those packages end up inside the squashfs and are as
persistent as the image itself. That is correct and nothing here touches it.
Only install-*time* apt — apt run by `registry.go` on a booted box — is the
subject.

Note also that `build.sh` runs `apt-get clean` and `rm -rf /var/lib/apt/lists/*`
before packing, so `packages.CacheReady()` is false on a fresh box and the first
registry install triggers `apt-get update` — pulling the Debian package lists
(tens to hundreds of MB) into the tmpfs before the package itself.

## 5. What this means for the 29 entries

**Counted precisely**, from `registry.json` (56 entries total):

- **29** entries have an `install` string that runs `apt-get`.
- **28** of those actually reach the branch that executes it. `drawio` also
  declares a `download_url`, and `registry.go` tests `DownloadURL`/`HasArtifacts`
  *before* falling through to the install command, so its apt string is dead
  code.
- The other install mechanisms: **13** flatpak, **10** download/artifact, **4**
  raw `wget`/`curl` into the app dir or `/usr/local/bin`, **1** with no install
  step.

**Removing apt entries does not make the App Hub work on the overlay paths.**
The 13 flatpak entries write to `/var/lib/flatpak`, which is in the same tmpfs.
The 10 download entries write into `appDir` under `/root/.vulos`, which is in
the same tmpfs. `minio` writes to `/usr/local/bin`, same tmpfs. On the two
`--disk` paths, all 56 persist.

So the decision in front of the founder is not "keep apt or not". It is:

- **On `--disk`, apt is fine and needs no migration.**
- **On the netboot-installed path, no install mechanism persists.** That is the
  real defect, it is the same family as the `/var/cache/vulos` one, and it is
  unfixed. A migration away from apt would leave it exactly as broken.
- **On live-USB / live ESP, ephemerality is the design of live media** and is
  arguably not a defect at all — but it does mean the App Hub is a demo on those
  boots, and nothing in the UI says so.

There is one genuinely apt-specific consideration, and it is about RAM rather
than persistence. The writable layer is mounted `mount -t tmpfs -o mode=0755
tmpfs-rw` with **no `size=`**, so it defaults to half of RAM. `apt-get install
-y libreoffice`, `blender`, `steam` or `qgis` unpacks hundreds of megabytes to
gigabytes into that tmpfs, on top of the freshly downloaded package lists. On a
small box the install does not merely vanish at reboot — it can exhaust RAM
before it finishes. The download-based entries have the same exposure; flatpak,
being larger still, has more.

---

## How this was established, and what it does NOT cover

**Route: static, by EXECUTING the hook. Nobody booted anything.** Host load was
~90 and a QEMU guest had soft-locked twice at that level; it turned out not to
be needed, exactly as with the `/var/cache/vulos` question.

`backend/internal/docsref/aptpersist_test.go` reuses the harness
`livehook_test.go` established: it runs the whole of
`scripts/initramfs/vulos-live` under `dash` against a fabricated `$rootmnt`
with klibc-shaped `mount`/`mkdir` stubs, and reads the mount log. Mount topology
is the only thing that decides where a write lands, so this settles the
question without a kernel. Every kernel command line is read out of the file
that writes it, so the tests cannot drift from the installers.

**What this proves:** which mounts the hook issues, with what arguments, in what
order, on each real command line — i.e. the mount topology `$rootmnt` is handed
to `switch_root` with, and therefore which paths resolve to the tmpfs.

**What it does NOT prove**, stated so nobody reads more into it:

- The stubs always succeed. It does not show the kernel accepts these mounts on
  real hardware (though every one of them is already exercised by real boots).
- It does not observe a running OS installing a package and failing to find it
  after a reboot.
- It does not cover a box whose operator has set `VULOS_DATA_DIR` or attached
  storage of their own. Those are outside what the repository decides.

### What would settle the remainder, on a booted box

`scripts/verify-apt-persistence.sh --on-box` runs it. The commands, spelled out
so they can be run by hand on a serial console:

```sh
# 1. Which filesystem backs each thing apt writes to?
findmnt -o TARGET,SOURCE,FSTYPE /usr /var/lib/dpkg /opt /etc /root/.vulos
#    overlay        → volatile, this note's answer holds
#    an ext4 device → persistent, this note is wrong for that path

# 2. End to end, and the one thing no static test replaces:
apt-get install -y --no-install-recommends sl && command -v sl   # → /usr/games/sl
reboot
command -v sl                                                    # → nothing, if volatile

# 3. And what the user sees, on the same reboot:
curl -s localhost:8080/api/apps | grep -c '"id"'   # before and after
```

`scripts/netboot-install-smoke.sh` cannot see any of this: it only ever touches
the disk offline, loop-mounted from the host, and never asks the booted guest to
write anything. Extending it to install a package in the guest and survive a
reboot is the natural follow-up and is not done here.
