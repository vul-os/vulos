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
against a mocked backend (`e2e/_polish-shots.e2e.ts` installBackend), so no app
had ever actually been launched.

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
    req = urllib.request.Request(url, headers={"User-Agent": "vulos-check-apps-run"})
    with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
        return resp.status, resp.read()


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


def check_ship_paths(app_ids: list[str]) -> list[str]:
    """Assert build.sh copies bundled apps from a path that really has them.

    This is the check that would have caught v0.2.0. It does not care HOW the
    path is written — it extracts whatever source path the copy loop iterates,
    resolves it, and demands the apps actually be there.
    """
    failures: list[str] = []
    if not os.path.isfile(BUILD_SH):
        return [f"build.sh not found at {BUILD_SH}"]

    with open(BUILD_SH) as f:
        build = f.read()

    # The loop that populates $OUTDIR/apps, e.g.
    #   for app in "$ROOT_DIR/frontend/apps/"*/; do
    m = re.search(
        r'for\s+\w+\s+in\s+"?\$\{?ROOT_DIR\}?/([^"*]*)"?\*/\s*;\s*do', build
    )
    if not m:
        return [
            "could not find the bundled-app copy loop in build.sh "
            "(expected `for app in \"$ROOT_DIR/<path>/\"*/; do`) — if the copy "
            "mechanism changed, update this check to follow it"
        ]

    rel = m.group(1).strip("/")
    src = os.path.join(ROOT, rel)
    if not os.path.isdir(src):
        failures.append(
            f"build.sh copies bundled apps from $ROOT_DIR/{rel}/ which DOES NOT "
            f"EXIST — the glob matches nothing, so the image ships ZERO apps "
            f"(this is exactly the v0.2.0 defect)"
        )
        return failures

    present = {
        n for n in os.listdir(src) if os.path.isfile(os.path.join(src, n, "app.json"))
    }
    missing = sorted(set(app_ids) - present)
    if missing:
        failures.append(
            f"build.sh copies from $ROOT_DIR/{rel}/ but these apps are not "
            f"there: {', '.join(missing)}"
        )
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
    if ship_failures:
        for f in ship_failures:
            print(f"  {RED}FAIL{RESET}  build.sh ships apps: {f}")
    else:
        print(f"  {GREEN}PASS{RESET}  build.sh ships apps: "
              f"copy source resolves and contains all {len(apps)} apps")

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
