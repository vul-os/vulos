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

## 3. The dev keypair

A fresh clone must be able to run `make dev` and `make test-local` (the Go
module root is `backend/`, so the raw form is `cd backend && go test ./...`)
and have real
signature verification pass — with no flags, no key material to fetch, and no
insecure mode. So the repo ships a **development** anchor and cert:

```
keys/trust-anchor.pub    dev ROOT public key      (committed, shipped by default)
keys/release-cert.json   dev release cert         (committed, shipped by default)
keys/root.pub.json       dev ROOT public key      (committed)
keys/release.pub.json    dev RELEASE public key   (committed)
keys/*.priv.json         dev private keys         (GITIGNORED, regenerated on demand)
```

The dev private keys are **not committed** — but they are also **not secret**.
They are derived deterministically from two published seed strings:

```
root:    vulos-dev-signing-root-v1
release: vulos-dev-signing-release-v1
```

Anyone can reproduce them in one command. That is the point: it makes the dev
keys reproducible for every contributor without anyone shipping a secret.

**This is only safe because a dev key cannot be trusted in production.**
`backend/services/signing/devanchor.go` pins both dev public keys as constants
and `RefuseDevKeyInProd` rejects them whenever `VULOS_ENV=prod` — through *any*
door: the baked anchor file, the release cert, or `VULOS_REGISTRY_PUBKEY`. A
production box holding the dev anchor refuses to install anything and says why.
`TestDevKeys_PinnedConstantsMatchSeeds` fails if the seeds and the pinned
constants ever drift apart.

Regenerate them (reproducible, byte-identical every time):

```bash
make dev-keys
```

---

## 4. The ceremony

Everything below runs on an **air-gapped machine**. It never touches a network.
Build `vulos-sign` on your workstation, copy the binary across on removable
media, and copy only *public* artifacts back.

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

```bash
# new app added to registry.json
make sign-registry RELEASE_PRIV=/path/to/release.priv.json
git commit -am "registry: add <app>"

# OS image
./vulos-sign sign-image    -release-priv release.priv.json -key-id release-2026-07 ...
./vulos-sign sign-manifest -release-priv release.priv.json -key-id release-2026-07 ...
```

If you add an app and forget to sign it, CI fails with the app's name. It cannot
ship unsigned.

---

## 6. Rotation and revocation

### Routine rotation — every 12 months

The cert expires; a box refuses an expired cert (`ValidateReleaseCert`), so this
is not optional maintenance. Repeat §4.2 → §4.5 with a new release key and a new
`-key-id`. **The anchor does not change, so nothing is reflashed.**

### Release key compromised

1. Generate a new release key (§4.2).
2. Issue a new cert with a **bumped `-min-epoch`** (§4.3). The epoch floor is
   what makes the old key's artifacts unacceptable rather than merely stale — a
   box that has seen epoch *N* refuses any cert below *N*, so an attacker cannot
   replay the old, still-unexpired cert.
3. Re-sign `registry.json` and any in-flight images with the new key (§4.5).
4. Ship an OS update carrying the new cert. Boxes pick it up and stop trusting
   the old key.

No reflash. This is the entire reason the root key exists.

### Root key compromised

There is no recovery path in software. The anchor is baked into the image and
the initramfs precisely so that nothing on the box can change it. Every fielded
device must be reflashed with an image carrying a new anchor. Treat the root key
accordingly: air-gapped, two copies, two locations, and out of reach of anything
that runs code you did not write.

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

- a box holding only the **shipped anchor** verifies all 55 committed entries;
- a box holding a **ceremony anchor** installs a signed app end-to-end **under
  `VULOS_ENV=prod`**;
- a **tampered** entry, an **unsigned** entry, an entry signed by a **foreign
  key**, an **expired** cert, and a cert signed by a **different root** are each
  refused — and leave nothing on disk;
- the **dev anchor is refused in prod**.

`registry_prodgate_test.go` pins the `VULOS_REGISTRY_INSECURE` prod refusal,
including the unset-`VULOS_ENV` and typo'd-`VULOS_ENV` cases.
`registry_preflight_test.go` pins the boot-time gate: insecure-in-prod is a
refusal to **start**, a missing anchor degrades to "installs refused" without
bricking the box, and the dev anchor is refused in prod.

```bash
make verify-registry                       # public-key check, no secrets
make verify-registry-prod                  # the release gate — fails on the dev key
cd backend && go test ./services/appnet/   # the acceptance suite
```

### The release build halts until the ceremony is run

`make verify-registry-prod` (run by `.github/workflows/release.yml` on every tag)
**refuses to build a release** whose registry is signed by the dev key. Today, on
an un-ceremonied repo, tagging a release fails with `REFUSING TO RELEASE`. That is
deliberate and it is the same contract as netboot's `os-core.roothash.sig`: no
founder signature, no image. Completing §4 is what turns it green.
