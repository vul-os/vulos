# The four errors at the top of every boot

Every Vulos boot ever produced opens with the same four lines, in the same
order, immediately after the kernel hands over and before anything else:

```
mount: No such file or directory
mount: invalid option --
mkdir: /root/run: Read-only file system
mount: No such file or directory
```

They are deterministic, they predate the host-starvation on
`boot-evidence-2026-08-15.log`, and they appear on live-USB boots, netboot
installs and the multi-screen QEMU runs alike. This note attributes each one to
the code that emits it and records the verdict.

## Why only these four lines are ever visible

The installed boot entry carries `quiet splash`. initramfs-tools' `_log_msg`
returns immediately when `quiet=y`, so **every deliberate line
`scripts/initramfs/vulos-live` prints — every `log_begin_msg`, every itemised
`log_warning_msg` about verity being inactive — is invisible on a real boot.**
What survives is the raw stderr of commands that failed.

So the only output anyone has ever seen from the live hook is four errors, none
of which were the hook telling them anything. That is how the initramfs log
stopped being read.

The non-quiet counterpart, `output/_netboot-serial-fallback.log`, is what made
attribution possible: it brackets the four lines with the initramfs' own
`Begin: Running /scripts/init-bottom ...` / `done.`, and interleaves the hook's
suppressed messages.

## The structural cause behind all four

`root=` names the **data partition**, not the OS. `/scripts/init-bottom/vulos-live`
is the thing that makes `$rootmnt` the OS root, and it runs *in the middle of*
the init-bottom stage:

```
/scripts/init-bottom/ORDER, read out of the shipped initrd:
  udev        <- $rootmnt is the read-only DATA PARTITION here
  vulos-live  <- makes $rootmnt the OS root, on its last line
  plymouth    <- $rootmnt is the OS root here
```

Anything before that last line which touches `$rootmnt` is operating on a
read-only ext4 that holds `image.squashfs` and nothing resembling a root
filesystem. All four messages are that, twice over.

## Line-by-line

### 1 & 2 — `mount: No such file or directory` / `mount: invalid option --`

**Debian's, not ours.** `/scripts/init-bottom/udev`, one line:

```sh
mount -n -o move /dev "${rootmnt:?}/dev" || mount -n --move /dev "${rootmnt}/dev"
```

- The first half fails because `/root/dev` does not exist — the data partition
  has no `/dev`.
- The fallback exists for a util-linux `mount`; the initramfs' is **klibc's**,
  which has no long options at all, so `--move` is a usage error. klibc's getopt
  prints `mount: invalid option -- ` with nothing after the dashes, which is
  exactly the byte sequence in the logs.

**Verdict: expected, non-fatal, and not silently costly.** `/dev` is not moved
to the new root; systemd mounts `devtmpfs` on `/dev` itself moments later, which
is why every boot works.

**One thing to know, because it is luck rather than design.** That script is
`#!/bin/sh -e`, and under `set -e` a failed `a || b` aborts it — so the two lines
that follow never run:

```sh
nuke /dev                      # rm -rf
ln -s "${rootmnt}/dev" /dev
```

Had they run, `/dev` would be a dangling symlink into a partition with no `/dev`
**before `vulos-live` executes**, and the hook would lose `/dev/mapper/control`
and `/dev/loop*` — i.e. dm-verity would silently fall back to an unverified loop
mount, or `losetup` would fail and panic the boot.

That they did not run is measured, not assumed: in the same boot,
`vulos-live` goes on to print `device-mapper: /dev/mapper/control present` and
the kernel logs `loop0: detected capacity change`. `/dev` was intact.

**Not fixed here.** The clean fix is to give `vulos-live` a `PREREQ` that puts it
*before* `udev`, so `$rootmnt` is already the OS root when `udev` runs and its
move succeeds as designed. That reorders the init-bottom stage of every boot and
cannot be validated by anything short of a real boot on a quiet machine, so it is
recorded rather than attempted.

### 3 & 4 — `mkdir: /root/run: Read-only file system` / `mount: No such file or directory`

**Ours, and dead code.** `scripts/initramfs/vulos-live` had, immediately before
its final rebind:

```sh
# Bind-mount /run/vulos into the new root so our sub-mounts remain visible.
mkdir -p "${rootmnt}/run/vulos"
mount -o bind /run/vulos "${rootmnt}/run/vulos"
```

`$rootmnt` is still the read-only data partition, so the `mkdir` returns EROFS
and the bind then has no target. **These lines have never once done what their
comment says.**

Reordering would not have rescued them. initramfs-tools' `init` does this a few
lines after running us (read out of the shipped initrd, line 281):

```sh
mount -n -o move /run ${rootmnt}/run
```

`MS_MOVE` takes the whole subtree, so `/run/vulos/lower`, `/run/vulos/rw` and
`/run/vulos/merged` travel into the real root with it — and would shadow anything
bound under `${rootmnt}/run` beforehand. The stated goal is already met by the
stock boot path.

That the `/run` in a booted Vulos really is the initramfs' own tmpfs, moved
rather than recreated, is measured: the multi-screen kiosk failed with `EACCES`
exec'ing `/run/vulos-kiosk/session.sh` because the mount carries the initramfs'
`noexec` flag for the life of the system (`roadmap/SCREENS-QEMU.md`).

**Verdict: real defect, fixed by deletion**, with the reasoning left in the file.

## Answering the specific worry: does any of this cost persistence?

**No — not these four lines.** Checked explicitly:

- The data partition mount itself succeeded (`EXT4-fs (vda2): mounted filesystem
  ... ro`) on every log.
- The overlay chain — squashfs/verity lower, tmpfs rw, overlayfs merge, the
  rebind onto `$rootmnt` — all `panic` on failure. The boot continuing is
  positive evidence they succeeded, and the driven run confirms the exact mount
  sequence.
- `record_booted_slot`'s klibc remount is correct: it names the device as klibc
  requires, and on the installed path the harness sees `remount,rw` →
  write → `remount,ro` and a well-formed `booted-slot` marker.
- Nothing in the repository consumes `/run/vulos` from userspace, so the deleted
  bind had no reader even in principle.

## How this is verified now

`backend/internal/docsref/livehook_test.go` runs `scripts/initramfs/vulos-live`
— the whole file, unmodified but for the one absolute `. /scripts/functions` it
cannot satisfy off a real initramfs — under `dash`, against a fabricated
read-only `$rootmnt`, with **klibc-shaped** `mount` and `mkdir` stubs. It covers
both the live-USB and the netboot-installed slot layouts and asserts:

- no `mkdir` may land inside `$rootmnt` during init-bottom
- no `Read-only file system`, no `mount: No such file or directory`, no
  `invalid option --` in the hook's output
- **on a live boot**, exactly one mount may name `$rootmnt`, it must be the
  overlay bind, and it must be **last**

That last assertion is deliberately live-only. The netboot-installed path adds
exactly one mount after the rebind — the on-disk `/var/cache/vulos`, which has to
sit *on top of* the overlay and therefore cannot precede it — and that path has
its own topology assertions in the section at the end of this file. Keeping the
live rule strict is the point: the exception is one named boot, not a licence to
append mounts.

Every assertion is "the hook did *not* do X", which a harness that failed to
start the hook satisfies trivially — so `assertHarnessActuallyRanTheHook` pins
what a real run must have done first. Mutation-verified in all four directions,
including the "hook exited at its own gate" case.

## Still unknowable without a boot on a quiet machine

1. **Whether the `PREREQ` reorder above is safe.** It changes when `udevadm
   control --exit` happens relative to `veritysetup open`, and the hook already
   carries `DM_DISABLE_UDEV=1` for udev-related hangs found the hard way.

2. ~~**Whether `/var/cache/vulos` is persistent on a NETBOOT-installed disk.**~~
   **SETTLED 2026-08-16 — the hypothesis was RIGHT. See the section below.**
   The original wording is kept intact for the record:

   - `netboot_install.go:600` writes
     `root=LABEL=vulos-root ro ... vulos.live=0 vulos.squashfs=/var/cache/vulos/slot-a/os-core.squashfs`.
   - `vulos.live=0` **activates** the hook — `cmdline_has` matches the `KEY=`
     form deliberately (see the comment at `SQUASHFS_PATH`) — so that disk boots
     the overlay, and the ext4 holding `/var/cache/vulos/slot-*` ends up
     *shadowed* beneath the overlay bind at `/`.
   - There is no fstab entry, no `.mount` unit, and nothing in `backend/` that
     remounts that partition once the OS is up.
   - If that reading is right, the running OS's writes to `/var/cache/vulos` —
     the boot counter, `MarkHealthy`, and OTA staging into the inactive slot —
     land in the overlay's **tmpfs upper layer** and are gone on reboot, while
     every unit test passes because they all run against a real directory.
   - `netboot-install-smoke.sh` Phase 4 does **not** cover this: it stages slot-b
     and flips `boot-state.json` *offline*, by loop-mounting the disk image from
     the host. It proves the initramfs reads the file. It proves nothing about
     the OS being able to write it.
   - The plain `--disk` install is unaffected: `disk.go:479` boots
     `root=LABEL=vulos-root rw init=/sbin/vulos-init` with no `vulos.live`, the
     hook exits 0, and the ext4 is the real writable root.

   **What would settle it, in one command on a booted netboot-installed box:**
   `findmnt -o TARGET,SOURCE,FSTYPE /var/cache/vulos` — `overlay` means the
   writes are volatile, the ext4 device means they persist. Equivalently: write a
   file under `/var/cache/vulos`, reboot, and look for it.

---

# `/var/cache/vulos` on a netboot-installed disk — SETTLED, and it was volatile

**Verdict: the hypothesis above was RIGHT.** On a netboot-installed disk the
running OS's `/var/cache/vulos` resolved inside the overlay — squashfs lower,
tmpfs upper — so the OSDIST-03 boot counter, `MarkHealthy`, and OTA staging into
the inactive slot all wrote to RAM and were gone on the next boot, while the
initramfs went on reading the untouched on-disk `boot-state.json`. Fixed in
`scripts/initramfs/vulos-live`; guarded by
`TestNetbootInstalledDiskKeepsVarCacheVulosOnDisk`.

## Which route settled it, and what that route does NOT cover

**Route 1: static, by EXECUTING the hook.** Not a boot. Nobody booted a
netboot-installed image for this; host load was 70–100 and a QEMU guest had
soft-locked twice at that level, and it turned out not to be needed.

`backend/internal/docsref/livehook_test.go` already ran the whole hook under
`dash` against a fabricated `$rootmnt` with klibc-shaped `mount`/`mkdir` stubs.
A third mode was added — `"netboot"` — which differs from the existing
`"installed"` mode in the one way that matters: it drives the hook with the
**exact kernel command line `netboot_install.go` writes**, read out of that file
at test time rather than restated, so the two cannot drift.

The hook's own mount log, from that run, before the fix:

```
mount -o remount,rw /dev/vda2 $rootmnt          <- record_booted_slot
mount -o remount,ro /dev/vda2 $rootmnt          <- record_booted_slot restores ro
mount -t squashfs -o ro /dev/loop0 /run/vulos/lower
mount -t tmpfs -o mode=0755 tmpfs-rw /run/vulos/rw
mount -t overlay -o lowerdir=…,upperdir=…,workdir=… overlay /run/vulos/merged
mount -o bind /run/vulos/merged $rootmnt        <- the ext4 is now shadowed
```

Nothing after the rebind. No mount targets `$rootmnt/var/cache/vulos`, ever.
Together with the three code facts below, that is conclusive without a kernel.

**What this route proves:** which mounts the hook issues, with what arguments,
in what order, on the real installer command line — i.e. the mount topology
`$rootmnt` is handed to `switch_root` with.

**What it does NOT prove**, stated so nobody reads more into it than it carries:

- The stubs always succeed. It does not show the kernel *accepts* the two new
  mounts on real hardware.
- It does not exercise `run-init`'s `MS_MOVE` of `$rootmnt`, nor
  initramfs-tools' `mount -n -o move /run ${rootmnt}/run`, with the extra child
  mounts now present.
- It does not observe a running OS writing a file and finding it after a reboot.

The strongest available evidence for the one step that could not be simulated —
`mount -o remount,rw DEV DIR` succeeding on a real ext4 mounted `ro` — is that
`record_booted_slot` already does exactly that spelling, and it was verified on a
real installed boot (a well-formed `booted-slot` marker on the disk afterwards).
The fix reuses that identical form and the same `rootmnt_device` resolution.

## The three code facts that close it

1. **`vulos.live=0` activates the hook, and must.** `cmdline_has` matches
   `*" $1="*`; `netboot_install.go:569` documents the token as *required, not
   decorative*, because on that machine the OS **is** the squashfs at
   `/var/cache/vulos/slot-a` on that same partition, and this hook is the only
   thing that mounts it. So "stop writing `vulos.live=0`", and "make `cmdline_has`
   ignore the `KEY=0` form", are both **wrong**: either one bricks every
   netboot-installed disk. That framing is refuted, not deferred.

2. **The rebind shadows the ext4 and nothing brings it back.** The image ships
   no `.mount` unit anywhere in the tree, and `backend/cmd/init/main.go`'s
   `mountAll()` mounts the pseudo-filesystems plus `LABEL=vulos-data` at
   `~/.vulos` — nothing else. `main.go:130` even states the assumption out loud
   ("The cache partition is mounted at `/var/cache/vulos` by convention"); no
   code anywhere makes it true.

3. **The one fstab that exists is written to the shadowed partition.**
   `writeFstabNetboot` (`netboot_install.go:769`) writes `/etc/fstab` to
   `netbootInstallMount` — i.e. onto the ext4, whose *only* other content is
   `/var/cache/vulos/slot-*`. The running OS's `/etc/fstab` comes from the
   squashfs. **That fstab is inert: it is written to a filesystem that is never
   the root at runtime.** It is also not the fix even in principle — it says
   `UUID=… / ext4`, which would mount the partition at `/`, not at
   `/var/cache/vulos`. Left as-is (harmless, and it mirrors the `--disk` path
   where it is correct), but recorded here because it is the same
   misunderstanding that produced the defect.

## The fix, and the alternative that was rejected

The subtree is **captured before the rebind and re-exposed after it**:

```sh
mount -o bind "${rootmnt}/var/cache/vulos" /run/vulos/cache   # still reachable
mount -o bind "$MERGED" "$rootmnt"                            # overlay becomes /
mount -o bind /run/vulos/cache "${rootmnt}/var/cache/vulos"   # on top of it
```

It cannot be one step before the rebind. klibc's `mount` has **no long options at
all**, so there is no `--rbind`, and `-o bind` does not carry submounts —
anything mounted under `$MERGED` beforehand simply would not appear under
`$rootmnt`. This is the one mount allowed to follow the rebind, and it has to.

`$MERGED/var/cache/vulos` is `mkdir`'d before the rebind, so no `mkdir` ever
touches `$rootmnt` and the guard from the four-errors work above still holds.

**The remount is not optional.** The cmdline says `ro` and a bind shares its
source's superblock, so without it the OS gets a *read-only* window onto the disk
and still persists nothing. The partition is remounted `rw` and **left** `rw` —
`record_booted_slot` deliberately restores `ro` after its marker, so this comes
after it. That end state is correct: on this layout the ext4 is a data partition,
not the OS root (the OS root is the read-only verity squashfs), and it must be
writable for the update mechanism to function at all.

Gated narrowly on the `/var/cache/vulos/slot-*` layout `writeSlotABootEntry`
pins. A live-USB (`/image.squashfs`) and a re-flashed live ESP
(`/EFI/vulos/os-core.squashfs`) get **not one extra mount**; the `--disk` install
never reaches here at all. Every step is best-effort and nothing panics: a
failure degrades to the old volatile behaviour rather than adding a new way to
fail on the one boot path that cannot be re-tested cheaply.

**Rejected: doing it in `vulos-init`.** It would have to find the partition by
label, mount it at a scratch point, and bind its `/var/cache/vulos` subdir —
because mounting `LABEL=vulos-root` *at* `/var/cache/vulos` exposes the ext4's
root, which contains `var/cache/vulos/slot-a/…`, one level too deep. It would
also have to `remount,rw` a superblock already mounted `ro`, and it would
duplicate slot-layout knowledge the initramfs already has. Worse, it is racy: the
window between PID 1 starting and the bind existing is a window in which writes
still vanish. The initramfs is the only place where the on-disk subtree is
reachable by path at all.

## The guard, and proof it kills

`TestNetbootInstalledDiskKeepsVarCacheVulosOnDisk` drives the whole hook and
asserts **mount topology**, which is the only thing that decides where a write to
`/var/cache/vulos` lands:

- a mount must target `${rootmnt}/var/cache/vulos`;
- its **source must be outside `$rootmnt`** (a source under it resolves to the
  overlay's own empty directory, not the disk);
- it must come **after** the overlay bind;
- the **last** remount must be `remount,rw` and must name `$rootmnt` (not any
  remount — `record_booted_slot` legitimately flips `rw`→`ro` first).

It inherits `assertHarnessActuallyRanTheHook`, so a harness that failed to start
the hook cannot satisfy it vacuously.

Mutation-verified, five directions, each applied to the real file and each
killing the test:

| # | mutation | assertion that fired |
|---|---|---|
| 1 | delete the post-rebind re-exposure | `leaves /var/cache/vulos INSIDE THE OVERLAY` |
| 2 | drop the `remount,rw` | `the last remount is … remount,ro — the disk is left READ-ONLY` |
| 3 | source the bind from `${rootmnt}/var/cache/vulos` (no capture) | `takes its source from … which is UNDER the overlay bind` |
| 4 | narrow the gate so `/var/cache/vulos/slot-*` no longer matches | `leaves /var/cache/vulos INSIDE THE OVERLAY` |
| 5 | move the re-exposure before the rebind | `mounted BEFORE the overlay bind … the rebind buries it immediately` |

The harness was restored byte-identical after each.

## What is still worth doing on a quiet machine

The static route settled *whether the defect exists* and *what the hook now
does*. It cannot confirm the kernel is happy with the result. On the next real
netboot-installed boot, one command is still the honest check:

```
findmnt -o TARGET,SOURCE,FSTYPE /var/cache/vulos
```

It must now report the **ext4 device**, not `overlay`. The end-to-end version —
and the one thing no static test can ever replace — is: write a file under
`/var/cache/vulos`, reboot, and look for it. `netboot-install-smoke.sh` Phase 4
still cannot see any of this, because it only ever touches the disk offline from
the host; extending it to make the *booted guest* write a file and survive a
reboot is the natural follow-up and is not done here.
