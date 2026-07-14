# Third-Party Licenses

This file is superseded by **[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)**,
which is **generated** from the dependency graph that is actually shipped and
therefore does not go stale.

Do not maintain attribution by hand here. Regenerate the notices with:

    node scripts/licensing/gen-notices.mjs

`THIRD_PARTY_NOTICES.md` reproduces the full licence text of every Go module
linked into the binaries, every npm package bundled into the web UI, and every
library vendored into the OS apps. When generated against a built image it also
lists the Debian packages installed into that image; those packages carry their
own copyright files inside the image at `/usr/share/doc/<pkg>/copyright`.

The notices are surfaced to users in **Settings → About → Open source licences**
(served by `GET /api/system/licenses`).

## Corresponding source (GPL / LGPL)

The Vulos image contains GPL- and LGPL-licensed binaries. Because Vulos is
distributed commercially, the source for those components is offered under a
written offer: see **[`WRITTEN-OFFER.md`](WRITTEN-OFFER.md)** (currently a DRAFT
pending founder and lawyer review). The mechanism that makes that offer
producible — snapshotting the exact source package versions of each image — is
`scripts/licensing/collect-corresponding-source.sh`.
