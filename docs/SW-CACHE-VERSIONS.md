# Service-Worker Cache Version Registry

**Scope:** FIX-SW-CACHE-COORD-01 (Wave A audit, 2026-05-24).

Vulos ships two cache-bearing surfaces, each with its own service worker and
its own `CACHE_NAME`. They share users — a single browser session may have
all three installed at once — so a stale shell in one can be hidden behind a
fresh shell in another. This file is the cross-repo coordination point.

## Current cache names

| Repo | Service worker | `CACHE_NAME` (current) |
| --- | --- | --- |
| `vulos` (OS shell) | `public/sw.js` | `vulos-os-shell-v1` |
| `vulos-office` (Spaces / Office) | `public/sw.js` | `vulos-office-v1` |

Source paths are stable; grep `CACHE_NAME` inside each repo to confirm the
current value before bumping.

## Coordination rule

When you bump a `CACHE_NAME` in any one of these surfaces:

1. **Update this file.** Increment the version in the row above so the
   registry stays in sync with the source. The file is authoritative for
   "what is currently live on each surface" — the SW source is authoritative
   for what gets shipped, but this table is what humans look at when
   diagnosing cross-surface staleness.
2. **Notify the other two surfaces.** Open a tracking note in each repo
   (issue, decisions log, or a PR comment cross-linking back to the bump).
   A bump in one surface without a coordinated bump elsewhere is allowed —
   the SWs are independent — but it means a user with all three installed
   will see a freshly-purged shell on the bumped surface and a stale one on
   the others. The rule is "*decide* whether the others need bumping, not
   "always bump together."
3. **Keep the version suffix monotonic.** Versions are `vN` integers
   (`v1`, `v2`, …). Do not reuse a prior version number — the activate
   handler in each SW deletes any cache key that does not match the current
   `CACHE_NAME`, so a downgrade is silently lossy.

## Why this is a coordination file, not a registry service

There is no runtime coordination between the three SWs: each one purges only
its own caches (see the `activate` handler in each `sw.js`). A central
registry would be over-engineering for three surfaces that ship from three
repos on independent cadences. A markdown file with a grep-derived table is
the right shape — it is human-readable, lives next to the OS docs, and is
trivially diffable in code review.

## Pointers

- `vulos/public/sw.js` — OS shell SW.
- `vulos-office/public/sw.js` — Ofisi (Docs / Sheets / Slides / PDF / Whiteboard) SW. (Spaces, Calendar, and Talk were extracted/retired and no longer live in this shell — Calendar is now a standalone OS builtin, and Talk is a retired product; see README.md.)
LilMail (github.com/vul-os/lilmail) also registers a service worker
(`assets/sw.js`), but it is push-only with no caching or offline logic, so it
has no `CACHE_NAME` to coordinate here.
