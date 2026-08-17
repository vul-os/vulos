# SYNC-APPS-02 — the app directory on a volatile root

Two halves were planned. The first landed. The second was **stopped on
measurement**, and this note is the measurement, so that the next person to
reach for it starts from what is true rather than from the plan.

## The defect

`AppSync.PlanReconcile` derived "what is realised here" from a scan of the app
directory alone. That is the right ground truth for *can this box launch it now*
and the wrong input for *has this box ever had it*.

`appsDir = datadir.Join("apps")` = `/root/.vulos/apps`, and on three of the five
boot paths — live-USB, live-ESP, netboot-installed — `/` is a squashfs lower
plus a **tmpfs upper in RAM** (`scripts/initramfs/vulos-live`). So after a
reboot the scan reads empty for apps the box really did install. The fleet
desired set is intact, because it replicates back from a peer. Every desired app
therefore looked like a first install, on every boot, forever: gigabytes
re-downloaded per boot, presenting as a slow boot and not as a defect.

Measured and recorded in commit `7e83049a` (the REPRO, still passing): nine
installs across three boots of three apps, and at the moment the plan said
"install", the box's OWN replicated `app_registry` rows — recovered from its
peer — already said `installed=true, realise_state="realised"`. The information
needed to tell the two cases apart was present and unread.

## Half A — landed

The reconciler now consults this box's own replicated realisation rows as well
as the filesystem, and an absence splits three ways:

| case | plan |
| --- | --- |
| no row, or a row that never realised | `never-realised` — install, exactly as before |
| this box's row says it realised it | `re-realise` — install, labelled, counted, reasoned |
| a re-realisation repeated too soon | `re-realise` + `Deferred` with a `NotBefore` |

Load-bearing decisions, each of which could reasonably have gone the other way:

- **A re-realisation is not a failure and not a removal.** The install
  succeeded and the storage then evaporated. `realise_state` stays `realised`
  and the desire is untouched — marking either would make the fleet show a
  working box as broken, or delete what the user asked for.
- **The counter replicates.** On the box this is about, the local database
  (`~/.vulos/db/multiinstance.db`) is in the *same tmpfs* as the app directory
  and dies with it. A count kept locally would read zero on exactly the boots it
  is meant to count. It rides `app_registry`, which is the only memory a
  volatile box has.
- **The counter is grow-only, merged as a `MAX`, not last-write-wins.** A peer
  still holding a pre-reboot copy must not be able to reset the fleet's only
  record of it. This also makes every pre-existing writer of that row
  (`LocalInstall`, `LocalUninstall`, `ReportRealiseFailure`, and the three
  partial rows `mergeEntry` constructs for LWW tie-breaks) a harmless no-op
  against it, since they all pass zero.
- **The backoff is a window since the last re-realisation, not a limit on the
  count.** A count limit would mean a live-USB user who reboots a fifth time
  does not get their browser back. There is no durable storage on that path;
  re-downloading *is* the correct behaviour there. The window (1 min, doubling,
  capped at 30) is never reached across an ordinary reboot interval, and catches
  only repetition fast enough to be pathological — a reboot loop, or the
  two-minute reconcile ticker in `cmd/server` re-downloading because an install
  reads back as empty.
- **The "why" is measured, never inferred.** It comes from
  `AppStore.StorageVolatility` (commit `49429424`), which reads
  `/proc/self/mounts` and follows an overlay's `upperdir`. A Realiser that
  cannot measure its storage — darwin, a stripped container — contributes an
  **empty** reason rather than a guess, because that string is read by a user
  standing at a different box.

Visible at `GET /api/apps/rerealisations`, which answers **for every box**
because it reads the replicated rows — the only way to get the answer for the
volatile box, whose own database does not survive the reboot in question.

## Half B — STOPPED, and why

The plan was to apply the `e07dc00e` treatment — capture the subtree before the
overlay rebind, re-expose it after — to the app directory on the
netboot-installed path, where a real disk exists.

**Mechanically it is the same. Semantically it is not, and it would create a
worse defect than the one it fixes.**

`/var/cache/vulos` is **self-contained**: the boot counter, `MarkHealthy` and
the A/B slot images are all inside that one subtree, so binding it out of the
overlay makes the whole thing whole.

`~/.vulos/apps` is **not**. It holds the manifest; for most entries the payload
lives somewhere else entirely. Measured against the shipped `registry.json`
(56 entries) on 2026-08-17:

| install vehicle | entries | where the payload lands |
| --- | --- | --- |
| Flatpak (`flatpak_id`) | **13** | `/var/lib/flatpak` — a *system* install (`FlatpakInstall` passes no `--user`), **outside** the app dir |
| pinned artifacts (`artifacts`) | 3 | unpacked into `appsDir/<id>` — **inside** the app dir |
| `deps` on top of the above | 4 | `apt-get install` → `/usr`, `/var/lib/dpkg` — **outside** |
| a bare shell `install` line | 35 | refused outright by `validateRecipeSecurity` and the `default:` branch of `InstallFromRegistry` — no install vehicle at all |

So persisting the app directory alone would, for 13 of the 16 actually
installable entries, leave **a persistent manifest above a payload that still
dies with the tmpfs**. `roadmap/APT-INSTALL-PERSISTENCE.md` §3 already names
that shape and calls it *strictly worse than the app being gone*: the App Hub
lists the app and the launch fails.

For this work specifically it is worse still, and in the same family as the
original defect. `AppStore.RealisedVersions()` is a directory scan with **no
flatpak liveness check** — unlike `Registry.ListEntries`, which does have one
and deletes the stale manifest. So the reconciler would read the surviving
manifest as `realised`, plan nothing, and the app would stay broken *forever*.
Today the manifest dies with the payload and Half A re-realises it correctly.

Two existing guards already cover this, and both would have gone red:

- `TestOnlyVarCacheVulosIsRescuedFromTheOverlay`
  (`backend/internal/docsref/aptpersist_test.go`) drives the whole hook under
  dash and pins the rescued set to exactly `["/var/cache/vulos"]` on netboot and
  `nil` on live. It computes that set from the hook's own mount log rather than
  reading the gate and reasoning about it.
- `TestInstalledAppManifestsShareTheRootFilesystemsFate` pins the two inputs
  (`HOME`, `VULOS_DATA_DIR`) that decide which world the box is in.

Nothing was improvised in the initramfs. The hook is untouched.

### UPDATE 2026-08-17 — the hook is no longer untouched, and this measurement is why

The last two sentences above stopped being true the same day. An owner account
was measured **not surviving a reboot** on a real netboot-installed box —
`/api/auth/status` answering `{"has_users":false}` and the login 401'ing after a
restart — so `/root/.vulos` is now bound out of the overlay onto the disk
(OWNSTATE-01, `roadmap/OWNER-STATE-PERSISTENCE.md`). `~/.vulos/apps` is a child
of that tree, so Half B's question was forced rather than chosen.

**Half B is still stopped, and this note is the reason it was not quietly
carried in on the back of the account fix.** The initramfs mounts a **tmpfs back
over `${rootmnt}/root/.vulos/apps`** immediately after the state bind, so the
app directory goes straight back into RAM with the Flatpak payload it points at.
Everything measured above holds unchanged: the manifest still dies with its
payload, `AppStore.RealisedVersions()` still reads an empty directory, and
Half A still re-realises correctly.

What changed in the guards, as a direct consequence:

- `TestOnlyVarCacheVulosIsRescuedFromTheOverlay` now pins **two** exact sets —
  what is pulled onto the disk (`/var/cache/vulos`, `/root/.vulos`,
  `/var/lib/vulos`) and what is deliberately put back in RAM
  (`/root/.vulos/apps`). Its helper also stopped counting a bind whose source is
  *under* `$rootmnt` as a rescue, since such a bind reads the overlay's own
  empty directory and persists nothing.
- `TestNetbootInstalledDiskKeepsTheOwnerAccountOnDisk` fails if the apps tmpfs
  is absent, is not a tmpfs, or is ordered before the `/root/.vulos` bind (which
  would bury it). All three were mutation-verified.

**The precondition list below is unchanged and still unmet.** Option 2 — teach
`AppStore.RealisedVersions()` the flatpak liveness check `Registry.ListEntries`
already performs — is now more valuable than it was, not less: today the ghost
shape is held off by a mount that a future edit could delete, whereas the
liveness check would make a surviving manifest merely useless.

### What would make Half B safe

Not a decision for this pass, and stated as a precondition rather than a plan:

1. Either make the payload share the app dir's fate — persist
   `/var/lib/flatpak` alongside it (a much larger subtree, and an OTA/disk-space
   question of its own), **or**
2. teach `AppStore.RealisedVersions()` the flatpak liveness check that
   `Registry.ListEntries` already performs, so a manifest whose payload is gone
   is not reported as realised. That alone closes the ghost-app shape and makes
   persisting the manifest merely useless rather than harmful — after which
   persisting the app dir buys the 3 artifact entries and nothing more.

Note the direction of travel: another agent is concurrently adding
`registry.d/apt-to-flatpak.json`, `registry.d/apt-retired.json` and
`registry.d/vulos-native.json`. If the 35 refused apt entries are being
converted to Flatpak, the flatpak share **rises**, and option 2 becomes the only
sane order to do this in.

### On live-USB and live-ESP there is nothing to fix

There is no durable storage on those paths at all. The honest behaviour is the
one Half A now implements: the box knows its storage is volatile, says so on a
row every other box can read, still gives the user the app they asked for, and
counts what that costs.

## Still not true

- No end-to-end run on a real Linux box. Every claim above about the reconciler
  is proven in-process; every claim about mount topology is proven by driving
  the hook under dash, not by booting.
- `cmd/server`'s reconcile ticker now logs re-realisations and deferrals
  separately, but nothing surfaces them in the UI.
- The `--disk` path is unaffected throughout and always was.
