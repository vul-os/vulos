#!/usr/bin/env python3
"""check-apps-run.py — prove every bundled app actually starts and serves.

WHY THIS EXISTS
---------------
v0.2.0 shipped with `/opt/vulos/apps/` EMPTY: build.sh copied from
`$ROOT_DIR/apps/` after the web tier moved to `frontend/apps/` (cea77898), so
the glob matched nothing and the loop body never ran. Every process-backed app
was absent from the image, `appStore.GetManifest()` 404'd, no namespace was
ever registered, and the gateway answered `{"error":"app not running"}`
(gateway.go). The product's screenshots were green because they were captured
against a mocked backend (`frontend/scripts/screenshots.mjs` installs
`e2e/mock-backend.js`, whose `POST /api/apps/launch` answers `{ok:true}` and
starts nothing), so no app had ever actually been launched.

Screenshots of the apps ACTUALLY RUNNING now exist alongside this check:
`frontend/e2e/shots-live-apps.e2e.ts` -> `docs/screenshots/live-apps/`, split
from the fixture-backed shots and enforced by
`scripts/check-screenshot-provenance.py`. This script remains the load-bearing
proof; those images are its visible counterpart.

Two independent things are checked here, because either alone gives a false
green:

  RUN   — every app with a `command` in app.json is really started as a
          subprocess, really bound to a port, and really answered a GET / with
          a 200 and a non-empty body. Nothing is mocked or stubbed.

  SHIP  — the paths build.sh copies bundled apps FROM actually exist and
          contain those same apps. An app that runs perfectly in the repo but
          is not copied into the rootfs can never run on a real box, which is
          precisely the defect that shipped.

Both are coverage-asserted: the check fails if it examined FEWER apps than the
repo contains, so deleting or renaming an app directory can never make this
script quietly pass by finding nothing to test.

Usage:
    python3 scripts/check-apps-run.py            # run every check
    python3 scripts/check-apps-run.py --only foo # single app (debugging)
    python3 scripts/check-apps-run.py --json     # machine-readable summary
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
APPS_DIR = os.path.join(ROOT, "frontend", "apps")
BUILD_SH = os.path.join(ROOT, "build.sh")

# The host is frequently under heavy build load; a slow start is not a defect.
# Boot budget is generous and polled tightly so a fast start still finishes
# fast. Overridable for even slower machines.
BOOT_TIMEOUT = float(os.environ.get("VULOS_APP_BOOT_TIMEOUT", "40"))
POLL_INTERVAL = 0.05
REQUEST_TIMEOUT = 10.0

GREEN, RED, YELLOW, DIM, RESET = (
    ("\033[32m", "\033[31m", "\033[33m", "\033[2m", "\033[0m")
    if sys.stdout.isatty()
    else ("", "", "", "", "")
)


def free_port() -> int:
    """Reserve an ephemeral port, then release it for the child to bind."""
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def discover_apps() -> list[dict]:
    """Every directory under frontend/apps/ carrying an app.json."""
    apps = []
    if not os.path.isdir(APPS_DIR):
        return apps
    for name in sorted(os.listdir(APPS_DIR)):
        d = os.path.join(APPS_DIR, name)
        manifest_path = os.path.join(d, "app.json")
        if not os.path.isfile(manifest_path):
            continue
        with open(manifest_path) as f:
            manifest = json.load(f)
        apps.append({"id": name, "dir": d, "manifest": manifest})
    return apps


def http_get(url: str):
    """GET a URL, treating an HTTP error response as an ANSWER, not an outage.

    urllib raises HTTPError (a subclass of URLError) for 4xx/5xx. Letting that
    fall into the connect-retry loop would make an app that is up and serving
    500s look like an app that is still booting, and burn the whole boot budget
    before failing — measured at 40s during mutation testing. A status line
    means the server bound and replied, so return it immediately.
    """
    req = urllib.request.Request(url, headers={"User-Agent": "vulos-check-apps-run"})
    try:
        with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def start_and_serve(app: dict) -> dict:
    """Start one app for real, GET /, and report what actually happened.

    Nothing here is mocked: a real subprocess is spawned with the manifest's
    own `command`, and a real HTTP request crosses a real socket.
    """
    app_id = app["id"]
    command = app["manifest"].get("command")
    result = {
        "id": app_id,
        "command": command,
        "ok": False,
        "status": None,
        "bytes": 0,
        "detail": "",
        "elapsed": 0.0,
    }

    port = free_port()
    # Isolate every filesystem side effect: these servers create ~/.vulos/...
    # at import time. A throwaway HOME keeps the check from writing into the
    # developer's real home directory.
    home = tempfile.mkdtemp(prefix=f"vulos-appcheck-{app_id}-")
    env = dict(os.environ)
    env.update(
        {
            "HOME": home,
            "PORT": str(port),
            "VULOS_PORT": str(port),
            # Apps that talk back to the box get a definitely-dead address
            # rather than a real one, so a passing result can never depend on
            # a live backend being up.
            "VULOS_API": "http://127.0.0.1:1",
            "VULOS_APP_SECRET": "check-apps-run",
        }
    )

    argv = command.split()
    if argv and argv[0] == "python3":
        # Use the interpreter running this check, so the result is attributable
        # to a known python rather than whatever `python3` resolves to.
        argv[0] = sys.executable

    log_path = os.path.join(home, "app.log")
    started = time.monotonic()
    try:
        with open(log_path, "wb") as logf:
            proc = subprocess.Popen(
                argv,
                cwd=app["dir"],
                env=env,
                stdout=logf,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
    except OSError as e:
        result["detail"] = f"spawn failed: {e}"
        shutil.rmtree(home, ignore_errors=True)
        return result

    try:
        deadline = started + BOOT_TIMEOUT
        last_err = "never became reachable"
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                with open(log_path, "rb") as f:
                    tail = f.read()[-600:].decode("utf-8", "replace").strip()
                result["detail"] = (
                    f"process exited early rc={proc.returncode}: {tail or '(no output)'}"
                )
                return result
            try:
                status, body = http_get(f"http://127.0.0.1:{port}/")
                result["status"] = status
                result["bytes"] = len(body)
                result["elapsed"] = round(time.monotonic() - started, 2)
                if status != 200:
                    result["detail"] = f"GET / returned {status}, want 200"
                elif not body.strip():
                    result["detail"] = "GET / returned 200 with an EMPTY body"
                elif b"<" not in body[:2048]:
                    result["detail"] = "GET / body is not markup"
                else:
                    result["ok"] = True
                    result["detail"] = f"200, {len(body)} bytes of markup"
                return result
            except (urllib.error.URLError, ConnectionError, socket.timeout, OSError) as e:
                last_err = str(e)
                time.sleep(POLL_INTERVAL)

        result["elapsed"] = round(time.monotonic() - started, 2)
        result["detail"] = f"timed out after {BOOT_TIMEOUT}s ({last_err})"
        return result
    finally:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
                proc.wait(timeout=5)
        except (ProcessLookupError, PermissionError):
            pass
        shutil.rmtree(home, ignore_errors=True)


SHIP_BLOCK_START = 'rm -rf "$OUTDIR/apps"'
SHIP_BLOCK_END = "${app_count} bundled apps"


def check_ship_paths(app_ids: list[str]) -> list[str]:
    """Assert build.sh really does place every app where the image expects it.

    This is the check that would have caught v0.2.0, and it works by RUNNING
    build.sh's own bundled-app copy block against a throwaway $OUTDIR rather
    than pattern-matching the path out of it. Reading the path is not enough:
    a first attempt at the fix pointed at the right directory and still
    flattened all 16 apps into one, because the glob yields a trailing slash
    and BSD `cp -r src/ dst/` copies contents rather than the directory. Only
    executing the block and looking at what landed catches that class of bug.
    """
    failures: list[str] = []
    if not os.path.isfile(BUILD_SH):
        return [f"build.sh not found at {BUILD_SH}"]

    with open(BUILD_SH) as f:
        build = f.read()

    start = build.find(SHIP_BLOCK_START)
    if start == -1:
        return [
            f"could not find the bundled-app copy block in build.sh (expected a "
            f"line containing {SHIP_BLOCK_START!r}) — if the copy mechanism "
            f"changed, update this check to follow it rather than deleting it"
        ]
    end = build.find(SHIP_BLOCK_END, start)
    if end == -1:
        return ["found the start of build.sh's app-copy block but not its end"]
    block = build[start : build.find("\n", end) + 1]

    if "frontend/apps" not in block:
        failures.append(
            "build.sh's app-copy block does not read frontend/apps/ — bundled "
            "apps live there since cea77898"
        )

    outdir = tempfile.mkdtemp(prefix="vulos-shipcheck-")
    try:
        # Run the REAL block, with only the variables it reads bound.
        script = (
            f'set -e\nROOT_DIR={ROOT!r}\nOUTDIR={outdir!r}\n'
            f'GREEN=""\nRED=""\nNC=""\n{block}\n'
        )
        proc = subprocess.run(
            ["bash", "-c", script], capture_output=True, text=True, timeout=180
        )
        if proc.returncode != 0:
            failures.append(
                f"build.sh's app-copy block exited {proc.returncode}: "
                f"{(proc.stderr or proc.stdout).strip()[:400]}"
            )
            return failures

        shipped = os.path.join(outdir, "apps")
        for app_id in app_ids:
            manifest = os.path.join(shipped, app_id, "app.json")
            if not os.path.isfile(manifest):
                failures.append(
                    f"{app_id} did not land at $OUTDIR/apps/{app_id}/app.json — "
                    f"it would be MISSING from /opt/vulos/apps in the image"
                )
        # Every app that has a server.py must still have it after the copy.
        for app_id in app_ids:
            src_server = os.path.join(APPS_DIR, app_id, "server.py")
            if os.path.isfile(src_server) and not os.path.isfile(
                os.path.join(shipped, app_id, "server.py")
            ):
                failures.append(f"{app_id}/server.py was not copied into the image")
    except subprocess.TimeoutExpired:
        failures.append("build.sh's app-copy block timed out")
    finally:
        shutil.rmtree(outdir, ignore_errors=True)

    return failures


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--only", help="check a single app id")
    ap.add_argument("--json", action="store_true", help="emit a JSON summary")
    args = ap.parse_args()

    apps = discover_apps()
    if not apps:
        print(f"{RED}FAIL{RESET} no apps found under {APPS_DIR} — "
              f"the check has nothing to examine, which is itself a failure")
        return 1

    process_apps = [a for a in apps if a["manifest"].get("command")]
    static_apps = [a for a in apps if not a["manifest"].get("command")]

    if args.only:
        process_apps = [a for a in process_apps if a["id"] == args.only]
        if not process_apps:
            print(f"{RED}FAIL{RESET} no process-backed app named {args.only!r}")
            return 1

    print(f"{DIM}apps dir: {APPS_DIR}{RESET}")
    print(f"{DIM}python:   {sys.executable}{RESET}")
    print(
        f"{DIM}found:    {len(apps)} manifests "
        f"({len(process_apps)} process-backed, {len(static_apps)} static){RESET}\n"
    )

    results = [start_and_serve(a) for a in process_apps]
    for r in results:
        mark = f"{GREEN}PASS{RESET}" if r["ok"] else f"{RED}FAIL{RESET}"
        print(f"  {mark}  {r['id']:<16} {r['detail']} {DIM}({r['elapsed']}s){RESET}")

    for a in static_apps:
        print(f"  {DIM}skip  {a['id']:<16} static app (no command in app.json){RESET}")

    ship_failures = [] if args.only else check_ship_paths([a["id"] for a in apps])
    print()
    if args.only:
        print(f"  {DIM}skip  build.sh ships apps: not checked under --only{RESET}")
    elif ship_failures:
        for f in ship_failures:
            print(f"  {RED}FAIL{RESET}  build.sh ships apps: {f}")
    else:
        print(f"  {GREEN}PASS{RESET}  build.sh ships apps: ran build.sh's own "
              f"copy block; all {len(apps)} apps landed with their manifests")

    # COVERAGE ASSERTION — a check that examined nothing must never report
    # success. Guards against an empty/renamed apps dir silently zeroing this
    # script out (the "guard that checks nothing" failure mode).
    coverage_failures = []
    if not args.only:
        expected = int(os.environ.get("VULOS_MIN_PROCESS_APPS", "15"))
        if len(process_apps) < expected:
            coverage_failures.append(
                f"expected at least {expected} process-backed apps, examined "
                f"{len(process_apps)} — apps went missing, or this floor is stale"
            )
        print(
            f"  {GREEN}PASS{RESET}  coverage: examined {len(process_apps)} "
            f"process-backed apps (floor {expected})"
            if not coverage_failures
            else f"  {RED}FAIL{RESET}  coverage: {coverage_failures[0]}"
        )

    failed = [r for r in results if not r["ok"]]
    print()
    if args.json:
        print(json.dumps({"apps": results, "ship": ship_failures}, indent=2))

    if failed or ship_failures or coverage_failures:
        print(
            f"{RED}FAILED{RESET} — {len(failed)}/{len(results)} apps did not serve; "
            f"{len(ship_failures) + len(coverage_failures)} structural failure(s)"
        )
        return 1

    print(f"{GREEN}OK{RESET} — all {len(results)} process-backed apps started and served, "
          f"and build.sh ships them")
    return 0


if __name__ == "__main__":
    sys.exit(main())
