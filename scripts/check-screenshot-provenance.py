#!/usr/bin/env python3
"""check-screenshot-provenance.py — every shipped screenshot declares how it was made.

WHY THIS EXISTS
---------------
The founder booted a real box, found the bundled apps failing, and asked: "in
screenshots these all work, why when I'm trying is it not?"

The answer was that every product screenshot was captured against a mocked
backend. `frontend/scripts/screenshots.mjs` and the whole Playwright suite
install `frontend/e2e/mock-backend.js`, whose `POST /api/apps/launch` answers
`{ok:true}` and starts nothing. Those images proved the shell RENDERS GIVEN
DATA. They could not say whether an app RUNS -- and at the time they were taken
no bundled app could run at all. README.md embeds 13 of them and docs/ embeds
more, so that is what the product is presented with.

A fixture-backed shot is legitimate for a built-in React surface: there is no
process behind Settings or the Launchpad, and a fixture there stands in for
DATA, not for a running app. What is not legitimate is being unable to tell.
So the split is made structural, and this script keeps it true:

    docs/screenshots/*.png            FIXTURE -- real shipping UI, mocked data
    docs/screenshots/live-apps/*.png  LIVE    -- a real process, really serving

WHAT IS ACTUALLY CHECKED
------------------------
The labels are checked AGAINST THE CODE, not taken on trust. A directory split
that anyone can quietly violate is decoration; these six checks give it teeth:

  CLASSIFIED  Every shipped PNG under docs/screenshots/ falls in exactly one
              class, and PROVENANCE.md lists exactly the files that exist.
              Adding a screenshot without declaring it fails.

  FIXTURE-IS-FIXTURE
              The generator behind the FIXTURE shots really does install the
              mock. If screenshots.mjs ever stopped mocking, calling its output
              fixture-backed would be a lie in the other direction.

  REFERENCED  Every shipped FIXTURE is embedded or cited by some tracked file.
              A figure nobody shows a reader is weight, and it accumulates
              silently: 24 such images (~14 MB) built up one run at a time
              before anyone looked. An exception costs one line and must carry
              a reason, and goes stale when the file is either referenced or
              deleted. LIVE shots are exempt on purpose -- see the check.

  LIVE-IS-LIVE
              Each spec behind the LIVE shots imports NOTHING from
              mock-backend.js, and still carries its liveness proof. This is the
              check that matters: it is what stops a LIVE-labelled shot from
              silently becoming another mocked one.

  SPEC-COVERAGE
              Every shots-live-*.e2e.ts is on the list LIVE-IS-LIVE walks, so a
              second live-shot harness cannot ship unchecked shots.

  COVERAGE    There is one LIVE shot per process-backed app in frontend/apps/,
              and the totals are non-zero. A rename or deletion that left the
              live set empty would otherwise pass by finding nothing to check --
              the "collection is not execution" failure this repo keeps hitting.

Usage:
    python3 scripts/check-screenshot-provenance.py           # verify
    python3 scripts/check-screenshot-provenance.py --write   # regenerate the manifest
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SHOTS_DIR = os.path.join(ROOT, "docs", "screenshots")
LIVE_SUBDIR = "live-apps"
MANIFEST = os.path.join(SHOTS_DIR, "PROVENANCE.md")
APPS_DIR = os.path.join(ROOT, "frontend", "apps")

FIXTURE_GENERATOR = os.path.join(ROOT, "frontend", "scripts", "screenshots.mjs")

# Every spec that writes into docs/screenshots/live-apps/, with the proof each
# one must still carry. A LIST, not a single path: when the fifteenth app got
# its own harness the single-path version kept passing while saying nothing
# about the new spec, so a shot could have been labelled LIVE with no check
# behind it at all. Adding a live-shot spec without adding it here leaves its
# output ungated, which SPEC-COVERAGE below turns into a failure.
LIVE_SPECS = {
    os.path.join(ROOT, "frontend", "e2e", "shots-live-apps.e2e.ts"): [
        # Both halves of the sequence: the app must be stopped, and the re-fetch
        # must be asserted to fail. Anchored on the PROOF, not on a variable
        # name -- an earlier version looked for the identifier `afterKill` and
        # survived a mutation that renamed the declaration, because the name
        # still appeared at the assertion site.
        ("await stopApp(started)", "never stops the app — nothing proves the shot was live"),
        ("is NOT backed by a live process", "the kill-then-refetch assertion is gone"),
    ],
    os.path.join(ROOT, "frontend", "e2e", "shots-live-system-info.e2e.ts"): [
        ("/api/apps/stop", "never asks the box to stop the app — nothing proves the shot was live"),
        ("is NOT backed by a live process", "the stop-then-refetch assertion is gone"),
        # This spec's whole reason to exist: the machine photographed must not
        # be the machine running the test. Lose that and it is the laptop leak
        # it was written to prevent.
        ("os.hostname()", "no longer checks that the box is not the capture host"),
    ],
}

GREEN, RED, DIM, RESET = (
    ("\033[32m", "\033[31m", "\033[2m", "\033[0m") if sys.stdout.isatty() else ("", "", "", "")
)

failures: list[str] = []


def fail(check: str, msg: str) -> None:
    failures.append(f"{check}: {msg}")
    print(f"{RED}FAIL{RESET} {check}: {msg}")


def ok(check: str, msg: str) -> None:
    print(f"{GREEN}ok{RESET}   {check}: {DIM}{msg}{RESET}")


def tracked_pngs() -> list[str]:
    """Shipped screenshots, per git -- what a reader of the repo actually gets.

    Deliberately git-tracked rather than a filesystem walk: an untracked local
    capture is nobody's evidence, and a tracked file that was deleted from disk
    is still what ships.
    """
    out = subprocess.run(
        ["git", "-C", ROOT, "ls-files", "docs/screenshots"],
        capture_output=True, text=True, check=True,
    ).stdout.split("\n")
    return sorted(p for p in out if p.endswith(".png"))


def process_backed_apps() -> list[str]:
    """Directories under frontend/apps/ whose app.json declares a `command`."""
    apps = []
    for name in sorted(os.listdir(APPS_DIR)):
        manifest = os.path.join(APPS_DIR, name, "app.json")
        if not os.path.isfile(manifest):
            continue
        with open(manifest) as f:
            if json.load(f).get("command"):
                apps.append(name)
    return apps


# File types that could plausibly point at a screenshot. Everything else is
# skipped so the scan does not try to read a PNG as text.
TEXTY = (
    ".md", ".html", ".htm", ".txt", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
    ".py", ".go", ".sh", ".yml", ".yaml", ".json", ".css", ".rs", ".toml",
)

# Markdown `![alt](path)`, inline `<img src="path">`, and a bare repo-rooted
# mention. The third form is deliberate: a path named in prose or in a code
# comment IS a reference -- somebody reading that line is expected to be able to
# open the file. Matching only embeds would call such a file dead and delete a
# path that a comment still points at.
MD_IMAGE = re.compile(r"!\[[^\]]*\]\(([^)\s]+)")
HTML_IMAGE = re.compile(r"""<img[^>]+src=["']([^"']+)["']""")
BARE_PATH = re.compile(r"docs/screenshots/[A-Za-z0-9_\-/]+\.png")


def referenced_shots() -> tuple[dict[str, list[str]], int]:
    """Map each referenced screenshot path to the tracked files that name it.

    Returns the map and the total number of image references examined, so the
    caller can refuse to trust a scan that found suspiciously little.
    """
    refs: dict[str, list[str]] = {}
    examined = 0
    tracked = subprocess.run(
        ["git", "-C", ROOT, "ls-files"], capture_output=True, text=True, check=True,
    ).stdout.split("\n")
    for rel in tracked:
        if not rel or not rel.endswith(TEXTY):
            continue
        try:
            with open(os.path.join(ROOT, rel), encoding="utf-8", errors="ignore") as f:
                body = f.read()
        except OSError:
            continue
        if ".png" not in body:
            continue
        here = os.path.dirname(rel)
        found = set()
        for pattern in (MD_IMAGE, HTML_IMAGE):
            for raw in pattern.findall(body):
                examined += 1
                target = raw.split("#")[0].split("?")[0].strip()
                if not target or target.startswith(("http://", "https://", "data:")):
                    continue
                if target.startswith("/"):
                    found.add(os.path.normpath(target.lstrip("/")))
                else:
                    found.add(os.path.normpath(os.path.join(here, target)))
        for raw in BARE_PATH.findall(body):
            examined += 1
            found.add(raw)
        for target in found:
            refs.setdefault(target, []).append(rel)
    return refs, examined


def classify(paths: list[str]) -> tuple[list[str], list[str]]:
    prefix = f"docs/screenshots/{LIVE_SUBDIR}/"
    live = [p for p in paths if p.startswith(prefix)]
    fixture = [p for p in paths if not p.startswith(prefix)]
    return fixture, live


def render_manifest(fixture: list[str], live: list[str]) -> str:
    def basename(p: str) -> str:
        return os.path.basename(p)

    lines = [
        "# Screenshot provenance",
        "",
        "**Generated by `scripts/check-screenshot-provenance.py`. Do not hand-edit —",
        "run `python3 scripts/check-screenshot-provenance.py --write`.**",
        "",
        "Every image shipped under `docs/screenshots/` is in exactly one of two",
        "classes. The class is the directory, so it is visible in a file listing",
        "without reading any code, and the script above fails if a shot's label does",
        "not match how it is actually produced.",
        "",
        "## LIVE — a real process, really serving",
        "",
        f"`docs/screenshots/{LIVE_SUBDIR}/` — {len(live)} images.",
        "",
        "Captured by `frontend/e2e/shots-live-apps.e2e.ts`, which imports nothing",
        "from `mock-backend.js`. Each app is spawned as a real subprocess from its",
        "own `app.json` `command`, and the browser is pointed at the port it really",
        "bound. After each capture the app is killed and the same URL is re-fetched",
        "from inside the page: that fetch must fail, so the image cannot have come",
        "from anything but a process that was genuinely up.",
        "",
        "These show real empty states, because that is what a freshly-booted box",
        "looks like. `VULOS_API` is pinned to the dead address `127.0.0.1:1`, so an",
        "app that reports itself offline or unconfigured is telling the truth.",
        "",
        "`system-info.png` is the exception, and has its own harness:",
        "`frontend/e2e/shots-live-system-info.e2e.ts`. That app's entire surface IS",
        "the machine it runs on, so shooting it where the other fourteen are shot",
        "would publish the capture machine's real hostname, kernel and disk. It is",
        "photographed on a **booted Vulos OS box** instead — started by the box's own",
        "launcher into its own network namespace — and the spec asserts that the",
        "hostname on screen is not this machine's, so a capture that quietly fell",
        "back to the developer's laptop fails instead of shipping.",
        "",
    ]
    lines += [f"- `{basename(p)}`" for p in live] or ["- _(none)_"]
    lines += [
        "",
        "## FIXTURE — real UI, mocked data",
        "",
        f"`docs/screenshots/` — {len(fixture)} images.",
        "",
        "Captured by `frontend/scripts/screenshots.mjs`, which drives the REAL",
        "shipping React bundle but answers every `/api` call from",
        "`frontend/e2e/mock-backend.js`. The UI is real; the data behind it is not.",
        "",
        "**These prove that the shell renders given data. They do NOT show anything",
        "running.** No process is started to produce them, and `POST /api/apps/launch`",
        "is answered `{ok:true}` by the mock. Read them as UI reference, never as",
        "evidence that a box works — that confusion is exactly why this file exists.",
        "",
        "This is a legitimate use of fixtures: these are built-in React surfaces with",
        "no process behind them, so the fixture stands in for DATA, not for a running",
        "app. Process-backed apps are shot live instead, above.",
        "",
    ]
    lines += [f"- `{basename(p)}`" for p in fixture] or ["- _(none)_"]
    lines.append("")
    return "\n".join(lines)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--write", action="store_true", help="regenerate PROVENANCE.md")
    args = ap.parse_args()

    pngs = tracked_pngs()
    fixture, live = classify(pngs)

    if args.write:
        with open(MANIFEST, "w") as f:
            f.write(render_manifest(fixture, live))
        print(f"wrote {MANIFEST} ({len(fixture)} fixture, {len(live)} live)")
        return 0

    # ── COVERAGE ────────────────────────────────────────────────────────────
    apps = process_backed_apps()
    if not pngs:
        fail("COVERAGE", "found NO tracked screenshots under docs/screenshots/")
    if not apps:
        fail("COVERAGE", "found NO process-backed apps under frontend/apps/")

    live_names = {os.path.basename(p)[: -len(".png")] for p in live}

    # Apps deliberately without a live shot. Not a general escape hatch, and the
    # two rules below stop it becoming one: an entry must carry a reason, and it
    # must be STALE the moment a shot appears -- so capturing one properly forces
    # the exemption to be deleted rather than quietly outliving its cause.
    #
    # It has been empty since 2026-08-16, and the mechanism is the reason. The
    # one entry it ever held was `system-info`, whose surface IS the machine it
    # runs on, so any capture on a developer machine published that machine's
    # hostname, kernel and disk into a public repository. The exemption expired
    # exactly as designed: frontend/e2e/shots-live-system-info.e2e.ts now shoots
    # it on a booted Vulos OS box, a live shot appeared, and the entry had to
    # go. Keep the dict, not the entry.
    NO_LIVE_SHOT: dict[str, str] = {}
    for name, reason in NO_LIVE_SHOT.items():
        if len(reason) < 60:
            fail("COVERAGE", f"{name} is exempted from a live shot with no real reason given")
        if name in live_names:
            fail("COVERAGE", f"{name} now HAS a live screenshot -- delete its NO_LIVE_SHOT entry")

    missing = sorted(set(apps) - live_names - set(NO_LIVE_SHOT))
    extra = sorted(live_names - set(apps))
    if missing:
        fail("COVERAGE", f"process-backed apps with no LIVE screenshot: {', '.join(missing)}")
    if extra:
        fail("COVERAGE", f"LIVE screenshots with no matching app: {', '.join(extra)}")
    if not missing and not extra and apps:
        ok("COVERAGE", f"{len(apps)} process-backed apps, {len(live)} live shots, 1:1")

    # ── REFERENCED ──────────────────────────────────────────────────────────
    # A committed FIXTURE that no document shows a reader is not documentation,
    # it is weight. This is an ERROR rather than a warning, and rather than
    # nothing, for one reason: the failure mode is silent ACCUMULATION. Nothing
    # here was ever wrong on the day it landed -- 24 unreferenced images (~14 MB)
    # arrived one plausible run at a time over months, and were found only when
    # somebody went looking. A warning is exactly what a build that already
    # prints hundreds of lines swallows; the repo's own history is a list of
    # green checks that were examining nothing.
    #
    # It is not the strictest rule available either. The strict version -- error,
    # no escape -- would force a legitimately-staged figure to be deleted merely
    # because its page is still being written, which is a worse outcome than
    # keeping it. So an exception costs exactly one line, and that line must say
    # WHY. It also goes stale automatically in both directions: writing the doc
    # (the file becomes referenced) and deleting the file both make the entry
    # fail, so an exception cannot outlive its cause.
    #
    # LIVE shots are deliberately NOT covered. A fixture is a FIGURE: its whole
    # purpose is to be embedded, so an unembedded one has no purpose. A live shot
    # is EVIDENCE that an app runs -- README.md and PROVENANCE.md link the
    # directory as a set, and the set is the artefact. Requiring each of the 15
    # to be embedded somewhere would push the docs to embed images to satisfy a
    # linter, which is how a gate starts generating the thing it measures.
    UNREFERENCED_OK: dict[str, str] = {}

    refs, examined = referenced_shots()

    # Anti-vacuity, both directions. A scan that matched nothing would report
    # every shot dead, which is loud; a scan that matched too eagerly would
    # report every shot alive, which is silent -- and silent is the failure this
    # whole file exists to prevent. So: enough references must be found to be
    # credible, AND a path that appears in no document must not be claimed as
    # referenced.
    if examined < 20:
        fail(
            "REFERENCED",
            f"only {examined} image reference(s) found across the repo — the scan is "
            "broken, and would pass by examining nothing",
        )
    # Assembled from pieces rather than written out, so the literal never appears
    # in any tracked file. The first version was spelled in full here, and this
    # script is itself tracked and scanned -- so the canary matched ITSELF and
    # the check failed on its own text. That was the scan proving it reads code
    # comments as references, which is exactly what it claims to do.
    canary = "docs/screenshots/" + "__no-document" + "-names-this__" + ".png"
    if canary in refs:
        fail("REFERENCED", "the reference scan matches paths nothing mentions — it is too permissive")

    for name, reason in UNREFERENCED_OK.items():
        path = f"docs/screenshots/{name}"
        if len(reason) < 60:
            fail("REFERENCED", f"{name} is kept unreferenced with no real reason given")
        if path not in fixture:
            fail("REFERENCED", f"{name} is declared unreferenced-but-kept, but is not shipped — delete its entry")
        elif path in refs:
            fail(
                "REFERENCED",
                f"{name} IS referenced now ({', '.join(sorted(set(refs[path])))}) — "
                "delete its UNREFERENCED_OK entry",
            )

    orphans = sorted(
        os.path.basename(p) for p in fixture
        if p not in refs and os.path.basename(p) not in UNREFERENCED_OK
    )
    if orphans:
        fail(
            "REFERENCED",
            f"{len(orphans)} fixture screenshot(s) that no file references: "
            f"{', '.join(orphans)} — embed them, delete them, or declare them in "
            "UNREFERENCED_OK with a reason",
        )
    else:
        ok(
            "REFERENCED",
            f"all {len(fixture)} fixtures are embedded or cited "
            f"({examined} image references scanned, {len(UNREFERENCED_OK)} declared exceptions)",
        )

    # ── CLASSIFIED ──────────────────────────────────────────────────────────
    if not os.path.isfile(MANIFEST):
        fail("CLASSIFIED", f"{MANIFEST} is missing")
    else:
        want = render_manifest(fixture, live)
        with open(MANIFEST) as f:
            got = f.read()
        if got != want:
            fail(
                "CLASSIFIED",
                "PROVENANCE.md does not match the shots on disk — "
                "run `python3 scripts/check-screenshot-provenance.py --write`",
            )
        else:
            ok("CLASSIFIED", f"{len(fixture)} fixture + {len(live)} live, all declared")

    # ── LIVE-IS-LIVE ────────────────────────────────────────────────────────
    before = len(failures)
    for spec, proofs in sorted(LIVE_SPECS.items()):
        name = os.path.basename(spec)
        if not os.path.isfile(spec):
            fail("LIVE-IS-LIVE", f"{spec} is missing, but LIVE shots are shipped")
            continue
        with open(spec) as f:
            src = f.read()
        # An IMPORT of the mock, as opposed to the header comment that explains
        # why there isn't one.
        if re.search(r"^\s*import\b[^\n]*mock-backend", src, re.M):
            fail("LIVE-IS-LIVE", f"{name} IMPORTS mock-backend — its shots are not live")
        if "page.route(" in src:
            fail("LIVE-IS-LIVE", f"{name} installs page.route — its shots are not live")
        for anchor, why in proofs:
            if anchor not in src:
                fail("LIVE-IS-LIVE", f"{name} {why} (lost anchor: {anchor!r})")
    if len(failures) == before:
        ok(
            "LIVE-IS-LIVE",
            f"{len(LIVE_SPECS)} live-shot specs: no mock import, no page.route, proofs present",
        )

    # ── SPEC-COVERAGE ───────────────────────────────────────────────────────
    # LIVE-IS-LIVE can only check the specs it is told about, so being told
    # about all of them is itself checked. Without this, adding
    # shots-live-<something>.e2e.ts and shipping its PNGs would pass every check
    # above while nothing verified that spec was live at all -- exactly the
    # "collection is not execution" gap this file was written against.
    e2e_dir = os.path.join(ROOT, "frontend", "e2e")
    on_disk = {
        os.path.join(e2e_dir, f)
        for f in os.listdir(e2e_dir)
        if f.startswith("shots-live-") and f.endswith(".e2e.ts")
    }
    unwatched = sorted(os.path.basename(p) for p in on_disk - set(LIVE_SPECS))
    if unwatched:
        fail(
            "SPEC-COVERAGE",
            "live-shot spec(s) not listed in LIVE_SPECS, so nothing checks they are live: "
            + ", ".join(unwatched),
        )
    else:
        ok("SPEC-COVERAGE", "every shots-live-*.e2e.ts under frontend/e2e/ is checked")

    # ── FIXTURE-IS-FIXTURE ──────────────────────────────────────────────────
    if not os.path.isfile(FIXTURE_GENERATOR):
        fail("FIXTURE-IS-FIXTURE", f"{FIXTURE_GENERATOR} is missing, but FIXTURE shots are shipped")
    else:
        with open(FIXTURE_GENERATOR) as f:
            src = f.read()
        # Both the import and the call. Checking for the bare token survived a
        # mutation that dropped `installBackend` from the import line, because
        # the name still appeared at its call site.
        imports_mock = re.search(r"^\s*import\b[^\n]*\binstallBackend\b[^\n]*mock-backend", src, re.M)
        calls_mock = re.search(r"\binstallBackend\s*\(", src)
        if not imports_mock or not calls_mock:
            missing_part = "imports" if not imports_mock else "calls"
            fail(
                "FIXTURE-IS-FIXTURE",
                f"screenshots.mjs no longer {missing_part} installBackend — docs/screenshots/ "
                "may no longer be fixture-backed, so the label is wrong",
            )
        else:
            ok("FIXTURE-IS-FIXTURE", "screenshots.mjs imports and calls installBackend, as declared")

    print()
    if failures:
        print(f"{RED}FAILED{RESET} — {len(failures)} problem(s)")
        return 1
    print(f"{GREEN}OK{RESET} — every shipped screenshot declares how it was made, and the labels match the code")
    return 0


if __name__ == "__main__":
    sys.exit(main())
