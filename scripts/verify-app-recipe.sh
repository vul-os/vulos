#!/usr/bin/env bash
# verify-app-recipe.sh — install-test one App Hub registry entry, for real.
#
# ─────────────────────────────────────────────────────────────────────────────
# WHAT THIS IS
#
# Takes an app id from registry.json, spins a throwaway debian:trixie container
# (the suite the shipped image is built from — build.sh SUITE="trixie"), runs
# THE PRODUCT'S OWN INSTALL PATH inside it, and asserts the app is genuinely
# installed and launchable.  Then it removes the app again and reports the disk
# it cost and the disk it gave back.
#
# THE HARD RULE — we do not re-implement the installer.
#
#   The container does NOT run `flatpak install` or `apt-get install` on its
#   own.  It runs a ~120-line driver binary whose entire job is:
#
#       appnet.LoadRegistry(path)  →  appnet.InstallFromRegistry(ctx, reg, id, ver, appsDir)
#
#   `appnet.InstallFromRegistry` is the exact function the shipping box calls:
#   cmd/server/main.go → AppStore.InstallFromRegistry (store.go:161) → this.
#   So the Ed25519 publisher-signature check, the pipe-to-shell rejection, the
#   mandatory-checksum gate, the flatpak path, the apt path, the static-download
#   path, the tar traversal screen, the app.json manifest generation and the
#   post-install step are all the real ones.  If the product's installer breaks,
#   this harness goes red.  (The driver source is generated from the heredoc
#   below into a scratch Go module that `replace`s vulos/backend — nothing is
#   written into the repo.)
#
#   Why this matters here: a previous CI gate in this repo validated window
#   placement with `foot --title` while the shipping client was `cog`, which
#   never sets a title.  The gate stayed green through a feature that had never
#   once worked.  A harness that drives its own copy of the install logic tests
#   the harness.
#
# SIGNATURES ARE NOT WEAKENED.  The container runs with VULOS_ENV unset, which
# services/env treats as prod, with VULOS_TRUST_ANCHOR / VULOS_RELEASE_CERT
# pointing at the repo's real keys/trust-anchor.pub + keys/release-cert.json.
# An unsigned or tampered entry is refused by the product, and the harness
# reports that as a FAILURE.  VULOS_REGISTRY_INSECURE is never set.
#
# ─────────────────────────────────────────────────────────────────────────────
# USAGE
#
#   scripts/verify-app-recipe.sh <app-id> [--version V] [--keep]
#   scripts/verify-app-recipe.sh --self-test          # prove the harness goes red
#   scripts/verify-app-recipe.sh --plan [--limit N]   # size-ordered work list
#   scripts/verify-app-recipe.sh --sweep [--limit N] [--max-installed-mb N]
#   scripts/verify-app-recipe.sh --ledger-render      # regenerate the .md ledger
#
# EXIT CODES
#   0  PASS        — installed, every assertion held, cleaned up
#   1  FAIL        — install failed, or an assertion did not hold
#   2  ERROR       — harness/infrastructure problem (no docker, bad args, …)
#   3  SKIP        — not testable on this machine's architecture (recorded as
#                    untestable-on-arm64 in the ledger, never as a pass)
#
# Results land in roadmap/app-verification-ledger.json (authoritative, machine
# readable) and roadmap/APP-VERIFICATION-LEDGER.md (rendered from it).
#
# Agent-generated, machine-verified, NOT human-reviewed.
set -euo pipefail

HARNESS_VERSION="2"
SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

# ─── in-container phases dispatch early (no repo, no docker in there) ─────────
IN_CONTAINER_MODE=""
if [[ "${1:-}" == "--in-container" ]]; then
  IN_CONTAINER_MODE="${2:-}"
  shift 2
fi

REPO_ROOT="${VULOS_REPO_ROOT:-$(cd "$(dirname "$SCRIPT_PATH")/.." && pwd)}"
IMAGE="${VULOS_VERIFY_IMAGE:-vulos-recipe-verify:trixie-v2}"
# TMPDIR on macOS ends in a slash, which produced paths like "…/T//vulos-verify".
# Docker accepts them, but a bind mount whose source path is re-created under a
# doubled separator can come back stale — see the note in verify_one.
WORKDIR="${VULOS_VERIFY_WORKDIR:-${TMPDIR:-/tmp}/vulos-verify}"
WORKDIR="$(printf '%s' "$WORKDIR" | sed 's|//*|/|g; s|/$||')"
LEDGER_JSON="$REPO_ROOT/roadmap/app-verification-ledger.json"
LEDGER_MD="$REPO_ROOT/roadmap/APP-VERIFICATION-LEDGER.md"

c_red=$'\033[31m'; c_grn=$'\033[32m'; c_yel=$'\033[33m'; c_dim=$'\033[2m'; c_off=$'\033[0m'
say()  { printf '%s\n' "$*"; }
step() { printf '\n▸ %s\n' "$*"; }
die()  { printf '%sERROR:%s %s\n' "$c_red" "$c_off" "$*" >&2; exit 2; }

# ═════════════════════════════════════════════════════════════════════════════
# IN-CONTAINER PHASES
# ═════════════════════════════════════════════════════════════════════════════

# assertion log: /out/assertions.tsv, one row per assertion.
ASSERT_FAILED=0
record() { # record OK|FAIL|INFO name detail
  printf '%s\t%s\t%s\n' "$1" "$2" "${3//$'\t'/ }" >> /out/assertions.tsv
  case "$1" in
    OK)   printf '  %s✓%s %s %s%s%s\n' "$c_grn" "$c_off" "$2" "$c_dim" "$3" "$c_off" ;;
    FAIL) printf '  %s✗ %s%s %s\n' "$c_red" "$2" "$c_off" "$3"; ASSERT_FAILED=1 ;;
    *)    printf '  %s· %s %s%s\n' "$c_dim" "$2" "$3" "$c_off" ;;
  esac
}

du_mb() { du -sxm / 2>/dev/null | awk '{print $1}'; }

fact() { jq -r "$1 // empty" /out/facts.json 2>/dev/null; }

# run_probe <timeout-secs> <cmd...> — execute a binary far enough to prove it is
# a real, loadable executable.  Exit 127 (not found) or a dynamic-loader failure
# is a FAIL.  Anything else — including a non-zero app exit or a timeout — means
# the kernel actually execve'd it, which is what we claim to prove.
run_probe() {
  local t="$1"; shift
  local out rc
  out="$(timeout "$t" "$@" 2>&1 | head -c 4000)" || rc=$?
  rc="${rc:-0}"
  printf '%s' "$out" > /out/probe.log
  if [[ "$rc" == "127" || "$rc" == "126" ]]; then return 127; fi
  # Loader-level failures only.  We execve the binary directly rather than
  # through a shell, so "command not found" / "no such file or directory" in the
  # OUTPUT is the app complaining about something of its own (a missing config,
  # a data dir) — matching on those would fail working apps, the same way a
  # missing python3 failed a working cinny recipe.
  if grep -qiE 'error while loading shared libraries|exec format error|cannot execute binary file' <<<"$out"; then
    return 126
  fi
  return 0
}

assert_flatpak() {
  local fpid="$1"
  if ! flatpak info "$fpid" >/dev/null 2>&1; then
    record FAIL flatpak-present "flatpak info $fpid failed — nothing is installed"
    return
  fi
  record OK flatpak-present "flatpak info $fpid resolves"

  # deployed tree must exist and be non-empty (an ostree ref with no files is a
  # perfectly happy `flatpak info` and a completely broken app).
  local loc
  loc="$(flatpak info -l "$fpid" 2>/dev/null || true)"
  if [[ -n "$loc" && -d "$loc/files" ]] && [[ -n "$(ls -A "$loc/files" 2>/dev/null)" ]]; then
    record OK flatpak-deployed "$(du -sxm "$loc" 2>/dev/null | awk '{print $1" MB at "$2}')"
  else
    record FAIL flatpak-deployed "deploy dir empty or missing: ${loc:-<none>}"
  fi

  # the runtime — a missing runtime is the classic install-succeeds/won't-start.
  local meta rt
  meta="$(flatpak info --show-metadata "$fpid" 2>/dev/null || true)"
  rt="$(sed -n 's/^runtime=//p' <<<"$meta" | head -1)"
  if [[ -z "$rt" ]]; then
    record FAIL flatpak-runtime-declared "app metadata declares no runtime"
  elif flatpak info "runtime/$rt" >/dev/null 2>&1; then
    record OK flatpak-runtime "$rt installed"
  else
    record FAIL flatpak-runtime "$rt is NOT installed — app cannot start"
  fi
  local want_rt; want_rt="$(fact '.flatpak_runtime')"
  if [[ -n "$want_rt" ]]; then
    if [[ "$rt" == "$want_rt"* || "$want_rt" == "$rt"* ]]; then
      record OK flatpak-runtime-pinned "matches registry flatpak_runtime=$want_rt"
    else
      record FAIL flatpak-runtime-pinned "registry says $want_rt, installed app wants $rt"
    fi
  fi

  # the app's own entry-point command, inside its own sandbox.
  local appcmd
  appcmd="$(sed -n 's/^command=//p' <<<"$meta" | head -1)"
  if [[ -z "$appcmd" ]]; then
    record FAIL flatpak-command-declared "app metadata declares no command="
    return
  fi
  if flatpak run --die-with-parent --command=/bin/sh "$fpid" -c "command -v '$appcmd' >/dev/null || test -x '$appcmd'" >/dev/null 2>&1; then
    record OK command-resolves "$appcmd resolves inside the sandbox"
  else
    record FAIL command-resolves "$appcmd does NOT resolve inside the sandbox"
    return
  fi
  if run_probe 40 flatpak run --die-with-parent --command="$appcmd" "$fpid" --version; then
    record OK command-executes "sandboxed $appcmd execve'd (probe: --version)"
  else
    record FAIL command-executes "sandboxed $appcmd would not execute: $(head -c 300 /out/probe.log)"
  fi
}

assert_native() {
  local app_dir="$1" mcmd="$2"
  # first token of the manifest command is the binary the launcher will exec.
  local bin resolved
  bin="$(awk '{print $1}' <<<"$mcmd")"
  if [[ "$bin" == /* ]]; then
    resolved="$bin"
  elif [[ -e "$app_dir/$bin" ]]; then
    resolved="$app_dir/$bin"
  else
    resolved="$(PATH="$app_dir/bin:$PATH" command -v "$bin" 2>/dev/null || true)"
  fi
  if [[ -z "$resolved" || ! -e "$resolved" ]]; then
    record FAIL command-resolves "manifest command starts with '$bin' — not found in PATH or $app_dir"
    return
  fi
  if [[ ! -x "$resolved" ]]; then
    record FAIL command-resolves "$resolved exists but is not executable"
    return
  fi
  record OK command-resolves "$bin → $resolved"

  # where did the bytes come from?  apt recipes must be traceable to a package;
  # download recipes must land inside the app dir.
  local dl; dl="$(fact '.download_url')"
  local inst; inst="$(fact '.install')"
  local arts; arts="$(fact '.artifacts_arches')"
  local real; real="$(readlink -f "$resolved")"
  if [[ -n "$arts" ]]; then
    # The Vulos-native vehicle (roadmap/INSTALL-METHODOLOGY.md). Two separate
    # claims, because "it landed somewhere" and "it landed here" are not the
    # same assertion and only the second one is worth anything.
    if [[ "$real" == "$app_dir"/* ]]; then
      record OK artifact-provenance "pinned per-arch artefact unpacked under the app dir (offers: $arts)"
    else
      record FAIL artifact-provenance "artifacts recipe but $real is outside $app_dir"
    fi
    # arch-correct: the assertion that catches a resolver handing over the
    # WRONG architecture's artefact. A wrong binary passes its own checksum and
    # installs perfectly; only reading the ELF header notices.
    local want_elf; want_elf="$(uname -m)"
    local got_elf; got_elf="$(file -b "$real" 2>/dev/null || echo unknown)"
    case "$want_elf:$got_elf" in
      x86_64:*x86-64*|aarch64:*aarch64*)
        record OK arch-correct "installed binary is $want_elf, matching the box" ;;
      *:*ELF*)
        record FAIL arch-correct "box is $want_elf but the installed binary is: $got_elf" ;;
      *)
        record INFO arch-correct "not an ELF (script or static bundle): $got_elf" ;;
    esac
  elif [[ -n "$dl" ]]; then
    if [[ "$real" == "$app_dir"/* ]]; then
      record OK artifact-provenance "installed under the app dir by the static-download path"
    else
      record FAIL artifact-provenance "download_url recipe but $real is outside $app_dir"
    fi
  else
    # Every native recipe gets a provenance line, including the curl-into-bin/
    # shape (gitea): without this branch those apps were asserted on only by
    # "the command resolves", and an assertion nobody notices is missing is the
    # same as one that never ran.
    local owner
    if owner="$(dpkg -S "$real" 2>/dev/null | head -1)"; then
      record OK artifact-provenance "dpkg owns it: $owner"
    elif [[ "$real" == "$app_dir"/* ]]; then
      record OK artifact-provenance "the recipe produced it into the app dir: $real"
    elif grep -qE '\bapt(-get)?\b' <<<"$inst"; then
      record FAIL artifact-provenance "an apt recipe, but no dpkg package owns $real and it is not under $app_dir"
    else
      record INFO artifact-provenance "$real comes from the base image, not from this recipe"
    fi
  fi

  # declared packages (registry `packages` field) must actually be installed.
  local pkgs; pkgs="$(jq -r '.packages[]? // empty' /out/facts.json 2>/dev/null)"
  if [[ -n "$pkgs" ]]; then
    local p missing=""
    while read -r p; do
      [[ -z "$p" ]] && continue
      dpkg -s "$p" >/dev/null 2>&1 || missing="$missing $p"
    done <<<"$pkgs"
    if [[ -z "$missing" ]]; then
      record OK packages-installed "$(tr '\n' ' ' <<<"$pkgs")"
    else
      record FAIL packages-installed "declared but not installed:$missing"
    fi
  fi

  if run_probe 30 "$resolved" --version; then
    record OK command-executes "$resolved execve'd (probe: --version)"
  else
    record FAIL command-executes "$resolved would not execute: $(head -c 300 /out/probe.log)"
  fi
}

in_container_verify() {
  local app="$1" version="${2:-latest}" registry="${3:-/verify/registry.json}"
  : > /out/assertions.tsv
  local apps_dir=/var/lib/vulos/apps
  mkdir -p "$apps_dir"

  local before after
  before="$(du_mb)"
  echo "disk_before_mb=$before" > /out/metrics.env

  local t0; t0=$(date +%s)
  if /verify/driver -registry "$registry" -app "$app" -version "$version" \
       -apps-dir "$apps_dir" -facts /out/facts.json 2>&1 | tee /out/install.log; then
    record OK install-path "appnet.InstallFromRegistry returned success"
  else
    record FAIL install-path "$(tail -3 /out/install.log | tr '\n' ' ' | head -c 500)"
    after="$(du_mb)"
    { echo "disk_after_mb=$after"; echo "seconds=$(( $(date +%s) - t0 ))"; } >> /out/metrics.env
    return 1
  fi
  after="$(du_mb)"
  { echo "disk_after_mb=$after"; echo "seconds=$(( $(date +%s) - t0 ))"; } >> /out/metrics.env

  local app_dir; app_dir="$(fact '.app_dir')"
  if [[ -f "$app_dir/app.json" ]]; then
    record OK manifest-written "$app_dir/app.json"
  else
    record FAIL manifest-written "no app.json at $app_dir — the product did not register the app"
  fi

  local mcmd; mcmd="$(jq -r '.manifest_command // empty' "$app_dir/app.json" 2>/dev/null || true)"
  [[ -z "$mcmd" ]] && mcmd="$(jq -r '.command // empty' "$app_dir/app.json" 2>/dev/null || true)"
  [[ -z "$mcmd" ]] && mcmd="$(fact '.manifest_command')"
  if [[ -z "$mcmd" ]]; then
    record FAIL command-declared "manifest has no command — the launcher would have nothing to run"
  else
    record OK command-declared "$mcmd"
  fi

  local fpid; fpid="$(fact '.flatpak_id')"
  if [[ -n "$fpid" ]]; then
    assert_flatpak "$fpid"
  elif [[ -n "$mcmd" ]]; then
    assert_native "$app_dir" "$mcmd"
  fi

  # ── delete after test: exercise the product's uninstall path, report reclaim
  if [[ "${VULOS_VERIFY_KEEP:-0}" != "1" ]]; then
    /verify/driver -registry "$registry" -app "$app" -version "$version" \
      -apps-dir "$apps_dir" -uninstall >/out/uninstall.log 2>&1 || true
    local reclaimed; reclaimed="$(du_mb)"
    echo "disk_after_cleanup_mb=$reclaimed" >> /out/metrics.env
    if [[ -n "$fpid" ]]; then
      if flatpak info "$fpid" >/dev/null 2>&1; then
        record FAIL uninstall "appnet.FlatpakUninstall left $fpid installed"
      else
        record OK uninstall "removed; $(( after - reclaimed )) MB reclaimed"
      fi
    else
      record INFO uninstall "native recipe: container teardown reclaims $(( after - reclaimed )) MB"
    fi
  fi

  return $ASSERT_FAILED
}

# --- self-test: five synthetic recipes, four of which MUST go red ------------
in_container_selftest() {
  mkdir -p /out/selftest
  /verify/driver -make-selftest /out/selftest || { echo "cannot build self-test fixture"; return 2; }
  local reg=/out/selftest/registry.json
  export VULOS_TRUST_ANCHOR=/out/selftest/trust-anchor.pub
  export VULOS_RELEASE_CERT=/out/selftest/release-cert.json

  local -a cases=(
    "selftest-good:pass:a signed recipe that really installs (the control)"
    "selftest-unsigned:fail:entry carries no publisher signature"
    "selftest-bad-flatpak:fail:flathub id does not exist"
    "selftest-bad-checksum:fail:download_url with a wrong sha256"
    "selftest-bad-command:fail:installs fine, but command does not exist"
  )
  local overall=0
  : > /out/selftest-results.tsv
  for spec in "${cases[@]}"; do
    local id="${spec%%:*}"; local rest="${spec#*:}"
    local want="${rest%%:*}"; local why="${rest#*:}"
    printf '\n%s─── %s (expect %s: %s)%s\n' "$c_dim" "$id" "$want" "$why" "$c_off"
    ASSERT_FAILED=0
    local got=pass
    in_container_verify "$id" latest "$reg" || got=fail
    local verdict
    if [[ "$got" == "$want" ]]; then verdict=CORRECT; else verdict=WRONG; overall=1; fi
    printf '%s\t%s\t%s\t%s\n' "$id" "$want" "$got" "$verdict" >> /out/selftest-results.tsv
    if [[ "$verdict" == CORRECT ]]; then
      printf '%s  ⇒ %s: expected %s, got %s%s\n' "$c_grn" "$verdict" "$want" "$got" "$c_off"
    else
      printf '%s  ⇒ %s: expected %s, got %s%s\n' "$c_red" "$verdict" "$want" "$got" "$c_off"
    fi
  done
  printf '\n── self-test summary ──\n'
  column -t -s$'\t' /out/selftest-results.tsv 2>/dev/null || cat /out/selftest-results.tsv
  return $overall
}

if [[ -n "$IN_CONTAINER_MODE" ]]; then
  case "$IN_CONTAINER_MODE" in
    verify)   in_container_verify "$@"; exit $? ;;
    selftest) in_container_selftest; exit $? ;;
    *) die "unknown in-container mode: $IN_CONTAINER_MODE" ;;
  esac
fi

# ═════════════════════════════════════════════════════════════════════════════
# HOST SIDE
# ═════════════════════════════════════════════════════════════════════════════

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

docker_arch() { docker version --format '{{.Server.Arch}}' 2>/dev/null || echo unknown; }

# GOARCH for the driver == the container's arch.
go_arch() { case "$(docker_arch)" in amd64|x86_64) echo amd64 ;; arm64|aarch64) echo arm64 ;; *) echo "" ;; esac; }

# flathub_arch maps a Debian arch name to Flathub's arch name.
flathub_arch() { case "$1" in amd64) echo x86_64 ;; arm64) echo aarch64 ;; *) echo "$1" ;; esac; }

build_driver() {
  local goarch="$1" dest="$2"
  local d="$WORKDIR/driver-src"
  mkdir -p "$d"
  sed 's|^module vulos/backend$|module vulosrecipeverifydriver|' "$REPO_ROOT/backend/go.mod" > "$d/go.mod"
  printf '\nrequire vulos/backend v0.0.0\n\nreplace vulos/backend => %s\n' "$REPO_ROOT/backend" >> "$d/go.mod"
  cp "$REPO_ROOT/backend/go.sum" "$d/go.sum"
  emit_driver_source > "$d/main.go"
  ( cd "$d" && GOPROXY=off GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
      go build -trimpath -o "$dest" . ) || die "driver build failed (GOARCH=$goarch)"
}

# ensure_image builds the install-test base ONCE and commits it, so the ~20
# minutes of apt setup is paid once for a whole sweep.
#
# THE PACKAGE SET IS NOT INVENTED HERE.  It is scripts/image-packages.txt — the
# same pinned list scripts/check-image-packages.sh gates the shipped
# Dockerfile against — so the container a recipe is tested in has the same
# userland the recipe will meet on a real box.  Deriving it any other way is how
# the first version of this harness reported `cinny` as broken: its recipe runs
# `python3 -m http.server`, python3 is in the shipped image, and it was not in
# my hand-written list.  A harness whose environment is thinner than the
# product's invents failures, which is the same disease as inventing passes.
#
# `intel-media-va-driver-non-free` is amd64-only, exactly as the Dockerfile has
# it.  flatpak + the flathub remote match Dockerfile:154,163.  The extra four
# (gnupg, xz-utils, file, procps) are the verifier's own tools, not the
# product's, and are listed separately so the difference stays visible.
#
# It deliberately does NOT use `docker build`: on this machine buildkit sat for
# 25 minutes on the equivalent Dockerfile with no output and no progress
# (OrbStack's btrfs store is degraded and the host runs at load 70-260).
# run + exec + commit does the same work with visible progress.
ensure_image() {
  if docker image inspect "$IMAGE" >/dev/null 2>&1; then return; fi
  local pkglist="$REPO_ROOT/scripts/image-packages.txt"
  [[ -f "$pkglist" ]] || die "missing $pkglist — that file is the package set, not a suggestion"
  local arch; arch="$(docker_arch)"
  local pkgs
  pkgs="$(grep -v '^[[:space:]]*$' "$pkglist" | tr '\n' ' ')"
  if [[ "$arch" != "amd64" ]]; then
    pkgs="${pkgs//intel-media-va-driver-non-free/}"
  fi
  step "building the verification base image $IMAGE (once; 15-30 min under load)"
  say "  package set: $(wc -w <<<"$pkgs") packages from scripts/image-packages.txt + 4 verifier tools"
  local c="vulos-verify-base-$$"
  docker rm -f "$c" >/dev/null 2>&1 || true
  docker run -d --name "$c" debian:trixie sleep 10800 >/dev/null || die "cannot start debian:trixie"
  if ! docker exec -e PKGS="$pkgs" "$c" bash -c '
      set -e
      export DEBIAN_FRONTEND=noninteractive
      printf "force-unsafe-io\n" > /etc/dpkg/dpkg.cfg.d/99-unsafe-io
      printf "Acquire::Languages \"none\";\n" > /etc/apt/apt.conf.d/99-no-languages
      apt-get update -qq
      apt-get install -y --no-install-recommends $PKGS gnupg xz-utils file procps
      rm -rf /var/lib/apt/lists/*
      flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo
      echo SETUP_DONE'; then
    docker rm -f "$c" >/dev/null 2>&1 || true
    die "base image setup failed"
  fi
  docker commit -c 'CMD ["bash"]' "$c" "$IMAGE" >/dev/null || die "docker commit failed"
  docker rm -f "$c" >/dev/null 2>&1 || true
  say "  committed $IMAGE"
}

# stage_common — everything the container needs, read-only, in one directory.
# We copy rather than bind-mount the repo: eleven other agents are writing to
# this checkout right now and a torn read of registry.json would be a mystery.
stage_common() {
  local stage="$1" registry="${2:-$REPO_ROOT/registry.json}"
  rm -rf "$stage"; mkdir -p "$stage/keys"
  cp "$registry" "$stage/registry.json"
  cp "$REPO_ROOT/keys/trust-anchor.pub" "$stage/keys/trust-anchor.pub"
  cp "$REPO_ROOT/keys/release-cert.json" "$stage/keys/release-cert.json"
  cp "$SCRIPT_PATH" "$stage/verify-app-recipe.sh"
  build_driver "$(go_arch)" "$stage/driver"
}

# run_container <stage> <out> <mode> [args…]
run_container() {
  local stage="$1" out="$2"; shift 2
  mkdir -p "$out"
  # --privileged: flatpak's bwrap sandbox needs it to deploy and to `flatpak run`.
  # The container is thrown away at exit (--rm), so nothing survives it.
  docker run --rm --privileged \
    -v "$stage:/verify:ro" -v "$out:/out" \
    -e VULOS_TRUST_ANCHOR=/verify/keys/trust-anchor.pub \
    -e VULOS_RELEASE_CERT=/verify/keys/release-cert.json \
    -e VULOS_VERIFY_KEEP="${VULOS_VERIFY_KEEP:-0}" \
    "$IMAGE" bash /verify/verify-app-recipe.sh --in-container "$@"
}

# ─── registry / Flathub metadata (host side, read-only) ──────────────────────

app_json() { # app_json <app-id> → the entry's resolved facts as JSON
  python3 - "$REPO_ROOT/registry.json" "$1" <<'PY'
import json,sys
reg=json.load(open(sys.argv[1]))["apps"]
aid=sys.argv[2]
e=reg.get(aid)
if e is None:
    print(json.dumps({"error":"no such app id"})); sys.exit(0)
vers=e.get("versions") or {}
latest=sorted(vers,reverse=True)[0] if vers else ""
r=vers.get(latest) or {}
arch=e.get("arch") or []
ra=r.get("arch")
if isinstance(ra,str): ra=[ra]
if not arch and ra: arch=ra
print(json.dumps({
 "id":aid,"name":e.get("name"),"version":latest,"type":e.get("type"),
 "arch":arch,"source":e.get("source"),"verified":e.get("verified"),
 "proprietary":e.get("proprietary"),
 "disabled":bool(e.get("_disabled")) or bool(r.get("_disabled")),
 "flatpak_id":r.get("flatpak_id") or "","install":r.get("install") or "",
 "download_url":r.get("download_url") or "","command":r.get("command") or "",
 "signed":bool(e.get("signature")),
}))
PY
}

# META holds the current app's registry facts; meta_get pulls one key out of it
# (lists come back comma-joined).  Host side only — no jq dependency on macOS.
META=""
meta_get() {
  python3 -c '
import json,sys
v=json.load(sys.stdin).get(sys.argv[1])
print(",".join(v) if isinstance(v,list) else ("" if v is None else v))' "$1" <<<"$META"
}

flathub_meta() { # flathub_meta <flatpak-id> → {arches, download_mb, installed_mb, runtime_mb, verified}
  python3 - "$1" <<'PY'
import json,sys,urllib.request
fid=sys.argv[1]
def get(u):
    try:
        with urllib.request.urlopen(u,timeout=25) as r: return json.load(r)
    except Exception as e: return None
s=get(f"https://flathub.org/api/v2/summary/{fid}")
v=get(f"https://flathub.org/api/v2/verification/{fid}/status")
if s is None:
    print(json.dumps({"error":"flathub summary unavailable"})); sys.exit(0)
md=s.get("metadata") or {}
mb=lambda n:(round((n or 0)/1048576))
print(json.dumps({
 "arches":s.get("arches") or [],
 "download_mb":mb(s.get("download_size")),
 "installed_mb":mb(s.get("installed_size")),
 "runtime_mb":mb(md.get("runtimeInstalledSize")),
 "runtime":md.get("runtime") or "",
 "command":md.get("command") or "",
 "verified":(v or {}).get("verified"),
 "verified_method":(v or {}).get("method"),
}))
PY
}

# ─── ledger ──────────────────────────────────────────────────────────────────

ledger_put() { # ledger_put <json-row>
  python3 - "$LEDGER_JSON" "$1" <<'PY'
import json,os,sys
path,row=sys.argv[1],json.loads(sys.argv[2])
doc={"_comment":"Authoritative App Hub install-verification ledger. Written by scripts/verify-app-recipe.sh. Render the .md with --ledger-render.","rows":[]}
if os.path.exists(path):
    try: doc=json.load(open(path))
    except Exception: pass
rows=[r for r in doc.get("rows",[]) if r.get("id")!=row["id"]]
rows.append(row)
rows.sort(key=lambda r:r["id"])
doc["rows"]=rows
json.dump(doc,open(path,"w"),indent=2,sort_keys=True)
open(path,"a").write("\n")
PY
}

ledger_status() { # ledger_status <app-id> → status or empty
  python3 - "$LEDGER_JSON" "$1" <<'PY'
import json,os,sys
p,a=sys.argv[1],sys.argv[2]
if not os.path.exists(p): print(""); raise SystemExit
try: rows=json.load(open(p)).get("rows",[])
except Exception: rows=[]
print(next((r.get("status","") for r in rows if r.get("id")==a),""))
PY
}

ledger_render() {
  python3 - "$LEDGER_JSON" "$LEDGER_MD" <<'PY'
import json,os,sys
src,dst=sys.argv[1],sys.argv[2]
rows=[]
if os.path.exists(src):
    rows=json.load(open(src)).get("rows",[])
icon={"passed":"✅","failed":"❌","untestable-on-arm64":"⛔","skipped":"⏭","disabled":"🚫"}
out=[]
out.append("# App Hub — install verification ledger\n")
out.append("Generated from `roadmap/app-verification-ledger.json` by")
out.append("`scripts/verify-app-recipe.sh --ledger-render`. **Do not hand-edit this file** —")
out.append("edit nothing, run the harness. A row exists only because a container really ran.\n")
out.append("`passed` = the product's own installer (`appnet.InstallFromRegistry`) ran in a")
out.append("debian:trixie container, every assertion in the *Asserted* column held, and the")
out.append("app was then removed again. `untestable-on-arm64` = the upstream publishes no")
out.append("aarch64 build, so this machine cannot install it — that is a stated limit, **not**")
out.append("a pass and **not** a claim the app works. `disabled` = the entry carries")
out.append("`_disabled`, so the product refuses to install it by design — nothing was run.\n")
counts={}
for r in rows: counts[r.get("status","?")]=counts.get(r.get("status","?"),0)+1
out.append("| status | apps |")
out.append("| --- | --- |")
for k in sorted(counts): out.append(f"| {icon.get(k,'')} {k} | {counts[k]} |")
out.append("")
out.append("| App | Source | Arch | Verified | Status | Disk MB | Mins | Date | Asserted / why not |")
out.append("| --- | --- | --- | --- | --- | ---: | ---: | --- | --- |")
for r in sorted(rows,key=lambda r:(r.get("status",""),r.get("id",""))):
    a=r.get("assertions") or []
    detail=", ".join(a) if a else (r.get("note") or "")
    v=r.get("flathub_verified")
    vtxt={True:"yes",False:"**no**"}.get(v,"-")
    secs=r.get("seconds") or 0
    out.append("| `{}` | {} | {} | {} | {} {} | {} | {} | {} | {} |".format(
        r.get("id",""), r.get("source",""), r.get("arch") or "-", vtxt,
        icon.get(r.get("status",""),""), r.get("status",""),
        r.get("disk_delta_mb") or "", (round(secs/60) if secs else ""),
        (r.get("date") or "")[:10], detail))
out.append("")
open(dst,"w").write("\n".join(out))
print(f"wrote {dst} ({len(rows)} rows)")
PY
}

# ─── single-app verification ─────────────────────────────────────────────────

verify_one() {
  local app="$1" version="${2:-latest}"
  need docker; need python3; need go
  mkdir -p "$WORKDIR"

  META="$(app_json "$app")"
  [[ -z "$(meta_get error)" ]] || die "app id '$app' is not in $REPO_ROOT/registry.json"
  local fpid name src arch_decl
  fpid="$(meta_get flatpak_id)"; name="$(meta_get name)"; src="$(meta_get source)"
  arch_decl="$(meta_get arch)"
  [[ "$(meta_get signed)" == "True" ]] || die "entry '$app' has no signature — sign it (make sign-registry) before verifying"
  [[ -z "$src" ]] && src="$( [[ -n "$fpid" ]] && echo flathub || echo unclassified )"

  local date_disabled; date_disabled="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  # Administratively disabled entries (_disabled on the entry or the recipe) are
  # REFUSED by the product on purpose — 11 of 55 entries are in this state today.
  # Recording that as "failed" would read as a broken recipe, which is a
  # different and much louder claim than "nobody has turned this on".
  if [[ "$(meta_get disabled)" == "True" ]]; then
    say "  ${c_yel}SKIP${c_off} — entry is administratively disabled (_disabled); the product refuses to install it"
    ledger_put "$(python3 -c '
import json,sys
print(json.dumps({"id":sys.argv[1],"source":sys.argv[2],"arch":sys.argv[3],"status":"disabled",
 "date":sys.argv[4],"note":"_disabled is set on the entry or its latest recipe — appnet refuses the install by design; nothing was run",
 "harness":sys.argv[5],"assertions":[]}))' \
      "$app" "$src" "$(go_arch)" "$date_disabled" "$HARNESS_VERSION")"
    ledger_render >/dev/null
    return 3
  fi

  local host_arch fh_arch; host_arch="$(go_arch)"; fh_arch="$(flathub_arch "$host_arch")"
  [[ -n "$host_arch" ]] || die "cannot determine the container architecture from docker"

  step "$app ($name) — source=$src arch-declared=${arch_decl:-<none>} host=$host_arch"

  local date_now; date_now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  local dl_mb="" inst_mb="" rt_mb="" fh_verified=""
  # ── metadata checks that need no container ────────────────────────────────
  if [[ -n "$fpid" ]]; then
    local fh; fh="$(flathub_meta "$fpid")"
    local arches; arches="$(python3 -c 'import json,sys;print(",".join(json.load(sys.stdin).get("arches") or []))' <<<"$fh")"
    dl_mb="$(python3 -c 'import json,sys;print(json.load(sys.stdin).get("download_mb") or "")' <<<"$fh")"
    inst_mb="$(python3 -c 'import json,sys;print(json.load(sys.stdin).get("installed_mb") or "")' <<<"$fh")"
    rt_mb="$(python3 -c 'import json,sys;print(json.load(sys.stdin).get("runtime_mb") or "")' <<<"$fh")"
    fh_verified="$(python3 -c 'import json,sys;v=json.load(sys.stdin).get("verified");print("" if v is None else ("true" if v else "false"))' <<<"$fh")"
    say "  flathub: arches=[$arches] download=${dl_mb}MB installed=${inst_mb}MB runtime=${rt_mb}MB verified=$fh_verified"

    # The registry may not claim a publisher attestation the source does not
    # make.  `verified` is a trust claim shown in the UI; an entry asserting it
    # over Flathub's "not verified" is worse than an entry that says nothing.
    if [[ "$(meta_get verified)" == "True" && "$fh_verified" == "false" ]]; then
      say "  ${c_red}FAIL${c_off} — registry says verified:true, flathub's verification API says NOT verified"
      ledger_put "$(python3 -c '
import json,sys
print(json.dumps({"id":sys.argv[1],"source":sys.argv[2],"flatpak_id":sys.argv[3],"arch":sys.argv[4],
 "status":"failed","date":sys.argv[5],"flathub_verified":False,
 "note":"registry claims verified:true but flathub/api/v2/verification/"+sys.argv[3]+"/status reports NOT verified — nothing was installed",
 "harness":sys.argv[6],"assertions":[]}))' \
        "$app" "$src" "$fpid" "$host_arch" "$date_now" "$HARNESS_VERSION")"
      ledger_render >/dev/null
      return 1
    fi

    if [[ -n "$arches" ]]; then
      # DEFECT CHECK: the registry must not offer an app on an arch Flathub does
      # not build.  An entry that appears on an arm64 box and cannot install is
      # a bug, and this is the cheapest place to catch it.
      if [[ ",$arches," != *",$fh_arch,"* ]]; then
        if [[ ",$arch_decl," == *",$host_arch,"* || -z "$arch_decl" ]]; then
          say "  ${c_yel}registry defect:${c_off} entry offers $host_arch (arch=${arch_decl:-<unset> = all}) but flathub builds only [$arches]"
        fi
        local defect=""
        if [[ ",$arch_decl," == *",$host_arch,"* || -z "$arch_decl" ]]; then
          defect=" REGISTRY DEFECT: the entry offers $host_arch (arch=${arch_decl:-unset, meaning all}) and cannot install there — set arch to the set flathub actually builds."
        fi
        say "  ${c_yel}SKIP${c_off} — no $fh_arch build upstream; cannot be install-tested on this machine"
        ledger_put "$(python3 -c '
import json,sys
print(json.dumps({"id":sys.argv[1],"source":sys.argv[2],"flatpak_id":sys.argv[3],"arch":sys.argv[4],
 "status":"untestable-on-arm64" if sys.argv[4]=="arm64" else "skipped","date":sys.argv[5],
 "note":"flathub publishes only ["+sys.argv[6]+"] — no "+sys.argv[7]+" build exists to install."+sys.argv[10],
 "flathub_verified":{"true":True,"false":False}.get(sys.argv[8]),
 "harness":sys.argv[9],"assertions":[]}))' \
          "$app" "$src" "$fpid" "$host_arch" "$date_now" "$arches" "$fh_arch" "$fh_verified" "$HARNESS_VERSION" "$defect")"
        ledger_render >/dev/null
        return 3
      fi
    fi
  fi

  if [[ -n "$arch_decl" && ",$arch_decl," != *",$host_arch,"* ]]; then
    say "  ${c_yel}SKIP${c_off} — entry declares arch=[$arch_decl], this host is $host_arch"
    ledger_put "$(python3 -c '
import json,sys
print(json.dumps({"id":sys.argv[1],"source":sys.argv[2],"arch":sys.argv[3],
 "status":"untestable-on-arm64" if sys.argv[3]=="arm64" else "skipped","date":sys.argv[4],
 "note":"registry declares arch=["+sys.argv[5]+"]; this machine is "+sys.argv[3],
 "harness":sys.argv[6],"assertions":[]}))' \
      "$app" "$src" "$host_arch" "$date_now" "$arch_decl" "$HARNESS_VERSION")"
    ledger_render >/dev/null
    return 3
  fi

  ensure_image
  # A FRESH results directory per run, never a re-used path.  Deleting and
  # re-creating a bind-mount source leaves OrbStack's file sharing holding the
  # old inode: every write inside the container then fails ENOENT on a path that
  # plainly exists on the host, and it surfaces as a bogus "install path failed"
  # for an app that installed fine an hour earlier.  Chasing that as a recipe
  # defect would have been an hour wasted on the wrong layer.
  local stage="$WORKDIR/stage-$app"
  local out="$WORKDIR/out/$app/$(date -u +%Y%m%d-%H%M%S)"
  mkdir -p "$out"
  # keep the last three runs per app, discard older ones
  ls -1dt "$WORKDIR/out/$app"/* 2>/dev/null | tail -n +4 | while read -r old; do rm -rf "$old"; done
  stage_common "$stage"

  local rc=0
  run_container "$stage" "$out" verify "$app" "$version" || rc=$?

  local secs="" dmb=""
  if [[ -f "$out/metrics.env" ]]; then
    # shellcheck disable=SC1090
    source "$out/metrics.env"
    secs="${seconds:-}"
    dmb=$(( ${disk_after_mb:-0} - ${disk_before_mb:-0} ))
  fi
  local asserts=""
  [[ -f "$out/assertions.tsv" ]] && asserts="$(awk -F'\t' '$1=="OK"{printf "%s ",$2}' "$out/assertions.tsv")"
  local failed=""
  [[ -f "$out/assertions.tsv" ]] && failed="$(awk -F'\t' '$1=="FAIL"{printf "%s: %s; ",$2,$3}' "$out/assertions.tsv")"

  local status; if [[ $rc -eq 0 ]]; then status=passed; else status=failed; fi
  ledger_put "$(python3 -c '
import json,sys
print(json.dumps({"id":sys.argv[1],"source":sys.argv[2],"flatpak_id":sys.argv[3],"arch":sys.argv[4],
 "status":sys.argv[5],"date":sys.argv[6],"seconds":int(sys.argv[7] or 0),
 "installed_mb":int(sys.argv[8] or 0),"download_mb":int(sys.argv[9] or 0),
 "disk_delta_mb":int(sys.argv[10] or 0),
 "flathub_verified":{"true":True,"false":False}.get(sys.argv[11]),
 "assertions":[a for a in sys.argv[12].split() if a],"note":sys.argv[13],
 "harness":sys.argv[14]}))' \
    "$app" "$src" "$fpid" "$host_arch" "$status" "$date_now" "${secs:-0}" \
    "${inst_mb:-0}" "${dl_mb:-0}" "${dmb:-0}" "$fh_verified" "$asserts" "$failed" "$HARNESS_VERSION")"
  ledger_render >/dev/null

  if [[ $rc -eq 0 ]]; then
    printf '\n%sPASS%s %s — %ss, %s MB installed in-container, cleaned up\n' "$c_grn" "$c_off" "$app" "${secs:-?}" "${dmb:-?}"
  else
    printf '\n%sFAIL%s %s — %s\n' "$c_red" "$c_off" "$app" "${failed:-install path failed}"
  fi
  say "  ledger: $LEDGER_JSON   logs: $out"
  return $rc
}

# ─── plan / sweep ────────────────────────────────────────────────────────────

plan() { # emit "size_mb<TAB>app<TAB>source<TAB>note", smallest first
  local limit="${1:-999}"
  python3 - "$REPO_ROOT/registry.json" "$LEDGER_JSON" "$(flathub_arch "$(go_arch)")" "$limit" <<'PY'
import json,os,sys,urllib.request
reg=json.load(open(sys.argv[1]))["apps"]
done={}
if os.path.exists(sys.argv[2]):
    try: done={r["id"]:r.get("status") for r in json.load(open(sys.argv[2])).get("rows",[])}
    except Exception: pass
arch=sys.argv[3]; limit=int(sys.argv[4])
def summary(fid):
    try:
        with urllib.request.urlopen(f"https://flathub.org/api/v2/summary/{fid}",timeout=25) as r:
            return json.load(r)
    except Exception: return None
rows=[]
for aid,e in reg.items():
    if done.get(aid) in ("passed","untestable-on-arm64","disabled"): continue
    vers=e.get("versions") or {}
    if not vers: continue
    r=vers[sorted(vers,reverse=True)[0]]
    # _disabled entries are refused by the product by design — planning a
    # container for them wastes ~20s each and produces a row saying nothing.
    if e.get("_disabled") or r.get("_disabled"):
        continue
    fid=r.get("flatpak_id") or ""
    if fid:
        s=summary(fid)
        if s is None:
            rows.append((10**9,aid,"flathub","flathub metadata unavailable")); continue
        if arch not in (s.get("arches") or []):
            rows.append((-1,aid,"flathub",f"NO {arch} BUILD — untestable here")); continue
        md=s.get("metadata") or {}
        mb=round((s.get("installed_size") or 0)/1048576)+round((md.get("runtimeInstalledSize") or 0)/1048576)
        rows.append((mb,aid,"flathub",f"app+runtime ≈ {mb} MB, download {round((s.get('download_size') or 0)/1048576)} MB"))
    elif r.get("artifacts"):
        rows.append((300,aid,"vulos-native",
                     "pinned per-arch artefacts: " + ",".join(sorted(r["artifacts"]))))
    elif r.get("download_url"):
        rows.append((300,aid,"vendor-download","REFUSED by the installer (DOWNLOAD-01)"))
    elif (r.get("install") or "").strip():
        rows.append((400,aid,"shell/apt","REFUSED by the installer (INSTALL-01)"))
    else:
        rows.append((200,aid,"other","size unknown"))
rows.sort()
for mb,aid,src,note in rows[:limit]:
    print(f"{mb}\t{aid}\t{src}\t{note}")
PY
}

sweep() {
  local limit="${1:-5}" maxmb="${2:-4000}"
  need docker; need python3
  mkdir -p "$WORKDIR"
  local lock="$WORKDIR/sweep.lock"
  mkdir "$lock" 2>/dev/null || die "another sweep is running (remove $lock if it is stale)"
  trap 'rmdir "$lock" 2>/dev/null || true' EXIT

  step "planning (smallest first, skipping anything already passed/untestable)"
  local planfile="$WORKDIR/plan.tsv"
  plan 999 > "$planfile"
  local n=0
  while IFS=$'\t' read -r mb app src note; do
    [[ -z "$app" ]] && continue
    if [[ "$mb" -lt 0 ]]; then
      say "${c_yel}·${c_off} $app — $note (recording, not running)"
      verify_one "$app" latest || true
      continue
    fi
    if [[ "$mb" -gt "$maxmb" ]]; then
      say "${c_dim}· $app — ${mb} MB exceeds --max-installed-mb $maxmb, left for later${c_off}"
      continue
    fi
    n=$((n+1))
    [[ $n -gt $limit ]] && break
    say ""
    say "═══ [$n/$limit] $app  (~${mb} MB, $src) ═══"
    verify_one "$app" latest || true
    docker system df 2>/dev/null | sed -n '2,3p' | sed 's/^/  docker: /' || true
  done < "$planfile"
  step "sweep finished — $LEDGER_MD"
}

# ─── driver source (generated; never written into the repo) ──────────────────
emit_driver_source() {
cat <<'GO_EOF'
// Code generated by scripts/verify-app-recipe.sh. DO NOT EDIT, DO NOT COMMIT.
//
// A thin driver around the PRODUCT'S OWN installer.  It contains no install
// logic of its own: the whole point is that appnet.InstallFromRegistry — the
// same function cmd/server's POST /api/store/install reaches through
// AppStore.InstallFromRegistry — does the work, so that the harness cannot pass
// while the product is broken.
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
	"sort"
	"strings"
	"time"

	"vulos/backend/services/appnet"
	"vulos/backend/services/signing"
)

// artifactArches lists the architectures a recipe pins, comma-joined, so the
// shell side can tell a Vulos-native recipe from every other shape without
// re-parsing the registry. Empty for a recipe that pins none.
func artifactArches(r *appnet.VersionRecipe) string {
	if len(r.Artifacts) == 0 {
		return ""
	}
	out := make([]string, 0, len(r.Artifacts))
	for a := range r.Artifacts {
		out = append(out, appnet.NormalizeArch(a))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func main() {
	registryPath := flag.String("registry", "/verify/registry.json", "registry.json to install from")
	appID := flag.String("app", "", "app id")
	version := flag.String("version", "latest", "version")
	appsDir := flag.String("apps-dir", "/var/lib/vulos/apps", "install root")
	factsPath := flag.String("facts", "", "write resolved recipe facts here")
	uninstall := flag.Bool("uninstall", false, "remove instead of install")
	makeSelfTest := flag.String("make-selftest", "", "write the self-test fixture into this dir")
	flag.Parse()

	if *makeSelfTest != "" {
		if err := writeSelfTest(*makeSelfTest); err != nil {
			fmt.Fprintln(os.Stderr, "selftest fixture:", err)
			os.Exit(2)
		}
		return
	}

	reg, err := appnet.LoadRegistry(*registryPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load registry:", err)
		os.Exit(2)
	}
	entry, ok := reg.Apps[*appID]
	if !ok {
		fmt.Fprintf(os.Stderr, "no such app %q in %s\n", *appID, *registryPath)
		os.Exit(2)
	}
	v := *version
	if v == "" || v == "latest" {
		v = entry.LatestVersion()
	}
	recipe := entry.GetRecipe(v)
	if recipe == nil {
		fmt.Fprintf(os.Stderr, "app %q has no version %q\n", *appID, v)
		os.Exit(2)
	}

	if *uninstall {
		if recipe.FlatpakID != "" {
			if err := appnet.FlatpakUninstall(context.Background(), recipe.FlatpakID); err != nil {
				fmt.Fprintln(os.Stderr, "uninstall:", err)
				os.Exit(1)
			}
		}
		os.RemoveAll(filepath.Join(*appsDir, *appID))
		fmt.Println("UNINSTALL-OK")
		return
	}

	// Facts are the RESOLVED recipe as the installer itself sees it — the
	// assertion stage reads these rather than re-parsing registry.json, so it
	// can never assert against a different recipe than the one installed.
	manifestCmd := recipe.Command
	if recipe.FlatpakID != "" && manifestCmd == "" {
		manifestCmd = appnet.FlatpakRunCommand(recipe.FlatpakID)
	}
	facts := map[string]any{
		"app_id":           *appID,
		"version":          v,
		"type":             entry.Type,
		"arch":             entry.Arch,
		"flatpak_id":       recipe.FlatpakID,
		"install":          recipe.Install,
		"download_url":     recipe.DownloadURL,
		"checksum":         recipe.Checksum,
		"artifacts_arches": artifactArches(recipe),
		"extract_dir":      recipe.ExtractDir,
		"command":          recipe.Command,
		"manifest_command": manifestCmd,
		"deps":             recipe.Deps,
		"app_dir":          filepath.Join(*appsDir, *appID),
	}
	// Fields the Go structs do not model yet (see roadmap/APP-RECIPE-STANDARD.md)
	// still round-trip through Extra, so the harness can assert on them today.
	for _, k := range []string{"packages", "flatpak_runtime", "source", "verified", "proprietary"} {
		if raw, ok := recipe.Extra[k]; ok {
			var val any
			if json.Unmarshal(raw, &val) == nil {
				facts[k] = val
			}
		}
		if raw, ok := entry.Extra[k]; ok {
			var val any
			if json.Unmarshal(raw, &val) == nil {
				facts[k] = val
			}
		}
	}
	if *factsPath != "" {
		b, _ := json.MarshalIndent(facts, "", "  ")
		if err := os.WriteFile(*factsPath, append(b, '\n'), 0644); err != nil {
			fmt.Fprintln(os.Stderr, "write facts:", err)
			os.Exit(2)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	if err := appnet.InstallFromRegistry(ctx, reg, *appID, v, *appsDir); err != nil {
		fmt.Fprintln(os.Stderr, "INSTALL-FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("INSTALL-OK", *appID, v)
}

// writeSelfTest builds a throwaway trust root and a five-entry registry that
// proves the harness can go red.  The keys are generated fresh and thrown away
// with the container: the repo's real signing keys are never used here, and
// registry.json is never touched.
func writeSelfTest(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	relPub, relPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	cert, err := signing.IssueReleaseCert(rootPriv, relPub, "selftest-ephemeral", time.Now().Add(24*time.Hour), 0)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "trust-anchor.pub"), []byte(signing.EncodeAnchor(rootPub)), 0644); err != nil {
		return err
	}
	cb, err := json.MarshalIndent(cert, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "release-cert.json"), append(cb, '\n'), 0644); err != nil {
		return err
	}

	mk := func(name string, r *appnet.VersionRecipe) *appnet.RegistryEntry {
		return &appnet.RegistryEntry{
			Name: name, Vetted: true, Type: "service", Category: "selftest",
			Author: "Vulos harness self-test", Versions: map[string]*appnet.VersionRecipe{"latest": r},
		}
	}
	apps := map[string]*appnet.RegistryEntry{
		// The control: a real, tiny apt install that must PASS.  Without it, a
		// harness that failed everything would look like a working harness.
		"selftest-good": mk("Self-test control", &appnet.VersionRecipe{
			Install: "apt-get install -y --no-install-recommends hello",
			Command: "hello",
		}),
		// Must fail: no publisher signature (left unsigned below).
		"selftest-unsigned": mk("Self-test unsigned", &appnet.VersionRecipe{
			Install: "true", Command: "/bin/echo",
		}),
		// Must fail: the flathub id does not exist.
		"selftest-bad-flatpak": mk("Self-test bad flathub id", &appnet.VersionRecipe{
			FlatpakID: "org.vulos.selftest.NoSuchApplication",
		}),
		// Must fail: checksum does not match the bytes.
		"selftest-bad-checksum": mk("Self-test bad checksum", &appnet.VersionRecipe{
			DownloadURL: "https://deb.debian.org/debian/dists/trixie/Release",
			Checksum:    "0000000000000000000000000000000000000000000000000000000000000000",
			Command:     "bin/Release",
		}),
		// Must fail at the ASSERTION stage: installs cleanly, but the command
		// the launcher would run does not exist.  This is the one that proves
		// the assertions do work, not just the install path.
		"selftest-bad-command": mk("Self-test missing command", &appnet.VersionRecipe{
			Install: "apt-get install -y --no-install-recommends hello",
			Command: "vulos-selftest-command-that-does-not-exist",
		}),
	}
	reg := &appnet.Registry{Apps: apps}
	for id, e := range apps {
		if id == "selftest-unsigned" {
			continue
		}
		if err := appnet.SignEntry(e, id, relPriv); err != nil {
			return err
		}
	}
	return appnet.SaveRegistry(filepath.Join(dir, "registry.json"), reg)
}
GO_EOF
}

self_test() {
  need docker; need python3; need go
  mkdir -p "$WORKDIR"
  ensure_image
  local stage="$WORKDIR/stage-selftest"
  local out="$WORKDIR/out/selftest/$(date -u +%Y%m%d-%H%M%S)"
  mkdir -p "$out"
  stage_common "$stage"
  step "self-test: five synthetic recipes — one must go green, four must go red"
  run_container "$stage" "$out" selftest
}

usage() {
  sed -n '2,60p' "$SCRIPT_PATH" | sed 's/^# \{0,1\}//'
  exit 2
}

# ─── argument parsing ────────────────────────────────────────────────────────
APP=""; VERSION="latest"; LIMIT=5; MAXMB=4000; MODE=one
while [[ $# -gt 0 ]]; do
  case "$1" in
    --self-test)        MODE=selftest; shift ;;
    --plan)             MODE=plan; shift ;;
    --sweep)            MODE=sweep; shift ;;
    --ledger-render)    MODE=render; shift ;;
    --version)          VERSION="$2"; shift 2 ;;
    --limit)            LIMIT="$2"; shift 2 ;;
    --max-installed-mb) MAXMB="$2"; shift 2 ;;
    --keep)             export VULOS_VERIFY_KEEP=1; shift ;;
    -h|--help)          usage ;;
    -*)                 die "unknown flag: $1" ;;
    *)                  APP="$1"; shift ;;
  esac
done

case "$MODE" in
  selftest) self_test ;;
  plan)     need python3; plan "$LIMIT" | awk -F'\t' 'BEGIN{printf "%8s  %-22s %-18s %s\n","MB","APP","SOURCE","NOTE"}{printf "%8s  %-22s %-18s %s\n",$1,$2,$3,$4}' ;;
  sweep)    sweep "$LIMIT" "$MAXMB" ;;
  render)   ledger_render ;;
  one)      [[ -n "$APP" ]] || usage; verify_one "$APP" "$VERSION" ;;
esac
