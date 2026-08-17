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
# PASS while checking nothing, so `--self-test` runs seventeen synthetic
# fixtures through the same checker: TWO controls that MUST go green (one
# single-URL, one per-architecture) and fifteen that MUST go red — one per rule.
# A checker that has never failed is not evidence, and a per-arch control is
# what stops the per-arch reds from passing merely because the checker rejects
# every `artifacts` recipe it sees.

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

        # ── Resolve the artefact set ────────────────────────────────────────
        # A recipe carries EITHER a single download_url/checksum (55 entries) or
        # the per-architecture `artifacts` map {arch: {download_url, checksum}}.
        # Every artefact is checked, not just the first: the whole point of the
        # per-arch form is that an arm64 box gets its own binary, so a checker
        # that hashed one URL and called the recipe verified would leave the
        # arm64 half exactly as unverified as it was before the field existed.
        artifacts = r.get("artifacts") or {}
        binary_name = (r.get("binary_name") or "").strip()

        if artifacts and url:
            bad(tag, "one-artifact-form",
                "recipe sets BOTH download_url and artifacts — the engine refuses this (ARTIFACTS-01)")
            continue
        if artifacts:
            ok(tag, "one-artifact-form", "per-architecture (artifacts)")
            if checksum:
                bad(tag, "no-shared-checksum",
                    "top-level checksum set alongside artifacts — one hash cannot be right "
                    "for two different binaries (ARTIFACTS-01)")
            else:
                ok(tag, "no-shared-checksum")
            # An entry that claims an architecture it publishes no artefact for
            # is offered in the App Hub and then refused at install time.
            declared = set(arch) if isinstance(arch, list) else set()
            offered = set(artifacts)
            # ARTIFACTS-02: `any` is the exclusive key for a payload that carries
            # no machine code (a static bundle, a WAR, a source archive). It
            # covers every declared architecture by definition. The engine
            # refuses it alongside a real architecture key, so it can never act
            # as a fallback — which is why accepting it here does not weaken the
            # per-arch rules below by one inch.
            arch_independent = offered == {"any"}
            if arch_independent:
                ok(tag, "arch-coverage", "any (architecture-independent payload)")
            elif "any" in offered:
                bad(tag, "arch-coverage",
                    f"artifacts mixes 'any' with per-architecture keys {sorted(offered)} — "
                    "'any' is exclusive; the engine refuses this (ARTIFACTS-02)")
            elif declared and declared != offered:
                bad(tag, "arch-coverage",
                    f"entry declares arch {sorted(declared)} but artifacts cover {sorted(offered)} — "
                    "every declared architecture needs its own artefact")
            else:
                ok(tag, "arch-coverage", ",".join(sorted(offered)))
            artefact_list = [(a, (artifacts[a] or {}).get("download_url", "").strip(),
                              (artifacts[a] or {}).get("checksum", "").strip().lower())
                             for a in sorted(artifacts)]
        else:
            ok(tag, "one-artifact-form", "single download_url")
            arch_independent = False
            artefact_list = [(None, url, checksum)]

        if not any(u for _, u, _ in artefact_list):
            bad(tag, "pinned-artifact", "no download_url — nothing places the binary")
            continue

        # Must match appnet's tarExtensions + zipExtensions exactly. `.war` is
        # dispatched to extractZip because a WAR is a ZIP by specification; when
        # the checker did not know that, it predicted drawio's artefact would
        # land at bin/draw.war — which is what the ENGINE used to do, and the
        # broken install that motivated the fix.
        ARCHIVE_EXT = (".tar.gz", ".tgz", ".tar.bz2", ".tar.xz", ".zip", ".war")
        good_artefacts = []
        for a, u, c in artefact_list:
            atag = tag if a is None else f"{tag}[{a}]"
            if not u:
                bad(atag, "pinned-artifact", "artifact has no download_url")
                continue
            if not u.startswith("https://"):
                bad(atag, "pinned-artifact", f"download_url is not https: {u}")
                continue
            if re.search(r"/(latest|master|main)/", u) or u.endswith("/latest"):
                bad(atag, "pinned-artifact",
                    f"download_url is a moving reference, not a pinned release asset: {u}")
            else:
                ok(atag, "pinned-artifact", u.rsplit("/", 2)[-2] + "/" + u.rsplit("/", 1)[-1])

            if not c:
                bad(atag, "checksum-present",
                    "download_url is set but checksum is empty — the engine refuses this "
                    "outright (SECAUDIT2-H1)")
            elif not re.fullmatch(r"[0-9a-f]{64}", c):
                bad(atag, "checksum-present", f"checksum is not a sha256 hex digest: {c!r}")
            else:
                ok(atag, "checksum-present", c[:12] + "…")
                good_artefacts.append((atag, u, c))

        # Two architectures pinned to the SAME hash means the release published
        # one file under several platform names — kerf v0.1.9 shipped three
        # "per-platform" tarballs with one identical sha256, which is how that
        # entry looked plausible while being unusable.
        digests = {}
        for a, _, c in artefact_list:
            if c:
                digests.setdefault(c, []).append(a)
        dupes = {c: v for c, v in digests.items() if len(v) > 1}
        if artifacts and arch_independent:
            # One artefact, one digest — there is no pair of "different builds"
            # to be suspicious of. The rule below is UNCHANGED for every recipe
            # that really does claim two architectures.
            ok(tag, "distinct-per-arch-digests", "single architecture-independent artefact")
        elif artifacts and dupes:
            bad(tag, "distinct-per-arch-digests",
                f"architectures {sorted(sum(dupes.values(), []))} share one sha256 — "
                "they cannot be different builds")
        elif artifacts:
            ok(tag, "distinct-per-arch-digests")

        # The command must name the path the engine will actually create.
        # staticInstall drops a non-archive download at bin/<binary_name> when
        # one is pinned, else bin/<basename-of-url>; an archive is extracted in
        # place. Getting this wrong is an app that installs cleanly and then
        # cannot start.
        urls = [u for _, u, _ in artefact_list if u]
        is_archive = all(u.lower().split("?", 1)[0].endswith(ARCHIVE_EXT) for u in urls)
        if not is_archive:
            if binary_name:
                expected = "bin/" + binary_name
            else:
                bases = {os.path.basename(u.split("?", 1)[0]) for u in urls}
                if len(bases) > 1:
                    bad(tag, "command-matches-artifact",
                        f"per-architecture artefacts install under different names {sorted(bases)} "
                        "but `command` is one string — set binary_name so the installed path is "
                        "the same on every architecture")
                    expected = None
                else:
                    expected = "bin/" + bases.pop()
            if expected is not None:
                if expected in command:
                    ok(tag, "command-matches-artifact", expected)
                else:
                    bad(tag, "command-matches-artifact",
                        f"staticInstall will place the binary at {expected!r}, but command is {command!r}")
        elif binary_name:
            bad(tag, "command-matches-artifact",
                f"binary_name {binary_name!r} is set on an ARCHIVE recipe, where it does nothing — "
                "the engine refuses this (ARTIFACTS-01)")
        else:
            # EXTRACT-01: an archive unpacks into extract_dir when one is set,
            # and into the app dir otherwise. A command that names neither is
            # pointing at a path the installer will not create.
            extract_dir = (r.get("extract_dir") or "").strip().strip("/")
            if extract_dir:
                if extract_dir + "/" in command or extract_dir + " " in command:
                    ok(tag, "command-matches-artifact", f"archive unpacks into {extract_dir}/")
                else:
                    bad(tag, "command-matches-artifact",
                        f"archive unpacks into {extract_dir!r} (extract_dir) but command "
                        f"{command!r} never names it")
            else:
                ok(tag, "command-matches-artifact", "archive unpacks into the app dir")

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

        if offline:
            continue

        # The assertion that actually costs something: fetch the pinned bytes and
        # hash them. This is what catches an upstream that re-tagged, moved or
        # deleted an asset we already vetted. EVERY architecture is fetched — a
        # per-arch recipe whose arm64 asset had been deleted would otherwise
        # verify green on the strength of its amd64 one.
        for atag, u, c in good_artefacts:
            try:
                proc = subprocess.run(["curl", "-fsSL", "--max-time", "900", u],
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            except Exception as exc:
                bad(atag, "artifact-digest", f"download failed: {exc}")
                continue
            if proc.returncode != 0:
                bad(atag, "artifact-digest",
                    f"download failed (curl exit {proc.returncode}): "
                    f"{proc.stderr.decode('utf-8', 'replace').strip()[:200]}")
                continue
            got = hashlib.sha256(proc.stdout).hexdigest()
            if got == c:
                ok(atag, "artifact-digest", f"{len(proc.stdout)} bytes, sha256 {got[:12]}… matches")
            else:
                bad(atag, "artifact-digest", f"expected {c}, got {got}")

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
  # A SECOND real asset with a DIFFERENT digest, so the per-architecture
  # fixtures pin two genuinely distinct files rather than one file twice.
  local real_url2="https://github.com/vul-os/diwan/releases/download/v0.1.0/checksums.txt"
  local real_sum2="d92c51f169199261e6d3868831dc9de293467ce4ed8912707936dab2b627e3b6"
  # A THIRD asset whose BASENAME differs from the other two. The two above are
  # both called checksums.txt, so a fixture built from them cannot exercise the
  # rule about per-arch artefacts installing under different names — the first
  # attempt at that fixture passed for exactly this reason.
  local real_url3="https://github.com/vul-os/lilmail/releases/download/v1.14.0/SHA256SUMS"
  local real_sum3="dea252444c782375b56cf06307d8993ac48ad255d6406b4b4c46851cc2f0820e"

  python3 - "$tmp" "$real_url" "$real_sum" "$real_url2" "$real_sum2" "$real_url3" "$real_sum3" <<'PY'
import json, sys
tmp, url, good, url2, good2, url3, good3 = sys.argv[1:8]

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

    # ── per-architecture rules (ARTIFACTS-01) ────────────────────────────────
    # A per-arch control: this one MUST go green, or the reds below prove only
    # that the checker rejects every `artifacts` recipe.
    "perarch-control":  lambda e, v: (e.update(arch=["amd64", "arm64"]), v.update(
                            download_url="", checksum="", binary_name="checksums.txt",
                            artifacts={"amd64": {"download_url": url, "checksum": good},
                                       "arm64": {"download_url": url2, "checksum": good2}})),
    # Both forms at once: which one installs is a coin flip to every reader.
    "perarch-both-forms": lambda e, v: (e.update(arch=["amd64", "arm64"]), v.update(
                            binary_name="checksums.txt",
                            artifacts={"amd64": {"download_url": url, "checksum": good},
                                       "arm64": {"download_url": url2, "checksum": good2}})),
    # An arm64 box would be offered the app and refused at install time.
    "perarch-missing-arch": lambda e, v: (e.update(arch=["amd64", "arm64"]), v.update(
                            download_url="", checksum="", binary_name="checksums.txt",
                            artifacts={"amd64": {"download_url": url, "checksum": good}})),
    # An unchecksummed artefact is an unverified binary whichever key it hides under.
    "perarch-no-checksum": lambda e, v: (e.update(arch=["amd64", "arm64"]), v.update(
                            download_url="", checksum="", binary_name="checksums.txt",
                            artifacts={"amd64": {"download_url": url, "checksum": good},
                                       "arm64": {"download_url": url2, "checksum": ""}})),
    # One file wearing two platform names — the kerf v0.1.9 shape.
    "perarch-same-digest": lambda e, v: (e.update(arch=["amd64", "arm64"]), v.update(
                            download_url="", checksum="", binary_name="checksums.txt",
                            artifacts={"amd64": {"download_url": url, "checksum": good},
                                       "arm64": {"download_url": url, "checksum": good}})),
    # Different installed names but one `command` — no command can name both.
    "perarch-no-binary-name": lambda e, v: (e.update(arch=["amd64", "arm64"]), v.update(
                            download_url="", checksum="",
                            artifacts={"amd64": {"download_url": url, "checksum": good},
                                       "arm64": {"download_url": url3, "checksum": good3}})),
    # A top-level checksum cannot be right for two different binaries.
    "perarch-shared-checksum": lambda e, v: (e.update(arch=["amd64", "arm64"]), v.update(
                            download_url="", binary_name="checksums.txt",
                            artifacts={"amd64": {"download_url": url, "checksum": good},
                                       "arm64": {"download_url": url2, "checksum": good2}})),
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
    [[ "$name" == "control" || "$name" == "perarch-control" ]] && want="pass"
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
  if [[ $n -lt 17 ]]; then
    printf '\n  \033[31mself-test ran only %d fixtures (expected 17) — refusing to call that a pass\033[0m\n' "$n"
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
