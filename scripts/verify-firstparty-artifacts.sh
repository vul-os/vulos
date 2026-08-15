#!/usr/bin/env bash
# verify-firstparty-artifacts.sh — prove the staged first-party App Hub entries
# point at real, pinned, unmodified release artefacts.
#
#   scripts/verify-firstparty-artifacts.sh                  # check registry.d/vulos-first-party.json
#   scripts/verify-firstparty-artifacts.sh <file.json> ...  # check specific staging files
#   scripts/verify-firstparty-artifacts.sh --offline        # skip the network re-download
#   scripts/verify-firstparty-artifacts.sh --self-test      # prove this script can go RED
#
# WHAT THIS IS FOR.  registry.d/*.json entries are authored before they are
# signed, so `make verify-registry` cannot see them yet — it only ever looks at
# the signed registry.json.  This is the check that covers the gap: it asserts
# the things a signature does NOT assert, namely that the bytes a recipe points
# at still exist and are still the bytes we vetted.
#
# WHAT IT DOES NOT DO.  It does not sign anything, does not touch registry.json,
# does not weaken or stand in for REGISTRY-SIGN-01, and it is not a substitute
# for scripts/verify-app-recipe.sh (which runs the product's OWN installer in a
# container and can only run once an entry is signed and merged).  Order is:
# this script → merge → `make sign-registry` → `make verify-registry-prod` →
# `scripts/verify-app-recipe.sh <id>`.
#
# WHY IT HAS A SELF-TEST.  This repo's dominant defect is a gate that prints
# PASS while checking nothing, so `--self-test` runs eight synthetic fixtures
# through the same checker: one control that MUST go green and seven that MUST
# go red — one per rule.  A checker that has never failed is not evidence.

set -uo pipefail

SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
REPO_ROOT="$(cd "$(dirname "$SCRIPT_PATH")/.." && pwd)"
DEFAULT_TARGET="$REPO_ROOT/registry.d/vulos-first-party.json"

OFFLINE=0
SELFTEST=0
TARGETS=()

for arg in "$@"; do
  case "$arg" in
    --offline)   OFFLINE=1 ;;
    --self-test) SELFTEST=1 ;;
    -h|--help)   sed -n '2,28p' "$SCRIPT_PATH" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)          echo "unknown flag: $arg" >&2; exit 2 ;;
    *)           TARGETS+=("$arg") ;;
  esac
done

command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 2; }
if [[ $OFFLINE -eq 0 ]]; then
  command -v curl >/dev/null 2>&1 || { echo "curl is required (or pass --offline)" >&2; exit 2; }
fi

# ─── the checker ─────────────────────────────────────────────────────────────
# Reads one staging file, applies every rule to every version of every app, and
# prints one line per assertion.  Exits 1 if any assertion failed OR if it
# checked zero entries — "all 0 entries passed" is the exact shape of a hollow
# gate, so it is a failure here, not a pass.
check_file() {
  local path="$1" offline="$2"
  python3 - "$path" "$offline" <<'PY'
import hashlib, json, os, re, subprocess, sys

path, offline = sys.argv[1], sys.argv[2] == "1"
fails = 0
checked = 0

def ok(app, rule, detail=""):
    print(f"  \033[32mOK\033[0m   {app:<12} {rule}" + (f" — {detail}" if detail else ""))

def bad(app, rule, detail):
    global fails
    fails += 1
    print(f"  \033[31mFAIL\033[0m {app:<12} {rule} — {detail}")

try:
    with open(path) as fh:
        doc = json.load(fh)
except Exception as exc:
    print(f"  \033[31mFAIL\033[0m {'(file)':<12} parse — {exc}")
    sys.exit(1)

apps = doc.get("apps")
if not isinstance(apps, dict):
    print(f"  \033[31mFAIL\033[0m {'(file)':<12} shape — no top-level \"apps\" object")
    sys.exit(1)

# Pipe-to-shell, mirroring appnet.rejectPipeToShell. A recipe that trips this is
# refused by the engine at install time; catching it here is only earlier.
PIPE_TO_SHELL = re.compile(r"(?i)\b(curl|wget)\b[^|]*\|\s*(ba)?sh\b")
SH_C_PIPE = re.compile(r"(?i)^\s*(ba)?sh\s+-c\s+['\"]?[^'\"]*\|")

# The swallow. `|| true`, `2>/dev/null || (…)` and friends turn a failed install
# into a silent success — the exact pattern the kerf entry used to write a
# placeholder page and report OK. Any of these in an install/post_install is a
# hard failure, not a style note.
SWALLOW = re.compile(r"\|\|\s*(true|:|\()")

for app_id in sorted(apps):
    entry = apps[app_id]
    versions = entry.get("versions") or {}

    arch = entry.get("arch")
    if isinstance(arch, list) and arch and all(isinstance(a, str) and a for a in arch):
        ok(app_id, "arch-explicit", ",".join(arch))
    else:
        bad(app_id, "arch-explicit",
            f"entry-level arch must be a non-empty list of Debian arch names, got {arch!r} "
            "(APP-RECIPE-STANDARD section 3: null/omitted is a defect, not \"all\")")

    # Staging files are pre-signature by construction. A non-empty signature here
    # would mean someone hand-wrote one, which REGISTRY-SIGN-01 forbids.
    sig = entry.get("signature", "")
    if sig == "":
        ok(app_id, "unsigned-in-staging")
    else:
        bad(app_id, "unsigned-in-staging",
            "signature is non-empty in a staging file — signatures are minted by "
            "`make sign-registry`, never hand-written")

    if not versions:
        bad(app_id, "has-versions", "entry declares no versions")

    for ver in sorted(versions):
        checked += 1
        r = versions[ver] or {}
        tag = f"{app_id}@{ver}"
        url = (r.get("download_url") or "").strip()
        checksum = (r.get("checksum") or "").strip().lower()
        install = r.get("install") or ""
        post = r.get("post_install") or ""
        command = r.get("command") or ""

        for field, value in (("install", install), ("post_install", post)):
            if PIPE_TO_SHELL.search(value) or SH_C_PIPE.search(value):
                bad(tag, f"no-pipe-to-shell({field})", "contains a pipe-to-shell pattern")
            elif SWALLOW.search(value):
                bad(tag, f"no-swallowed-failure({field})",
                    "contains `|| true` / `|| :` / `|| (…)` — a failure that reports success")
            else:
                ok(tag, f"fails-loudly({field})")

        if not url:
            bad(tag, "pinned-artifact", "no download_url — nothing places the binary")
            continue
        if not url.startswith("https://"):
            bad(tag, "pinned-artifact", f"download_url is not https: {url}")
            continue
        if re.search(r"/(latest|master|main)/", url) or url.endswith("/latest"):
            bad(tag, "pinned-artifact",
                f"download_url is a moving reference, not a pinned release asset: {url}")
        else:
            ok(tag, "pinned-artifact", url.rsplit("/", 2)[-2] + "/" + url.rsplit("/", 1)[-1])

        if not checksum:
            bad(tag, "checksum-present",
                "download_url is set but checksum is empty — the engine refuses this "
                "outright (SECAUDIT2-H1)")
        elif not re.fullmatch(r"[0-9a-f]{64}", checksum):
            bad(tag, "checksum-present", f"checksum is not a sha256 hex digest: {checksum!r}")
        else:
            ok(tag, "checksum-present", checksum[:12] + "…")

        # The command must name the path the engine will actually create.
        # staticInstall drops a non-archive download at bin/<basename-of-url>;
        # an archive is extracted in place. Getting this wrong is an app that
        # installs cleanly and then cannot start.
        is_archive = url.lower().endswith((".tar.gz", ".tgz", ".tar.bz2", ".tar.xz"))
        if not is_archive:
            expected = "bin/" + os.path.basename(url.split("?", 1)[0])
            if expected in command:
                ok(tag, "command-matches-artifact", expected)
            else:
                bad(tag, "command-matches-artifact",
                    f"staticInstall will place the binary at {expected!r}, but command is {command!r}")

        # Port reachability. appPort is taken verbatim from this recipe's `port`
        # (main.go:1853) and ${PORT} is substituted ONLY into `command`, at launch
        # (launcher.go:248) — never during post_install. So a recipe that bakes the
        # port into a config file at post_install time must bake the SAME number
        # this recipe declares, or the app listens where nothing is proxied.
        port = r.get("port") or 0
        if port:
            if "${PORT}" in command:
                ok(tag, "port-wired", f"command takes ${{PORT}} → {port}")
            elif str(port) in post:
                ok(tag, "port-wired", f"post_install writes the literal {port}, matching `port`")
            else:
                bad(tag, "port-wired",
                    f"recipe declares port {port}, but command has no ${{PORT}} and post_install "
                    f"never writes {port} — nothing tells the app which port to listen on")
            if "${PORT}" in post:
                bad(tag, "no-PORT-in-post-install",
                    "post_install references ${PORT}, which is NOT expanded there "
                    "(launcher.go:248 substitutes it into `command` only) — it will be empty")
            else:
                ok(tag, "no-PORT-in-post-install")

        if offline or not checksum or not re.fullmatch(r"[0-9a-f]{64}", checksum):
            continue

        # The assertion that actually costs something: fetch the pinned bytes and
        # hash them. This is what catches an upstream that re-tagged, moved or
        # deleted an asset we already vetted.
        try:
            proc = subprocess.run(["curl", "-fsSL", "--max-time", "900", url],
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        except Exception as exc:
            bad(tag, "artifact-digest", f"download failed: {exc}")
            continue
        if proc.returncode != 0:
            bad(tag, "artifact-digest",
                f"download failed (curl exit {proc.returncode}): "
                f"{proc.stderr.decode('utf-8', 'replace').strip()[:200]}")
            continue
        got = hashlib.sha256(proc.stdout).hexdigest()
        if got == checksum:
            ok(tag, "artifact-digest", f"{len(proc.stdout)} bytes, sha256 {got[:12]}… matches")
        else:
            bad(tag, "artifact-digest", f"expected {checksum}, got {got}")

# COVERAGE ASSERTION. "Everything passed" means nothing unless something ran.
if checked == 0:
    print(f"  \033[31mFAIL\033[0m {'(file)':<12} coverage — zero recipes checked")
    sys.exit(1)
print(f"  checked {checked} recipe(s) across {len(apps)} app(s)")
sys.exit(1 if fails else 0)
PY
}

# ─── self-test ───────────────────────────────────────────────────────────────
self_test() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # A real, tiny (423-byte), pinned asset. Used so the digest rule is exercised
  # against the network for real rather than mocked.
  local real_url="https://github.com/vul-os/wede/releases/download/v0.1.3/checksums.txt"
  local real_sum="a99e54e6515698c640012ada9c07466bc811c971feb1ca436c7925ede0e16923"

  python3 - "$tmp" "$real_url" "$real_sum" <<'PY'
import json, sys
tmp, url, good = sys.argv[1], sys.argv[2], sys.argv[3]

def doc(mutate=None):
    d = {"apps": {"fixture": {
        "name": "Fixture", "arch": ["amd64"], "signature": "",
        "versions": {"1.0": {
            "download_url": url, "checksum": good,
            "command": "bin/checksums.txt -port ${PORT}",
            "install": "", "post_install": "mkdir -p data", "port": 8080,
        }},
    }}}
    if mutate:
        mutate(d["apps"]["fixture"], d["apps"]["fixture"]["versions"]["1.0"])
    return d

cases = {
    # The control. If this ever goes red the other seven prove nothing.
    "control":          None,
    "bad-digest":       lambda e, v: v.update(checksum="0" * 64),
    "no-checksum":      lambda e, v: v.update(checksum=""),
    "no-download-url":  lambda e, v: v.update(download_url=""),
    "moving-url":       lambda e, v: v.update(
                            download_url="https://github.com/vul-os/wede/releases/latest/download/x"),
    "swallowed-failure":lambda e, v: v.update(post_install="mkdir -p static || (echo stub > index.html)"),
    "port-in-post":     lambda e, v: v.update(post_install="printf 'port=%s' \"${PORT}\" > c.conf"),
    "hand-signature":   lambda e, v: e.update(signature="ZmFrZQ=="),
    "null-arch":        lambda e, v: e.update(arch=None),
    "wrong-command":    lambda e, v: v.update(command="/root/.local/bin/wede -port ${PORT}"),
}
for name, mut in cases.items():
    with open(f"{tmp}/{name}.json", "w") as fh:
        json.dump(doc(mut), fh, indent=2)
print(" ".join(cases))
PY

  local names
  names="$(python3 -c "
import json,sys
print(' '.join(sorted(f[:-5] for f in __import__('os').listdir('$tmp') if f.endswith('.json'))))")"

  local overall=0 n=0
  for name in $names; do
    local want="fail"
    [[ "$name" == "control" ]] && want="pass"
    printf '\n  ── self-test fixture: %s (must %s)\n' "$name" "$want"
    if check_file "$tmp/$name.json" "$OFFLINE" >/dev/null 2>&1; then
      local got="pass"
    else
      local got="fail"
    fi
    n=$((n + 1))
    if [[ "$got" == "$want" ]]; then
      printf '     \033[32mgot %s — as required\033[0m\n' "$got"
    else
      printf '     \033[31mgot %s, wanted %s — THE CHECKER IS BROKEN\033[0m\n' "$got" "$want"
      overall=1
    fi
  done

  # Guard the guard: if the fixture set ever shrinks to nothing, a clean run
  # would look like a passing self-test.
  if [[ $n -lt 10 ]]; then
    printf '\n  \033[31mself-test ran only %d fixtures (expected 10) — refusing to call that a pass\033[0m\n' "$n"
    overall=1
  fi

  if [[ $overall -eq 0 ]]; then
    printf '\n\033[32m✓ self-test: %d fixtures, the control went green and every other rule went red\033[0m\n' "$n"
  else
    printf '\n\033[31m✗ self-test FAILED\033[0m\n'
  fi
  return $overall
}

# ─── main ────────────────────────────────────────────────────────────────────
if [[ $SELFTEST -eq 1 ]]; then
  self_test
  exit $?
fi

if [[ ${#TARGETS[@]} -eq 0 ]]; then
  TARGETS=("$DEFAULT_TARGET")
fi

rc=0
for target in "${TARGETS[@]}"; do
  if [[ ! -f "$target" ]]; then
    echo "no such staging file: $target" >&2
    exit 2
  fi
  printf '\n▸ %s\n' "${target#"$REPO_ROOT"/}"
  if ! check_file "$target" "$OFFLINE"; then
    rc=1
  fi
done

if [[ $rc -eq 0 ]]; then
  printf '\n\033[32m✓ every staged first-party entry points at a pinned artefact whose bytes still match\033[0m\n'
  printf '  Next: merge into registry.json → make sign-registry RELEASE_PRIV=<key> → make verify-registry-prod\n'
else
  printf '\n\033[31m✗ staged entries did NOT verify — fix the entry, never the check\033[0m\n'
fi
exit $rc
