# Screenshot provenance — what the product's images actually showed

Status: **done for the mechanism, open for one release-quality item** (below).

## The question

The founder booted a real box, found the bundled apps failing, and asked:

> "in screenshots these all work, why when I'm trying is it not?"

## What the harness actually was

Two separate pipelines existed, and only one of them was what the product is
presented with.

**`frontend/scripts/screenshots.mjs`** (1567 lines) is the real one. It drives
the genuine production `vite build` bundle and mocks the entire backend at the
browser network layer via `frontend/e2e/mock-backend.js`. It writes 48 PNGs to
`docs/screenshots/`, all committed (13 MB). 20 of them are consumed: 13 by
`README.md` and the rest by `docs/USER-GUIDE.md`, `APPS.md`, `SETTINGS.md`,
`MAIL-CALENDAR-CONTACTS.md`, `ADD-DEVICE.md`, `REMOVE-DEVICE.md`,
`CUSTOM-DOMAIN.md`, `GETTING-STARTED.md`, `FILES.md`, `ASSISTANT.md`,
`TERMINAL.md`. Every consumer references the `-light` variant. There is no
`site/` directory in this repo, and no CI job generates, uploads or link-checks
screenshots.

**`frontend/e2e/_polish-shots.e2e.ts`** — since deleted — was the file whose header read
`NOT part of the suite — never commit this file`. It was committed, and it
matched `playwright.config.ts`'s `**/*.e2e.{js,ts}`, so it ran on every CI E2E
run. Its output went to `/tmp/polish-shots`, which **nothing reads**. It was
cost with no consumer.

So the file with the alarming comment was the harmless one. The images the
product is actually sold with come from `screenshots.mjs` — and they are
fixture-backed too.

### What the mock does that matters

`mock-backend.js` answers `POST /api/apps/launch` with `{ok:true}` and starts
nothing. That single line is the whole gap: the screenshots could not, even in
principle, distinguish a working box from a broken one. v0.2.0 shipped with
`/opt/vulos/apps/` empty and every bundled app answering
`{"error":"app not running"}`, and these images stayed green throughout.

### The finding that reframed the work

**Not one of the 48 committed screenshots showed a process-backed app.** Every
shot — hero, files, settings, calendar, contacts, apphub, dashboard, instances,
terminal, launchpad, stacked, tiled, mobile — is a built-in React surface inside
the shell. The 15 apps under `frontend/apps/` (browser, calculator, camera,
clock, gallery, image-editor, music, notes, phone, screenshot, system-info,
text-editor, video, voice-recorder, weather) had **no screenshot at all**, in
any state.

The founder was not misled by a fake picture of Notes. He was shown a polished
desktop and reasonably inferred the apps in it worked, when nothing had ever
photographed them.

## `ip netns` was not the obstacle

The expectation going in was that real app screenshots would need a Linux
container, because app isolation uses `ip netns`. That is wrong, and worth
recording because it nearly stopped the work.

`ip netns` is how the **box routes to** an app. It is not needed to **run**
one. Every bundled app is a plain Python stdlib HTTP server invoked as
`python3 server.py`. `python3 scripts/check-apps-run.py` starts and serves all
15 **on this macOS host**, in 0.1–0.5s each. No container, no VM.

The Linux-only part is the namespace-plus-gateway path, which keeps its own
coverage (`check-apps-run.py` for start-and-serve; the container run that
verified namespace reclaim). A green live-shot run means "the app runs and
renders", not "the box's gateway routes to it" — stated in the spec header.

## What now exists

| Class | Location | Count | Backed by |
|---|---|---|---|
| FIXTURE | `docs/screenshots/` | 48 | real shipping UI, mocked data |
| LIVE | `docs/screenshots/live-apps/` | 15 | a real process, really serving |

`frontend/e2e/shots-live-apps.e2e.ts` imports nothing from `mock-backend.js`,
installs no `page.route`, spawns each app from its own manifest `command` in a
throwaway `HOME`, and points the browser at the port it really bound.

**Liveness proof.** After each capture the app is killed and the same URL is
re-fetched from inside the page with `cache:'no-store'`. That fetch must fail.
A mocked or cached page would keep answering, so the image cannot come from
anything but a process that was genuinely up. Mutation M7 — skipping the kill —
fails the spec, which is the proof the check bites.

`scripts/check-screenshot-provenance.py` (CI job `SHOTS-PROV`) checks the labels
against the code: the live spec must import no mock and keep its proof, the
fixture generator must still actually mock, every process-backed app must have a
live shot, and `PROVENANCE.md` must match the shots on disk.

### What the live shots show

Real empty states, because that is what a freshly-booted box looks like:
Weather reports "Location detection unavailable", Notes is an empty editor,
Browser is a blank viewport. `VULOS_API` is pinned to the dead `127.0.0.1:1`
(matching `check-apps-run.py`), so an app reporting itself offline is telling
the truth rather than being staged.

These are more informative about "will this work when I boot it" than any of the
48 fixture shots, and they are also less flattering. Both facts are the point.

## Open

- **`system-info.png` reads the capture host, not a box.** It currently shows
  this developer machine's real hostname, kernel and disk (`pcs-MacBook-Air.local`,
  Darwin 24.6.0, arm64, 792.8/926.4 GB). That is the clearest evidence the shot
  is not staged, and it is also a laptop's details published in `docs/`. For a
  release-quality capture, run `shots-live-apps.e2e.ts` inside the Linux
  container used for the namespace verification, so the machine shown is a box.
  Noted in `PROVENANCE.md` rather than quietly cropped.

- **28 of the 48 fixture screenshots are referenced by nothing** (every dark
  variant, the whole `settings-relay*` set, `mobile-assistant*`, `stacked-light`).
  Roughly 29 MB of committed images with 20 actually consumed.

  **`frontend/e2e-shots/` is now untracked and gitignored (2026-08-16).** The
  blocker named here — nothing verifies that image links resolve — was closed by
  `TestDocImagesResolve` in `backend/internal/docsref/`, so a delete that took a
  live embed with it now fails the build. That is what made the cleanup safe.

  The deciding evidence was not the size. Those 20 PNGs are Playwright OUTPUT:
  every run re-renders them, so they churned on every invocation with zero
  content change, permanently dirtying `git status` and hiding real edits in the
  noise. They are diagnostic captures an agent looks at while debugging a
  layout, worth exactly one run — not artifacts, and not the product screenshots,
  which live in `docs/screenshots/` under a provenance gate.

  The 28 unreferenced fixture screenshots in `docs/screenshots/` are a separate
  question and are deliberately left: they are stable rather than churning, and
  several document features whose docs are unwritten rather than absent.
