#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

DRY_RUN=0
CONFIRM=0
REMOVE_ETH0_CONFIG=0
PURGE_AGENT_PACKAGES=0
REMOVE_MEDIA=0
LOG_DIR_OVERRIDDEN=0

INSTALL_PREFIX="${ATLAS_ONBOARD_INSTALL_PREFIX:-/opt/atlas}"
ENV_DIR="${ATLAS_ONBOARD_ENV_DIR:-${HOME}/.config/atlas-agent}"
ENV_FILE="${ATLAS_ONBOARD_ENV_FILE:-${ENV_DIR}/onboard.env}"
STATE_DIR="${ATLAS_ONBOARD_STATE_DIR:-${HOME}/.local/state/atlas-agent}"
LOG_DIR="${ATLAS_ONBOARD_LOG_DIR:-${STATE_DIR}/logs}"
MEDIAMTX_DIR="${ATLAS_MEDIAMTX_DIR:-${INSTALL_PREFIX}/mediamtx}"
MAVSDK_SERVER_BIN="${ATLAS_MAVSDK_SERVER_BIN:-${INSTALL_PREFIX}/bin/mavsdk_server}"
MAVLINK_ROUTER_SOURCE_DIR="${ATLAS_MAVLINK_ROUTER_SOURCE_DIR:-${INSTALL_PREFIX}/src/mavlink-router}"
MAVLINK_ROUTER_SOURCE_MARKER="${MAVLINK_ROUTER_SOURCE_DIR}/.atlas-source-install"
ETH0_NETPLAN_FILE="${ATLAS_ONBOARD_ETH0_NETPLAN_FILE:-/etc/netplan/99-siyi-eth0-local.yaml}"

CORE_SERVICES=(
  atlas-video-agent.service
  atlas-agent.service
  atlas-mavsdk.service
  atlas-mavlink-router.service
)

MEDIA_SERVICES=(
  atlas-mediamtx.service
)

CORE_UNIT_FILES=(
  /etc/systemd/system/atlas-video-agent.service
  /etc/systemd/system/atlas-agent.service
  /etc/systemd/system/atlas-mavsdk.service
  /etc/systemd/system/atlas-mavlink-router.service
)

MEDIA_UNIT_FILES=(
  /etc/systemd/system/atlas-mediamtx.service
)

AGENT_PACKAGE_CANDIDATES=(
  mavlink-router
  golang-go
  build-essential
  python3-venv
  python3-pip
  netcat-openbsd
)

usage() {
  cat <<EOF
Usage: scripts/cleanup-onboard-pi.sh [options]

Removes Atlas onboard agent setup created by install-onboard-pi.sh.

By default this preserves ffmpeg, GStreamer/media packages, Hailo packages,
MediaMTX files, and atlas-mediamtx.service.

Options:
  --dry-run                 Print actions without changing the system.
  --yes                     Required for destructive cleanup.
  --remove-eth0-config      Remove ${ETH0_NETPLAN_FILE}.
  --purge-agent-packages    apt purge agent-side packages only; preserves ffmpeg/media packages.
  --remove-media            Also remove atlas-mediamtx.service, ${MEDIAMTX_DIR}, and MediaMTX logs.
  --install-prefix PATH     Install prefix used by install script. Default: ${INSTALL_PREFIX}
  --env-file PATH           Onboard env file path. Default: ${ENV_FILE}
  --state-dir PATH          Atlas state dir. Default: ${STATE_DIR}
  --log-dir PATH            Atlas log dir. Default: ${LOG_DIR}
  -h, --help                Show this help.
EOF
}

log() {
  printf '[atlas-onboard-cleanup] %s\n' "$*"
}

fail() {
  printf '[atlas-onboard-cleanup] error: %s\n' "$*" >&2
  exit 1
}

require_value() {
  local option="$1"
  local value="${2:-}"
  if [[ -z "$value" || "$value" == --* ]]; then
    fail "${option} requires a value"
  fi
}

run() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+'
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

run_optional() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+'
    printf ' %q' "$@"
    printf ' || true\n'
    return 0
  fi
  "$@" || true
}

remove_empty_dir() {
  local path="$1"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ rmdir %q 2>/dev/null || true\n' "$path"
    return 0
  fi
  rmdir "$path" 2>/dev/null || true
}

sudo_remove_empty_dir() {
  local path="$1"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ sudo rmdir %q 2>/dev/null || true\n' "$path"
    return 0
  fi
  sudo rmdir "$path" 2>/dev/null || true
}

assert_safe_dir() {
  local path="$1"
  local label="$2"

  case "$path" in
    ""|"/"|"/opt"|"/etc"|"$HOME")
      fail "refusing to recursively remove unsafe ${label}: ${path}"
      ;;
  esac
}

systemctl_available() {
  command -v systemctl >/dev/null 2>&1
}

cleanup_services() {
  local services=("${CORE_SERVICES[@]}")
  if [[ "$REMOVE_MEDIA" -eq 1 ]]; then
    services+=("${MEDIA_SERVICES[@]}")
  fi

  if ! systemctl_available; then
    log "systemctl not found; skipping service stop/disable"
    return
  fi

  log "stopping and disabling Atlas services"
  local service
  for service in "${services[@]}"; do
    run_optional sudo systemctl stop "$service"
    run_optional sudo systemctl disable "$service"
  done
}

remove_systemd_units() {
  log "removing Atlas systemd unit files"
  run sudo rm -f "${CORE_UNIT_FILES[@]}"
  if [[ "$REMOVE_MEDIA" -eq 1 ]]; then
    run sudo rm -f "${MEDIA_UNIT_FILES[@]}"
  else
    log "preserving atlas-mediamtx.service"
  fi

  if systemctl_available; then
    run_optional sudo systemctl daemon-reload
    run_optional sudo systemctl reset-failed
  fi
}

remove_agent_binaries() {
  log "removing Atlas agent binaries"
  run sudo rm -f "${INSTALL_PREFIX}/bin/atlas-agent" "${INSTALL_PREFIX}/bin/atlas-video-agent.py" "$MAVSDK_SERVER_BIN"
  sudo_remove_empty_dir "${INSTALL_PREFIX}/bin"

  if [[ "$REMOVE_MEDIA" -eq 1 ]]; then
    assert_safe_dir "$MEDIAMTX_DIR" "MediaMTX dir"
    log "removing MediaMTX files"
    run sudo rm -rf "$MEDIAMTX_DIR"
  else
    log "preserving MediaMTX files in ${MEDIAMTX_DIR}"
  fi
}

remove_mavlink_router_source_install() {
  if [[ -f "$MAVLINK_ROUTER_SOURCE_MARKER" ]]; then
    log "removing Atlas source-built mavlink-routerd"
    run sudo rm -f /usr/bin/mavlink-routerd
    run sudo rm -f /usr/lib/systemd/system/mavlink-router.service /lib/systemd/system/mavlink-router.service
  else
    log "no Atlas mavlink-router source marker found; preserving mavlink-routerd binary"
  fi

  if [[ -d "$MAVLINK_ROUTER_SOURCE_DIR" ]]; then
    assert_safe_dir "$MAVLINK_ROUTER_SOURCE_DIR" "MAVLink Router source dir"
    log "removing MAVLink Router source checkout"
    run sudo rm -rf "$MAVLINK_ROUTER_SOURCE_DIR"
    sudo_remove_empty_dir "$(dirname "$MAVLINK_ROUTER_SOURCE_DIR")"
  fi
}

remove_config() {
  local mavlink_config_dir="${ENV_DIR}/mavlink-router"

  log "removing Atlas onboard config"
  run rm -f "$ENV_FILE"
  assert_safe_dir "$ENV_DIR" "Atlas config dir"
  assert_safe_dir "$mavlink_config_dir" "MAVLink Router config dir"
  run rm -rf "$mavlink_config_dir"
  remove_empty_dir "$ENV_DIR"
}

remove_state() {
  log "removing Atlas agent state and logs"
  assert_safe_dir "$STATE_DIR" "Atlas state dir"
  assert_safe_dir "$LOG_DIR" "Atlas log dir"
  run rm -f \
    "${LOG_DIR}/atlas-agent.log" \
    "${LOG_DIR}/atlas-video-agent.log" \
    "${LOG_DIR}/atlas-mavsdk.log" \
    "${LOG_DIR}/atlas-mavlink-router.log"

  assert_safe_dir "${STATE_DIR}/perception" "perception state dir"
  run rm -rf "${STATE_DIR}/perception"

  if [[ "$REMOVE_MEDIA" -eq 1 ]]; then
    run rm -f "${LOG_DIR}/atlas-mediamtx.log"
  else
    log "preserving MediaMTX log at ${LOG_DIR}/atlas-mediamtx.log"
  fi

  remove_empty_dir "$LOG_DIR"
  remove_empty_dir "$STATE_DIR"
}

remove_eth0_config() {
  if [[ "$REMOVE_ETH0_CONFIG" -ne 1 ]]; then
    log "preserving eth0 netplan config; pass --remove-eth0-config to remove it"
    return
  fi

  log "removing eth0 netplan config"
  run sudo rm -f "$ETH0_NETPLAN_FILE"
  log "netplan was not applied automatically; run sudo netplan try/apply manually if needed"
}

purge_agent_packages() {
  if [[ "$PURGE_AGENT_PACKAGES" -ne 1 ]]; then
    log "skipping apt package removal; pass --purge-agent-packages to purge agent-side packages"
    return
  fi

  if [[ "$DRY_RUN" -eq 0 ]] && ! command -v apt-get >/dev/null 2>&1; then
    fail "--purge-agent-packages requires apt-get"
  fi

  log "purging agent-side apt packages"
  run sudo apt-get remove -y --purge "${AGENT_PACKAGE_CANDIDATES[@]}"
  run sudo apt-get autoremove -y
  log "preserved ffmpeg, GStreamer/media packages, Hailo packages, and MediaMTX"
}

print_plan() {
  log "cleanup plan:"
  log "  remove services: ${CORE_SERVICES[*]}"
  if [[ "$REMOVE_MEDIA" -eq 1 ]]; then
    log "  remove media service: ${MEDIA_SERVICES[*]}"
    log "  remove MediaMTX dir: ${MEDIAMTX_DIR}"
  else
    log "  preserve media service/files: ${MEDIA_SERVICES[*]}, ${MEDIAMTX_DIR}"
  fi
  log "  remove env file: ${ENV_FILE}"
  log "  remove config dir: ${ENV_DIR}/mavlink-router"
  log "  remove source-built mavlink-router only when marker exists: ${MAVLINK_ROUTER_SOURCE_MARKER}"
  log "  remove agent binaries: ${INSTALL_PREFIX}/bin/atlas-agent, ${INSTALL_PREFIX}/bin/atlas-video-agent.py, ${MAVSDK_SERVER_BIN}"
  log "  remove agent logs/state under: ${STATE_DIR}"
  if [[ "$REMOVE_ETH0_CONFIG" -eq 1 ]]; then
    log "  remove eth0 netplan file: ${ETH0_NETPLAN_FILE}"
  fi
  if [[ "$PURGE_AGENT_PACKAGES" -eq 1 ]]; then
    log "  purge apt packages: ${AGENT_PACKAGE_CANDIDATES[*]}"
    log "  preserve apt media packages: ffmpeg, gstreamer*, hailo*, rpicam*"
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --yes|--force)
      CONFIRM=1
      shift
      ;;
    --remove-eth0-config)
      REMOVE_ETH0_CONFIG=1
      shift
      ;;
    --purge-agent-packages)
      PURGE_AGENT_PACKAGES=1
      shift
      ;;
    --remove-media)
      REMOVE_MEDIA=1
      shift
      ;;
    --install-prefix)
      require_value "$1" "${2:-}"
      INSTALL_PREFIX="$2"
      MEDIAMTX_DIR="${INSTALL_PREFIX}/mediamtx"
      MAVSDK_SERVER_BIN="${INSTALL_PREFIX}/bin/mavsdk_server"
      MAVLINK_ROUTER_SOURCE_DIR="${INSTALL_PREFIX}/src/mavlink-router"
      MAVLINK_ROUTER_SOURCE_MARKER="${MAVLINK_ROUTER_SOURCE_DIR}/.atlas-source-install"
      shift 2
      ;;
    --env-file)
      require_value "$1" "${2:-}"
      ENV_FILE="$2"
      ENV_DIR="$(dirname "$ENV_FILE")"
      shift 2
      ;;
    --state-dir)
      require_value "$1" "${2:-}"
      STATE_DIR="$2"
      if [[ "$LOG_DIR_OVERRIDDEN" -eq 0 ]]; then
        LOG_DIR="${STATE_DIR}/logs"
      fi
      shift 2
      ;;
    --log-dir)
      require_value "$1" "${2:-}"
      LOG_DIR="$2"
      LOG_DIR_OVERRIDDEN=1
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

print_plan

if [[ "$DRY_RUN" -eq 0 && "$CONFIRM" -ne 1 ]]; then
  fail "refusing to remove files without --yes; rerun with --dry-run first to inspect actions"
fi

cleanup_services
remove_systemd_units
remove_agent_binaries
remove_mavlink_router_source_install
remove_config
remove_state
remove_eth0_config
purge_agent_packages

log "cleanup complete"
