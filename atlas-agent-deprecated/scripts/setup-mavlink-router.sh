#!/usr/bin/env bash
set -euo pipefail

CONFIG_DIR="${ATLAS_MAVLINK_ROUTER_CONFIG_DIR:-"${HOME}/.config/atlas-agent/mavlink-router"}"
CONFIG_FILE="${ATLAS_MAVLINK_ROUTER_CONFIG_FILE:-"${CONFIG_DIR}/main.conf"}"
ENV_FILE="${ATLAS_MAVLINK_ROUTER_ENV_FILE:-"${CONFIG_DIR}/atlas-mavlink.env"}"

UART_DEVICE="${ATLAS_MAVLINK_ROUTER_UART_DEVICE:-/dev/serial0}"
UART_BAUD="${ATLAS_MAVLINK_ROUTER_UART_BAUD:-921600}"

MAVSDK_ADDR="${ATLAS_MAVLINK_ROUTER_MAVSDK_ADDR:-127.0.0.1}"
MAVSDK_PORT="${ATLAS_MAVLINK_ROUTER_MAVSDK_PORT:-14540}"
OBSERVER_ADDR="${ATLAS_MAVLINK_ROUTER_OBSERVER_ADDR:-127.0.0.1}"
OBSERVER_PORT="${ATLAS_MAVLINK_ROUTER_OBSERVER_PORT:-14550}"
QGC_ADDR="${ATLAS_MAVLINK_ROUTER_QGC_ADDR:-}"
QGC_PORT="${ATLAS_MAVLINK_ROUTER_QGC_PORT:-14560}"
MAVSDK_GRPC_ADDR="${ATLAS_MAVSDK_GRPC_ADDR:-127.0.0.1:50051}"

INSTALL=0
START=0
DRY_RUN=0

usage() {
  cat <<EOF
Usage: scripts/setup-mavlink-router.sh [options]

Generates an Atlas MAVLink Router config for a Pixhawk serial link and local UDP fanout.

Defaults:
  serial device:     ${UART_DEVICE}
  serial baud:       ${UART_BAUD}
  MAVSDK UDP output: ${MAVSDK_ADDR}:${MAVSDK_PORT}
  observer output:   ${OBSERVER_ADDR}:${OBSERVER_PORT}
  config file:       ${CONFIG_FILE}
  env file:          ${ENV_FILE}

Options:
  --device PATH       Pixhawk serial device on the companion computer.
  --baud RATE         Serial baud rate. Must match Pixhawk TELEM2 config.
  --mavsdk-port PORT  UDP port routed to mavsdk_server. Default: ${MAVSDK_PORT}
  --observer-port PORT UDP port routed to Atlas MAVLink observer. Default: ${OBSERVER_PORT}
  --qgc ADDR:PORT     Optional QGroundControl UDP output, for example 192.168.1.20:14550.
  --config PATH       Output mavlink-router config path.
  --env PATH          Output Atlas environment file path.
  --install           Try to install mavlink-router with apt-get if missing.
  --start             Start mavlink-routerd in the foreground after writing config.
  --dry-run           Print planned files and commands without writing or starting.
  -h, --help          Show this help.

Useful environment overrides:
  ATLAS_MAVLINK_ROUTER_CONFIG_DIR
  ATLAS_MAVLINK_ROUTER_CONFIG_FILE
  ATLAS_MAVLINK_ROUTER_ENV_FILE
  ATLAS_MAVLINK_ROUTER_UART_DEVICE
  ATLAS_MAVLINK_ROUTER_UART_BAUD
  ATLAS_MAVLINK_ROUTER_MAVSDK_ADDR
  ATLAS_MAVLINK_ROUTER_MAVSDK_PORT
  ATLAS_MAVLINK_ROUTER_OBSERVER_ADDR
  ATLAS_MAVLINK_ROUTER_OBSERVER_PORT
  ATLAS_MAVLINK_ROUTER_QGC_ADDR
  ATLAS_MAVLINK_ROUTER_QGC_PORT
  ATLAS_MAVSDK_GRPC_ADDR
EOF
}

log() {
  printf '[atlas-mavlink-router] %s\n' "$*"
}

fail() {
  printf '[atlas-mavlink-router] error: %s\n' "$*" >&2
  exit 1
}

require_positive_int() {
  local label="$1"
  local value="$2"

  if [[ ! "$value" =~ ^[0-9]+$ ]] || [[ "$value" -le 0 ]]; then
    fail "${label} must be a positive integer, got: ${value}"
  fi
}

parse_host_port() {
  local value="$1"
  local host_var="$2"
  local port_var="$3"

  if [[ "$value" != *:* ]]; then
    fail "--qgc must use ADDR:PORT, got: ${value}"
  fi

  local host="${value%:*}"
  local port="${value##*:}"
  if [[ -z "$host" || -z "$port" ]]; then
    fail "--qgc must use ADDR:PORT, got: ${value}"
  fi
  require_positive_int "QGC port" "$port"

  printf -v "$host_var" '%s' "$host"
  printf -v "$port_var" '%s' "$port"
}

require_option_value() {
  local option="$1"
  local value="${2:-}"

  if [[ -z "$value" || "$value" == --* ]]; then
    fail "${option} requires a value"
  fi
}

ensure_mavlink_router() {
  if command -v mavlink-routerd >/dev/null 2>&1; then
    return
  fi

  if [[ "$INSTALL" -ne 1 ]]; then
    if [[ "$START" -eq 1 ]]; then
      fail "mavlink-routerd not found. Install mavlink-router, or rerun with --install on Debian/Raspberry Pi OS."
    fi

    log "warning: mavlink-routerd not found; generated config only"
    log "         install mavlink-router before starting the router"
    return
  fi

  if ! command -v apt-get >/dev/null 2>&1; then
    fail "--install currently supports apt-get based systems only"
  fi

  log "installing mavlink-router via apt-get"
  sudo apt-get update
  sudo apt-get install -y mavlink-router
}

write_config() {
  local qgc_block=""
  if [[ -n "$QGC_ADDR" ]]; then
    qgc_block="
[UdpEndpoint qgroundcontrol]
Mode = Normal
Address = ${QGC_ADDR}
Port = ${QGC_PORT}
"
  fi

  mkdir -p "$(dirname "$CONFIG_FILE")"
  cat >"$CONFIG_FILE" <<EOF
[General]
TcpServerPort = 0
ReportStats = true

[UartEndpoint pixhawk-telem2]
Device = ${UART_DEVICE}
Baud = ${UART_BAUD}
FlowControl = false

[UdpEndpoint mavsdk]
Mode = Normal
Address = ${MAVSDK_ADDR}
Port = ${MAVSDK_PORT}

[UdpEndpoint atlas-observer]
Mode = Normal
Address = ${OBSERVER_ADDR}
Port = ${OBSERVER_PORT}${qgc_block}
EOF
}

write_env() {
  mkdir -p "$(dirname "$ENV_FILE")"
  cat >"$ENV_FILE" <<EOF
export ATLAS_MAVSDK_GRPC_ADDR="${MAVSDK_GRPC_ADDR}"
export ATLAS_PX4_SYSTEM_ADDRESS="udpin://0.0.0.0:${MAVSDK_PORT}"
export ATLAS_MAVLINK_OBSERVER_ENDPOINT="udp-server://0.0.0.0:${OBSERVER_PORT}"
export ATLAS_MAVLINK_ROUTER_CONFIG_FILE="${CONFIG_FILE}"
EOF
}

print_summary() {
  log "MAVLink Router config: ${CONFIG_FILE}"
  log "Atlas env file:        ${ENV_FILE}"
  log "Pixhawk serial:        ${UART_DEVICE}:${UART_BAUD}"
  log "MAVSDK UDP output:     ${MAVSDK_ADDR}:${MAVSDK_PORT}"
  log "Observer UDP output:   ${OBSERVER_ADDR}:${OBSERVER_PORT}"
  if [[ -n "$QGC_ADDR" ]]; then
    log "QGC UDP output:        ${QGC_ADDR}:${QGC_PORT}"
  fi
  log "Run router:            mavlink-routerd -c \"${CONFIG_FILE}\""
  log "Use Atlas env:         source \"${ENV_FILE}\""
  log "Run mavsdk_server:     mavsdk_server -p \"${MAVSDK_GRPC_ADDR##*:}\" \"udpin://0.0.0.0:${MAVSDK_PORT}\""
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --device)
      require_option_value "$1" "${2:-}"
      UART_DEVICE="$2"
      shift 2
      ;;
    --baud)
      require_option_value "$1" "${2:-}"
      UART_BAUD="$2"
      shift 2
      ;;
    --mavsdk-port)
      require_option_value "$1" "${2:-}"
      MAVSDK_PORT="$2"
      shift 2
      ;;
    --observer-port)
      require_option_value "$1" "${2:-}"
      OBSERVER_PORT="$2"
      shift 2
      ;;
    --qgc)
      require_option_value "$1" "${2:-}"
      parse_host_port "$2" QGC_ADDR QGC_PORT
      shift 2
      ;;
    --config)
      require_option_value "$1" "${2:-}"
      CONFIG_FILE="$2"
      shift 2
      ;;
    --env)
      require_option_value "$1" "${2:-}"
      ENV_FILE="$2"
      shift 2
      ;;
    --install)
      INSTALL=1
      shift
      ;;
    --start)
      START=1
      shift
      ;;
    --dry-run)
      DRY_RUN=1
      shift
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

require_positive_int "baud" "$UART_BAUD"
require_positive_int "MAVSDK port" "$MAVSDK_PORT"
require_positive_int "observer port" "$OBSERVER_PORT"
if [[ -n "$QGC_ADDR" ]]; then
  require_positive_int "QGC port" "$QGC_PORT"
fi

if [[ ! -e "$UART_DEVICE" ]]; then
  log "warning: serial device does not exist yet: ${UART_DEVICE}"
  log "         on Raspberry Pi this usually means serial is disabled, the device name differs, or Pixhawk is not connected"
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
  print_summary
  exit 0
fi

ensure_mavlink_router
write_config
write_env
print_summary

if [[ "$START" -eq 1 ]]; then
  log "starting mavlink-routerd in foreground"
  exec mavlink-routerd -c "$CONFIG_FILE"
fi
