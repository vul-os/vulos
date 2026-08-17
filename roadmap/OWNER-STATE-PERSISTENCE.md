# OWNSTATE-01 — the owner's account did not survive a reboot

**Observed on a real box, not reasoned from mount topology.** An agent booted a
netboot-installed disk (the real `TestNetbootInstall_RealPipeline_E2E` pipeline,
dm-verity active, arm64 QEMU), created an owner account, and rebooted:

```
/api/auth/status → {"has_users":false}
login as founder  → 401 invalid username or password
/root/.vulos/*    → entirely recreated at reboot time
```

This is the defect `roadmap/BOOT-FOUR-ERRORS.md` settled for `/var/cache/vulos`,
on a second subtree, and it costs a great deal more than an update counter. A
person sets up their box, reboots, and their account, their PIN, their SSH key
and their recovery material are gone — while every unit test passes, because
every auth test builds its store in a `t.TempDir()`.

## 1. Where owner state lives, and on which boot path it survives

`backend/internal/datadir` resolves the box data root as `VULOS_DATA_DIR`, else
`$HOME/.vulos`. The `vulos-server` unit `build.sh` writes sets `HOME=/root` and
**never sets `VULOS_DATA_DIR`** (`build.sh:521`, and the second copy at
`build.sh:1316`), so on every shipped image the root is `/root/.vulos`.

| what | path at runtime |
| --- | --- |
| owner accounts, password hashes, sessions, recovery + master-key blobs | `/root/.vulos/db/auth.db` (+`-wal`, `-shm`) |
| the secret every session cookie is signed with | `/root/.vulos/db/auth.key` (mode 0600) |
| profiles/roles | `/root/.vulos/db/auth.json` |
| box identity | `/root/.vulos/db/instance.json`, `/root/.vulos/instance-id` |
| box Ed25519 peering identity + VulosID | `/root/.vulos/peering/identity/` |
| recovery anchor | `/root/.vulos/peering/identity/recovery_anchor.json` |
| device key / revocations | `/root/.vulos/auth/tpm/` |
| passkeys, TOTP, credential vault | `/root/.vulos/auth/passkeys`, `auth/totp/<user>`, `auth/vault/<user>` |
| ~25 further databases | `/root/.vulos/db/*.db` |
| user files, app data, models, logs | `/root/.vulos/{storage,data,ai-apps,models,logs,tunnel,wine}` |
| per-user per-app storage | `/root/.vulos/<userID>/<appID>/` (created dynamically) |
| installed-app manifests | `/root/.vulos/apps` |

**And a second tree that is not under the data dir at all.** `cmd/server`
hardcodes `/var/lib/vulos`:

| what | path | source |
| --- | --- | --- |
| "this box has been set up" marker | `/var/lib/vulos/.setup-complete` | `backend/cmd/server/routes_setup.go:101` |
| LAN TLS cert/key + LAN root | `/var/lib/vulos/tls/` | `backend/internal/lan/fileloader.go:19`, `backend/internal/lan/lanroot.go:66` |
| OTA anti-rollback epoch floor | `/var/lib/vulos/epoch-floor.json` | `backend/services/signing/epoch.go:15` |
| object-store data | `/var/lib/vulos/minio-data` | `backend/services/storageprov/storageprov.go:185` |

Per boot path, before this work:

| boot path | `/` at runtime | owner account survives a reboot? |
| --- | --- | --- |
| `--disk` install / `--disk` image | the real ext4, mounted `rw` | **YES**, always did |
| live-USB (`build.sh --live`) | overlay: squashfs + **tmpfs in RAM** | NO — and there is nowhere to persist |
| live ESP re-flash (`internal/installer/esp.go`) | overlay: squashfs + **tmpfs in RAM** | NO — same |
| **netboot-installed disk** | overlay: slot squashfs + **tmpfs in RAM** | **NO — and there IS a disk. This is the defect.** |

`mountDataPartition()` in `backend/cmd/init/main.go` is the only mechanism that
could ever have put `$HOME/.vulos` on independent storage, and it is doubly
inert: it looks up `LABEL=vulos-data`, which nothing in this repository creates,
and it lives in `vulos-init`, which is PID 1 only on the two command lines that
carry `init=/sbin/vulos-init` — i.e. exactly the two paths that were already
persistent. Both facts are guarded (`TestNothingCreatesTheDataPartitionLabel`,
`TestVulosInitRunsOnlyWhereTheRootIsAlreadyPersistent`).

## 2. Is the subtree self-contained? Yes — except for one child, which is excluded

`roadmap/APP-DIR-PERSISTENCE.md` stopped the equivalent extension for the app
directory on measurement, and that measurement applies directly here, because
`~/.vulos/apps` is a **child of the tree this note wants to persist**.

Going through it item by item:

- **auth.db, auth.key, auth.json, the vaults, the device key, the peering
  identity, instance.json** — self-contained. They are files (or rows) inside
  the tree; nothing they depend on lives elsewhere.
- **`db/files.db` and the Drive index** — self-contained *given that the whole
  tree is persisted*: the blobs it indexes are under `/root/.vulos/storage` and
  `/root/.vulos/data`, which persist with it. Persisting `db/` alone would have
  produced exactly the ghost shape (an index above missing blobs), which is why
  the unit of persistence here is the tree and not a hand-picked subset.
- **`/var/lib/vulos`** — self-contained, and persisted alongside. Leaving it out
  would mean a box that has an owner re-runs its setup wizard, regenerates its
  LAN TLS identity, and forgets its OTA anti-rollback floor on every boot.
- **`apps/` — NOT self-contained.** Measured against the shipped
  `registry.json`: 13 of the 16 actually-installable entries are Flatpak
  **system** installs whose payload lands in `/var/lib/flatpak`, and the `deps`
  entries `apt-get install` into `/usr` and `/var/lib/dpkg`. None of that is
  persisted. And `AppStore.RealisedVersions()` is a directory scan with **no
  flatpak liveness check** — unlike `Registry.ListEntries`, which has one — so a
  manifest that outlives its payload is read as *realised*, the reconciler plans
  nothing, and the app stays broken forever. Today the manifest dies with the
  payload and the box re-installs it, which is correct.

**So `apps/` is deliberately kept volatile.** The initramfs mounts a tmpfs back
over `${rootmnt}/root/.vulos/apps` after the state bind, so the app directory
keeps sharing the fate of the payload it points at and **nothing about the
measured app behaviour changes**. The two preconditions
`APP-DIR-PERSISTENCE.md` lists for making that subtree safe are still unmet, and
this work does not pretend otherwise.

## 3. The fix

Identical mechanism to `e07dc00e`: capture the on-disk subtree **before** the
overlay rebind, while it is still reachable by path, and re-expose it **after**.
It cannot be one step — klibc's `mount` has no long options at all, so there is
no `--rbind`, and `-o bind` does not carry submounts.

```sh
# before the rebind, gated on /var/cache/vulos/slot-*
mount -o remount,rw  $DEV $rootmnt                   # (already done for the cache)
mount -o bind "${rootmnt}/root/.vulos"    /run/vulos/state-home
mount -o bind "${rootmnt}/var/lib/vulos"  /run/vulos/state-varlib

mount -o bind "$MERGED" "$rootmnt"                   # the overlay becomes /

# after it
mount -o bind /run/vulos/state-home   "${rootmnt}/root/.vulos"
mount -t tmpfs -o mode=0755 tmpfs-apps "${rootmnt}/root/.vulos/apps"
mount -o bind /run/vulos/state-varlib "${rootmnt}/var/lib/vulos"
```

**The source directories must already exist on the disk, and only the installer
can create them.** The initramfs cannot: until the rebind that partition is
`$rootmnt` mounted read-only, and a `mkdir` into it is precisely the failure
`roadmap/BOOT-FOUR-ERRORS.md` is about. The running OS cannot either: once the
overlay is bound over `$rootmnt` the partition is unreachable by path for the
life of the machine. So `netboot_install.go` gains a `state-dirs` step creating
`/root/.vulos` (0700), `/root/.vulos/apps` and `/var/lib/vulos`.

**A disk installed before that step exists gets nothing.** `[ -d ]` fails, the
hook logs that the box will lose its owner until it is reinstalled, and the old
volatile behaviour continues. That is stated rather than papered over: there is
no way to create a directory on that partition from the initramfs without the
write this hook must not do.

**Gated exactly as the cache fix is**, on the `/var/cache/vulos/slot-*` layout
`writeSlotABootEntry` pins. A live-USB (`/image.squashfs`) and a re-flashed live
ESP (`/EFI/vulos/os-core.squashfs`) get **not one extra mount**: there is no
durable storage on those media, and the honest behaviour is to add nothing
rather than to pretend. Every step is best-effort; a failure degrades to the old
volatile behaviour and nothing panics.

## 4. Security: what is now at rest on that partition

**This does move secrets from RAM onto a plaintext disk, and that must be said
plainly rather than buried.** What is now written to the unencrypted ext4 on a
netboot-installed box:

- `auth.db` — the owner's password hash, live session records, and the
  *encrypted* recovery and master-key blobs;
- `auth.key` — the secret every session cookie is signed with. Whoever holds it
  can mint a valid session for any user on that box;
- `peering/identity/ed25519.priv` — the box's identity private key. Whoever
  holds it can impersonate the box to its peers;
- `auth/tpm/device_key.priv` in the software-key fallback (a TPM-sealed key is
  useless off that TPM; the software one is not);
- the credential vault, TOTP secrets and passkey records;
- `/var/lib/vulos/tls/lan.key` — the LAN TLS private key.

**There is no full-disk encryption anywhere in this project, on any boot path.**
That is a standing choice, visible in `build.sh` (it installs `cryptsetup-bin`,
the library half, deliberately *not* `cryptsetup`), and dm-verity protects the
OS image's integrity, not user data's confidentiality.

Three things bound what changed:

1. **The `--disk` install has always done exactly this.** `/root/.vulos` on a
   `--disk` box has sat on a plaintext ext4 since the path existed. This makes
   netboot-installed match it; it does not open a new class of exposure for the
   product.
2. **The alternative is not "secrets in RAM", it is "no account".** The state
   was volatile, so the box could not have an owner across a reboot at all.
   Nobody was being protected by the old behaviour; they were losing their box.
3. **`/root/.vulos` is created `0700` and re-`chmod`ed** (`mkdir -m` does not
   fix the mode of a directory that already exists, e.g. on a re-install), so
   on the running machine the directory mode is the access control. It is the
   only one there is, which is why a mutation flipping it to `0755` is one of
   the twelve that must fail.

**What is NOT fixed, and is the honest open item:** an attacker with physical
possession of the disk can read all of the above. Closing that needs disk
encryption with a key that is not on the same platter — a TPM-sealed LUKS volume
for the data partition — which is a separate piece of work and is not attempted
here. Anyone deploying a netboot-installed box in a physically untrusted place
should know that today the account material is readable by anyone who can pull
the drive.

## 5. How this is verified

**Statically**, by executing the hook — `backend/internal/docsref`, driving the
whole of `scripts/initramfs/vulos-live` under `dash` with klibc-shaped
`mount`/`mkdir` stubs and the kernel command line read out of
`netboot_install.go` at test time:

- `TestNetbootInstalledDiskKeepsTheOwnerAccountOnDisk` — both subtrees are bound
  from a source **outside** `$rootmnt`, **after** the rebind, the tmpfs over
  `apps` comes after the `/root/.vulos` bind, and the last remount leaves the
  partition `rw`.
- `TestLiveBootGetsNoOwnerStateMounts` — a live boot gets none of them.
- `TestOnlyVarCacheVulosIsRescuedFromTheOverlay` now pins **two** exact sets: what
  is rescued onto the disk, and what is deliberately put back in RAM.
- `TestCreateOwnerStateDirs`, `TestOwnerStateDirsCoverTheDataDirAndVarLib`,
  `TestNetbootPipelineActuallyRunsTheStateDirsStep` — the installer side.

Twelve mutations, each applied to the real file and each killing a named
assertion. **Two survived the first pass and both were the guard's fault, not
the mutation's**, which is the reason to record the exercise at all:

- `TestNetbootPipelineActuallyRunsTheStateDirsStep` matched the substring
  `createOwnerStateDirs(ctx`, which is also how the function's own *definition*
  begins — so deleting the pipeline step left it green.
- `TestLiveBootGetsNoOwnerStateMounts` passed because the live-mode fabricated
  `$rootmnt` had no `/root/.vulos`, so the hook's `[ -d ]` check failed before
  the gate was consulted. Widening the gate to match every boot changed nothing.
  The absence of a directory was doing the gate's job.

**End to end**, which no static test replaces:
`scripts/owner-state-reboot-smoke.sh` builds a real `--live` squashfs with this
hook baked into the initramfs, runs the real install pipeline against a real
loop-backed disk, boots it in QEMU, creates the owner over `/api/auth/register`,
logs in once *before* the reboot so a later 401 cannot be blamed on the password,
powers the guest down through ACPI, boots the same disk again, and requires
`has_users=true` plus a 200 from `/api/auth/login`. It then loop-mounts the
partition from the host and requires `auth.db` to be **on the ext4** and
`/root/.vulos/apps` to be **empty**.

### It was run, and it passed — 2026-08-17

arm64 QEMU/HVF, dm-verity active, `TestNetbootInstall_RealPipeline_E2E` doing
the install for real:

```
── step: state-dirs
[netboot-install] owner-state dirs created: /root/.vulos (0700), /root/.vulos/apps, /var/lib/vulos
OWNSTATE-01: owner-state dirs present on the installed partition with the
declared modes: [{root/.vulos 0700} {root/.vulos/apps 0755} {var/lib/vulos 0755}]

✓ first boot up — HTTP answering
  /api/auth/status (before setup): {"has_users":false}
  creating the owner account (POST /api/auth/register)…
  /api/auth/status (after setup):  {"has_users":true}
  login BEFORE reboot → HTTP 200
▸ Phase 4 — ACPI powerdown, then a second boot of the same disk
✓ guest powered down
✓ second boot up
  /api/auth/status AFTER REBOOT: {"has_users":true}
  login AFTER REBOOT → HTTP 200
✓ the owner logged in after a reboot
```

And the partition itself, read from the host with the guest off:

```
=== /root/.vulos (mode must be 0700) ===
drwx------ 13 root root 4096 /mnt/i/root/.vulos
  .appfs_migrated_v2   .ssh/   ai-apps/   apps/   auth/   data/   db/
  instance-id          os-cache/  peering/  sandbox/  web/   wine/

=== /root/.vulos/db ===
-rw-r--r--  auth.db          -rw-r--r--  auth.db-wal (168952 bytes)
-rw-------  auth.key (32)    -rw-------  instance.json    -rw-------  vapid.json
  … plus files.db, multiinstance.db, accountsecurity.db and ~15 more

=== /root/.vulos/apps — must be EMPTY (tmpfs kept manifests in RAM) ===
total 8
drwxr-xr-x  2 root root 4096 17:00 .          <- install time; never written to
drwx------ 13 root root 4096 17:07 ..

=== /var/lib/vulos ===
-rw------- 1 root root 11 17:07 epoch-floor.json
```

Three things in that listing are the whole answer. `auth.db` and its 168 KB WAL
are **on the ext4**, with mtimes from the *second* boot — the running OS's
writes reached the disk, so the login is not a cache. `/root/.vulos/apps` still
carries only its install-time mtime and holds nothing: the tmpfs kept every
installed-app manifest in RAM exactly as intended. And `epoch-floor.json`
appeared under `/var/lib/vulos` because the running OS wrote it there, which is
what the second bind is for.

Both boots reached HTTP at ~390 s of a 420 s deadline, on a host at load ~260.
Nothing about the box was slow; the harness deadline was simply close enough to
produce a false failure on a busy machine, so it was raised to 900 s. That is
the instrument, not the assertion.

## 6. Still not true

- Disks installed before the `state-dirs` step keep their old volatile
  behaviour until reinstalled. There is no in-place migration and none is
  possible from the initramfs.
- Nothing at rest is encrypted (§4).
- `scripts/netboot-install-smoke.sh` still cannot see any of this: it only ever
  touches the disk offline, loop-mounted from the host. The round trip lives in
  its own script rather than as a ninth phase of that one.
- On live-USB and live-ESP the box still does not *say* it is ephemeral. The app
  reconciler knows (`AppStore.StorageVolatility`, `GET /api/apps/rerealisations`),
  but nothing tells a user creating an account on live media that it will not
  survive. That is a UI gap, not a boot gap.
