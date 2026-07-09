#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

DRY_RUN=0
CONFIGURE_ETH0=0
GROUND_GRPC_ADDR="${ATLAS_VEHICLE_AGENT_GRPC_ADDR:-192.168.144.50:9090}"
DRONE_ID="${ATLAS_DRONE_ID:-drone-001}"
DRONE_NAME="${ATLAS_DRONE_NAME:-Atlas Pi 5}"
VEHICLE_AGENT_ID="${ATLAS_VEHICLE_AGENT_ID:-agent-001}"
INSTALL_PREFIX="${ATLAS_ONBOARD_INSTALL_PREFIX:-/opt/atlas}"
ENV_DIR="${ATLAS_ONBOARD_ENV_DIR:-${HOME}/.config/atlas-agent}"
ENV_FILE="${ATLAS_ONBOARD_ENV_FILE:-${ENV_DIR}/onboard.env}"
LOG_DIR="${ATLAS_ONBOARD_LOG_DIR:-${HOME}/.local/state/atlas-agent/logs}"
MEDIAMTX_VERSION="${ATLAS_MEDIAMTX_VERSION:-v1.14.0}"
MEDIAMTX_DIR="${ATLAS_MEDIAMTX_DIR:-${INSTALL_PREFIX}/mediamtx}"
MODEL_PATH="${ATLAS_PERCEPTION_MODEL_PATH:-${INSTALL_PREFIX}/models/yolov6n.hef}"

APT_PACKAGES=(
  curl
  git
  ca-certificates
  build-essential
  python3
  python3-venv
  python3-pip
  ffmpeg
  gstreamer1.0-tools
  gstreamer1.0-plugins-base
  gstreamer1.0-plugins-good
  gstreamer1.0-plugins-bad
  gstreamer1.0-plugins-ugly
  gstreamer1.0-libav
  libgstreamer1.0-0
  libgstreamer-plugins-base1.0-0
  mavlink-router
  netcat-openbsd
  golang-go
)

usage() {
  cat <<EOF
Usage: scripts/install-onboard-pi.sh [options]

Installs/configures the Atlas onboard Raspberry Pi stack.

Options:
  --dry-run                 Print commands/files without changing the system.
  --configure-eth0          Write local-only static eth0 netplan config.
  --ground-grpc ADDR        Backend vehicle-agent gRPC address. Default: ${GROUND_GRPC_ADDR}
  --drone-id ID             Drone id. Default: ${DRONE_ID}
  --drone-name NAME         Drone display name. Default: ${DRONE_NAME}
  --vehicle-agent-id ID     Vehicle agent id. Default: ${VEHICLE_AGENT_ID}
  --install-prefix PATH     Install prefix. Default: ${INSTALL_PREFIX}
  --env-file PATH           Env file path. Default: ${ENV_FILE}
  -h, --help                Show this help.
EOF
}

log() {
  printf '[atlas-onboard-install] %s\n' "$*"
}

fail() {
  printf '[atlas-onboard-install] error: %s\n' "$*" >&2
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
    printf '+ %s\n' "$*"
    return
  fi
  "$@"
}

run_shell() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ %s\n' "$*"
    return
  fi
  bash -lc "$*"
}

write_file() {
  local path="$1"
  local mode="$2"
  local content="$3"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf -- '--- %s (%s) ---\n%s\n' "$path" "$mode" "$content"
    return
  fi

  if [[ "$path" == /etc/* || "$path" == /opt/* ]]; then
    printf '%s\n' "$content" | sudo tee "$path" >/dev/null
    sudo chmod "$mode" "$path"
  else
    mkdir -p "$(dirname "$path")"
    printf '%s\n' "$content" >"$path"
    chmod "$mode" "$path"
  fi
}

detect_platform() {
  log "platform: $(uname -a)"
  if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    source /etc/os-release
    log "os: ${PRETTY_NAME:-unknown}"
  fi
  if [[ "$(uname -m)" != "aarch64" && "$(uname -m)" != "arm64" ]]; then
    log "warning: expected arm64/aarch64 Raspberry Pi OS, got $(uname -m)"
  fi
  if [[ -r /proc/device-tree/model ]]; then
    local model
    model="$(tr -d '\0' </proc/device-tree/model)"
    log "board: ${model}"
    if [[ "$model" != *"Raspberry Pi 5"* ]]; then
      log "warning: expected Raspberry Pi 5 for AI HAT/Hailo MVP"
    fi
  else
    log "warning: /proc/device-tree/model not readable; cannot confirm Pi 5"
  fi
}

install_apt_packages() {
  log "installing apt packages"
  run sudo apt-get update
  run sudo apt-get install -y "${APT_PACKAGES[@]}"
}

install_hailo_packages() {
  log "checking Raspberry Pi/Hailo apt packages"
  local hailo_packages=()
  for package_name in hailo-all hailort hailo-tappas-core rpicam-apps-hailo-postprocess; do
    if apt-cache show "$package_name" >/dev/null 2>&1; then
      hailo_packages+=("$package_name")
    fi
  done

  if [[ "${#hailo_packages[@]}" -eq 0 ]]; then
    log "warning: no Hailo apt packages were found in configured repositories"
    log "         install Raspberry Pi AI Kit/Hailo runtime packages from official Raspberry Pi docs"
    return
  fi

  run sudo apt-get install -y "${hailo_packages[@]}"
}

verify_hailo() {
  log "verifying Hailo device visibility"
  if command -v hailortcli >/dev/null 2>&1; then
    run_shell "hailortcli fw-control identify || true"
    return
  fi
  run_shell "lspci | grep -i hailo || true"
  log "warning: hailortcli not found; Hailo runtime may still need setup"
}

install_mediamtx() {
  log "installing MediaMTX into ${MEDIAMTX_DIR}"
  if [[ -x "${MEDIAMTX_DIR}/mediamtx" ]]; then
    log "MediaMTX already installed"
    return
  fi

  local archive="/tmp/mediamtx_${MEDIAMTX_VERSION}_linux_arm64v8.tar.gz"
  run sudo mkdir -p "$MEDIAMTX_DIR"
  run sudo chown "$USER":"$USER" "$MEDIAMTX_DIR"
  run curl -L "https://github.com/bluenviron/mediamtx/releases/download/${MEDIAMTX_VERSION}/mediamtx_${MEDIAMTX_VERSION#v}_linux_arm64v8.tar.gz" -o "$archive"
  run tar -xzf "$archive" -C "$MEDIAMTX_DIR"
}

build_atlas_agent() {
  log "building atlas-agent binary"
  run sudo mkdir -p "${INSTALL_PREFIX}/bin"
  run sudo chown "$USER":"$USER" "${INSTALL_PREFIX}/bin"
  run_shell "cd '${ROOT_DIR}' && go build -o '${INSTALL_PREFIX}/bin/atlas-agent' ./cmd/atlas-agent"
  run install -m 0755 "${SCRIPT_DIR}/atlas-video-agent.py" "${INSTALL_PREFIX}/bin/atlas-video-agent.py"
}

ensure_mavsdk_server() {
  if command -v mavsdk_server >/dev/null 2>&1; then
    log "mavsdk_server already available"
    return
  fi
  log "warning: mavsdk_server not found"
  log "         install mavsdk_server and set ATLAS_MAVSDK_SERVER_BIN if it is not on PATH"
}

write_env_file() {
  local content
  content="$(cat <<EOF
ATLAS_DRONE_ID="${DRONE_ID}"
ATLAS_DRONE_NAME="${DRONE_NAME}"
ATLAS_VEHICLE_AGENT_ID="${VEHICLE_AGENT_ID}"
ATLAS_VEHICLE_AGENT_GRPC_ADDR="${GROUND_GRPC_ADDR}"
ATLAS_MAVSDK_GRPC_ADDR="127.0.0.1:50051"
ATLAS_PX4_SYSTEM_ADDRESS="udpin://0.0.0.0:14540"
ATLAS_MAVLINK_OBSERVER_ENDPOINT="udp-server://0.0.0.0:14550"
ATLAS_A8_RTSP_URL="rtsp://192.168.144.25:8554/main.264"
ATLAS_PROCESSED_RTSP_URL="rtsp://127.0.0.1:8554/atlas"
ATLAS_PERCEPTION_MODEL_PATH="${MODEL_PATH}"
ATLAS_PERCEPTION_ACCELERATOR="hailo"
ATLAS_PERCEPTION_SOURCE_ID="a8-main"
ATLAS_PERCEPTION_METADATA_PATH="${HOME}/.local/state/atlas-agent/perception/metadata.jsonl"
ATLAS_COMPANION_LOG_DIR="${LOG_DIR}"
ATLAS_MAVLINK_ROUTER_CONFIG_FILE="${HOME}/.config/atlas-agent/mavlink-router/main.conf"
EOF
)"
  write_file "$ENV_FILE" "0644" "$content"
}

configure_eth0() {
  if [[ "$CONFIGURE_ETH0" -ne 1 ]]; then
    log "eth0 static config skipped; rerun with --configure-eth0 to enable"
    return
  fi

  local content
  content="$(cat <<'EOF'
network:
  version: 2
  ethernets:
    eth0:
      dhcp4: false
      dhcp6: false
      addresses:
        - 192.168.144.168/24
      optional: true
EOF
)"
  write_file "/etc/netplan/99-siyi-eth0-local.yaml" "0644" "$content"
  log "eth0 netplan written; apply manually with: sudo netplan try"
}

write_systemd_units() {
  log "writing systemd units"
  local user_name="${SUDO_USER:-$USER}"
  local group_name
  group_name="$(id -gn "$user_name")"

  write_file "/etc/systemd/system/atlas-mediamtx.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas MediaMTX RTSP server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${user_name}
Group=${group_name}
WorkingDirectory=${MEDIAMTX_DIR}
ExecStart=${MEDIAMTX_DIR}/mediamtx
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-mediamtx.log
StandardError=append:${LOG_DIR}/atlas-mediamtx.log

[Install]
WantedBy=multi-user.target
EOF
)"

  write_file "/etc/systemd/system/atlas-mavlink-router.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas MAVLink Router
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${user_name}
Group=${group_name}
EnvironmentFile=${ENV_FILE}
ExecStart=/usr/bin/mavlink-routerd -c \${ATLAS_MAVLINK_ROUTER_CONFIG_FILE}
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-mavlink-router.log
StandardError=append:${LOG_DIR}/atlas-mavlink-router.log

[Install]
WantedBy=multi-user.target
EOF
)"

  write_file "/etc/systemd/system/atlas-mavsdk.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas MAVSDK Server
After=atlas-mavlink-router.service
Requires=atlas-mavlink-router.service

[Service]
Type=simple
User=${user_name}
Group=${group_name}
EnvironmentFile=${ENV_FILE}
ExecStart=/usr/bin/env bash -lc 'exec mavsdk_server -p "\${ATLAS_MAVSDK_GRPC_ADDR##*:}" "\${ATLAS_PX4_SYSTEM_ADDRESS}"'
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-mavsdk.log
StandardError=append:${LOG_DIR}/atlas-mavsdk.log

[Install]
WantedBy=multi-user.target
EOF
)"

  write_file "/etc/systemd/system/atlas-agent.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas Vehicle Agent
After=atlas-mavsdk.service
Requires=atlas-mavsdk.service

[Service]
Type=simple
User=${user_name}
Group=${group_name}
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_PREFIX}/bin/atlas-agent
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-agent.log
StandardError=append:${LOG_DIR}/atlas-agent.log

[Install]
WantedBy=multi-user.target
EOF
)"

  write_file "/etc/systemd/system/atlas-video-agent.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas Hailo Video Agent
After=atlas-mediamtx.service
Requires=atlas-mediamtx.service

[Service]
Type=simple
User=${user_name}
Group=${group_name}
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_PREFIX}/bin/atlas-video-agent.py
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-video-agent.log
StandardError=append:${LOG_DIR}/atlas-video-agent.log

[Install]
WantedBy=multi-user.target
EOF
)"

  if [[ "$DRY_RUN" -eq 0 ]]; then
    run mkdir -p "$LOG_DIR"
    run sudo systemctl daemon-reload
    run sudo systemctl enable atlas-mediamtx.service atlas-mavlink-router.service atlas-mavsdk.service atlas-agent.service atlas-video-agent.service
  fi
}

generate_mavlink_router_config() {
  run "${SCRIPT_DIR}/setup-mavlink-router.sh" --env "${HOME}/.config/atlas-agent/mavlink-router/atlas-mavlink.env" --dry-run
  if [[ "$DRY_RUN" -eq 0 ]]; then
    "${SCRIPT_DIR}/setup-mavlink-router.sh"
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --configure-eth0)
      CONFIGURE_ETH0=1
      shift
      ;;
    --ground-grpc)
      require_value "$1" "${2:-}"
      GROUND_GRPC_ADDR="$2"
      shift 2
      ;;
    --drone-id)
      require_value "$1" "${2:-}"
      DRONE_ID="$2"
      shift 2
      ;;
    --drone-name)
      require_value "$1" "${2:-}"
      DRONE_NAME="$2"
      shift 2
      ;;
    --vehicle-agent-id)
      require_value "$1" "${2:-}"
      VEHICLE_AGENT_ID="$2"
      shift 2
      ;;
    --install-prefix)
      require_value "$1" "${2:-}"
      INSTALL_PREFIX="$2"
      MEDIAMTX_DIR="${INSTALL_PREFIX}/mediamtx"
      MODEL_PATH="${INSTALL_PREFIX}/models/yolov6n.hef"
      shift 2
      ;;
    --env-file)
      require_value "$1" "${2:-}"
      ENV_FILE="$2"
      ENV_DIR="$(dirname "$ENV_FILE")"
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

detect_platform
install_apt_packages
install_hailo_packages
verify_hailo
install_mediamtx
build_atlas_agent
ensure_mavsdk_server
write_env_file
configure_eth0
generate_mavlink_router_config
write_systemd_units

log "onboard install complete"
log "env file: ${ENV_FILE}"
log "start stack: ${SCRIPT_DIR}/start-onboard-stack.sh"
log "status: ${SCRIPT_DIR}/status-onboard-stack.sh"
