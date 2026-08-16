#!/usr/bin/env bash
# verify-app-install-perarch.sh — install ONE registry entry through the
# PRODUCT'S OWN installer, on a NAMED architecture, and prove the app serves.
#
# ─────────────────────────────────────────────────────────────────────────────
# WHY THIS EXISTS ALONGSIDE verify-app-recipe.sh
#
# scripts/verify-app-recipe.sh is the catalogue sweep: it runs one app at a
# time, on the HOST's architecture, against the repo's REAL trust anchor, and
# proves the recipe's command is a loadable executable.  That is the right tool
# for the 55 third-party entries, and it cannot test the three first-party ones:
#
#   1. It only ever runs on the machine's own arch.  A per-architecture recipe
#      (`artifacts: {amd64: …, arm64: …}`) has TWO answers and it can only ask
#      one of them.  An arm64-only pass tells an amd64 owner nothing.
#   2. It verifies against keys/trust-anchor.pub, so an entry carrying
#      `signature: ""` — which diwan, wede and lilmail deliberately do, because
#      the founder runs the signing ceremony — can never be install-tested at
#      all.  Waiting for the ceremony to find out whether the recipe works is
#      the wrong order.
#   3. `run_probe` proves the binary execve's.  lilmail execve's happily while
#      serving nothing, and it served DEGRADED (no `cache/`, so no scheduled
#      send) in a way only an HTTP request plus a log read could see.
#
# This script closes those three gaps, and NOTHING ELSE about the installer is
# re-implemented.  The container runs a ~200-line driver whose install step is
# exactly:
#
#     appnet.LoadRegistry(path) → appnet.InstallFromRegistry(ctx, reg, id, ver, dir)
#
# the same function POST /api/store/install reaches through
# AppStore.InstallFromRegistry (store.go).  So the publisher-signature check,
# the ARCH-01 gate, validateRecipeSecurity, ResolveArtifact, the checksum
# verification, extractZip, the manifest write and POSTINSTALL-01 are all the
# shipped ones.  The driver is generated into a scratch module and is never
# written into the repo.
#
# ─────────────────────────────────────────────────────────────────────────────
# SIGNATURES ARE NOT WEAKENED — they are moved to an ephemeral root
#
#   VULOS_ENV is UNSET (services/env treats that as prod).
#   VULOS_REGISTRY_INSECURE is NEVER set.
#   VULOS_SIGN_ALLOW_KEY_MISMATCH is NEVER set.
#
# Instead, the container generates a fresh Ed25519 ROOT key and RELEASE key,
# issues a real release cert with signing.IssueReleaseCert, signs the ONE entry
# under test with appnet.SignEntry, and points VULOS_TRUST_ANCHOR /
# VULOS_RELEASE_CERT at that ephemeral material.  Verification runs at full
# strength against a root that exists for ninety seconds and dies with the
# container.  The repo's keys are never read, registry.json is never written,
# and the shipped entry keeps `signature: ""`.
#
# What this therefore proves: THE RECIPE WORKS.  It deliberately proves nothing
# about who vetted the entry — that is what the founder's ceremony is for, and
# conflating the two is how a signature stops meaning anything.
#
# ─────────────────────────────────────────────────────────────────────────────
# USAGE
#
#   scripts/verify-app-install-perarch.sh <app-id> [--arch amd64|arm64|both]
#   scripts/verify-app-install-perarch.sh <app-id> --arch arm64 --control cachedir
#   scripts/verify-app-install-perarch.sh <app-id> --self-test [--arch A]
#   scripts/verify-app-install-perarch.sh --print-remote-command <app-id>
#
#   --arch both        runs the native arch first, then the emulated one.
#   --control cachedir installs a variant whose post_install does NOT mkdir
#                      cache/, to establish whether anything else creates it.
#                      Expected verdict is DEGRADED; a clean run FAILS the
#                      control, because that would mean the mkdir is decoration.
#   --self-test        three runs on one arch: the real entry (must PASS), the
#                      entry with a corrupted artifact checksum (must FAIL), and
#                      the entry left UNSIGNED (must FAIL).  A checker that has
#                      never gone red is not evidence.
#   --keep             leave the container's /out directory in place.
#
# EXIT CODES
#   0 PASS   1 FAIL (assertion or install)   2 ERROR (harness/infrastructure)
#
# EMULATION.  This Mac is arm64: linux/arm64 runs natively, linux/amd64 runs
# under qemu-user through Docker Desktop/OrbStack and is SLOW (the app's own
# startup included).  Which one ran native is printed in the result banner and
# recorded in the roadmap note.  An amd64 run that cannot finish here is
# reported as NOT VERIFIED with the command to run on a Linux/amd64 host —
# never inferred from the arm64 result.
#
# Agent-generated, machine-verified, NOT human-reviewed.
set -uo pipefail

SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

# ─── in-container phase dispatches before anything host-side is touched ──────
IN_CONTAINER=""
if [[ "${1:-}" == "--in-container" ]]; then IN_CONTAINER=1; shift; fi

REPO_ROOT="${VULOS_REPO_ROOT:-$(cd "$(dirname "$SCRIPT_PATH")/.." && pwd)}"
WORKDIR="${VULOS_PERARCH_WORKDIR:-${TMPDIR:-/tmp}/vulos-perarch}"
WORKDIR="$(printf '%s' "$WORKDIR" | sed 's|//*|/|g; s|/$||')"

c_red=$'\033[31m'; c_grn=$'\033[32m'; c_yel=$'\033[33m'; c_dim=$'\033[2m'; c_off=$'\033[0m'
say()  { printf '%s\n' "$*"; }
step() { printf '\n▸ %s\n' "$*"; }
die()  { printf '%sERROR:%s %s\n' "$c_red" "$c_off" "$*" >&2; exit 2; }

# ═════════════════════════════════════════════════════════════════════════════
# IN-CONTAINER
# ═════════════════════════════════════════════════════════════════════════════

ASSERT_FAILED=0
record() { # record OK|FAIL|INFO <name> <detail>
  printf '%s\t%s\t%s\n' "$1" "$2" "${3//$'\t'/ }" >> /out/assertions.tsv
  case "$1" in
    OK)   printf '  %s✓%s %-26s %s%s%s\n' "$c_grn" "$c_off" "$2" "$c_dim" "$3" "$c_off" ;;
    FAIL) printf '  %s✗ %-26s%s %s\n' "$c_red" "$2" "$c_off" "$3"; ASSERT_FAILED=1 ;;
    *)    printf '  %s· %-26s %s%s\n' "$c_dim" "$2" "$3" "$c_off" ;;
  esac
}

fact() { python3 -c 'import json,sys;print(json.load(open("/out/facts.json")).get(sys.argv[1]) or "")' "$1" 2>/dev/null; }
mfact() { python3 -c 'import json,sys;print(json.load(open(sys.argv[2])).get(sys.argv[1]) or "")' "$1" "$2" 2>/dev/null; }

in_container() {
  local app="$1" mode="$2"     # mode: real | tamper-checksum | unsigned | control-cachedir
  : > /out/assertions.tsv
  local apps_dir=/var/lib/vulos/apps
  mkdir -p "$apps_dir" /out/prep

  # ── 1. ephemeral trust root, and the ONE entry under test signed against it
  #
  # Everything about verification stays at production strength; only the root
  # of trust is ephemeral. `-mode prepare` refuses to run if VULOS_ENV or
  # VULOS_REGISTRY_INSECURE is set, so the harness cannot quietly slip into a
  # weaker posture.
  if ! /verify/driver -mode prepare -registry /verify/registry.json -app "$app" \
        -out /out/prep -variant "$mode" -facts /out/facts.json >/out/prepare.log 2>&1; then
    record FAIL prepare "$(tail -3 /out/prepare.log)"
    return 1
  fi
  record INFO trust-root "ephemeral ed25519 root+release issued in-container; VULOS_ENV unset"

  export VULOS_TRUST_ANCHOR=/out/prep/trust-anchor.pub
  export VULOS_RELEASE_CERT=/out/prep/release-cert.json
  unset VULOS_ENV VULOS_REGISTRY_INSECURE VULOS_REGISTRY_PUBKEY VULOS_BOX_ARCH || true

  local want_arch; want_arch="$(fact box_arch)"
  local url;       url="$(fact resolved_url)"
  local sum;       sum="$(fact resolved_checksum)"
  record INFO box-arch "installer resolved for $want_arch (driver runtime.GOARCH)"
  record INFO artifact "$url"

  # ── 2. THE REAL INSTALLER
  local t0 t1; t0="$(date +%s)"
  if ! /verify/driver -mode install -registry /out/prep/registry.json -app "$app" \
        -apps-dir "$apps_dir" >/out/install.log 2>&1; then
    record FAIL install "$(grep -m1 -E 'INSTALL-FAILED|failed|refus' /out/install.log || tail -3 /out/install.log)"
    sed 's/^/    | /' /out/install.log | tail -20
    return 1
  fi
  t1="$(date +%s)"
  record OK install "InstallFromRegistry returned OK in $((t1-t0))s"

  # signature verification really ran: an unsigned entry must not reach here.
  if grep -qi 'signature' /out/install.log; then
    record INFO signature "$(grep -i -m1 'signature\|trust' /out/install.log)"
  fi

  # ── 3. the checksum was verified for THIS arch's artifact
  # staticInstall logs `checksum OK (<first 12 hex>)` after comparing. Absent
  # means either verification was skipped or a different branch ran; both are
  # exactly the failure this exists to catch.
  if grep -q 'checksum OK' /out/install.log; then
    local logged; logged="$(sed -n 's/.*checksum OK (\([0-9a-f]*\)).*/\1/p' /out/install.log | head -1)"
    if [[ -n "$sum" && "${sum:0:12}" == "$logged" ]]; then
      record OK checksum-verified "matched the registry pin ${sum:0:12}… for $want_arch"
    else
      record FAIL checksum-verified "installer logged $logged, registry pins ${sum:0:12}…"
    fi
  else
    record FAIL checksum-verified "staticInstall never logged a checksum comparison"
  fi

  local app_dir="$apps_dir/$app"

  # ── 4. manifest
  if [[ -f "$app_dir/app.json" ]]; then
    record OK manifest-written "$app_dir/app.json"
  else
    record FAIL manifest-written "no app.json"; return 1
  fi
  local cmd port workdir
  cmd="$(mfact command "$app_dir/app.json")"
  port="$(mfact port "$app_dir/app.json")"
  workdir="$(mfact work_dir "$app_dir/app.json")"
  [[ -n "$workdir" ]] || workdir="$app_dir"
  if [[ -n "$cmd" ]]; then record OK command-declared "$cmd"; else record FAIL command-declared "manifest has no command"; fi

  # ── 5. EXTRACTED, NOT COPIED.
  # The pre-extractZip bug installed bin/<name>.zip chmod 0755 and reported
  # success. Two assertions, because either alone can be satisfied by the bug:
  # no *.zip anywhere under the app dir, AND the command's target is a real
  # ELF for THIS machine.
  local strays; strays="$(find "$app_dir" -name '*.zip' 2>/dev/null | head -5)"
  if [[ -n "$strays" ]]; then
    record FAIL extracted-not-copied "archive installed as a file: $strays"
  else
    record OK extracted-not-copied "no .zip left under the app dir"
  fi

  # resolve the command's binary the way the launcher would: relative to workdir
  local bin_rel bin_path
  bin_rel="$(printf '%s' "$cmd" | awk '{print $1}')"
  case "$bin_rel" in
    /*) bin_path="$bin_rel" ;;
    *)  bin_path="$workdir/${bin_rel#./}" ;;
  esac
  if [[ -x "$bin_path" ]]; then
    record OK binary-executable "$bin_path"
  else
    record FAIL binary-executable "$bin_path missing or not executable"
  fi

  # ── 6. ARCH-CORRECT. `file` on the installed binary. This is the assertion
  # that catches a resolver handing over the other architecture's artifact:
  # the checksum would still match, and the app would simply never run.
  local ftype want_pat
  ftype="$(file -b "$bin_path" 2>/dev/null)"
  case "$want_arch" in
    amd64) want_pat='x86-64' ;;
    arm64) want_pat='aarch64' ;;
    *)     want_pat='' ;;
  esac
  if [[ -n "$want_pat" && "$ftype" == *"$want_pat"* ]]; then
    record OK arch-correct "$want_arch ⇐ $ftype"
  else
    record FAIL arch-correct "expected $want_pat for $want_arch, file says: $ftype"
  fi

  # ── 7. post_install really ran (POSTINSTALL-01 makes failure fatal, but a
  # recipe that writes nothing would still 'succeed')
  if [[ -f "$app_dir/config.toml" ]]; then
    record OK post-install-config "config.toml written ($(stat -c '%a %U:%G' "$app_dir/config.toml"))"
  else
    record INFO post-install-config "no config.toml (recipe may not write one)"
  fi

  # ── 8. LAUNCH, the way appnet.Launcher does it minus the netns.
  #
  # Faithful in the parts that can change the verdict: cwd = manifest work_dir,
  # `sh -c` on the manifest command with ${PORT} expanded, a SCRUBBED env of
  # exactly PATH/HOME/TMPDIR/PORT, and setpriv dropping to uid/gid 65534 with
  # --clear-groups --no-new-privs. That last part is not ceremony: post_install
  # writes config.toml mode 640 owned by 65534, so running as root here would
  # hide a permissions defect that every real box would hit.
  # NOT faithful: no `ip netns exec` (needs CAP_SYS_ADMIN and an iproute2 stack
  # this image does not carry) and no run-lease. Neither can make a
  # non-serving app serve.
  local expanded; expanded="${cmd//\$\{PORT\}/$port}"; expanded="${expanded//\$\{CONSOLE_PORT\}/$port}"
  ( cd "$workdir" && setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs \
      env -i PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin \
             HOME=/tmp TMPDIR=/tmp "PORT=$port" \
      sh -c "$expanded" ) >/out/app.log 2>&1 &
  local app_pid=$!

  # ── 9. it SERVES
  local code="" body_ok=0 i
  for i in $(seq 1 "${VULOS_SERVE_TRIES:-90}"); do
    if ! kill -0 "$app_pid" 2>/dev/null; then break; fi
    code="$(curl -s -o /out/login.html -w '%{http_code}' --max-time 5 "http://127.0.0.1:$port/login" 2>/dev/null)"
    [[ "$code" == "200" ]] && { body_ok=1; break; }
    sleep 1
  done
  if [[ "$body_ok" == "1" ]]; then
    record OK serves-login "GET /login → 200 after ${i}s"
    if grep -qi "<title>[^<]*$app" /out/login.html; then
      record OK serves-own-page "$(grep -o -i '<title>[^<]*</title>' /out/login.html | head -1)"
    else
      record FAIL serves-own-page "200 but the page is not $app's (title: $(grep -o -i '<title>[^<]*</title>' /out/login.html | head -1))"
    fi
    local root_code; root_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$port/" 2>/dev/null)"
    record INFO serves-root "GET / → $root_code"
  else
    record FAIL serves-login "no 200 from /login (last code=${code:-none}); app log tail:"
    sed 's/^/    | /' /out/app.log | tail -15
  fi

  # ── 10. and it did not SILENTLY DEGRADE.
  # lilmail without cache/ logs `scheduled send unavailable (store open failed)`
  # and serves anyway — a shallower check calls that a pass.
  local degraded
  degraded="$(grep -i -m3 'unavailable\|store open failed\|degraded\|failed to open' /out/app.log || true)"
  if [[ -n "$degraded" ]]; then
    if [[ "$mode" == "control-cachedir" ]]; then
      record OK control-degraded-as-expected "${degraded//$'\n'/ ; }"
    else
      record FAIL not-degraded "app started but reported a degraded subsystem: ${degraded//$'\n'/ ; }"
    fi
  else
    if [[ "$mode" == "control-cachedir" ]]; then
      record FAIL control-degraded-as-expected "post_install's mkdir cache/ was removed and NOTHING degraded — the mkdir is decoration, or the app creates it"
    else
      record OK not-degraded "no degradation reported in the app's own log"
    fi
  fi
  for d in cache sessions; do
    if [[ -d "$app_dir/$d" ]]; then
      record INFO "dir-$d" "present ($(stat -c '%a %U:%G' "$app_dir/$d"))"
    else
      record INFO "dir-$d" "ABSENT"
    fi
  done

  kill "$app_pid" 2>/dev/null; wait "$app_pid" 2>/dev/null
  cp -f /out/app.log "/out/app-$mode.log" 2>/dev/null
  return $ASSERT_FAILED
}

if [[ -n "$IN_CONTAINER" ]]; then
  in_container "$@"; exit $?
fi

# ═════════════════════════════════════════════════════════════════════════════
# HOST SIDE
# ═════════════════════════════════════════════════════════════════════════════

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }
host_arch() { case "$(uname -m)" in arm64|aarch64) echo arm64 ;; x86_64|amd64) echo amd64 ;; *) echo unknown ;; esac; }

# The base image: debian:trixie-slim (the suite build.sh builds from) plus
# ca-certificates (the recipe's own dep, so appnet's InstallDeps is a no-op
# rather than an apt round trip), curl and file (the harness's own tools, kept
# separate from the product's on purpose). Committed once per arch — apt under
# emulation is the expensive part and it is paid once.
ensure_image() {
  local arch="$1" image="$2"
  docker image inspect "$image" >/dev/null 2>&1 && return 0
  step "building $image (once per arch; slow under emulation)"
  local c="vulos-perarch-base-$arch-$$"
  docker rm -f "$c" >/dev/null 2>&1 || true
  docker run -d --name "$c" --platform "linux/$arch" debian:trixie-slim sleep 3600 >/dev/null \
    || die "cannot start debian:trixie-slim for linux/$arch"
  if ! docker exec "$c" bash -c '
      set -e
      export DEBIAN_FRONTEND=noninteractive
      printf "force-unsafe-io\n" > /etc/dpkg/dpkg.cfg.d/99-unsafe-io
      printf "Acquire::Languages \"none\";\n" > /etc/apt/apt.conf.d/99-no-languages
      apt-get update -qq
      apt-get install -y --no-install-recommends ca-certificates curl file python3 procps
      rm -rf /var/lib/apt/lists/*
      command -v setpriv >/dev/null || { echo "setpriv missing from util-linux"; exit 1; }
      echo SETUP_DONE'; then
    docker logs "$c" 2>&1 | tail -20
    docker rm -f "$c" >/dev/null 2>&1 || true
    die "base image setup failed for linux/$arch"
  fi
  docker commit -c 'CMD ["bash"]' "$c" "$image" >/dev/null || die "docker commit failed"
  docker rm -f "$c" >/dev/null 2>&1 || true
  say "  committed $image"
}

build_driver() {
  local goarch="$1" dest="$2"
  local d="$WORKDIR/driver-src"
  rm -rf "$d"; mkdir -p "$d"
  sed 's|^module vulos/backend$|module vulosperarchdriver|' "$REPO_ROOT/backend/go.mod" > "$d/go.mod"
  printf '\nrequire vulos/backend v0.0.0\n\nreplace vulos/backend => %s\n' "$REPO_ROOT/backend" >> "$d/go.mod"
  cp "$REPO_ROOT/backend/go.sum" "$d/go.sum"
  emit_driver_source > "$d/main.go"
  ( cd "$d" && GOPROXY=off GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
      go build -trimpath -o "$dest" . ) || die "driver build failed (GOARCH=$goarch)"
}

# We COPY registry.json rather than bind-mounting the checkout: other agents are
# writing to this tree right now and a torn read would be an unexplainable
# failure. The copy is also what proves the run used the SHIPPED entry — its
# sha256 goes into the report.
stage_common() {
  local stage="$1" goarch="$2"
  rm -rf "$stage"; mkdir -p "$stage"
  cp "$REPO_ROOT/registry.json" "$stage/registry.json"
  cp "$SCRIPT_PATH" "$stage/verify-app-install-perarch.sh"
  build_driver "$goarch" "$stage/driver"
}

run_one() { # run_one <app> <arch> <mode>
  local app="$1" arch="$2" mode="$3"
  local image="vulos-perarch-verify:trixie-$arch"
  local native; native="$(host_arch)"
  local emu="EMULATED (qemu)"; [[ "$arch" == "$native" ]] && emu="native"
  local stage="$WORKDIR/stage-$arch"
  local out="$WORKDIR/out/$app-$arch-$mode-$(date -u +%Y%m%d-%H%M%S)"
  mkdir -p "$out"

  step "$app on linux/$arch — $emu — variant=$mode"
  say "  registry.json sha256: $(shasum -a 256 "$REPO_ROOT/registry.json" | cut -c1-16)…"
  ensure_image "$arch" "$image"
  stage_common "$stage" "$arch"

  # No --privileged: this path needs no flatpak, no bwrap and no namespaces.
  # --network is the default bridge because the installer really downloads.
  docker run --rm --platform "linux/$arch" \
    -v "$stage:/verify:ro" -v "$out:/out" \
    -e "VULOS_SERVE_TRIES=${VULOS_SERVE_TRIES:-90}" \
    "$image" bash /verify/verify-app-install-perarch.sh --in-container "$app" "$mode"
  local rc=$?
  say ""
  if [[ $rc -eq 0 ]]; then
    printf '%s══ PASS %s on linux/%s (%s) ══%s\n' "$c_grn" "$app" "$arch" "$emu" "$c_off"
  else
    printf '%s══ FAIL %s on linux/%s (%s) — rc=%d ══%s\n' "$c_red" "$app" "$arch" "$emu" "$rc" "$c_off"
  fi
  say "  evidence: $out"
  return $rc
}

self_test() { # prove the harness can go red
  local app="$1" arch="$2"
  local overall=0
  local -a cases=(
    "real:pass:the shipped entry, unmodified (the control)"
    "tamper-checksum:fail:one hex digit flipped in the artifact checksum, then signed"
    "unsigned:fail:entry left unsigned against the ephemeral root"
  )
  : > "$WORKDIR/selftest.tsv"
  for spec in "${cases[@]}"; do
    local id="${spec%%:*}" rest="${spec#*:}"
    local want="${rest%%:*}" why="${rest#*:}"
    printf '\n%s─── self-test %s (expect %s: %s)%s\n' "$c_dim" "$id" "$want" "$why" "$c_off"
    local got=pass
    run_one "$app" "$arch" "$id" || got=fail
    local verdict=WRONG; [[ "$got" == "$want" ]] && verdict=CORRECT
    [[ "$verdict" == WRONG ]] && overall=1
    printf '%s\t%s\t%s\t%s\n' "$id" "$want" "$got" "$verdict" >> "$WORKDIR/selftest.tsv"
  done
  printf '\n── self-test summary (linux/%s) ──\n' "$arch"
  column -t -s$'\t' "$WORKDIR/selftest.tsv" 2>/dev/null || cat "$WORKDIR/selftest.tsv"
  return $overall
}

# ─── driver source (generated; never written into the repo) ──────────────────
emit_driver_source() {
cat <<'GO_EOF'
// Code generated by scripts/verify-app-install-perarch.sh. DO NOT COMMIT.
//
// Two modes and no install logic of its own:
//
//   -mode prepare  builds an EPHEMERAL trust root, signs the one entry under
//                  test with the product's own appnet.SignEntry, and writes the
//                  resolved recipe facts the assertions read.
//   -mode install  appnet.LoadRegistry + appnet.InstallFromRegistry — the same
//                  call POST /api/store/install reaches.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"vulos/backend/services/appnet"
	"vulos/backend/services/signing"
)

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(2)
}

func main() {
	mode := flag.String("mode", "", "prepare | install")
	registryPath := flag.String("registry", "", "registry.json to read")
	appID := flag.String("app", "", "app id")
	version := flag.String("version", "latest", "version")
	appsDir := flag.String("apps-dir", "/var/lib/vulos/apps", "install root")
	outDir := flag.String("out", "", "prepare: where to write the ephemeral trust material")
	factsPath := flag.String("facts", "", "prepare: where to write resolved recipe facts")
	variant := flag.String("variant", "real", "real | tamper-checksum | unsigned | control-cachedir")
	flag.Parse()

	switch *mode {
	case "prepare":
		prepare(*registryPath, *appID, *version, *outDir, *factsPath, *variant)
	case "install":
		install(*registryPath, *appID, *version, *appsDir)
	default:
		fail("unknown -mode %q", *mode)
	}
}

// prepare refuses to run under a weakened posture. The harness's whole claim is
// that verification was at full strength, so the check belongs where it cannot
// be forgotten rather than in a comment.
func assertStrictPosture() {
	for _, k := range []string{"VULOS_REGISTRY_INSECURE", "VULOS_SIGN_ALLOW_KEY_MISMATCH"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			fail("%s is set (%q) — this harness verifies at full strength or not at all", k, v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("VULOS_ENV")); v != "" {
		fail("VULOS_ENV is set (%q) — it must be unset so services/env resolves to prod", v)
	}
	if v := strings.TrimSpace(os.Getenv("VULOS_BOX_ARCH")); v != "" {
		fail("VULOS_BOX_ARCH is set (%q) — the box arch must come from the driver's own runtime.GOARCH", v)
	}
}

func prepare(registryPath, appID, version, outDir, factsPath, variant string) {
	assertStrictPosture()
	if outDir == "" {
		fail("-out is required for -mode prepare")
	}
	reg, err := appnet.LoadRegistry(registryPath)
	if err != nil {
		fail("load registry: %v", err)
	}
	entry, ok := reg.Apps[appID]
	if !ok {
		fail("no such app %q in %s", appID, registryPath)
	}
	v := version
	if v == "" || v == "latest" {
		v = entry.LatestVersion()
	}
	recipe := entry.GetRecipe(v)
	if recipe == nil {
		fail("app %q has no version %q", appID, v)
	}

	boxArch := appnet.BoxArch()

	// Variants mutate the recipe BEFORE signing, so the signature is always
	// valid and each self-test case fails for exactly the reason it names.
	switch variant {
	case "tamper-checksum":
		if a, ok := recipe.Artifacts[boxArch]; ok && a != nil {
			a.Checksum = flipHex(a.Checksum)
		} else {
			recipe.Checksum = flipHex(recipe.Checksum)
		}
	case "control-cachedir":
		// Remove ONLY the cache/ mkdir from post_install. sessions/ stays, so a
		// failure here cannot be blamed on a wholesale missing post_install.
		before := recipe.PostInstall
		recipe.PostInstall = strings.Replace(recipe.PostInstall, "mkdir -p cache sessions", "mkdir -p sessions", 1)
		if recipe.PostInstall == before {
			fail("control-cachedir: post_install does not contain `mkdir -p cache sessions`; the control would prove nothing")
		}
	case "real", "unsigned":
	default:
		fail("unknown -variant %q", variant)
	}

	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fail("root key: %v", err)
	}
	relPub, relPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fail("release key: %v", err)
	}
	cert, err := signing.IssueReleaseCert(rootPriv, relPub, "perarch-verify-ephemeral", time.Now().Add(1*time.Hour), 0)
	if err != nil {
		fail("issue release cert: %v", err)
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fail("mkdir out: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "trust-anchor.pub"), []byte(signing.EncodeAnchor(rootPub)), 0644); err != nil {
		fail("write anchor: %v", err)
	}
	cb, _ := json.MarshalIndent(cert, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "release-cert.json"), append(cb, '\n'), 0644); err != nil {
		fail("write cert: %v", err)
	}

	// Only the entry under test is signed. Every other shipped entry keeps
	// whatever it had, which for the three first-party ones is `signature: ""` —
	// this run does not and must not make them look vetted.
	if variant != "unsigned" {
		if err := appnet.SignEntry(entry, appID, relPriv); err != nil {
			fail("sign entry: %v", err)
		}
	}
	if err := appnet.SaveRegistry(filepath.Join(outDir, "registry.json"), reg); err != nil {
		fail("save prepared registry: %v", err)
	}

	url, sum, rerr := recipe.ResolveArtifact(boxArch)
	facts := map[string]any{
		"app_id":            appID,
		"version":           v,
		"box_arch":          boxArch,
		"goarch":            runtime.GOARCH,
		"entry_arch":        entry.Arch,
		"resolved_url":      url,
		"resolved_checksum": sum,
		"resolve_error":     errString(rerr),
		"command":           recipe.Command,
		"port":              recipe.Port,
		"archive_strip":     recipe.ArchiveStrip,
		"binary_name":       recipe.BinaryName,
		"variant":           variant,
		"app_dir":           filepath.Join("/var/lib/vulos/apps", appID),
	}
	if factsPath != "" {
		b, _ := json.MarshalIndent(facts, "", "  ")
		if err := os.WriteFile(factsPath, append(b, '\n'), 0644); err != nil {
			fail("write facts: %v", err)
		}
	}
	fmt.Printf("PREPARE-OK app=%s version=%s arch=%s variant=%s\n", appID, v, boxArch, variant)
}

func install(registryPath, appID, version, appsDir string) {
	assertStrictPosture()
	if strings.TrimSpace(os.Getenv("VULOS_TRUST_ANCHOR")) == "" {
		fail("VULOS_TRUST_ANCHOR is unset — refusing to install against an unresolved trust root")
	}
	reg, err := appnet.LoadRegistry(registryPath)
	if err != nil {
		fail("load registry: %v", err)
	}
	entry, ok := reg.Apps[appID]
	if !ok {
		fail("no such app %q", appID)
	}
	v := version
	if v == "" || v == "latest" {
		v = entry.LatestVersion()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	if err := appnet.InstallFromRegistry(ctx, reg, appID, v, appsDir); err != nil {
		fmt.Fprintln(os.Stderr, "INSTALL-FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("INSTALL-OK", appID, v)
}

func flipHex(s string) string {
	if s == "" {
		return "0000000000000000000000000000000000000000000000000000000000000000"
	}
	b := []byte(s)
	if b[0] == '0' {
		b[0] = '1'
	} else {
		b[0] = '0'
	}
	return string(b)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
GO_EOF
}

print_remote_command() {
  local app="$1"
  cat <<EOF
# Run this on any Linux/amd64 host with docker and go1.25+, from a checkout of
# this repo at the same commit. It is the identical harness — no emulation, so
# the app's own startup is not qemu-slowed:
#
#   git clone <this repo> vulos && cd vulos
#   bash scripts/verify-app-install-perarch.sh $app --arch amd64
#
# Expected: "PASS $app on linux/amd64 (native)" and an assertions.tsv whose
# arch-correct row reads "amd64 <= ELF 64-bit LSB executable, x86-64".
EOF
}

usage() { sed -n '2,95p' "$SCRIPT_PATH" | sed 's/^# \{0,1\}//'; exit 2; }

APP=""; ARCH=""; MODE=real; SELFTEST=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --arch)     ARCH="$2"; shift 2 ;;
    --control)  MODE="control-$2"; shift 2 ;;
    --self-test) SELFTEST=1; shift ;;
    --print-remote-command) shift; print_remote_command "${1:-<app>}"; exit 0 ;;
    -h|--help)  usage ;;
    -*)         die "unknown flag: $1" ;;
    *)          APP="$1"; shift ;;
  esac
done
[[ -n "$APP" ]] || usage
[[ -n "$ARCH" ]] || ARCH="$(host_arch)"

need docker; need go; need python3
mkdir -p "$WORKDIR"

if [[ "$SELFTEST" == "1" ]]; then
  [[ "$ARCH" == "both" ]] && die "--self-test takes one --arch"
  self_test "$APP" "$ARCH"; exit $?
fi

rc=0
if [[ "$ARCH" == "both" ]]; then
  native="$(host_arch)"; other=amd64; [[ "$native" == "amd64" ]] && other=arm64
  run_one "$APP" "$native" "$MODE" || rc=1
  run_one "$APP" "$other"  "$MODE" || rc=1
else
  run_one "$APP" "$ARCH" "$MODE" || rc=1
fi
exit $rc
