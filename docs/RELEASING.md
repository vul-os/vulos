# Vulos OS – Versioning & Release Policy

## Versioning Scheme

Vulos OS uses **Semantic Versioning** (semver): `MAJOR.MINOR.PATCH`.

- Currently **v0.x**: API is not yet stable. Breaking changes may occur between minor versions with a note in CHANGELOG.md.
- **v1.0** will be tagged when the device-pairing and auth APIs are stable and the self-hosted upgrade path is non-destructive.

## Tag Format

```
vX.Y.Z
```

Examples: `v0.3.1`, `v1.0.0`, `v1.0.1`

## Release Branches

- Development happens on `main`.
- Before a release, create a release branch: `release/X.Y` (e.g. `release/0.3`).
- Patch releases cherry-pick fixes onto the release branch.

```sh
git checkout -b release/0.3 main
git tag v0.3.0
git push origin release/0.3 v0.3.0
```

## Commit Message Convention

We follow **Conventional Commits**:

| Prefix | Use for |
|--------|---------|
| `feat:` | New feature (minor version bump) |
| `fix:` | Bug fix (patch bump) |
| `chore:` | Build/tooling changes |
| `docs:` | Documentation only |
| `refactor:` | Code restructuring |
| `test:` | Tests only |
| `BREAKING CHANGE:` | In commit footer — triggers major bump |

## CHANGELOG

`CHANGELOG.md` is maintained manually at the root of the repo. Template:

```markdown
## v0.4.0 — 2026-06-01

### Added
- Feature description (feat: ...)

### Fixed
- Bug fix description (fix: ...)

### Changed
- ...

### Breaking Changes
- ...
```

## Signed Artifacts

> This section previously described `cosign sign-blob` over a `manifest.json`
> and a maintainer GPG key. Neither exists: `cosign` appears nowhere in this
> repository, nothing emits a `manifest.json`, and `SECURITY.md` states that no
> PGP key has been published. Corrected 2026-08-10.

Release signing is **Ed25519**, done with `backend/cmd/sign`, and it is
deliberately **not** automated in CI — `release.yml` holds no private key,
because signing is a human operation on an offline machine (see
[decisions.md D99](decisions.md)).

The shape of a release is therefore two phases:

1. **CI builds, unsigned.** It publishes the live images for both architectures
   plus the content-identity root hash
   (`vulos-<version>-<arch>.roothash`, computed by `build.sh`'s VERITY-01 over
   the same squashfs).
2. **The maintainer signs offline.** `sign sign-manifest` signs `stable.json`
   against that root hash with the release key; the signed `stable.json` and its
   `.sig` ship as release assets. This works because the root hash is computed
   over a rootfs with the manifest removed, so signing afterwards does not
   invalidate it.

`sign issue-release-cert` (offline root key → release cert) and the rotation and
revocation procedure are in [KEY-CEREMONY.md](KEY-CEREMONY.md), which is
authoritative. `docs/REPRODUCIBLE-BUILDS.md` §4–§5 covers the verity artefacts
and the signer's subcommands.

**Git tags are not currently signed.** `git tag -s` needs a GPG key this project
has not published; use a plain annotated tag (`git tag -a`) until one exists.
