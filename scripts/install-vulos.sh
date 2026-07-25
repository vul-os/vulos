#!/usr/bin/env bash
# install-vulos.sh — idempotent meta-bundle installer for Vulos (OS + lilmail + Ofisi)
# Usage:  curl -fsSL https://get.vulos.org | sudo bash
#         curl -fsSL https://get.vulos.org | sudo bash -s -- --dry-run
#         curl -fsSL https://get.vulos.org | sudo bash -s -- --storage=minio
#
# Installs and supervises three co-located services on one machine:
#   1. vulos    — Vulos OS backend (API gateway + app fabric)
#   2. lilmail  — self-hosted mail/calendar/contacts CLIENT (github.com/vul-os/lilmail)
#                 — connects OUTBOUND to the box owner's OWN IMAP/SMTP/CalDAV/
#                   CardDAV account; it hosts no mail and binds no privileged port.
#   3. ofisi    — collaborative office suite backend (github.com/vul-os/ofisi)
#                 — binary/module name is still literally "vulos-office" (kept
#                   deliberately by upstream to avoid a churny rename)
#
# Shared infrastructure:
#   /etc/vulos/            — unified config directory
#   /var/lib/vulos/        — unified data directory
#   /etc/vulos/fabric.yaml — shared fabric identity + mesh credentials
#   /etc/vulos/storage.yaml — shared S3/MinIO storage selector
#
# There is no Vulos Cloud and nothing here is operated by us: this installs
# software the box owner runs on their own hardware or a VPS they rent.
# Reachability (so the box is dialable from outside) is via Ephor
# (github.com/vul-os/ephor), an open, self-hostable broker the box dials OUT
# to — configure it yourself; this installer does not provision it.
#
# Supports:
#   Linux x86_64 / arm64 — Debian/Ubuntu, Fedora/RHEL, Arch, Alpine
#   macOS / Windows → print Docker path and exit (not natively supported)
#   systemd, OpenRC, or no-init (warn and install binaries only)
#
# Exit codes:  0 success | 1 unsupported platform or fatal error
#
# Security hardening:
#   - SHA-256 verification is MANDATORY for every downloaded binary (no skip)
#   - Services run as non-root 'vulos' system account (UID < 1000)
#   - UID-collision guard: aborts if 'vulos' maps to a non-system account
#   - Symlink-safe directory creation (aborts on /etc/vulos symlink)
#   - No capabilities for vulos, lilmail, or ofisi — none of the three binds a
#     privileged (< 1024) port, so CapabilityBoundingSet is empty for all three
#   - ProtectSystem=strict, PrivateTmp=yes, NoNewPrivileges=yes for all units

set -euo pipefail

# ── Flags ─────────────────────────────────────────────────────────────────────

DRY_RUN=false
STORAGE_MODE="tigris"    # tigris | minio | local
INSTALL_MINIO=false
SKIP_ENABLE=false        # do not enable/start services (CI / container use)

for arg in "$@"; do
  case "${arg}" in
    --dry-run)           DRY_RUN=true ;;
    --storage=minio)     STORAGE_MODE="minio"; INSTALL_MINIO=true ;;
    --storage=local)     STORAGE_MODE="minio"; INSTALL_MINIO=true ;;  # alias
    --storage=tigris)    STORAGE_MODE="tigris" ;;
    --no-enable)         SKIP_ENABLE=true ;;
    --help|-h)
      printf "Usage: install-vulos.sh [--dry-run] [--storage=tigris|minio] [--no-enable]\n"
      exit 0
      ;;
    *)
      printf "Unknown argument: %s\n" "${arg}" >&2
      exit 1
      ;;
  esac
done

# ── Colour helpers ────────────────────────────────────────────────────────────

if [ -t 1 ] && command -v tput >/dev/null 2>&1; then
  RED=$(tput setaf 1)
  GRN=$(tput setaf 2)
  YEL=$(tput setaf 3)
  CYN=$(tput setaf 6)
  BLD=$(tput bold)
  RST=$(tput sgr0)
else
  RED='' GRN='' YEL='' CYN='' BLD='' RST=''
fi

info()  { printf "${GRN}[vulos]${RST} %s\n" "$*"; }
warn()  { printf "${YEL}[vulos] WARN:${RST} %s\n" "$*" >&2; }
fatal() { printf "${RED}[vulos] FATAL:${RST} %s\n" "$*" >&2; exit 1; }
step()  { printf "\n${BLD}${CYN}==> %s${RST}\n" "$*"; }
plan()  { printf "${YEL}  [DRY-RUN]${RST} %s\n" "$*"; }

# ── Constants ─────────────────────────────────────────────────────────────────

VULOS_USER="vulos"
VULOS_GROUP="vulos"

# Shared dirs (co-located bundle — all three services share these)
CONFIG_DIR="/etc/vulos"
DATA_DIR="/var/lib/vulos"

# Per-service config files (under the shared CONFIG_DIR)
FABRIC_CONFIG="${CONFIG_DIR}/fabric.yaml"
STORAGE_CONFIG="${CONFIG_DIR}/storage.yaml"
OS_CONFIG="${CONFIG_DIR}/vulos.yaml"
OFISI_CONFIG="${CONFIG_DIR}/office.yaml"
BUNDLE_CONFIG="${CONFIG_DIR}/bundle.yaml"

# lilmail is a client, not a server: it hard-codes a literal "config.toml" in
# its current working directory (no --config flag exists), so its config
# lives in its own directory rather than a single file under CONFIG_DIR.
LILMAIL_CONFIG_DIR="${CONFIG_DIR}/lilmail"
LILMAIL_CONFIG="${LILMAIL_CONFIG_DIR}/config.toml"

# Binary install paths
BIN_VULOS="/usr/local/bin/vulos"
BIN_LILMAIL="/usr/local/bin/lilmail"
# Ofisi's binary is still literally named "vulos-office" upstream (kept
# deliberately to avoid a churny module/binary rename) — see github.com/vul-os/ofisi.
BIN_OFISI="/usr/local/bin/vulos-office"

# GitHub release endpoints. The OS backend is fetched as a signed release
# artefact; lilmail and ofisi are built from source (REPO_* below).
GITHUB_VULOS="https://github.com/vul-os/vulos/releases"

API_VULOS="https://api.github.com/repos/vul-os/vulos/releases/latest"
API_LILMAIL="https://api.github.com/repos/vul-os/lilmail/releases/latest"
API_OFISI="https://api.github.com/repos/vul-os/ofisi/releases/latest"

# Source repositories. lilmail and ofisi ship source releases (their embedded
# assets are committed at the tag), so the bundle builds them from a pinned tag
# on the box — the same binary you would build by hand. Requires the Go
# toolchain and git; both are checked in preflight.
REPO_LILMAIL="https://github.com/vul-os/lilmail.git"
REPO_OFISI="https://github.com/vul-os/ofisi.git"

# systemd unit files
SYSTEMD_DIR="/etc/systemd/system"
UNIT_FABRIC="${SYSTEMD_DIR}/vulos-fabric.service"
UNIT_OS="${SYSTEMD_DIR}/vulos.service"
UNIT_LILMAIL="${SYSTEMD_DIR}/vulos-lilmail.service"
UNIT_OFISI="${SYSTEMD_DIR}/vulos-ofisi.service"
UNIT_MINIO="${SYSTEMD_DIR}/vulos-minio.service"
UNIT_BUNDLE="${SYSTEMD_DIR}/vulos-bundle.target"

# OpenRC init scripts
OPENRC_DIR="/etc/init.d"

# ── Dry-run: print plan and exit ──────────────────────────────────────────────

if [ "${DRY_RUN}" = "true" ]; then
  printf "\n${BLD}${CYN}Vulos Bundle Installer — DRY-RUN PLAN${RST}\n"
  printf "${CYN}%s${RST}\n\n" "$(printf '%.0s─' {1..55})"

  printf "${BLD}Services to be installed:${RST}\n"
  plan "vulos          — OS backend API gateway (port 8443)"
  plan "lilmail        — mail/calendar/contacts client, connects to your OWN"
  plan "                 IMAP/SMTP/CalDAV/CardDAV account (no inbound ports)"
  plan "ofisi          — office suite backend (port 8445)"
  if [ "${INSTALL_MINIO}" = "true" ]; then
    plan "minio          — local S3-compatible object storage (port 9000)"
  fi

  printf "\n${BLD}Shared directories:${RST}\n"
  plan "Config dir:   ${CONFIG_DIR}/"
  plan "Data dir:     ${DATA_DIR}/"
  plan "  ${DATA_DIR}/vulos/        — OS backend data"
  plan "  ${DATA_DIR}/lilmail/      — lilmail durable store (bbolt) + cache"
  plan "  ${DATA_DIR}/office/       — office suite data + uploads"
  if [ "${INSTALL_MINIO}" = "true" ]; then
    plan "  ${DATA_DIR}/minio/        — MinIO object store data"
  fi

  printf "\n${BLD}Shared config files:${RST}\n"
  plan "${FABRIC_CONFIG}    — fabric mesh identity"
  plan "${STORAGE_CONFIG}  — S3 backend selector: ${STORAGE_MODE}"
  plan "${OS_CONFIG}        — vulos OS backend config"
  plan "${LILMAIL_CONFIG}  — lilmail config (your IMAP/SMTP account — edit before starting)"
  plan "${OFISI_CONFIG}    — ofisi config"
  plan "${BUNDLE_CONFIG}    — bundle-level metadata"

  printf "\n${BLD}Storage backend:${RST}\n"
  if [ "${STORAGE_MODE}" = "tigris" ]; then
    plan "Tigris (S3-compatible hosted object storage — https://www.tigrisdata.com)"
    plan "Credentials: set TIGRIS_ACCESS_KEY and TIGRIS_SECRET_KEY in ${STORAGE_CONFIG}"
  else
    plan "Local MinIO (BYO — installed at /usr/local/bin/minio)"
    plan "API endpoint: http://127.0.0.1:9000"
    plan "Data:         ${DATA_DIR}/minio/"
  fi

  printf "\n${BLD}Service ordering (systemd):${RST}\n"
  plan "network-online.target"
  if [ "${INSTALL_MINIO}" = "true" ]; then
    plan "  → vulos-minio.service   (local object store)"
  fi
  plan "  → vulos-fabric.service  (shared mesh identity / fabric)"
  plan "  → vulos.service         (OS backend)"
  plan "  → vulos-lilmail.service (mail/calendar/contacts client — no capabilities)"
  plan "  → vulos-ofisi.service   (office backend)"
  plan "  → vulos-bundle.target   (all-up sentinel)"

  printf "\n${BLD}Security hardening:${RST}\n"
  plan "All services run as non-root user '${VULOS_USER}' (system UID < 1000)"
  plan "NoNewPrivileges=yes, ProtectSystem=strict, PrivateTmp=yes"
  plan "No capabilities — vulos, lilmail, ofisi, fabric (none binds a privileged port)"
  plan "SHA-256 checksum mandatory for every binary (no skip path)"
  plan "Symlink-safe directory creation (abort on /etc/vulos symlink)"
  plan "UID-collision guard: abort if 'vulos' maps to UID >= 1000"

  printf "\n${BLD}Next steps after install:${RST}\n"
  plan "1. Edit ${CONFIG_DIR}/fabric.yaml — set your domain"
  plan "2. Edit ${LILMAIL_CONFIG} — set your own IMAP/SMTP account"
  plan "3. Enable services:  systemctl enable --now vulos-bundle.target"
  plan "4. Reachability from outside your network is BYO: point a self-hosted"
  plan "   Ephor broker (github.com/vul-os/ephor) at this box — there is no"
  plan "   Vulos Cloud and nothing here is operated by us."

  printf "\n${GRN}Dry-run complete. No changes made.${RST}\n\n"
  exit 0
fi

# ── Privilege check ───────────────────────────────────────────────────────────

if [ "$(id -u)" -ne 0 ]; then
  fatal "This installer must be run as root (try: sudo bash $0)"
fi

# ── Platform detection ────────────────────────────────────────────────────────

step "Detecting platform"

OS_TYPE="$(uname -s)"
ARCH="$(uname -m)"

case "${OS_TYPE}" in
  Darwin)
    printf "\n${YEL}macOS detected.${RST}\n"
    printf "Vulos Bundle runs natively on Linux only.\n"
    printf "On macOS, use Docker Compose:\n\n"
    printf "  ${CYN}curl -fsSL https://get.vulos.org/bundle/docker-compose.yml | docker compose up -d${RST}\n\n"
    printf "See https://docs.vulos.org/self-host/bundle for full instructions.\n"
    exit 1
    ;;
  MINGW*|MSYS*|CYGWIN*|Windows_NT)
    printf "\n${YEL}Windows detected.${RST}\n"
    printf "Vulos Bundle runs natively on Linux only.\n"
    printf "On Windows, use Docker Desktop with the compose file above.\n"
    exit 1
    ;;
  Linux)
    info "Linux detected — continuing."
    ;;
  *)
    fatal "Unsupported OS: ${OS_TYPE}. Only Linux is supported for native install."
    ;;
esac

# Architecture normalisation
case "${ARCH}" in
  x86_64|amd64)  GOARCH="amd64" ;;
  aarch64|arm64) GOARCH="arm64" ;;
  *)
    fatal "Unsupported architecture: ${ARCH}. Supported: x86_64 (amd64), aarch64 (arm64)."
    ;;
esac

info "Architecture: ${GOARCH}"

# ── Init system detection ─────────────────────────────────────────────────────

step "Detecting init system"

INIT_SYSTEM="none"
if command -v systemctl >/dev/null 2>&1 && systemctl --version >/dev/null 2>&1; then
  INIT_SYSTEM="systemd"
elif command -v openrc >/dev/null 2>&1 || [ -d /etc/init.d ]; then
  if command -v openrc >/dev/null 2>&1; then
    INIT_SYSTEM="openrc"
  fi
fi

case "${INIT_SYSTEM}" in
  systemd) info "Init: systemd" ;;
  openrc)  info "Init: OpenRC" ;;
  none)
    warn "No recognised init system (systemd/OpenRC). Binaries installed but no services registered."
    ;;
esac

# ── Linux distribution detection ─────────────────────────────────────────────

step "Detecting Linux distribution"

DISTRO="unknown"
if [ -f /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  DISTRO="${ID:-unknown}"
fi

case "${DISTRO}" in
  ubuntu|debian|linuxmint|pop)
    info "Distribution: Debian/Ubuntu family"
    PKG_INSTALL="apt-get install -y"
    ;;
  fedora|rhel|centos|rocky|almalinux)
    info "Distribution: Fedora/RHEL family"
    PKG_INSTALL="dnf install -y"
    ;;
  arch|manjaro|endeavouros)
    info "Distribution: Arch family"
    PKG_INSTALL="pacman -S --noconfirm"
    ;;
  alpine)
    info "Distribution: Alpine"
    PKG_INSTALL="apk add"
    ;;
  *)
    info "Distribution: ${DISTRO} (generic)"
    PKG_INSTALL=""
    ;;
esac

# Ensure required tools are present
for tool in curl sha256sum; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    warn "${tool} not found."
    if [ -n "${PKG_INSTALL}" ]; then
      info "Attempting to install ${tool}..."
      ${PKG_INSTALL} "${tool}" || warn "Could not auto-install ${tool} — please install it manually."
    fi
  fi
done

# lilmail and ofisi are built from source on the box, so the Go toolchain and
# git must be present. These are not auto-installed: a distro's `go` package is
# often older than the module's `go` directive, which fails the build in a
# confusing way — better to ask the operator to install a current toolchain.
for tool in go git; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    fatal "${tool} is required to build the office and mail services from source.\nInstall a current Go toolchain (https://go.dev/dl/) and git, then re-run."
  fi
done

# ── Resolve release tags ──────────────────────────────────────────────────────

step "Resolving latest release tags"

resolve_tag() {
  local api_url="$1"
  local fallback="$2"
  local tag=""
  if command -v curl >/dev/null 2>&1; then
    tag="$(curl -fsSL "${api_url}" 2>/dev/null \
      | grep '"tag_name"' \
      | head -1 \
      | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')"
  fi
  if [ -z "${tag}" ]; then
    warn "Could not auto-detect latest release from ${api_url}. Falling back to ${fallback}."
    tag="${fallback}"
  fi
  printf "%s" "${tag}"
}

TAG_VULOS="$(resolve_tag "${API_VULOS}" "v0.1.0")"
TAG_LILMAIL="$(resolve_tag "${API_LILMAIL}" "v0.1.0")"
TAG_OFISI="$(resolve_tag "${API_OFISI}" "v0.1.0")"

info "vulos        release: ${TAG_VULOS}"
info "lilmail      release: ${TAG_LILMAIL}"
info "ofisi        release: ${TAG_OFISI}"

# ── Download + verify helper ──────────────────────────────────────────────────

# download_and_verify <binary_name> <binary_url> <checksum_url> <dest_path>
#
# Mandatory SHA-256 verification: a checksum fetch failure, a missing entry,
# or a mismatch all abort the installation.  There is NO skip path.
download_and_verify() {
  local binary_name="$1"
  local binary_url="$2"
  local checksum_url="$3"
  local dest_path="$4"

  local tmp_binary="${BUNDLE_TMPDIR}/${binary_name}"
  local tmp_checksum="${BUNDLE_TMPDIR}/${binary_name}.checksums.txt"

  info "Fetching: ${binary_url}"
  if ! curl -fsSL --progress-bar -o "${tmp_binary}" "${binary_url}"; then
    fatal "Download failed for ${binary_name}.\nCheck network or download manually from:\n  ${binary_url}"
  fi

  info "Fetching checksums: ${checksum_url}"
  # SECURITY: checksum fetch failure is fatal — never install unverified binaries.
  if ! curl -fsSL -o "${tmp_checksum}" "${checksum_url}"; then
    fatal "Could not fetch checksums for ${binary_name} from:\n  ${checksum_url}\nNetwork failure or release artefact missing — aborting for security."
  fi

  local expected
  expected="$(grep "${binary_name}" "${tmp_checksum}" | awk '{print $1}' | head -1)"
  if [ -z "${expected}" ]; then
    fatal "No checksum entry for '${binary_name}' in checksums.txt — aborting.\nThis may indicate a release packaging error or tampered artefact."
  fi

  local actual
  actual="$(sha256sum "${tmp_binary}" | awk '{print $1}')"

  if [ "${actual}" != "${expected}" ]; then
    fatal "SHA-256 mismatch for ${binary_name}!\n  Expected: ${expected}\n  Got:      ${actual}\nDO NOT use this binary — aborting for security."
  fi

  info "SHA-256 OK (${binary_name}): ${actual}"
  chmod +x "${tmp_binary}"
  cp "${tmp_binary}" "${dest_path}"
  chown root:root "${dest_path}"
  chmod 755 "${dest_path}"
  info "Installed: ${dest_path}"
}

# build_from_source <name> <repo_url> <tag> <artifact> <dest_path>
#
# Clones a pinned tag over HTTPS and builds the binary on the box. Used for
# services that publish source releases (their web assets are committed at the
# tag, so `go build` alone reproduces the release binary — no npm step). The
# trust anchor here is the tag on the official repo, not a checksummed artefact;
# for a source-first project that is the intended model — you run what you build.
build_from_source() {
  local name="$1"
  local repo_url="$2"
  local tag="$3"
  local artifact="$4"
  local dest_path="$5"

  local src="${BUNDLE_TMPDIR}/src-${name}"
  rm -rf "${src}"

  info "Cloning ${name} ${tag} from ${repo_url}"
  if ! git clone --depth 1 --branch "${tag}" "${repo_url}" "${src}" >/dev/null 2>&1; then
    warn "Tag ${tag} not found for ${name}; cloning the default branch instead."
    git clone --depth 1 "${repo_url}" "${src}" >/dev/null 2>&1 \
      || fatal "Could not clone ${name} from ${repo_url} — check network and the repository URL."
  fi

  info "Building ${name} (this can take a few minutes on first run)…"
  if ! ( cd "${src}" && CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build -trimpath -o "${artifact}" . ); then
    fatal "Build failed for ${name}.\nEnsure the installed Go toolchain satisfies the module's go directive (go version in ${src}/go.mod)."
  fi

  install -m 0755 -o root -g root "${src}/${artifact}" "${dest_path}" 2>/dev/null \
    || { cp "${src}/${artifact}" "${dest_path}"; chown root:root "${dest_path}" 2>/dev/null || true; chmod 755 "${dest_path}"; }
  info "Installed: ${dest_path} (built from ${tag})"
}

# download_data_and_verify <name> <url> <checksum_url> <dest_path>
#
# Same mandatory SHA-256 contract as download_and_verify, but installs a
# read-only DATA file (0444 root:root) rather than an executable.  Used for the
# trust anchor and release cert, which must be world-readable (the vulos user
# reads them) and writable by nobody.
download_data_and_verify() {
  local name="$1"
  local url="$2"
  local checksum_url="$3"
  local dest_path="$4"

  local tmp_file="${BUNDLE_TMPDIR}/${name}"
  local tmp_checksum="${BUNDLE_TMPDIR}/${name}.checksums.txt"

  info "Fetching: ${url}"
  if ! curl -fsSL -o "${tmp_file}" "${url}"; then
    fatal "Download failed for ${name}.\nCheck network or download manually from:\n  ${url}"
  fi

  info "Fetching checksums: ${checksum_url}"
  if ! curl -fsSL -o "${tmp_checksum}" "${checksum_url}"; then
    fatal "Could not fetch checksums for ${name} from:\n  ${checksum_url}\nNetwork failure or release artefact missing — aborting for security."
  fi

  local expected
  expected="$(grep "${name}" "${tmp_checksum}" | awk '{print $1}' | head -1)"
  if [ -z "${expected}" ]; then
    fatal "No checksum entry for '${name}' in checksums.txt — aborting.\nThis may indicate a release packaging error or tampered artefact."
  fi

  local actual
  actual="$(sha256sum "${tmp_file}" | awk '{print $1}')"

  if [ "${actual}" != "${expected}" ]; then
    fatal "SHA-256 mismatch for ${name}!\n  Expected: ${expected}\n  Got:      ${actual}\nDO NOT use this file — aborting for security."
  fi

  info "SHA-256 OK (${name}): ${actual}"
  cp "${tmp_file}" "${dest_path}"
  chown root:root "${dest_path}"
  chmod 444 "${dest_path}"
  info "Installed: ${dest_path}"
}

# ── Shared temp directory ─────────────────────────────────────────────────────

BUNDLE_TMPDIR="$(mktemp -d)"
trap 'rm -rf "${BUNDLE_TMPDIR}"' EXIT

# ── Create vulos system user + group ─────────────────────────────────────────

step "Creating vulos user and group"

if ! getent group "${VULOS_GROUP}" >/dev/null 2>&1; then
  groupadd --system "${VULOS_GROUP}"
  info "Group '${VULOS_GROUP}' created."
else
  info "Group '${VULOS_GROUP}' already exists — skipping."
fi

if ! getent passwd "${VULOS_USER}" >/dev/null 2>&1; then
  useradd \
    --system \
    --no-create-home \
    --shell /usr/sbin/nologin \
    --gid "${VULOS_GROUP}" \
    --comment "Vulos bundle service account" \
    "${VULOS_USER}"
  info "User '${VULOS_USER}' created."
else
  # UID-collision guard: 'vulos' must be a system account (UID < 1000).
  EXISTING_UID="$(getent passwd "${VULOS_USER}" | cut -d: -f3)"
  if [ -n "${EXISTING_UID}" ] && [ "${EXISTING_UID}" -ge 1000 ]; then
    fatal "User '${VULOS_USER}' already exists but has UID ${EXISTING_UID} (>= 1000).\nThis is not a system account. Remove or rename it before running this installer."
  fi
  info "User '${VULOS_USER}' already exists (UID ${EXISTING_UID}) — skipping."
fi

# ── Create shared directories ─────────────────────────────────────────────────

step "Creating shared directories"

# Symlink attack guard: abort if CONFIG_DIR already exists as a symlink.
if [ -L "${CONFIG_DIR}" ]; then
  fatal "${CONFIG_DIR} is a symlink — aborting to prevent symlink traversal attack.\nRemove the symlink before running this installer."
fi
install -d -m 755 -o root -g root "${CONFIG_DIR}"
# Post-install verification: ensure it is a real directory (not a symlink).
if [ -L "${CONFIG_DIR}" ]; then
  fatal "${CONFIG_DIR} is a symlink after install — aborting."
fi
if [ ! -d "${CONFIG_DIR}" ]; then
  fatal "${CONFIG_DIR} was not created as a directory — aborting."
fi

# Service-specific subdirectories under the shared data root
install -d -m 750 -o "${VULOS_USER}" -g "${VULOS_GROUP}" "${DATA_DIR}"
install -d -m 750 -o "${VULOS_USER}" -g "${VULOS_GROUP}" "${DATA_DIR}/vulos"
install -d -m 750 -o "${VULOS_USER}" -g "${VULOS_GROUP}" "${DATA_DIR}/lilmail"
install -d -m 750 -o "${VULOS_USER}" -g "${VULOS_GROUP}" "${DATA_DIR}/office"
if [ "${INSTALL_MINIO}" = "true" ]; then
  install -d -m 750 -o "${VULOS_USER}" -g "${VULOS_GROUP}" "${DATA_DIR}/minio"
fi

info "Config dir:  ${CONFIG_DIR}/"
info "Data dir:    ${DATA_DIR}/ (vulos/ mail/ office/$([ "${INSTALL_MINIO}" = "true" ] && printf " minio/" || true))"

# ── Write default fabric config ───────────────────────────────────────────────

step "Writing shared fabric config"

if [ ! -f "${FABRIC_CONFIG}" ]; then
  cat > "${FABRIC_CONFIG}" <<'YAML'
# /etc/vulos/fabric.yaml — shared fabric identity and mesh config
# Shared by vulos, lilmail, and ofisi.
# Edit then restart the vulos-fabric service (or all bundle services).
#
# Full reference: https://docs.vulos.org/self-host/bundle#fabric

# ── Identity ──────────────────────────────────────────────────────────────────
# Canonical hostname for this Vulos bundle node.
domain: ""            # REQUIRED — e.g. "vulos.example.com"

# ── TLS ───────────────────────────────────────────────────────────────────────
tls:
  mode: "acme"          # acme | manual
  acme_email: ""        # REQUIRED for mode=acme
  # cert_file: "/etc/vulos/tls/cert.pem"
  # key_file:  "/etc/vulos/tls/key.pem"

# ── Fabric mesh credentials ───────────────────────────────────────────────────
# The keypair is shared by all three services so they can identify each other
# over the fabric mesh without external CA.
keypair:
  public_key_file:  "/var/lib/vulos/fabric_public.pem"
  private_key_file: "/var/lib/vulos/fabric_private.pem"

# ── Logging ───────────────────────────────────────────────────────────────────
log_level: "info"     # debug | info | warn | error
YAML
  chmod 640 "${FABRIC_CONFIG}"
  chown root:"${VULOS_GROUP}" "${FABRIC_CONFIG}"
  info "Fabric config written: ${FABRIC_CONFIG}"
else
  info "Fabric config already exists — not overwriting."
fi

# ── Write default storage config ─────────────────────────────────────────────

step "Writing shared storage config"

if [ ! -f "${STORAGE_CONFIG}" ]; then
  if [ "${STORAGE_MODE}" = "tigris" ]; then
    cat > "${STORAGE_CONFIG}" <<'YAML'
# /etc/vulos/storage.yaml — shared S3 storage selector
# Shared by vulos, lilmail, and ofisi.
#
# backend: tigris   — Tigris hosted S3-compatible storage (recommended)
# backend: minio    — local MinIO running on this machine

backend: "tigris"

tigris:
  endpoint:   "https://fly.storage.tigris.dev"
  region:     "auto"
  access_key: ""    # REQUIRED — set TIGRIS_ACCESS_KEY or fill here
  secret_key: ""    # REQUIRED — set TIGRIS_SECRET_KEY or fill here
  bucket:     ""    # REQUIRED — e.g. "vulos-bundle-yourdomain"
YAML
  else
    cat > "${STORAGE_CONFIG}" <<YAML
# /etc/vulos/storage.yaml — shared S3 storage selector (local MinIO)
# Shared by vulos, lilmail, and ofisi.

backend: "minio"

minio:
  endpoint:          "http://127.0.0.1:9000"
  access_key:        "vulos"
  secret_key_file:   "${DATA_DIR}/minio/.minio_secret"
  bucket:            "vulos-bundle"
YAML
  fi
  chmod 640 "${STORAGE_CONFIG}"
  chown root:"${VULOS_GROUP}" "${STORAGE_CONFIG}"
  info "Storage config written: ${STORAGE_CONFIG} (backend=${STORAGE_MODE})"
else
  info "Storage config already exists — not overwriting."
fi

# ── Write per-service configs ─────────────────────────────────────────────────

step "Writing per-service configs"

if [ ! -f "${OS_CONFIG}" ]; then
  cat > "${OS_CONFIG}" <<'YAML'
# /etc/vulos/vulos.yaml — Vulos OS backend config
# Inherits fabric identity and storage from /etc/vulos/fabric.yaml and storage.yaml.

server:
  port: "8443"
  data_dir: "/var/lib/vulos/vulos"

fabric:
  config: "/etc/vulos/fabric.yaml"

storage:
  config: "/etc/vulos/storage.yaml"

log_level: "info"
YAML
  chmod 640 "${OS_CONFIG}"
  chown root:"${VULOS_GROUP}" "${OS_CONFIG}"
  info "OS config written: ${OS_CONFIG}"
else
  info "OS config already exists — not overwriting."
fi

mkdir -p "${LILMAIL_CONFIG_DIR}" "${DATA_DIR}/lilmail/cache"
chown -R "${VULOS_USER}:${VULOS_GROUP}" "${DATA_DIR}/lilmail" 2>/dev/null || true
if [ ! -f "${LILMAIL_CONFIG}" ]; then
  # lilmail is a webmail CLIENT: it connects outbound to a mailbox you already
  # own and reads config.toml from its working directory. The [imap] block must
  # point at your own account before the service is useful.
  MAIL_JWT_SECRET="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  # 32-byte (64 hex chars) key lilmail uses to encrypt stored IMAP credentials;
  # without it, login fails. Truncated to 32 chars so it is exactly 32 bytes.
  MAIL_ENC_KEY="$(openssl rand -hex 16 2>/dev/null || head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  cat > "${LILMAIL_CONFIG}" <<TOML
# /etc/vulos/lilmail/config.toml — lilmail (mail + calendar + contacts client).
# lilmail connects OUTBOUND to a mailbox you already own; it hosts no mail and
# binds no privileged port. EDIT [imap] (and [smtp] if it differs) to your own
# account before starting the service.

[server]
port = 3000
# lilmail sits behind the box's TLS; set true when it is served over HTTPS directly.
secure_cookies = false

[auth]
allow_full_email_username = true

[imap]
server = "mail.example.com"   # <-- EDIT: your IMAP host
port = 993
tls = true

[cache]
folder = "${DATA_DIR}/lilmail/cache"

[storage]
backend = "bolt"

[jwt]
secret = "${MAIL_JWT_SECRET}"
TOML
  chmod 640 "${LILMAIL_CONFIG}"
  chown root:"${VULOS_GROUP}" "${LILMAIL_CONFIG}"
  info "lilmail config written: ${LILMAIL_CONFIG} (edit [imap] before starting)"
else
  info "lilmail config already exists — not overwriting."
fi

mkdir -p "${DATA_DIR}/office/uploads"
chown -R "${VULOS_USER}:${VULOS_GROUP}" "${DATA_DIR}/office" 2>/dev/null || true
if [ ! -f "${OFISI_CONFIG}" ]; then
  OFFICE_PASSWORD="$(openssl rand -hex 12 2>/dev/null || head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  cat > "${OFISI_CONFIG}" <<YAML
# /etc/vulos/office.yaml — Ofisi (vulos-office binary) config.
# Ofisi serves the collaborative office suite from its own store on this box.

server:
  addr: ":8445"
  data_dir:    "${DATA_DIR}/office"
  uploads_dir: "${DATA_DIR}/office/uploads"

auth:
  enabled:         true
  password:        "${OFFICE_PASSWORD}"   # <-- generated; change it if you like
  max_attempts:    5
  lockout_minutes: 15
  session_hours:   24

storage:
  type: "local"

log_level: "info"
YAML
  chmod 640 "${OFISI_CONFIG}"
  chown root:"${VULOS_GROUP}" "${OFISI_CONFIG}"
  info "Ofisi config written: ${OFISI_CONFIG} (login password: ${OFFICE_PASSWORD})"
else
  info "Ofisi config already exists — not overwriting."
fi

if [ ! -f "${BUNDLE_CONFIG}" ]; then
  cat > "${BUNDLE_CONFIG}" <<YAML
# /etc/vulos/bundle.yaml — Vulos bundle metadata (written by installer)
bundle_version: "1"
installed_by:   "install-vulos.sh"
storage_mode:   "${STORAGE_MODE}"
arch:           "${GOARCH}"
distro:         "${DISTRO}"
init:           "${INIT_SYSTEM}"
YAML
  chmod 644 "${BUNDLE_CONFIG}"
  chown root:root "${BUNDLE_CONFIG}"
fi

install -d -m 750 -o "${VULOS_USER}" -g "${VULOS_GROUP}" "${DATA_DIR}/office/uploads"
info "Office uploads dir created: ${DATA_DIR}/office/uploads"

# lilmail needs no keypair: it authenticates to your existing mailbox with your
# own IMAP/SMTP credentials (set in config.toml), so there is nothing to generate.

# ── Set up fabric keypair placeholder ────────────────────────────────────────

FAB_PUB="${DATA_DIR}/fabric_public.pem"
FAB_PRIV="${DATA_DIR}/fabric_private.pem"

if [ ! -f "${FAB_PUB}" ]; then
  cat > "${FAB_PUB}" <<'PEM'
# PLACEHOLDER — run `vulos keygen --fabric` to generate real keys before starting.
PEM
  chown "${VULOS_USER}:${VULOS_GROUP}" "${FAB_PUB}"
  chmod 644 "${FAB_PUB}"
fi

if [ ! -f "${FAB_PRIV}" ]; then
  touch "${FAB_PRIV}"
  chown "${VULOS_USER}:${VULOS_GROUP}" "${FAB_PRIV}"
  chmod 600 "${FAB_PRIV}"
fi

# ── Install the signing trust anchor + release cert ───────────────────────────
#
# REGISTRY-SIGN-01 / SEED-01. These are PUBLIC keys, but they are the root of
# every trust decision the box makes: which App Hub entries it will install and
# which OS images it will boot.
#
#   /etc/vulos/trust-anchor.pub   — the offline ROOT public key. This is the path
#                                   signing.DefaultAnchorPath resolves to.
#   /etc/vulos/release-cert.json  — root-signed cert authorising the RELEASE key
#                                   that signs registry.json and OS manifests.
#
# Without these the backend fails CLOSED: app installs are refused rather than
# performed unverified (there is no fall-open path in VULOS_ENV=prod, which is
# the default). They are fetched with the same mandatory SHA-256 verification as
# the binaries — an unverified anchor would defeat the entire point of having one.

step "Installing signing trust anchor"
download_data_and_verify \
  "trust-anchor.pub" \
  "${GITHUB_VULOS}/download/${TAG_VULOS}/trust-anchor.pub" \
  "${GITHUB_VULOS}/download/${TAG_VULOS}/checksums.txt" \
  "${CONFIG_DIR}/trust-anchor.pub"

download_data_and_verify \
  "release-cert.json" \
  "${GITHUB_VULOS}/download/${TAG_VULOS}/release-cert.json" \
  "${GITHUB_VULOS}/download/${TAG_VULOS}/checksums.txt" \
  "${CONFIG_DIR}/release-cert.json"

# ── Download + verify all three binaries ──────────────────────────────────────

step "Downloading vulos (OS backend) ${TAG_VULOS}"
download_and_verify \
  "vulos_linux_${GOARCH}" \
  "${GITHUB_VULOS}/download/${TAG_VULOS}/vulos_linux_${GOARCH}" \
  "${GITHUB_VULOS}/download/${TAG_VULOS}/checksums.txt" \
  "${BIN_VULOS}"

step "Building lilmail ${TAG_LILMAIL} from source"
build_from_source \
  "lilmail" \
  "${REPO_LILMAIL}" \
  "${TAG_LILMAIL}" \
  "lilmail" \
  "${BIN_LILMAIL}"

step "Building ofisi ${TAG_OFISI} from source"
build_from_source \
  "ofisi" \
  "${REPO_OFISI}" \
  "${TAG_OFISI}" \
  "vulos-office" \
  "${BIN_OFISI}"

# ── Install MinIO if requested ────────────────────────────────────────────────

if [ "${INSTALL_MINIO}" = "true" ]; then
  step "Installing local MinIO"

  MINIO_URL="https://dl.min.io/server/minio/release/linux-${GOARCH}/minio"
  MINIO_CHECKSUM_URL="https://dl.min.io/server/minio/release/linux-${GOARCH}/minio.sha256sum"
  MINIO_BIN="/usr/local/bin/minio"

  if [ -x "${MINIO_BIN}" ]; then
    info "MinIO already installed at ${MINIO_BIN} — skipping download."
  else
    info "Fetching MinIO from ${MINIO_URL}"
    MINIO_TMP="${BUNDLE_TMPDIR}/minio"
    if ! curl -fsSL --progress-bar -o "${MINIO_TMP}" "${MINIO_URL}"; then
      fatal "MinIO download failed from ${MINIO_URL}"
    fi

    info "Fetching MinIO checksum from ${MINIO_CHECKSUM_URL}"
    # SECURITY: checksum verification is MANDATORY — no skip path.
    MINIO_CS_TMP="${BUNDLE_TMPDIR}/minio.sha256sum"
    if ! curl -fsSL -o "${MINIO_CS_TMP}" "${MINIO_CHECKSUM_URL}"; then
      fatal "Could not fetch MinIO checksum — aborting for security."
    fi

    MINIO_EXPECTED="$(awk '{print $1}' "${MINIO_CS_TMP}")"
    MINIO_ACTUAL="$(sha256sum "${MINIO_TMP}" | awk '{print $1}')"
    if [ "${MINIO_ACTUAL}" != "${MINIO_EXPECTED}" ]; then
      fatal "MinIO SHA-256 mismatch!\n  Expected: ${MINIO_EXPECTED}\n  Got:      ${MINIO_ACTUAL}\nAborted for security."
    fi
    info "MinIO SHA-256 OK: ${MINIO_ACTUAL}"

    chmod +x "${MINIO_TMP}"
    cp "${MINIO_TMP}" "${MINIO_BIN}"
    chown root:root "${MINIO_BIN}"
    chmod 755 "${MINIO_BIN}"
    info "MinIO installed: ${MINIO_BIN}"
  fi

  # Generate a random MinIO secret key (if not already present)
  MINIO_SECRET_FILE="${DATA_DIR}/minio/.minio_secret"
  if [ ! -f "${MINIO_SECRET_FILE}" ]; then
    # Use /dev/urandom for a 32-byte hex secret
    dd if=/dev/urandom bs=32 count=1 2>/dev/null | sha256sum | awk '{print $1}' > "${MINIO_SECRET_FILE}"
    chown "${VULOS_USER}:${VULOS_GROUP}" "${MINIO_SECRET_FILE}"
    chmod 600 "${MINIO_SECRET_FILE}"
    info "MinIO secret key generated: ${MINIO_SECRET_FILE}"
  else
    info "MinIO secret key already exists — skipping."
  fi
fi

# ── Install systemd unit files ────────────────────────────────────────────────

case "${INIT_SYSTEM}" in

  systemd)
    step "Installing systemd unit files"

    # ── vulos-minio (conditional) ───────────────────────────────────────────
    if [ "${INSTALL_MINIO}" = "true" ]; then
      MINIO_SECRET_FILE="${DATA_DIR}/minio/.minio_secret"
      cat > "${UNIT_MINIO}" <<UNIT
[Unit]
Description=Vulos — local MinIO object storage
Documentation=https://min.io/docs/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${VULOS_USER}
Group=${VULOS_GROUP}
EnvironmentFile=-/etc/default/vulos-minio
ExecStartPre=/bin/sh -c 'export MINIO_ROOT_PASSWORD=\$(cat ${MINIO_SECRET_FILE})'
ExecStart=/usr/local/bin/minio server ${DATA_DIR}/minio --address 127.0.0.1:9000 --console-address 127.0.0.1:9001
Restart=on-failure
RestartSec=5s
TimeoutStartSec=30s
TimeoutStopSec=20s

# Environment (override MINIO_ROOT_USER / MINIO_ROOT_PASSWORD in /etc/default/vulos-minio)
Environment=MINIO_ROOT_USER=vulos

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=${DATA_DIR}/minio
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
UNIT
      chmod 644 "${UNIT_MINIO}"
      info "systemd unit installed: ${UNIT_MINIO}"
    fi

    # ── vulos-fabric ────────────────────────────────────────────────────────
    # Lightweight oneshot that validates config + generates keypair if absent.
    # Real services (vulos, mail, office) depend on it completing first.
    MINIO_AFTER=""
    MINIO_WANTS=""
    if [ "${INSTALL_MINIO}" = "true" ]; then
      MINIO_AFTER="After=vulos-minio.service"
      MINIO_WANTS="Wants=vulos-minio.service"
    fi

    cat > "${UNIT_FABRIC}" <<UNIT
[Unit]
Description=Vulos — shared fabric identity initialisation
Documentation=https://docs.vulos.org/self-host/bundle#fabric
After=network-online.target
Wants=network-online.target
${MINIO_AFTER}
${MINIO_WANTS}

[Service]
Type=oneshot
RemainAfterExit=yes
User=${VULOS_USER}
Group=${VULOS_GROUP}
ExecStart=/usr/local/bin/vulos fabric --init --config ${FABRIC_CONFIG}
TimeoutStartSec=30s

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=${DATA_DIR}
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
UNIT
    chmod 644 "${UNIT_FABRIC}"
    info "systemd unit installed: ${UNIT_FABRIC}"

    # ── vulos (OS backend) ──────────────────────────────────────────────────
    cat > "${UNIT_OS}" <<UNIT
[Unit]
Description=Vulos — OS backend API gateway and app fabric
Documentation=https://docs.vulos.org/self-host/bundle
After=network-online.target vulos-fabric.service
Wants=network-online.target
Requires=vulos-fabric.service

[Service]
Type=simple
User=${VULOS_USER}
Group=${VULOS_GROUP}
ExecStart=${BIN_VULOS} serve --config ${OS_CONFIG}
Restart=on-failure
RestartSec=5s
TimeoutStartSec=60s
TimeoutStopSec=30s

# Hardening — no raw network capabilities needed (listens on 8443)
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=${DATA_DIR}/vulos
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target vulos-bundle.target
UNIT
    chmod 644 "${UNIT_OS}"
    info "systemd unit installed: ${UNIT_OS}"

    # ── lilmail (mail + calendar + contacts client) ─────────────────────────
    # Not an inbound mail server: lilmail connects OUTBOUND to the owner's own
    # mailbox and reads config.toml from its working directory (no --config
    # flag, no privileged port), so no capabilities are needed.
    cat > "${UNIT_LILMAIL}" <<UNIT
[Unit]
Description=Vulos — lilmail (mail + calendar + contacts client)
Documentation=https://docs.vulos.org/self-host/bundle
After=network-online.target vulos-fabric.service vulos.service
Wants=network-online.target
Requires=vulos-fabric.service

[Service]
Type=simple
User=${VULOS_USER}
Group=${VULOS_GROUP}
WorkingDirectory=${LILMAIL_CONFIG_DIR}
ExecStart=${BIN_LILMAIL}
Restart=on-failure
RestartSec=5s
TimeoutStartSec=60s
TimeoutStopSec=30s

# Hardening — no capabilities: lilmail only makes outbound connections.
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=${DATA_DIR}/lilmail
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target vulos-bundle.target
UNIT
    chmod 644 "${UNIT_LILMAIL}"
    info "systemd unit installed: ${UNIT_LILMAIL}"

    # ── ofisi (vulos-office binary) ─────────────────────────────────────────
    # The CLI takes server flags directly; a positional "serve" would make
    # flag.Parse() stop before it sees -config, so config.yaml would be ignored.
    cat > "${UNIT_OFISI}" <<UNIT
[Unit]
Description=Vulos — Ofisi collaborative office suite backend
Documentation=https://docs.vulos.org/self-host/bundle
After=network-online.target vulos-fabric.service vulos.service
Wants=network-online.target
Requires=vulos-fabric.service

[Service]
Type=simple
User=${VULOS_USER}
Group=${VULOS_GROUP}
ExecStart=${BIN_OFISI} -config ${OFISI_CONFIG}
Restart=on-failure
RestartSec=5s
TimeoutStartSec=60s
TimeoutStopSec=30s

# Hardening — no raw network capabilities (listens on 8445)
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=${DATA_DIR}/office
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target vulos-bundle.target
UNIT
    chmod 644 "${UNIT_OFISI}"
    info "systemd unit installed: ${UNIT_OFISI}"

    # ── vulos-bundle.target ─────────────────────────────────────────────────
    cat > "${UNIT_BUNDLE}" <<UNIT
[Unit]
Description=Vulos Bundle — OS + Mail + Office (all-up sentinel)
Documentation=https://docs.vulos.org/self-host/bundle
Wants=vulos.service vulos-lilmail.service vulos-ofisi.service
After=vulos.service vulos-lilmail.service vulos-ofisi.service

[Install]
WantedBy=multi-user.target
UNIT
    chmod 644 "${UNIT_BUNDLE}"
    info "systemd target installed: ${UNIT_BUNDLE}"

    systemctl daemon-reload
    info "systemd daemon reloaded."

    if [ "${SKIP_ENABLE}" = "false" ]; then
      step "Enabling Vulos Bundle services"
      if [ "${INSTALL_MINIO}" = "true" ]; then
        systemctl enable vulos-minio.service
      fi
      systemctl enable vulos-fabric.service
      systemctl enable vulos.service
      systemctl enable vulos-lilmail.service
      systemctl enable vulos-ofisi.service
      systemctl enable vulos-bundle.target
      info "All services enabled. Start with: systemctl start vulos-bundle.target"
    else
      info "Service enable skipped (--no-enable). Enable manually after configuration."
    fi
    ;;

  openrc)
    step "Installing OpenRC init scripts"

    # vulos (OS backend)
    cat > "${OPENRC_DIR}/vulos" <<'SCRIPT'
#!/sbin/openrc-run
description="Vulos — OS backend API gateway and app fabric"

command="/usr/local/bin/vulos"
command_args="serve --config /etc/vulos/vulos.yaml"
command_user="vulos:vulos"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"

depend() {
  need net
  after vulos-fabric
}

start_pre() {
  checkpath --directory --owner vulos:vulos --mode 0750 /var/lib/vulos/vulos
}
SCRIPT
    chmod 755 "${OPENRC_DIR}/vulos"

    # lilmail — outbound mail/calendar/contacts client; reads config.toml from
    # its working directory, binds no privileged port.
    cat > "${OPENRC_DIR}/vulos-lilmail" <<'SCRIPT'
#!/sbin/openrc-run
description="Vulos — lilmail (mail + calendar + contacts client)"

command="/usr/local/bin/lilmail"
directory="/etc/vulos/lilmail"
command_user="vulos:vulos"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"

depend() {
  need net
  after vulos vulos-fabric
}

start_pre() {
  checkpath --directory --owner vulos:vulos --mode 0750 /var/lib/vulos/lilmail
}
SCRIPT
    chmod 755 "${OPENRC_DIR}/vulos-lilmail"

    # ofisi — office suite backend; the CLI takes -config directly (no "serve").
    cat > "${OPENRC_DIR}/vulos-ofisi" <<'SCRIPT'
#!/sbin/openrc-run
description="Vulos — Ofisi collaborative office suite backend"

command="/usr/local/bin/vulos-office"
command_args="-config /etc/vulos/office.yaml"
command_user="vulos:vulos"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"

depend() {
  need net
  after vulos vulos-fabric
}

start_pre() {
  checkpath --directory --owner vulos:vulos --mode 0750 /var/lib/vulos/office
}
SCRIPT
    chmod 755 "${OPENRC_DIR}/vulos-ofisi"

    info "OpenRC init scripts installed: ${OPENRC_DIR}/vulos{,-lilmail,-ofisi}"
    ;;

  none)
    info "No service units installed (no init system detected)."
    ;;
esac

# ── Next-steps banner ─────────────────────────────────────────────────────────

printf "\n"
printf "${GRN}${BLD}%s${RST}\n" "$(printf '%.0s=' {1..52})"
printf "${GRN}${BLD}  Vulos Bundle installed!${RST}\n"
printf "${GRN}${BLD}  OS + Mail + Office on one box.${RST}\n"
printf "${GRN}${BLD}%s${RST}\n" "$(printf '%.0s=' {1..52})"
printf "\n"
printf "${BLD}Versions:${RST}\n"
printf "  vulos          %s\n" "${TAG_VULOS}"
printf "  lilmail        %s\n" "${TAG_LILMAIL}"
printf "  ofisi          %s\n" "${TAG_OFISI}"
printf "  Storage:       %s\n" "${STORAGE_MODE}"
printf "\n"
printf "${BLD}Next steps:${RST}\n\n"
printf "  1. ${BLD}Edit the shared config:${RST}\n"
printf "     ${CYN}sudo nano ${FABRIC_CONFIG}${RST}\n"
printf "     → Set your domain and acme_email.\n\n"
if [ "${STORAGE_MODE}" = "tigris" ]; then
  printf "  2. ${BLD}Set Tigris credentials:${RST}\n"
  printf "     ${CYN}sudo nano ${STORAGE_CONFIG}${RST}\n"
  printf "     → Set access_key, secret_key, and bucket.\n\n"
else
  printf "  2. ${BLD}MinIO secret:${RST}\n"
  printf "     ${CYN}sudo cat ${DATA_DIR}/minio/.minio_secret${RST}\n"
  printf "     → Set MINIO_ROOT_PASSWORD in /etc/default/vulos-minio\n\n"
fi
printf "  3. ${BLD}Generate the fabric keypair:${RST}\n"
printf "     ${CYN}sudo -u ${VULOS_USER} ${BIN_VULOS} keygen --fabric${RST}\n"
printf "     (lilmail needs no keypair — it signs in to your own mailbox.)\n\n"
printf "  3b. ${BLD}Point lilmail at your mailbox:${RST}\n"
printf "     ${CYN}sudo nano ${LILMAIL_CONFIG}${RST}\n"
printf "     → Set [imap] server/port to your own account.\n\n"

case "${INIT_SYSTEM}" in
  systemd)
    printf "  4. ${BLD}Start the bundle:${RST}\n"
    if [ "${INSTALL_MINIO}" = "true" ]; then
      printf "     ${CYN}sudo systemctl start vulos-minio.service${RST}\n"
    fi
    printf "     ${CYN}sudo systemctl enable --now vulos-bundle.target${RST}\n\n"
    printf "     Check status:  ${CYN}sudo systemctl status 'vulos*'${RST}\n"
    printf "     View logs:     ${CYN}sudo journalctl -u 'vulos*' -f${RST}\n\n"
    ;;
  openrc)
    printf "  4. ${BLD}Start the bundle:${RST}\n"
    printf "     ${CYN}sudo rc-update add vulos default${RST}\n"
    printf "     ${CYN}sudo rc-update add vulos-lilmail default${RST}\n"
    printf "     ${CYN}sudo rc-update add vulos-ofisi default${RST}\n"
    printf "     ${CYN}sudo rc-service vulos start${RST}\n"
    printf "     ${CYN}sudo rc-service vulos-lilmail start${RST}\n"
    printf "     ${CYN}sudo rc-service vulos-ofisi start${RST}\n\n"
    ;;
  none)
    printf "  4. ${BLD}Start the services manually:${RST}\n"
    printf "     ${CYN}sudo -u ${VULOS_USER} ${BIN_VULOS} serve --config ${OS_CONFIG}${RST}\n"
    printf "     ${CYN}cd ${LILMAIL_CONFIG_DIR} \&\& sudo -u ${VULOS_USER} ${BIN_LILMAIL}${RST}\n"
    printf "     ${CYN}sudo -u ${VULOS_USER} ${BIN_OFISI} -config ${OFISI_CONFIG}${RST}\n\n"
    ;;
esac

printf "  5. ${BLD}Make the box reachable (optional):${RST}\n"
printf "     → Point a self-hosted Ephor (github.com/vul-os/ephor) at this box,\n"
printf "       or expose it directly with a static IP and an A record.\n\n"
printf "  6. ${BLD}Configure your domain DNS:${RST}\n"
printf "     → A record pointing to this server's IP\n\n"
printf "${YEL}Tip: Keep your private keys backed up offline.${RST}\n"
printf "     Fabric key: ${FAB_PRIV}\n"
printf "     Mail key:   ${PRIV_KEY_FILE}\n\n"
printf "${GRN}Docs: https://docs.vulos.org/self-host/bundle${RST}\n\n"
