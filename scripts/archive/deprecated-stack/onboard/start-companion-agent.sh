#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

ENV_FILE="${ATLAS_MAVLINK_ROUTER_ENV_FILE:-"${HOME}/.config/atlas-agent/mavlink-router/atlas-mavlink.env"}"
LOG_DIR="${ATLAS_COMPANION_LOG_DIR:-"${HOME}/.local/state/atlas-agent/logs/$(date +%Y%m%d-%H%M%S)"}"

MAVSDK_SERVER_BIN="${ATLAS_MAVSDK_SERVER_BIN:-mavsdk_server}"
ATLAS_AGENT_BIN="${ATLAS_AGENT_BIN:-}"
AUTO_SETUP_ROUTER="${ATLAS_COMPANION_AUTO_SETUP_ROUTER:-1}"

CLI_ROUTER_CONFIG=""
CLI_MAVSDK_GRPC_ADDR=""
CLI_BACKEND_GRPC_ADDR=""

SKIP_ROUTER=0
SKIP_MAVSDK=0
SKIP_AGENT=0
DRY_RUN=0

PIDS=()
NAMES=()

usage() {
  cat <<EOF
Usage: scripts/start-companion-agent.sh [options]

Starts the Atlas companion runtime stack:
  mavlink-routerd -> mavsdk_server -> atlas-agent

Options:
  --env PATH              Atlas MAVLink env file. Default: ${ENV_FILE}
  --router-config PATH    mavlink-router config path.
  --mavsdk-bin PATH       mavsdk_server binary path/name. Default: ${MAVSDK_SERVER_BIN}
  --mavsdk-grpc ADDR      mavsdk_server gRPC listen address. Default: ATLAS_MAVSDK_GRPC_ADDR or 127.0.0.1:50051
  --backend-grpc ADDR     Atlas backend vehicle-agent gRPC address.
  --agent-bin PATH        Atlas agent binary. If omitted, uses 'go run ./cmd/atlas-agent'.
  --log-dir PATH          Runtime log directory. Default: ${LOG_DIR}
  --skip-router           Do not start mavlink-routerd.
  --skip-mavsdk           Do not start mavsdk_server.
  --skip-agent            Do not start atlas-agent.
  --no-auto-setup-router  Do not generate router config/env if missing.
  --dry-run               Print commands without starting processes.
  -h, --help              Show this help.

Useful environment overrides:
  ATLAS_MAVLINK_ROUTER_ENV_FILE
  ATLAS_MAVLINK_ROUTER_CONFIG_FILE
  ATLAS_COMPANION_LOG_DIR
  ATLAS_COMPANION_AUTO_SETUP_ROUTER
  ATLAS_MAVSDK_SERVER_BIN
  ATLAS_MAVSDK_GRPC_ADDR
  ATLAS_PX4_SYSTEM_ADDRESS
  ATLAS_MAVLINK_OBSERVER_ENDPOINT
  ATLAS_VEHICLE_AGENT_GRPC_ADDR
  ATLAS_AGENT_BIN
EOF
}

log() {
  printf '[atlas-companion] %s\n' "$*"
}

fail() {
  printf '[atlas-companion] error: %s\n' "$*" >&2
  exit 1
}

require_option_value() {
  local option="$1"
  local value="${2:-}"

  if [[ -z "$value" || "$value" == --* ]]; then
    fail "${option} requires a value"
  fi
}

tcp_port_from_addr() {
  local addr="$1"
  printf '%s\n' "${addr##*:}"
}

load_env_file() {
  if [[ ! -f "$ENV_FILE" ]]; then
    if [[ "$AUTO_SETUP_ROUTER" -eq 1 && "$SKIP_ROUTER" -eq 0 ]]; then
      if [[ "$DRY_RUN" -eq 1 ]]; then
        log "router env file not found; dry-run would generate it with setup-mavlink-router.sh: ${ENV_FILE}"
        return
      fi

      log "router env file not found; generating default router config/env"
      if [[ -n "${ATLAS_MAVLINK_ROUTER_CONFIG_FILE:-}" ]]; then
        "${SCRIPT_DIR}/setup-mavlink-router.sh" --env "$ENV_FILE" --config "$ATLAS_MAVLINK_ROUTER_CONFIG_FILE"
      else
        "${SCRIPT_DIR}/setup-mavlink-router.sh" --env "$ENV_FILE"
      fi
    else
      log "warning: router env file not found: ${ENV_FILE}"
      log "         continuing with environment/default values"
      return
    fi
  fi

  # shellcheck disable=SC1090
  source "$ENV_FILE"
}

apply_runtime_defaults() {
  if [[ -n "$CLI_ROUTER_CONFIG" ]]; then
    ATLAS_MAVLINK_ROUTER_CONFIG_FILE="$CLI_ROUTER_CONFIG"
  fi
  if [[ -n "$CLI_MAVSDK_GRPC_ADDR" ]]; then
    ATLAS_MAVSDK_GRPC_ADDR="$CLI_MAVSDK_GRPC_ADDR"
  fi
  if [[ -n "$CLI_BACKEND_GRPC_ADDR" ]]; then
    ATLAS_VEHICLE_AGENT_GRPC_ADDR="$CLI_BACKEND_GRPC_ADDR"
  fi

  ATLAS_MAVSDK_GRPC_ADDR="${ATLAS_MAVSDK_GRPC_ADDR:-127.0.0.1:50051}"
  ATLAS_PX4_SYSTEM_ADDRESS="${ATLAS_PX4_SYSTEM_ADDRESS:-udpin://0.0.0.0:14540}"
  ATLAS_MAVLINK_OBSERVER_ENDPOINT="${ATLAS_MAVLINK_OBSERVER_ENDPOINT:-udp-server://0.0.0.0:14550}"
  ATLAS_MAVLINK_ROUTER_CONFIG_FILE="${ATLAS_MAVLINK_ROUTER_CONFIG_FILE:-"${HOME}/.config/atlas-agent/mavlink-router/main.conf"}"
  ATLAS_VEHICLE_AGENT_GRPC_ADDR="${ATLAS_VEHICLE_AGENT_GRPC_ADDR:-127.0.0.1:9090}"

  export ATLAS_MAVSDK_GRPC_ADDR
  export ATLAS_PX4_SYSTEM_ADDRESS
  export ATLAS_MAVLINK_OBSERVER_ENDPOINT
  export ATLAS_MAVLINK_ROUTER_CONFIG_FILE
  export ATLAS_VEHICLE_AGENT_GRPC_ADDR
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "required command not found: $1"
  fi
}

check_prerequisites() {
  if [[ "$SKIP_ROUTER" -eq 0 ]]; then
    require_command mavlink-routerd
    if [[ ! -f "$ATLAS_MAVLINK_ROUTER_CONFIG_FILE" ]]; then
      fail "mavlink-router config not found: ${ATLAS_MAVLINK_ROUTER_CONFIG_FILE}"
    fi
  fi

  if [[ "$SKIP_MAVSDK" -eq 0 ]]; then
    require_command "$MAVSDK_SERVER_BIN"
  fi

  if [[ "$SKIP_AGENT" -eq 0 && -z "$ATLAS_AGENT_BIN" ]]; then
    require_command go
  fi
}

kill_tree() {
  local pid="$1"
  local child

  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    kill_tree "$child"
  done

  kill "$pid" 2>/dev/null || true
}

cleanup() {
  local pid

  trap - INT TERM EXIT

  if [[ ${#PIDS[@]} -gt 0 ]]; then
    log "stopping ${#PIDS[@]} managed process(es)"
    for pid in "${PIDS[@]}"; do
      kill_tree "$pid"
    done

    sleep 2

    for pid in "${PIDS[@]}"; do
      kill -9 "$pid" 2>/dev/null || true
    done
  fi
}

trap cleanup INT TERM EXIT

start_process() {
  local name="$1"
  local workdir="$2"
  local command="$3"
  local logfile="${LOG_DIR}/${name}.log"
  local pid

  log "starting ${name}"
  log "  log: ${logfile}"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '  (cd %q && %s)\n' "$workdir" "$command"
    return
  fi

  mkdir -p "$LOG_DIR"
  (
    cd "$workdir"
    exec bash -lc "$command"
  ) >"$logfile" 2>&1 &
  pid=$!
  PIDS+=("$pid")
  NAMES+=("$name")

  sleep 1
  if ! kill -0 "$pid" 2>/dev/null; then
    fail "${name} exited during startup; see ${logfile}"
  fi
}

wait_for_tcp() {
  local label="$1"
  local host="$2"
  local port="$3"
  local timeout_seconds="$4"
  local elapsed=0

  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  if ! command -v nc >/dev/null 2>&1; then
    log "warning: nc not found; skipping ${label} readiness check"
    sleep 2
    return
  fi

  log "waiting for ${label} on ${host}:${port}"
  until nc -z "$host" "$port" >/dev/null 2>&1; do
    if [[ "$elapsed" -ge "$timeout_seconds" ]]; then
      fail "timed out waiting for ${label} on ${host}:${port}"
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
}

agent_command() {
  if [[ -n "$ATLAS_AGENT_BIN" ]]; then
    printf 'exec %q' "$ATLAS_AGENT_BIN"
    return
  fi

  printf 'go run ./cmd/atlas-agent'
}

monitor_processes() {
  local index
  local pid
  local name

  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  log "companion stack is running"
  log "  logs: ${LOG_DIR}"
  log "press Ctrl-C to stop the stack"

  while true; do
    sleep 2
    index=0
    for pid in "${PIDS[@]}"; do
      name="${NAMES[$index]}"
      if ! kill -0 "$pid" 2>/dev/null; then
        fail "${name} exited; see ${LOG_DIR}/${name}.log"
      fi
      index=$((index + 1))
    done
  done
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env)
      require_option_value "$1" "${2:-}"
      ENV_FILE="$2"
      shift 2
      ;;
    --router-config)
      require_option_value "$1" "${2:-}"
      CLI_ROUTER_CONFIG="$2"
      ATLAS_MAVLINK_ROUTER_CONFIG_FILE="$2"
      export ATLAS_MAVLINK_ROUTER_CONFIG_FILE
      shift 2
      ;;
    --mavsdk-bin)
      require_option_value "$1" "${2:-}"
      MAVSDK_SERVER_BIN="$2"
      shift 2
      ;;
    --mavsdk-grpc)
      require_option_value "$1" "${2:-}"
      CLI_MAVSDK_GRPC_ADDR="$2"
      ATLAS_MAVSDK_GRPC_ADDR="$2"
      export ATLAS_MAVSDK_GRPC_ADDR
      shift 2
      ;;
    --backend-grpc)
      require_option_value "$1" "${2:-}"
      CLI_BACKEND_GRPC_ADDR="$2"
      ATLAS_VEHICLE_AGENT_GRPC_ADDR="$2"
      export ATLAS_VEHICLE_AGENT_GRPC_ADDR
      shift 2
      ;;
    --agent-bin)
      require_option_value "$1" "${2:-}"
      ATLAS_AGENT_BIN="$2"
      shift 2
      ;;
    --log-dir)
      require_option_value "$1" "${2:-}"
      LOG_DIR="$2"
      shift 2
      ;;
    --skip-router)
      SKIP_ROUTER=1
      shift
      ;;
    --skip-mavsdk)
      SKIP_MAVSDK=1
      shift
      ;;
    --skip-agent)
      SKIP_AGENT=1
      shift
      ;;
    --no-auto-setup-router)
      AUTO_SETUP_ROUTER=0
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

load_env_file
apply_runtime_defaults

if [[ "$SKIP_ROUTER" -eq 1 && "$SKIP_MAVSDK" -eq 1 && "$SKIP_AGENT" -eq 1 ]]; then
  fail "nothing to start; all components were skipped"
fi

log "using MAVLink Router config=${ATLAS_MAVLINK_ROUTER_CONFIG_FILE}"
log "using MAVSDK gRPC=${ATLAS_MAVSDK_GRPC_ADDR}"
log "using MAVSDK system=${ATLAS_PX4_SYSTEM_ADDRESS}"
log "using observer endpoint=${ATLAS_MAVLINK_OBSERVER_ENDPOINT}"
log "using backend vehicle-agent gRPC=${ATLAS_VEHICLE_AGENT_GRPC_ADDR}"

if [[ "$DRY_RUN" -eq 0 ]]; then
  check_prerequisites
fi

if [[ "$SKIP_ROUTER" -eq 0 ]]; then
  start_process \
    "mavlink-router" \
    "$ROOT_DIR" \
    "mavlink-routerd -c \"${ATLAS_MAVLINK_ROUTER_CONFIG_FILE}\""
fi

if [[ "$SKIP_MAVSDK" -eq 0 ]]; then
  start_process \
    "mavsdk-server" \
    "$ROOT_DIR" \
    "\"${MAVSDK_SERVER_BIN}\" -p \"$(tcp_port_from_addr "$ATLAS_MAVSDK_GRPC_ADDR")\" \"${ATLAS_PX4_SYSTEM_ADDRESS}\""
  wait_for_tcp "mavsdk_server" "127.0.0.1" "$(tcp_port_from_addr "$ATLAS_MAVSDK_GRPC_ADDR")" 30
fi

if [[ "$SKIP_AGENT" -eq 0 ]]; then
  start_process \
    "atlas-agent" \
    "$ROOT_DIR" \
    "$(agent_command)"
fi

monitor_processes
