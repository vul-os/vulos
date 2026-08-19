# Key Ceremony

How Vulos generates, custodies, uses, rotates and revokes the keys that decide
**what a box will run**.

Three things on a Vulos box are trusted only because a signature says so:

| Artifact | Signed by | Verified by |
|---|---|---|
| `registry.json` entries (App Hub) | release key | `backend/services/appnet` before any install |
| `os-core.squashfs` + `stable.json` (A/B updater, netboot) | release key | `backend/cmd/verify`, `backend/services/osdist`, `backend/services/installer` |
| Release-key certificate | **root key** | every box, against the anchor baked into its image |

All three chain to one Ed25519 public key: the **trust anchor**.

---

## 1. The chain

```
        ┌──────────────────────────────────────────────┐
        │  ROOT key            (offline, air-gapped)   │   never signs an artifact
        └───────────────────┬──────────────────────────┘   never touches a network
                            │ signs, at ceremony time only
                            ▼
                  release-cert.json
        ┌──────────────────────────────────────────────┐
        │  RELEASE key         (signing machine)       │   signs every artifact
        └───────────────────┬──────────────────────────┘   rotatable without reflashing
                            │ signs, routinely
          ┌─────────────────┼──────────────────┐
          ▼                 ▼                  ▼
   registry.json      stable.json.sig   os-core.squashfs.sig
   (per-entry sigs)
```

The **root public key** ships in the image at `/etc/vulos/trust-anchor.pub`
(`signing.DefaultAnchorPath`). The **release cert** ships beside it at
`/etc/vulos/release-cert.json` (`signing.DefaultReleaseCertPath`).

Why two levels: the root key would otherwise have to come online every time an
app is added to the registry. Two levels mean the root is used a handful of
times in its life, and a compromised release key is recoverable by issuing a new
cert — no reflash, no re-pinning, no lost fleet.

---

## 2. Custody — what we actually implemented

> **CI is VERIFY-ONLY. No private key ever reaches a CI runner — not the root
> key, not the release key.**

Signing is a **human operation on an offline machine**, and the resulting
signatures are **committed to the repository**. CI's job is to prove that what
was committed verifies against the anchor that ships.

| Key | Lives | Used by | In CI? |
|---|---|---|---|
| ROOT private | Air-gapped machine / HSM. Offline, always. | A human, at a ceremony | **Never** |
| RELEASE private | Maintainer's signing machine (or HSM slot) | A human, running `make sign-registry` | **Never** |
| ROOT public (anchor) | `keys/trust-anchor.pub`, committed, shipped in the image | Every box | Yes (read-only) |
| Release cert | `keys/release-cert.json`, committed, shipped in the image | Every box | Yes (read-only) |

This is the conservative option, and it was chosen deliberately. The alternative
— giving CI a release key so it can sign on merge — means any workflow-file
change, any compromised action, any `pull_request_target` mistake is a path to a
signing oracle for the App Hub. That trade is not worth the convenience of not
running one command locally. The signatures are deterministic (Ed25519 over
canonical JSON), so a committed signature is fully reproducible and reviewable:
anyone can re-run `make sign-registry` and get byte-identical output.

Enforced by CI (`.github/workflows/ci.yml`, job `registry-signatures`):

- `make verify-registry` — every entry verifies against the shipped anchor.
- `git ls-files | grep -E '\.(priv\.json|key|pem)$'` — fails if a private key is
  ever committed.

And by `.gitignore`: `keys/*.priv.json`, `keys/*.key`, `keys/*.pem`.

---

## 3. What the repo ships — and the dev keypair it no longer ships

### The ceremony has been run

The four public trust files in `keys/` are **production ceremony material**, not
dev keys. They were installed by commits `423f532b` and `1d9b8cb9`:

```
keys/trust-anchor.pub    ceremony ROOT public key   (committed, shipped in the image)
keys/release-cert.json   ceremony release cert      (committed, shipped in the image)
keys/root.pub.json       ceremony ROOT public key   (committed)
keys/release.pub.json    ceremony RELEASE public key (committed)
```

The cert authorises release key `dbc913bf…` under key-id **`release-2026-08`**,
expiring **`2027-08-03`** (`keys/release-cert.json`).

### ⚠️ The registry is NOT signed — this is the step blocking the release

`registry.json` today holds **142 entries, of which 6 carry a signature and 136
do not.** The catalogue merge in commit `8cb788c8` took it from 74 entries to
142 and, in doing so, invalidated thirteen previously-good signatures — an
edited entry's old signature cannot verify, and `registry.d/arch-declarations.json`
edited them. So, on this tree, today:

```
make verify-registry                        FAILS — entry "ardour": has no publisher
                                                    signature (REGISTRY-SIGN-01), exit 2
make verify-registry-prod                   FAILS — same reason, so a `v*` tag
                                                    cannot ship an image
cd backend && go test ./services/appnet/    FAILS — 8 tests, all "missing signature"
```

That is the gate working, not a bug. It is exactly the **Degraded — App Hub
disabled** state described in §7, and clearing it is what §4 is for. Everything
above about the *anchor* and the *cert* still holds: those are production
ceremony material, and they are not what is missing. Signing the registry (§4.5,
or `make ceremony` in §4.0) is the single outstanding operation.

> **What signing these entries means, stated plainly.** Per the merge commit
> `8cb788c8`, **none of the 68 newly-added entries has been install- or
> launch-tested.** What was proven for them is resolvability, per-arch
> publication, branch unambiguity and the upstream `verified` flag — *not* that
> any of them runs. Signing makes 68 never-run apps installable. That is a
> decision to take at the ceremony; the signing command does not take it for you.

A fresh clone still runs `make dev` and `make test-local` (the Go module root is
`backend/`, so the raw form is `cd backend && go test ./...`) with real signature
verification and **no flags** — not because the shipped keys are dev keys, but
because all four files are *public* halves and are committed. Note what that
does *not* mean: until the registry is signed, `./services/appnet/` is **red**
on a fresh clone too, for the reason above. No flag fixes that, and none should.
`TestAcceptance_ShippedAnchorIsProductionAndAcceptedInProd` pins this from the
other side: it fails if a dev key is ever committed as the shipped anchor again.

### The dev keypair still exists — as a thing to refuse

`backend/services/signing/devanchor.go` still pins two development public keys,
derived deterministically from two published seed strings:

```
root:    vulos-dev-signing-root-v1     → 98ZXaIIl75yJ/2TKAJqPGSbfRP0YGJWKOOP3z1OTLSI=
release: vulos-dev-signing-release-v1  → uosei+A8s83COsu0CBIjmCf4H4PpxXaTQJar7rUafwE=
```

Anyone can reproduce their *private* halves in one command, so they are not
secret and must never be trusted. `RefuseDevKeyInProd` rejects them whenever
`VULOS_ENV=prod` — through *any* door: the baked anchor file, the release cert,
or `VULOS_REGISTRY_PUBKEY`. `TestDevKeys_PinnedConstantsMatchSeeds` fails if the
seeds and the pinned constants ever drift apart. They are kept pinned so the
refusal keeps working, not because anything ships them.

### ⚠️ `make dev-keys` overwrites the ceremony material

`scripts/signing/dev-keys.sh` writes all six key files, including
`keys/trust-anchor.pub` and `keys/release-cert.json`. Since commit `3f5a8dd8` it
**refuses to run** when the material already in `keys/` is not the dev material:
it reads `DevAnchorPubB64` out of `devanchor.go` rather than guessing, compares
it against the anchor on disk, checks the cert's `key_id`, and aborts if either
is production. The typed-out override is `VULOS_DEV_KEYS_OVERWRITE=1`, which
also prints a warning.

That guard matters because of how `make sign-registry` behaves: with no
`RELEASE_PRIV=` argument it defaults to `keys/release.priv.json`
(`Makefile:21`), and that path is gitignored (`.gitignore:47`). Two kinds of
machine, two different hazards:

- **A fresh clone** does not have that file. `sign-registry` used to regenerate
  the dev keys and carry on — clobbering the shipped production anchor and
  re-signing all 142 entries with the dev key. The `dev-keys.sh` guard above
  now stops that.
- **Any machine where `make dev-keys` has ever run** — including the
  maintainer's — *does* have the file, and it holds the **dev** key. Nothing
  about the filename says so, no regeneration step fires, and nothing looks
  unusual. This is the more dangerous case, and the guard above does not cover
  it.

A second, independent guard covers it: **`make check-release-key`**
(`Makefile:123-161`), which both `sign-registry` and `publish-feed` run before
signing anything. It compares the `public_key` recorded inside `RELEASE_PRIV`
against the `release_pubkey` the **shipped certificate** authorises and refuses
on mismatch, naming both halves and the cert's `key_id`. Nothing about "dev" or
"prod" is hardcoded in it, so it cannot drift away from the cert the image
actually ships.

**On this tree that refusal is live and correct.** `keys/release.priv.json`
here has public half `ba8b1e8b…` — the dev release key — while the shipped cert
authorises `dbc913bf…` (`release-2026-08`). A bare `make sign-registry`
therefore **exits 2 and writes nothing.** Pass the real key:

```bash
make sign-registry RELEASE_PRIV=/path/to/release.priv.json
```

The typed-out override is `VULOS_SIGN_ALLOW_KEY_MISMATCH=1`. **Do not reach for
it to make a ceremony proceed.** If that check fires during a ceremony, the key
on the table is not the key the shipped cert authorises, and signing anyway
produces a `registry.json` that no released image can verify.

If you have already run one of these by accident, `git checkout -- keys/` and
`git checkout -- registry.json` restore the committed ceremony material.

---

## 4. The ceremony

> **Read §4.0 before running anything.** These steps are **not idempotent**, and
> the wrong one *replaces the shipped trust anchor*, which orphans every fielded
> box. Pick the path first.

### 4.0 Which path do you need?

The root key already exists and **its anchor already ships**. `v0.1.0` (tagged
2026-08-07) and `v0.2.0` (tagged 2026-08-14) were both cut *after* commit
`1d9b8cb9` installed `keys/trust-anchor.pub`, and `build.sh:1060` bakes whatever
is in `keys/` into every image via `scripts/seed/embed-anchor.sh`. Both released
images are therefore pinned to anchor `8c092221…`.

| Situation | Run | Anchor changes? |
|---|---|---|
| You hold the release key the shipped cert authorises (`dbc913bf…`, `release-2026-08`) — **the expected case for 0.3.0** | §4.5 alone: `make sign-registry RELEASE_PRIV=<vault>/release.priv.json` | No |
| You hold the ROOT key, but the release key is lost, expired or compromised | `scripts/signing/ceremony.sh --vault DIR --rotate` (§6) | No |
| Genuinely the first ceremony — no root key exists anywhere | §4.1 → §4.6, or `make ceremony` | Yes, once |

> #### ⚠️ Do **not** run a full `make ceremony` for this release
>
> A full (non-`--rotate`) run **generates a new ROOT key**
> (`scripts/signing/ceremony.sh:128-131`) and **overwrites
> `keys/trust-anchor.pub`** (`ceremony.sh:150-154`). Every box already running
> v0.1.0 or v0.2.0 would then reject every OS update and every app install, and
> could be recovered only by reflashing.
>
> There is a guard, and it is narrower than it looks: the script refuses only
> when `root.priv.json` is **already present in the vault you passed**
> (`ceremony.sh:110-113`). Point `--vault` at a fresh folder — or run on a
> machine the vault was never copied to — and nothing stops it. **The guard is
> the vault, not the repo.** Bring the existing vault to the ceremony.

### 4.0.1 The scripted path — `make ceremony`

`scripts/signing/ceremony.sh` is the scripted form of §4.1–§4.5, and it is what
was exercised against the current 142-entry registry (in a throwaway worktree
with a throwaway vault: 142/142 signed, `verify-registry` OK, coverage 142/142,
0 skipped). It generates both keypairs, issues the cert, installs the four
public files into `keys/`, signs `registry.json`, re-runs `verify-registry
-require-prod-keys`, then writes the private keys, a `README.txt` and a
`MANIFEST.sha256` into `--vault` and `chmod 600`s them.

```bash
make ceremony VAULT=/Volumes/VULOS-VAULT          # or: scripts/signing/ceremony.sh --vault DIR
```

It prints the key-id, expiry, min-epoch and mode, and **prompts for
confirmation** before doing anything (`ceremony.sh:122-125`; `--yes` skips the
prompt). Private keys are written **only** into `--vault`, never into `keys/`.
It refuses a vault inside this repo — *"the vault must NOT live inside the vulos
OS repo (it is public)"* (`ceremony.sh:83-86`) — and refuses a vault that sits
inside any git work tree which does not gitignore it (`ceremony.sh:88-95`).

**Its one deliberate weakening, stated in the script itself
(`ceremony.sh:16-22`): it collapses the air-gap to a single machine.** Whoever
can read that machine during the run can read the root key. For a first
ceremony, prefer the manual steps below on a clean offline box; `--rotate`,
which never touches the root key beyond reading it, is the safer thing to script.

### 4.0.2 The manual path

Everything from §4.1 on runs on an **air-gapped machine**. It never touches a
network. Build `vulos-sign` on your workstation, copy the binary across on
removable media, and copy only *public* artifacts back.

```bash
cd backend && go build -o vulos-sign ./cmd/sign     # on the workstation
```

### 4.1 Generate the ROOT key — once, ever

```bash
# ── AIR-GAPPED MACHINE ───────────────────────────────────────────────────────
./vulos-sign gen-key \
    -out-priv root.priv.json \
    -out-pub  root.pub.json
```

`root.priv.json` **never leaves this machine.** Put it on encrypted removable
media (ideally two copies, two locations) or into an HSM slot. If you lose it you
cannot rotate the release key, and every fielded box must be reflashed. If it
leaks, every fielded box is forgeable and must be reflashed. There is no third
option — this is the key the whole system rests on.

Export the anchor — the public half, which ships in the image:

```bash
./vulos-sign export-anchor -pub root.pub.json -out trust-anchor.pub
```

Copy `root.pub.json` and `trust-anchor.pub` out. Leave `root.priv.json` behind.

### 4.2 Generate the RELEASE key

Generate it on the signing machine (the one that will run `make sign-registry`),
and carry only its **public** half to the air-gapped machine:

```bash
# ── SIGNING MACHINE ──────────────────────────────────────────────────────────
./vulos-sign gen-key \
    -out-priv release.priv.json \
    -out-pub  release.pub.json
```

`release.priv.json` stays here. `chmod 0600` it. Do not commit it, do not put it
in a secrets manager that CI can read, do not paste it into a workflow.

### 4.3 Root signs the release key — the ceremony proper

Carry `release.pub.json` to the air-gapped machine:

```bash
# ── AIR-GAPPED MACHINE ───────────────────────────────────────────────────────
./vulos-sign issue-release-cert \
    -root-priv   root.priv.json \
    -release-pub release.pub.json \
    -key-id      release-2026-07 \
    -not-after   2027-07-01T00:00:00Z \
    -min-epoch   1 \
    -out         release-cert.json
```

- `-key-id` — a label that appears in logs and `.sig` files. Date-stamp it.
- `-not-after` — **12 months**. A short life is what makes a quiet key compromise
  self-limiting. Re-issuing is one command; a cert that never expires is a cert
  that never gets re-examined.
- `-min-epoch` — the rollback floor. Bump it on every revocation (§6).

Carry `release-cert.json` out. It is public.

### 4.4 Install the trust material into the repo

```bash
# ── SIGNING MACHINE ──────────────────────────────────────────────────────────
cp trust-anchor.pub   keys/trust-anchor.pub
cp release-cert.json  keys/release-cert.json
cp root.pub.json      keys/root.pub.json
cp release.pub.json   keys/release.pub.json
```

These four files are public and **are committed**. They replace the dev keys.

### 4.5 Sign the registry

```bash
make sign-registry RELEASE_PRIV=/path/to/release.priv.json
```

This signs all entries, rewrites `registry.json`, and re-verifies the result
before exiting — a serialisation bug cannot ship a registry the box would reject.

Then commit:

```bash
git add keys/trust-anchor.pub keys/release-cert.json \
        keys/root.pub.json keys/release.pub.json registry.json
git commit -m "signing: install production trust anchor and re-sign registry"
```

CI re-verifies on push. `make verify-registry` is the same check you can run
locally.

### 4.6 Build the image

`scripts/seed/embed-anchor.sh` bakes the anchor and cert into the rootfs *and*
the initramfs. For a production build, point it at the real key explicitly so a
stale `keys/` cannot silently produce a dev-signed image:

```bash
export VULOS_TRUST_ANCHOR_PUBKEY=/path/to/trust-anchor.pub
export VULOS_RELEASE_CERT=/path/to/release-cert.json
./build.sh
```

The build **fails** if no anchor is found, and **warns loudly** if it falls back
to the repo dev key.

---

## 5. Routine releases

Adding an app to the registry, or cutting an OS image, uses the **release key
only**. The root key stays in its drawer.

**Order matters, and the two signings sit on opposite sides of the tag** — see
`docs/RELEASING.md` §"Signed Artifacts", which this must agree with:

1. **Registry signing happens *before* the tag.** It rewrites `registry.json`, a
   tracked file, which must be committed and pushed before tagging — CI then
   enforces it with `make verify-registry-prod`.
2. CI builds the images, unsigned.
3. **Image-manifest signing happens *after* the tag**, offline, against the root
   hash CI published.

```bash
# 1 — before the tag: new app added to registry.json
make sign-registry RELEASE_PRIV=/path/to/release.priv.json
git commit -m "registry: add <app>" -- registry.json

# 3 — after the tag: OS image. -key-id must be the key-id of the cert that
#     ships in the image, which today is release-2026-08.
./vulos-sign sign-image    -release-priv release.priv.json -key-id release-2026-08 ...
./vulos-sign sign-manifest -release-priv release.priv.json -key-id release-2026-08 ...
```

If you add an app and forget to sign it, CI fails with the app's name. It cannot
ship unsigned.

### 5.1 Entries that are not ready to be signed — `registry-unverified.json`

Sometimes an entry is written before it can be validated: the recipe exists, but
nobody has run it on hardware that has the prerequisites, so its own `_note`
says **UNVERIFIED**. Signing it would be a lie — a signature says "the trusted
publisher vouches for this", not "someone typed it out". Excusing it inside
`make verify-registry` would be worse: an exception path in a security gate is
how an unsigned entry eventually ships.

So neither happens. `registry.json` stays the **signed set, with no exception
path**, and an entry that is not fit to sign lives in `registry-unverified.json`
instead:

| | `registry.json` | `registry-unverified.json` |
|---|---|---|
| Signed by the release key | **every entry** | **no entry** (a signature here is a hard failure) |
| Loaded by the box | yes | **no** — `appnet.LoadRegistry` refuses it |
| Copied into the image | yes (`Dockerfile`, `build.sh`) | no |
| Checked by `make verify-registry` | every entry must verify | cross-checked: disjoint, unsigned, marked |

Two independent things keep it out of the trusted path: the filename, and the
`"_unverified": true` marker inside the file. Either one is enough for
`LoadRegistry` to refuse it, so neither renaming the file nor stripping the
marker launders it into the App Hub. The single deliberate way to read it is
`appnet.LoadUnverifiedRegistry`, which no runtime path calls, logs a banner
naming every entry it returns, and is refused outright under `VULOS_ENV=prod`
(which is also the default when `VULOS_ENV` is unset).

`make verify-registry` fails closed if the quarantine file exists and an app ID
appears in both files, an entry in it carries a signature, it has lost its
marker, or it is empty (an empty quarantine must be deleted, not left as a
check that passes by doing nothing). If the file does not exist at all — every
entry promoted — the run says so explicitly and names what it therefore did not
cross-check.

#### Promotion — the only route into the signed registry

1. **Validate on real hardware.** Discharge the caveat in the entry's own
   `_note`: run the install recipe end to end on a box that has the
   prerequisites. Record what was exercised, on what kernel and host, in the PR.
2. **Rewrite the caveat.** Edit `_note` so it states what was actually verified.
   Never promote an entry whose `_note` still says UNVERIFIED — that is the
   exact state the quarantine exists to keep out of the signed set.
3. **Move the entry** into `registry.json`'s `apps` object and delete it from
   `registry-unverified.json`. Delete the whole file once it would be empty.
4. **Sign**, on the signing machine — CI never holds a private key:
   ```bash
   make sign-registry RELEASE_PRIV=/path/to/release.priv.json
   ```
5. **Prove it.** `make verify-registry` must report the new entry count and a
   matching coverage line, and `cd backend && go test ./services/appnet/` must
   pass. The gate is what promotes the entry; this list is only how you get
   there.

   > Both of those are **red today** and will stay red until the registry is
   > signed (§3) — 136 of 142 entries carry no signature. Before the ceremony,
   > the honest form of step 5 is "no *new* failure": compare against the eight
   > `missing signature` failures already present, rather than expecting green.

Demoting is the same in reverse: move the entry out, delete its signature, and
re-run `make verify-registry`.

---

## 6. Rotation and revocation

### Routine rotation — every 12 months

The cert expires; a box refuses an expired cert (`ValidateReleaseCert`), so this
is not optional maintenance. Repeat §4.2 → §4.5 with a new release key and a new
`-key-id`. **The anchor does not change, so nothing is reflashed.**

### Release key compromised

1. Generate a new release key (§4.2).
2. Issue a new cert with a **bumped `-min-epoch`** (§4.3).
3. Re-sign `registry.json` and any in-flight images with the new key (§4.5).
4. Ship an OS update carrying the new cert. The cert is baked into the image by
   `scripts/seed/embed-anchor.sh`, so a box that boots the new slot reads the new
   `/etc/vulos/release-cert.json` and stops trusting the old key.

No reflash. This is the entire reason the root key exists.

**How the epoch floor makes that stick.** Two things happen on the OTA path
(`services/ota/ota.go`), in this order, before the manifest is looked at:

1. **The gate.** `EpochStore.AcceptEpoch(cert.MinEpoch)` refuses a release cert
   whose `min_epoch` is below this box's floor — a retired cert being replayed
   is rejected before its release key is used for anything.
2. **The raise.** `EpochStore.RaiseFromReleaseCert(anchorPub, cert)` re-validates
   the root signature over the cert and raises the floor to its `min_epoch`. The
   floor is monotonic, so it never falls.

The raise takes the cert rather than a pre-validated number, so the floor can
never be moved by a cert nobody proved the root signed. Only the **root-signed**
value may raise it: if a release-key-signed *manifest* could, the very attacker
this mechanism defends against could publish `min_epoch = MaxInt64` and
permanently brick every device's update path. The raise also runs *before* the
manifest is verified, so a hostile channel cannot hold the floor down by serving
a good cert with a broken manifest and replaying the retired cert afterwards —
and it fails closed, because reporting an update as verified while the rollback
defence silently failed to persist is the exact failure this exists to prevent.

> **This changed recently.** Until commit `8846d61c`, `AcceptEpoch` only ever
> *checked* the floor and nothing anywhere raised it: a device sat at floor 0 for
> life, every `min_epoch >= 0` passed, and bumping `-min-epoch` revoked nothing.
> If you are reading an older note that says the floor never rises, that is why.

A box still only learns the new floor when it **checks for an update**, so a box
that never reaches its channel keeps whatever floor it last recorded. Between a
revocation and that check, the old cert's own expiry is the remaining bound —
which is why the 12-month `-not-after` in §4.3 is a security parameter, not
paperwork.

### Root key compromised

There is no recovery path in software. Every fielded device must be reflashed
with an image carrying a new anchor. Treat the root key accordingly: air-gapped,
two copies, two locations, and out of reach of anything that runs code you did
not write.

Note one detail, because it looks like a loophole and is not. `VULOS_TRUST_ANCHOR`
(§7) does let the anchor *path* be overridden — but it is read in exactly one
place, `services/appnet`'s registry trust resolution, so it moves only what the
**App Hub** trusts. The OS trust paths ignore it entirely and use the compiled-in
`/etc/vulos/trust-anchor.pub`: the OTA client (`services/ota`), the pre-pivot
gate (`cmd/init`, `cmd/verify`), the disk installer (`internal/installer`) and
the netboot verifier (`services/installer`). So the override is not a way back
from a compromised root key — an attacker holding that key can still mint a
release cert the baked anchor validates on every OS-update and install path,
and no environment variable reaches those. It exists so a container or test can
point at a staged `/etc/vulos`; setting it on a real box requires editing the
service unit, which already implies root.

---

## 7. What a box does with all this

`backend/services/appnet` (`resolveRegistryTrust`), in order, fail-closed at
every step:

1. `VULOS_REGISTRY_INSECURE=1` **in prod** → hard error. It is a dev-only escape
   hatch. An unset `VULOS_ENV` **means prod** (`services/env`), so the hatch is
   closed by default, not open by default.
2. Load the anchor: `$VULOS_TRUST_ANCHOR`, else `/etc/vulos/trust-anchor.pub`.
   Present-but-unreadable is a hard error — it never falls through to a weaker
   source, because falling through is how one corrupted byte becomes a downgrade.
3. Else `VULOS_REGISTRY_PUBKEY` — a direct entry-verification key, for forks that
   sign with a single key and no cert.
4. Else, **non-prod only**, the repo's checked-in dev anchor.
5. Any **dev key** resolved in prod → hard error.
6. With an anchor from a file: validate `/etc/vulos/release-cert.json` against it
   and use the release key it authorises. No cert → the anchor verifies entries
   directly (the single-key fork model).
7. No key at all → hard error. Installs are refused.

This resolution runs **once at boot** (`appnet.PreflightTrust`, called from
`cmd/server`), not lazily on the first install, so a misconfigured box is caught
before it serves anything. The verdict is one of three:

- **Refuse to start.** `VULOS_REGISTRY_INSECURE=1` in prod, or an unrecognised
  `VULOS_ENV`. The process `log.Fatal`s. It is not possible to boot a production
  box with app signature verification switched off — the escape hatch is a startup
  failure, never a silent downgrade that only surfaces when a user clicks Install.
- **Degraded — App Hub disabled.** No anchor, an unusable anchor, or the dev anchor
  in prod. Verification stays **on** and refuses every entry, so installs fail
  closed; the rest of the box keeps serving (mail and meetings do not owe their
  availability to the app registry). This is the state a box is in until the
  ceremony in §4 is run. The boot log says so, loudly.
- **Healthy.** A trusted key resolved; the boot log names it.

Then each entry's Ed25519 signature is checked over
`Canonical({"app_id": <id>, "entry": <entry-without-signature>})`. The `app_id`
is inside the signed bytes, so a signed entry cannot be moved to a different app
slot. The **whole** entry is signed, including fields the Go struct does not
model (`_note`, `lane`, `admin_only`) — they round-trip through `Extra` for
exactly that reason.

### Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `VULOS_TRUST_ANCHOR` | `/etc/vulos/trust-anchor.pub` | Path to the root public key |
| `VULOS_RELEASE_CERT` | `/etc/vulos/release-cert.json` | Path to the root-signed release cert |
| `VULOS_REGISTRY_PUBKEY` | — | Direct entry-verification key, base64 (forks; no cert chain) |
| `VULOS_REGISTRY_INSECURE` | unset | Skip verification. **Refused when `VULOS_ENV=prod`** |
| `VULOS_ENV` | `prod` | `local` \| `dev` \| `prod` |

---

## 8. Proving it works

`backend/services/appnet/registry_acceptance_test.go` stages a temp directory as
the box's `/etc/vulos` and asserts, with **no insecure flag anywhere**:

- a box holding only the **shipped anchor** verifies every committed entry that
  carries a signature, and reports the split rather than a bare total — 142
  entries, 6 verified, 136 unsigned today. (The count that matters is how many
  *verified*, not how many were *read*: reporting the latter is what once let a
  registry with 55 unsigned entries print "verified 74" —
  `services/appnet/registry_acceptance_test.go:30-38`.)
- a box holding a **ceremony anchor** installs a signed app end-to-end **under
  `VULOS_ENV=prod`**;
- a **tampered** entry, an **unsigned** entry, an entry signed by a **foreign
  key**, an **expired** cert, and a cert signed by a **different root** are each
  refused — and leave nothing on disk;
- the **shipped anchor is a production anchor and is accepted in prod** — the
  test fails if a dev key is ever committed as the shipped anchor again — while
  an entry signed by the **dev release key is still refused in prod**.

`registry_prodgate_test.go` pins the `VULOS_REGISTRY_INSECURE` prod refusal,
including the unset-`VULOS_ENV` and typo'd-`VULOS_ENV` cases, and the dev-key
refusal (`TestProdGate_DevKeyRefusedInProd`).
`registry_preflight_test.go` pins the boot-time gate: insecure-in-prod is a
refusal to **start**, a missing anchor degrades to "installs refused" without
bricking the box, and a dev anchor in prod degrades the same way
(`TestPreflight_DevAnchorInProdIsDegraded`).

```bash
make verify-registry                       # public-key check, no secrets
make verify-registry-prod                  # the release gate — fails on the dev key
cd backend && go test ./services/appnet/   # the acceptance suite
```

### The release gate — RED, and this is what blocks 0.3.0

`make verify-registry-prod` (run by `.github/workflows/release.yml:87` on every
`v*` tag) **refuses to build a release** whose registry is not fully signed by a
production release key — the same contract as netboot's `os-core.roothash.sig`:
no founder signature, no image.

**It fails today**, on `entry "ardour": … has no publisher signature
(REGISTRY-SIGN-01)`, exit status 2. The root/release ceremony *has* been run —
the anchor and cert in `keys/` are production, key-id `release-2026-08` valid to
`2027-08-03` — but the 142-entry registry is 136 signatures short (§3). One
signing run with the release key the cert authorises clears it; the coordinator
measured 142/142 signed and `verify-registry` OK against a throwaway vault.

It stays red, or goes red again, if the cert expires, if an entry is added
without re-signing, or if anything restores the dev keys over `keys/` — see §3
on `make dev-keys`, `check-release-key`, and a bare `make sign-registry`.
