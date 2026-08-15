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
- exactly one mount may name `$rootmnt`, it must be the overlay bind, and it
  must be **last**

Every assertion is "the hook did *not* do X", which a harness that failed to
start the hook satisfies trivially — so `assertHarnessActuallyRanTheHook` pins
what a real run must have done first. Mutation-verified in all four directions,
including the "hook exited at its own gate" case.

## Still unknowable without a boot on a quiet machine

1. **Whether the `PREREQ` reorder above is safe.** It changes when `udevadm
   control --exit` happens relative to `veritysetup open`, and the hook already
   carries `DM_DISABLE_UDEV=1` for udev-related hangs found the hard way.

2. **Whether `/var/cache/vulos` is persistent on a NETBOOT-installed disk.**
   Adjacent, not caused by these four lines, and it is the shape of failure this
   investigation was told to watch for — so it is written down rather than
   guessed at:

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
