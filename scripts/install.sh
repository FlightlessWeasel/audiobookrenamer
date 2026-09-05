#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────────────────
# audiobookrenamer installer / updater  (Linux, systemd)
#
#   Fresh install : download the latest release archive, create the service
#                   user, write a systemd unit, enable + start it.
#   --update      : replace the installed binary with the latest release
#                   (or --version TAG) and restart the service, with
#                   automatic rollback if it fails to come back up.
#   --os-upgrade  : run a full OS package upgrade first (apt/dnf/pacman/zypper).
#
# No Go or Node toolchain required — this pulls the prebuilt archives that
# GoReleaser publishes to GitHub Releases.
#
# Bootstrap (fresh install or update) straight from the web:
#   curl -fsSL https://github.com/FlightlessWeasel/audiobookrenamer/releases/latest/download/install.sh | sudo bash
#   curl -fsSL https://github.com/FlightlessWeasel/audiobookrenamer/releases/latest/download/install.sh | sudo bash -s -- --update
#
# Layout: binary in /opt/<service>/<service>, state (SQLite DB, provider keys,
# session secret) in /var/lib/<service>. The binary lives under /opt, owned by
# the service user, so the in-app updater (Settings -> Updates) can replace it
# in place. The .deb package instead installs to /usr/bin and is managed by
# apt, which is why in-app self-update is disabled for .deb installs.
# ─────────────────────────────────────────────────────────

REPO="${ABR_REPO:-FlightlessWeasel/audiobookrenamer}"
SVC_NAME="audiobookrenamer"
SVC_USER="audiobookrenamer"
BINDIR=""           # empty => /opt/<service> (resolved after arg parsing)
PORT="8674"
VERSION=""          # empty => latest
DO_UPDATE=false
DO_OS_UPGRADE=false
NO_START=false
FORCE=false

usage() {
  cat <<EOF
Usage: install.sh [options]

  --update            Update an existing install to the latest release, then restart.
  --os-upgrade        Upgrade all OS packages first (apt / dnf / pacman / zypper).
  --version TAG       Install/update to a specific release tag (e.g. v1.2.3). Default: latest.
  --repo OWNER/NAME   GitHub repo to fetch releases from. Default: ${REPO}.
  --service NAME      systemd service + state-dir name. Default: ${SVC_NAME}.
  --user NAME         Service account to create and run as. Default: ${SVC_USER}.
  --bindir PATH       Directory to install the binary into. Default: /opt/${SVC_NAME}.
                      Must be writable by --user for in-app self-update to work.
  --port PORT         TCP port the server listens on (ABR_ADDR=:PORT). Default: ${PORT}.
  --no-start          Install and enable the unit but do not start it now.
  --force             Reinstall / rewrite the unit even if already at the target version.
  -h, --help          Show this help.

Environment:
  ABR_REPO            Same as --repo.
  GITHUB_TOKEN        Optional; raises the GitHub API rate limit for tag lookup.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --update)      DO_UPDATE=true; shift ;;
    --os-upgrade)  DO_OS_UPGRADE=true; shift ;;
    --version)     VERSION="${2:?--version needs a tag}"; shift 2 ;;
    --repo)        REPO="${2:?--repo needs owner/name}"; shift 2 ;;
    --service)     SVC_NAME="${2:?--service needs a name}"; shift 2 ;;
    --user)        SVC_USER="${2:?--user needs a name}"; shift 2 ;;
    --bindir)      BINDIR="${2:?--bindir needs a path}"; shift 2 ;;
    --port)        PORT="${2:?--port needs a value}"; shift 2 ;;
    --no-start)    NO_START=true; shift ;;
    --force)       FORCE=true; shift ;;
    -h|--help)     usage; exit 0 ;;
    *)             echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

BINDIR="${BINDIR:-/opt/${SVC_NAME}}"
BIN="${BINDIR}/${SVC_NAME}"
BACKUP="${BIN}.bak"
STATE_DIR="/var/lib/${SVC_NAME}"
VERSION_FILE="${STATE_DIR}/.version"
UNIT_FILE="/etc/systemd/system/${SVC_NAME}.service"
# Pre-0.1.22 installs put the binary here; --update relocates them to $BIN.
LEGACY_BIN="/usr/bin/${SVC_NAME}"

die()  { echo "error: $*" >&2; exit 1; }
info() { echo "==> $*"; }

[[ "$(id -u)" -eq 0 ]] || die "must run as root (try: sudo $0 $*)"

for c in curl tar sha256sum systemctl install; do
  command -v "$c" >/dev/null 2>&1 || die "required command not found: $c"
done

# ── OS package upgrade ──────────────────────────────────
os_upgrade() {
  info "upgrading OS packages"
  if   command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get -y upgrade
  elif command -v dnf >/dev/null 2>&1; then
    dnf -y upgrade
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Syu --noconfirm
  elif command -v zypper >/dev/null 2>&1; then
    zypper --non-interactive update
  else
    die "no supported package manager found (apt/dnf/pacman/zypper)"
  fi
}

# ── arch detection ──────────────────────────────────────
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *)             die "unsupported architecture: $(uname -m)" ;;
  esac
}

# ── release resolution ──────────────────────────────────
latest_tag() {
  local api="https://api.github.com/repos/${REPO}/releases/latest"
  local hdr=(-fsSL -H "Accept: application/vnd.github+json")
  [[ -n "${GITHUB_TOKEN:-}" ]] && hdr+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  # Buffer the whole body first; piping curl into `grep -m1` makes grep close
  # the pipe early and curl exits 23 ("failure writing output").
  local body
  body="$(curl "${hdr[@]}" "$api")" || return 1
  printf '%s\n' "$body" \
    | grep -m1 '"tag_name"' \
    | sed -E 's/.*"tag_name" *: *"([^"]+)".*/\1/'
}

# download_release <tag> <arch> <destdir> -> sets $EXTRACTED to the binary path
download_release() {
  local tag="$1" arch="$2" dest="$3"
  local ver="${tag#v}"                       # asset names carry no leading "v"
  local asset="${SVC_NAME}_${ver}_linux_${arch}.tar.gz"
  local base="https://github.com/${REPO}/releases/download/${tag}"

  info "downloading ${asset}"
  curl -fSL -o "${dest}/${asset}" "${base}/${asset}" \
    || die "download failed: ${base}/${asset}"

  if curl -fsSL -o "${dest}/checksums.txt" "${base}/checksums.txt"; then
    info "verifying checksum"
    ( cd "$dest" && grep " ${asset}\$" checksums.txt | sha256sum -c - ) \
      || die "checksum verification failed for ${asset}"
  else
    echo "    (no checksums.txt in release — skipping checksum verify)" >&2
  fi

  tar -C "$dest" -xzf "${dest}/${asset}"
  [[ -f "${dest}/${SVC_NAME}" ]] || die "archive did not contain '${SVC_NAME}'"
  EXTRACTED="${dest}/${SVC_NAME}"
}

# ── service account + state dir ─────────────────────────
ensure_user() {
  if ! getent group "$SVC_USER" >/dev/null; then
    info "creating group ${SVC_USER}"
    groupadd --system "$SVC_USER"
  fi
  if ! getent passwd "$SVC_USER" >/dev/null; then
    info "creating service user ${SVC_USER}"
    useradd --system --no-create-home --gid "$SVC_USER" \
      --home-dir "$STATE_DIR" --shell /usr/sbin/nologin "$SVC_USER"
  fi
  mkdir -p "$STATE_DIR"
  chown "$SVC_USER":"$SVC_USER" "$STATE_DIR"
  chmod 0700 "$STATE_DIR"
}

# ── systemd unit ────────────────────────────────────────
# Mirrors packaging/audiobookrenamer.service except for ExecStart: this unit
# runs the binary from /opt so the service can self-update; the .deb runs it
# from /usr/bin under apt's control. ProtectHome is off on purpose: the whole
# job is renaming media files in place, wherever the library lives (often under
# /home, /srv, or /mnt). ProtectSystem=true leaves /opt writable, and the
# service user owns ${BINDIR}, so the in-app updater can replace the binary.
write_unit() {
  info "writing ${UNIT_FILE}"
  cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Audiobook Library Manager
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SVC_USER}
Group=${SVC_USER}
Environment=ABR_CONFIG_DIR=${STATE_DIR}
Environment=ABR_ADDR=:${PORT}
ExecStart=${BIN}
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=true
ProtectHome=false
PrivateTmp=true
StateDirectory=${SVC_NAME}

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
}

install_binary() {
  local src="$1"
  # The dir and binary are owned by the service user so the in-app updater can
  # rewrite the binary in place. install -d re-chowns an existing dir too.
  install -d -o "$SVC_USER" -g "$SVC_USER" -m 0755 "$BINDIR"
  if [[ -f "$BIN" ]]; then
    cp -p "$BIN" "$BACKUP"
    echo "    backed up existing binary to ${BACKUP}"
  fi
  local tmp
  tmp="$(mktemp "${BIN}.new.XXXXXX")"
  install -o "$SVC_USER" -g "$SVC_USER" -m 0755 "$src" "$tmp"
  mv -f "$tmp" "$BIN"
}

rollback_binary() {
  [[ -f "$BACKUP" ]] || { echo "    no backup to roll back to" >&2; return; }
  info "rolling back ${BIN}"
  local tmp
  tmp="$(mktemp "${BIN}.rollback.XXXXXX")"
  cp -p "$BACKUP" "$tmp"
  mv -f "$tmp" "$BIN"
  systemctl restart "$SVC_NAME" || true
}

wait_active() {
  local i
  for ((i = 0; i < 10; i++)); do
    systemctl is-active --quiet "$SVC_NAME" && return 0
    sleep 1
  done
  return 1
}

# ── main ────────────────────────────────────────────────
$DO_OS_UPGRADE && os_upgrade

ARCH="$(detect_arch)"

TAG="$VERSION"
if [[ -z "$TAG" ]]; then
  info "resolving latest release for ${REPO}"
  TAG="$(latest_tag)" || die "could not reach the GitHub releases API"
  [[ -n "$TAG" ]] || die "could not determine latest release tag"
fi
info "target version: ${TAG} (linux/${ARCH})"

CURRENT=""
[[ -f "$VERSION_FILE" ]] && CURRENT="$(cat "$VERSION_FILE")"

if [[ "$TAG" == "$CURRENT" && "$FORCE" == false ]]; then
  echo "already at ${TAG} — nothing to do (use --force to reinstall)"
  exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
EXTRACTED=""
download_release "$TAG" "$ARCH" "$WORK"

# ── update path ─────────────────────────────────────────
if $DO_UPDATE; then
  RELOCATED_FROM=""
  if [[ ! -f "$BIN" && "$BIN" != "$LEGACY_BIN" && -f "$LEGACY_BIN" ]]; then
    info "relocating binary out of /usr/bin so the service can self-update: ${LEGACY_BIN} -> ${BIN}"
    RELOCATED_FROM="$LEGACY_BIN"
  fi
  [[ -f "$BIN" || -n "$RELOCATED_FROM" ]] \
    || die "--update: no existing install at ${BIN} (run without --update first)"

  install_binary "$EXTRACTED"
  echo "$TAG" > "$VERSION_FILE"

  # Point the unit at $BIN whenever it is stale — always so on a relocation,
  # but also picks up a hand-moved binary.
  UNIT_REPOINTED=false
  if [[ -f "$UNIT_FILE" ]] && ! grep -qxF "ExecStart=${BIN}" "$UNIT_FILE"; then
    info "updating ExecStart in ${UNIT_FILE} -> ${BIN}"
    sed -i "s#^ExecStart=.*#ExecStart=${BIN}#" "$UNIT_FILE"
    systemctl daemon-reload
    UNIT_REPOINTED=true
  fi

  if systemctl is-enabled "$SVC_NAME" >/dev/null 2>&1; then
    info "restarting ${SVC_NAME}"
    if ! systemctl restart "$SVC_NAME" || ! wait_active; then
      echo "    service did not come back up — rolling back" >&2
      if [[ -n "$RELOCATED_FROM" ]]; then
        if $UNIT_REPOINTED; then
          sed -i "s#^ExecStart=.*#ExecStart=${RELOCATED_FROM}#" "$UNIT_FILE"
          systemctl daemon-reload
        fi
        rm -f "$BIN"
        systemctl restart "$SVC_NAME" || true
      else
        rollback_binary
      fi
      echo "$CURRENT" > "$VERSION_FILE"
      die "update failed — rolled back to previous binary"
    fi
    echo "    restarted on ${TAG}"
    if [[ -n "$RELOCATED_FROM" ]]; then
      rm -f "$RELOCATED_FROM" "${RELOCATED_FROM}.bak"
      echo "    removed old binary ${RELOCATED_FROM}"
    fi
  else
    echo "    service ${SVC_NAME} not enabled — binary updated, start it yourself"
  fi
  info "done"
  exit 0
fi

# ── fresh install (or --force reinstall) ────────────────
ensure_user
install_binary "$EXTRACTED"
echo "$TAG" > "$VERSION_FILE"

if [[ ! -f "$UNIT_FILE" || "$FORCE" == true ]]; then
  write_unit
else
  echo "    ${UNIT_FILE} already exists — leaving it untouched (use --force to rewrite)"
fi

systemctl enable "$SVC_NAME" >/dev/null

if $NO_START; then
  echo "    --no-start: not starting ${SVC_NAME}"
else
  info "starting ${SVC_NAME}"
  systemctl restart "$SVC_NAME"
  if wait_active; then
    echo "    active on ${TAG}"
  else
    systemctl status "$SVC_NAME" --no-pager || true
    die "service failed to start — check: journalctl -u ${SVC_NAME} -e"
  fi
fi

cat <<EOF

audiobookrenamer ${TAG} is installed.

  Web UI:   http://<this-host>:${PORT}/
  Binary:   ${BIN}   (service-owned, so Settings -> Updates can self-update)
  State:    ${STATE_DIR}   (SQLite DB, provider API keys, session secret)
  Service:  systemctl status ${SVC_NAME}
  Logs:     journalctl -u ${SVC_NAME} -f
  Update:   sudo $0 --update

EOF
info "done"
