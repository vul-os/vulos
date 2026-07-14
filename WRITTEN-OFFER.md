# Written Offer for Source Code

> **STATUS: DRAFT — REQUIRES FOUNDER AND LAWYER REVIEW BEFORE RELEASE.**
>
> This file is a working draft of the written offer that the GPL and LGPL
> require a *commercial* redistributor of GPL/LGPL binaries to include with the
> product. The wording below is a starting point assembled from the licences'
> own suggested text; it is **not legal advice and is not final**. The founder
> and a lawyer must:
>
> 1. Replace every `<PLACEHOLDER>` with real, verified contact and delivery
>    details (a working postal address and/or email that will still be
>    monitored three years after the last unit ships).
> 2. Confirm the offer period and cost-recovery wording against the versions of
>    the licences that actually apply (GPL-2 §3(b), GPL-3 §6(b), LGPL-2.1 §6,
>    LGPL-3 §4), and against the law of the governing jurisdiction.
> 3. Decide the delivery mechanism the offer commits to (physical medium at
>    cost, a download URL, or both) and make sure the operational side of it —
>    see `scripts/licensing/collect-corresponding-source.sh` — is actually run
>    for every image that is distributed, and its output retained for the full
>    offer period.
> 4. Remove this DRAFT banner only once all of the above is done.
>
> Shipping the offer text below **as-is** would be making a binding legal
> promise that has not been reviewed. Do not do that.

---

## Why this document exists

Vulos OS is distributed as a disk/live/netboot image built on Debian. That image
contains, in binary form, software licensed under the GNU General Public License
(versions 2 and 3) and the GNU Lesser General Public License (versions 2.1 and
3) — including, among many others, the GNU coreutils, bash, sudo, systemd,
iptables, GStreamer and PulseAudio.

Those licences permit conveying the binaries only if the person conveying them
also makes the **Corresponding Source** available. Vulos is offered
commercially, so the reduced obligation for noncommercial distribution (pointing
recipients at a third party's server, GPL-2 §3(c) / GPL-3 §6(c)) does **not**
apply. Vulos therefore accompanies the binaries with this written offer, as
permitted by GPL-2 §3(b) and GPL-3 §6(b).

The mechanism that makes this offer honourable is in the repository:
`scripts/licensing/collect-corresponding-source.sh` snapshots the exact source
package versions installed into each image and can fetch and retain them, pinned
to a `snapshot.debian.org` instant so they remain reproducible for the life of
the offer. The per-image inventory is recorded in `SOURCES.manifest`.

Attribution and the licence texts of the components are in
`THIRD_PARTY_NOTICES.md`.

---

## The offer (DRAFT text — subject to the review above)

> For any Vulos OS image you have received, the complete Corresponding Source
> for all of the GPL- and LGPL-licensed components it contains is available.
>
> For at least three (3) years from the date on which you received your copy of
> the image, `<LEGAL ENTITY>` will give any third party, for a charge no more
> than `<our cost of physically performing this distribution>`, a complete
> machine-readable copy of the Corresponding Source for the version of those
> components contained in that image, on a physical medium customarily used for
> software interchange, or — at our option and yours — by download.
>
> To request the source, contact:
>
>     <LEGAL ENTITY>
>     <POSTAL ADDRESS>
>     <EMAIL / URL>
>
> Please identify the image by the version string shown in Settings → About, or
> by the `SOURCES.manifest` accompanying your image, so we can supply the source
> that corresponds to your exact build.
>
> This offer is valid to anyone in receipt of this information. The
> Corresponding Source is also available for the LGPL-licensed components under
> the same terms; for those components you may additionally exercise the rights
> granted by the LGPL (including relinking against a modified version of the
> library).

---

## What the founder/lawyer must still do (checklist)

- [ ] Fill in `<LEGAL ENTITY>`, `<POSTAL ADDRESS>`, `<EMAIL / URL>`.
- [ ] Confirm the three-year period and the "no more than our cost" charge
      wording are correct for GPL-2 §3(b) and GPL-3 §6(b) as applied here.
- [ ] Confirm the LGPL paragraph adequately covers LGPL-2.1 §6 / LGPL-3 §4
      (the right to relink), or point to where that is satisfied.
- [ ] Decide and state the delivery method actually offered.
- [ ] Establish the operational process: run
      `scripts/licensing/collect-corresponding-source.sh --fetch` for every
      distributed image and retain its `corresponding-source/` output and
      `SOURCES.manifest` for the full offer period; ensure someone monitors the
      contact channel for that period.
- [ ] Remove the DRAFT banner at the top of this file.
