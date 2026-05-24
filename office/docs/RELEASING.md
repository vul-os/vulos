# Vulos Office – Versioning & Release Policy

## Versioning

Semver (`vX.Y.Z`). Currently **v0.x**.

v1.0 when: the signing API and CRDT wire format are stable.

## Tag Format

`vX.Y.Z` on `main`. Release branches: `release/X.Y`.

## Commit Convention

Conventional Commits. Breaking CRDT wire-format changes require a `BREAKING CHANGE:` footer.

## Signed Artifacts

```sh
cosign sign-blob --key release.key vulos-office-linux-amd64 > vulos-office-linux-amd64.sig
git tag -s v0.3.1 -m "Release v0.3.1"
```

## CHANGELOG

`CHANGELOG.md` at repo root. Conventional Commits format.
