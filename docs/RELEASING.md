# Vulos OS – Versioning & Release Policy

## Versioning Scheme

Vulos OS uses **Semantic Versioning** (semver): `MAJOR.MINOR.PATCH`.

- Currently **v0.x**: API is not yet stable. Breaking changes may occur between minor versions with a note in CHANGELOG.md.
- **v1.0** will be tagged when the device-pairing and auth APIs are stable and the self-hosted upgrade path is non-destructive.

Under 0.x, both new features and breaking changes bump the **minor** number;
only a release that is purely fixes bumps the patch.

## Tag Format

```
vX.Y.Z
```

Examples: `v0.3.1`, `v1.0.0`, `v1.0.1`

## Where the version is asserted

Four files must agree before a tag is pushed. Two of them are checked by CI, and
two are not checked by anything.

| Location | Checked by | Note |
|---|---|---|
| `VERSION` | nothing | Read by no build step; it is documentation. |
| `frontend/package.json` | **release.yml** | The tag must equal `v` + this value, or the build stops. |
| `frontend/package-lock.json` | **release.yml** (`npm ci`) | Carries the version **twice** — the root object and `packages[""]`. `npm ci` fails if either disagrees with `package.json`. |
| `README.md` download example | nothing | Names a release filename; goes stale silently. |

There is **no root `package.json`** — the web tier moved to `frontend/`, and
that is the only one.

The Go binary takes its version from `-ldflags "-X main.Version=…"`, defaulting
to `dev` (`backend/cmd/server/main.go`). **Only the Dockerfile passes it.**
`build.sh` builds with `-ldflags="-s -w"` and no `-X`, so the binaries inside the
bare-metal `.img.gz` and rootfs tarball report `dev` regardless of the tag. The
CI step that compares the tag to `--version` compiles its *own* throwaway binary
with the right flags, so it passes without ever inspecting a shipped artefact.
Fixing this means teaching `build.sh` to stamp the version; until then, do not
read `vulos --version` on a flashed image as evidence of which release it is.

## Branches

Development happens on `main`, and releases are tagged from `main`.

> The previous version of this section prescribed cutting a `release/X.Y` branch
> before every release and cherry-picking patches onto it. No such branch has
> ever existed in this repository; `v0.1.0` and `v0.2.0` were both tagged on
> `main`. Corrected 2026-08-19 — a documented process nobody follows is worse
> than no documented process, because it makes the real one look like a mistake.

If a patch release is ever needed for a version that `main` has already moved
past, cut `release/X.Y` from the tag at that point. That situation has not
arisen yet.

## Commit Messages

Commit subjects are **descriptive sentences stating the defect or the change**,
not prefixed types — for example *"A percentage that never moves is a claim that
something is happening"*. The body explains what was wrong, what the fix is, and
what was measured.

> This section previously declared "We follow **Conventional Commits**" with a
> table of `feat:` / `fix:` / `chore:` prefixes, and stated that the prefix
> drives the version bump. It has never been true: 6 of the 378 commits in
> `v0.2.0..HEAD` carry such a prefix. Version bumps are decided by judgement
> against the changelog, not derived from commit subjects. Corrected 2026-08-19.

## CHANGELOG

`CHANGELOG.md` is maintained by hand at the repo root, in
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/) form.

**The heading format is load-bearing.** `release.yml` extracts the release notes
with an `awk` match on `^## [X.Y.Z]`, so the heading must be exactly:

```markdown
## [0.3.0] - 2026-08-19
```

If no section matches, the step does not fail — it **silently falls back to a
raw `git log` dump** as the release body. That is what happened to `v0.2.0`,
which was tagged with no changelog section at all.

Sections used: `Security`, `Added`, `Changed`, `Fixed`, and a
**`Known limitations`** section that is mandatory on every release. Group bullets
by what the change means to someone using the box, not by subsystem.

## Signed Artifacts

> This section previously described `cosign sign-blob` over a `manifest.json`
> and a maintainer GPG key. Neither exists: `cosign` appears nowhere in this
> repository, nothing emits a `manifest.json`, and `SECURITY.md` states that no
> PGP key has been published. Corrected 2026-08-10.

Release signing is **Ed25519**, done with `backend/cmd/sign`, and it is
deliberately **not** automated in CI — `release.yml` holds no private key,
because signing is a human operation on an offline machine (see
[decisions.md D99](decisions.md)).

There are **three** signing-related phases, and they do not all happen at the
same time. The middle one is the one most easily forgotten, because it is the
only one that must happen *before* the tag exists:

1. **Registry signing — before the tag.** Every entry in `registry.json` must
   carry a publisher signature. This modifies a tracked file, so it must be
   committed and pushed before tagging. CI enforces it with
   `make verify-registry-prod`.
2. **CI builds, unsigned.** It publishes the live images for both architectures
   plus the content-identity root hash
   (`vulos-<version>-<arch>.roothash`, computed by `build.sh`'s VERITY-01 over
   the same squashfs).
3. **The maintainer signs the image manifest offline — after the tag.**
   `sign sign-manifest` signs `stable.json` against that root hash with the
   release key; the signed `stable.json` and its `.sig` ship as release assets.
   This works because the root hash is computed over a rootfs with the manifest
   removed, so signing afterwards does not invalidate it.

`sign issue-release-cert` (offline root key → release cert) and the rotation and
revocation procedure are in [KEY-CEREMONY.md](KEY-CEREMONY.md), which is
authoritative. `docs/REPRODUCIBLE-BUILDS.md` §4–§5 covers the verity artefacts
and the signer's subcommands.

**Git tags are not currently signed.** `git tag -s` needs a GPG key this project
has not published; use a plain annotated tag (`git tag -a`) until one exists.

---

# The release runbook

Ordered steps from "the tree is ready" to "the release is published". Do them in
this order: steps 3 and 4 exist because the two things CI cannot do for itself
(hold a private key, and boot an image on your desk) are also the two things that
have actually broken releases.

Nothing here pushes a tag on your behalf. Tagging is the founder's action.

## 0. Preconditions

- You are on `main`, the tree is clean, and `main` is pushed. `release.yml`
  triggers on the tag, and GitHub builds **the commit the tag points at** — if
  `main` is ahead of `origin/main`, tag and push the branch first or CI will
  build something other than what you tested.
- You know which version you are cutting, and why (see *Versioning Scheme*).

## 1. Set the version in all four places

```sh
V=0.3.0
printf '%s\n' "$V" > VERSION
# frontend/package.json  -> "version": "0.3.0"
# frontend/package-lock.json -> BOTH the root "version" and packages[""].version
# README.md download example filename
```

Verify the two that CI checks, exactly as CI does:

```sh
node -p "require('./frontend/package.json').version"     # must print 0.3.0
cd frontend && npm ci && cd ..                           # must exit 0
```

`npm ci` is the check that catches a half-done bump: it validates the lockfile
against `package.json` and fails if the version disagrees.

## 2. Write the changelog section

Add `## [X.Y.Z] - YYYY-MM-DD` to `CHANGELOG.md`, above the previous release.
Derive it from `git log <previous-tag>..HEAD`, not from anyone's summary.

Then confirm CI will actually find it — this is a silent failure, so check it:

```sh
awk "/^## \[$V\]/{f=1;next} f&&/^## /{exit} f{print}" CHANGELOG.md | head
```

Empty output means the release notes will be a raw commit dump.

Commit steps 1 and 2 together and push.

## 3. The signing ceremony — registry

**This must happen before the tag, and it changes a tracked file.**

On the offline machine holding the release private key:

```sh
make sign-registry RELEASE_PRIV=/media/signing/release.priv.json
make verify-registry-prod        # must exit 0
```

`sign-registry` refuses to run with the dev key: `check-release-key` requires the
key that `keys/release-cert.json` authorises. If you see it reach for
`keys/release.priv.json`, stop — that key is derived from a published seed and
anyone can sign for it.

One trap worth knowing, because it produces a success message: `cmd/sign`
defaults `-registry` to a bare relative path, so an untracked `backend/registry.json`
sitting in the working directory will be signed instead of the shipping one, and
the command will print that it signed and verified every entry. Run the `make`
target rather than the tool directly, and confirm with `git status` that the
root `registry.json` is what changed.

Then, back on `main`:

```sh
go test -short ./...   # in backend/ — the six appnet tests must now pass
git commit -m "…" -- registry.json
git push
```

## 4. Prove the artefact boots before you tag

CI's BOOT-01 gate boots the amd64 image and will refuse to publish one that does
not serve. It cannot boot arm64 (that would be emulation inside emulation), so
the arm64 image is covered only by your local run.

Use a manual `workflow_dispatch` run of the Release workflow: it builds the same
images through the same steps, publishes nothing, and uploads the images as
workflow artifacts. Download and boot the arm64 one.

## 5. Tag and push

```sh
git tag -a "v$V" -m "Release $V"     # annotated, not signed — see above
git push origin "v$V"
```

Pushing the tag is what starts the release. Everything before this point is
reversible; this is not, because the workflow publishes to `ghcr.io` and creates
a GitHub release on success.

## 6. Watch the gates, in the order they fail

Do not walk away after step 5. The expensive discovery is a gate that fails
*after* the ceremony. See the gate reference below.

## 7. Sign the image manifest — after CI publishes

CI publishes `vulos-<version>-<arch>.roothash` but cannot sign it. On the offline
machine:

```sh
cd backend && go run ./cmd/sign sign-manifest \
  -release-priv /media/signing/release.priv.json \
  -key-id release-2026-08 \
  -channel stable \
  -latest "v$V" \
  -path "os/v$V/os-core.squashfs" \
  -roothash "$(cat vulos-v$V-x86_64.roothash)" \
  -size <bytes> \
  -out stable.json.sig
```

`-key-id`, `-latest`, `-path` and `-roothash` are all **required**; the command
exits 1 without them. Upload `stable.json` and `stable.json.sig` to the release.

**Until you do this, disk installs cannot be verified**, and the release body
says so in as many words. The live images are unaffected.

## 8. Confirm what actually shipped

- The release lists both `.img.gz` files, both rootfs tarballs, both
  `.roothash` files, and `SHA256SUMS`.
- `sha256sum -c SHA256SUMS --ignore-missing` passes against the downloaded files.
- The release body's "Changes" section is your changelog, not a commit dump.

---

# Gate reference: `release.yml`, step by step

What each step checks, and what to do when it stops.

| # | Step | Fails when | Fix |
|---|---|---|---|
| 1 | Verify tag matches package.json version | Tag ≠ `v` + `frontend/package.json` version | Step 1 of the runbook. Delete the tag, fix, re-tag. |
| 2 | **REGISTRY-SIGN** (`make verify-registry-prod`) | Any `registry.json` entry has an empty signature | The ceremony, step 3. **Cannot be fixed in CI** — no private key exists on a runner, by design. |
| 3 | Verify tag matches binary version | Rare — it compiles its own binary with the tag baked in | Almost always passes. Note it proves nothing about the shipped images (see *Where the version is asserted*). |
| 4 | `npm ci` | Lockfile disagrees with `package.json` | Usually a half-done version bump: the lockfile carries the version twice. |
| 5 | `npm run build` | Frontend compile error | Note this does **not** typecheck — `npm run build` is `vite build`; run `npm run typecheck` separately. |
| 6 | `go test -short ./...` | Any Go test fails | Today the six `services/appnet` registry-signing tests fail for the same reason as gate 2, and go green with it. |
| 7 | `go vet ./...` | Vet diagnostic | — |
| 8 | Docker build & push | Multi-arch build failure | This is the only artefact whose binary is version-stamped. |
| 9 | Build system images | A rootfs tarball or `os-core.roothash` is missing | The step fails loudly on purpose rather than shipping an incomplete release; a missing roothash usually means `cryptsetup-bin` is absent from the builder. |
| 10 | **BOOT-01** | The built amd64 image does not boot and serve, or binds loopback instead of the LAN address | The one gate that catches a dead image. It distinguishes "QEMU could not start" (harness fault) from "the image does not boot". |
| 11 | Extract CHANGELOG notes | **Never fails** | Silently falls back to a raw `git log`. Check it yourself — runbook step 2. |
| 12 | Create GitHub release | — | Unmatched asset globs do **not** fail this step; confirm the asset list by eye (runbook step 8). |

## If a gate fails after the tag is pushed

Delete the tag locally and remotely, fix, and re-tag the same version — the
release is only created on success, so a failed run leaves nothing published
except, possibly, a `ghcr.io` image from step 8.

```sh
git push --delete origin "v$V" && git tag -d "v$V"
```

Do not "fix forward" by tagging `X.Y.Z+1` to skip a failing gate. The gate that
is failing is the one thing standing between the release and a defect a user
would find.
