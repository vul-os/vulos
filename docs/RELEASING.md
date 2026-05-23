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

## Signed Tags & Artifacts

Tags are signed with the maintainer's GPG key:
```sh
git tag -s v0.3.1 -m "Release v0.3.1"
```

Release artifacts (SquashFS image + manifest) are signed with cosign:
```sh
cosign sign-blob --key release.key build/manifest.json > build/manifest.json.sig
```

See `docs/REPRODUCIBLE-BUILDS.md` for full signing + verification workflow.

Note: signing infrastructure (GPG key, cosign key) is documented; automated signing in CI is a v1.0 milestone.
