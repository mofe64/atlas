#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

PX4_DIR="${ATLAS_PX4_DIR:-"${ROOT_DIR}/../PX4-Autopilot"}"
PX4_VENV="${ATLAS_PX4_VENV:-"${PX4_DIR}/.venv/bin/activate"}"
PX4_TARGET="${ATLAS_PX4_TARGET:-px4_sitl}"
PX4_MODEL="${ATLAS_PX4_MODEL:-gz_x500_gimbal}"
PX4_WORLD="${ATLAS_PX4_WORLD:-baylands}"
PX4_BOOT_WAIT_SECONDS="${ATLAS_PX4_BOOT_WAIT_SECONDS:-20}"
STARTUP_TIMEOUT_SECONDS="${ATLAS_SITL_STARTUP_TIMEOUT_SECONDS:-120}"
ATLAS_GZ_IP="${ATLAS_GZ_IP:-127.0.0.1}"
ATLAS_GZ_AUTO_FOLLOW="${ATLAS_GZ_AUTO_FOLLOW:-1}"
ATLAS_GZ_FOLLOW_TARGET_EXPLICIT=0
if [[ -n "${ATLAS_GZ_FOLLOW_TARGET:-}" ]]; then
  ATLAS_GZ_FOLLOW_TARGET_EXPLICIT=1
fi
ATLAS_GZ_FOLLOW_TARGET="${ATLAS_GZ_FOLLOW_TARGET:-${PX4_MODEL#gz_}_0}"
ATLAS_GZ_FOLLOW_OFFSET_X="${ATLAS_GZ_FOLLOW_OFFSET_X:--4.0}"
ATLAS_GZ_FOLLOW_OFFSET_Y="${ATLAS_GZ_FOLLOW_OFFSET_Y:--4.0}"
ATLAS_GZ_FOLLOW_OFFSET_Z="${ATLAS_GZ_FOLLOW_OFFSET_Z:-3.0}"

ATLAS_SITL_VIDEO_ENABLED="${ATLAS_SITL_VIDEO_ENABLED:-1}"
ATLAS_SITL_VIDEO_TOPIC="${ATLAS_SITL_VIDEO_TOPIC:-}"
ATLAS_SITL_VIDEO_ADDRESS="${ATLAS_SITL_VIDEO_ADDRESS:-127.0.0.1}"
ATLAS_SITL_VIDEO_PORT="${ATLAS_SITL_VIDEO_PORT:-8554}"
ATLAS_SITL_VIDEO_PATH="${ATLAS_SITL_VIDEO_PATH:-/main.264}"
ATLAS_SITL_VIDEO_FPS="${ATLAS_SITL_VIDEO_FPS:-30}"
ATLAS_SITL_VIDEO_BITRATE_KBPS="${ATLAS_SITL_VIDEO_BITRATE_KBPS:-2500}"
ATLAS_SITL_VIDEO_FRAME_TIMEOUT_SECONDS="${ATLAS_SITL_VIDEO_FRAME_TIMEOUT_SECONDS:-45}"
ATLAS_SITL_VIDEO_BUILD_DIR="${ATLAS_SITL_VIDEO_BUILD_DIR:-${ROOT_DIR}/.atlas-run/bin}"
ATLAS_SITL_VIDEO_SOURCE="${ROOT_DIR}/scripts/sitl-video-bridge.cpp"
ATLAS_SITL_VIDEO_BRIDGE="${ATLAS_SITL_VIDEO_BUILD_DIR}/sitl-video-bridge"
ATLAS_VIDEO_RTSP_URL="${ATLAS_VIDEO_RTSP_URL:-}"
ATLAS_VIDEO_SOURCE_ID="${ATLAS_VIDEO_SOURCE_ID:-}"

SITL_MAVLINK_ROUTER="${ATLAS_SITL_MAVLINK_ROUTER:-none}"
MAVPROXY_BIN="${ATLAS_MAVPROXY_BIN:-"${PX4_DIR}/.venv/bin/mavproxy.py"}"
MAVPROXY_MASTER="${ATLAS_MAVPROXY_MASTER:-udp:127.0.0.1:14550}"
MAVPROXY_MAVSDK_OUT="${ATLAS_MAVPROXY_MAVSDK_OUT:-udp:127.0.0.1:14541}"
MAVPROXY_QGC_OUT="${ATLAS_MAVPROXY_QGC_OUT:-none}"

MAVSDK_SERVER_BIN="${ATLAS_MAVSDK_SERVER_BIN:-mavsdk_server}"
MAVSDK_PORT="${ATLAS_MAVSDK_PORT:-50051}"
ATLAS_MAVSDK_GRPC_ADDR="${ATLAS_MAVSDK_GRPC_ADDR:-127.0.0.1:${MAVSDK_PORT}}"
PX4_SYSTEM_ADDRESS_EXPLICIT=0
if [[ -n "${ATLAS_PX4_SYSTEM_ADDRESS:-}" ]]; then
  PX4_SYSTEM_ADDRESS_EXPLICIT=1
fi
ATLAS_PX4_SYSTEM_ADDRESS="${ATLAS_PX4_SYSTEM_ADDRESS:-}"

ATLAS_GROUND_STATION_LISTEN_ADDR="${ATLAS_GROUND_STATION_LISTEN_ADDR:-127.0.0.1:7443}"
ATLAS_NATIVE_UI_PORT=1420
ATLAS_NATIVE_NVM_DIR="${ATLAS_NATIVE_NVM_DIR:-"${HOME}/.nvm"}"
ATLAS_NATIVE_NODE_BIN_DIR="${ATLAS_NATIVE_NODE_BIN_DIR:-}"

SITL_STATE_DIR="${ATLAS_SITL_STATE_DIR:-"${ROOT_DIR}/.atlas-run/state/sitl"}"
ATLAS_SQLITE_PATH="${ATLAS_SQLITE_PATH:-"${SITL_STATE_DIR}/native/atlas.db"}"
ATLAS_AGENT_STATE_DIR="${ATLAS_AGENT_STATE_DIR:-"${SITL_STATE_DIR}/agent"}"
ATLAS_DRONE_NAME="${ATLAS_DRONE_NAME:-Atlas SITL Drone}"
ATLAS_AGENT_VERSION="${ATLAS_AGENT_VERSION:-0.1.0-dev}"
ATLAS_VEHICLE_TYPE="${ATLAS_VEHICLE_TYPE:-multicopter}"

LOG_DIR="${ATLAS_RUN_LOG_DIR:-"${ROOT_DIR}/.atlas-run/logs/$(date +%Y%m%d-%H%M%S)"}"

SKIP_PX4=0
SKIP_MAVSDK=0
SKIP_NATIVE=0
SKIP_AGENT=0
SKIP_VIDEO=0
DRY_RUN=0

PIDS=()
NAMES=()

usage() {
  cat <<EOF
Usage: scripts/start-sitl.sh [options]

Starts the current local-first Atlas SITL stack:
  PX4 Gazebo -> local RTSP video + mavsdk_server -> atlas-agent -> Atlas Native

Options:
  --px4-dir PATH       PX4-Autopilot checkout. Default: ${PX4_DIR}
  --px4-model MODEL    PX4 Gazebo model. Default: ${PX4_MODEL}
  --world WORLD        Gazebo world name without .sdf. Default: ${PX4_WORLD}
  --mavlink-router MODE
                       MAVLink fanout mode: none or mavproxy. Default: ${SITL_MAVLINK_ROUTER}
  --qgc-out ENDPOINT   MAVProxy QGC output endpoint, or "none". Default: ${MAVPROXY_QGC_OUT}
  --state-dir PATH     Persistent Native + Agent SITL state. Default: ${SITL_STATE_DIR}
  --skip-px4           Do not start PX4 Gazebo SITL.
  --skip-mavsdk        Do not start mavsdk_server.
  --skip-native        Do not start the Atlas Native Tauri application.
  --skip-agent         Do not start the current Atlas Agent.
  --skip-video         Do not stream the Gazebo gimbal camera over local RTSP.
  --dry-run            Validate inputs and print commands without starting processes.
  -h, --help           Show this help.

Useful environment overrides:
  ATLAS_PX4_DIR
  ATLAS_PX4_VENV
  ATLAS_PX4_MODEL
  ATLAS_PX4_WORLD
  ATLAS_GZ_IP
  ATLAS_GZ_AUTO_FOLLOW
  ATLAS_GZ_FOLLOW_TARGET
  ATLAS_GZ_FOLLOW_OFFSET_X
  ATLAS_GZ_FOLLOW_OFFSET_Y
  ATLAS_GZ_FOLLOW_OFFSET_Z
  ATLAS_SITL_VIDEO_ENABLED
  ATLAS_SITL_VIDEO_TOPIC
  ATLAS_SITL_VIDEO_ADDRESS
  ATLAS_SITL_VIDEO_PORT
  ATLAS_SITL_VIDEO_PATH
  ATLAS_SITL_VIDEO_FPS
  ATLAS_SITL_VIDEO_BITRATE_KBPS
  ATLAS_SITL_VIDEO_FRAME_TIMEOUT_SECONDS
  ATLAS_SITL_VIDEO_BUILD_DIR
  ATLAS_VIDEO_RTSP_URL
  ATLAS_VIDEO_SOURCE_ID
  ATLAS_SITL_STARTUP_TIMEOUT_SECONDS
  ATLAS_SITL_MAVLINK_ROUTER
  ATLAS_MAVPROXY_BIN
  ATLAS_MAVPROXY_MASTER
  ATLAS_MAVPROXY_MAVSDK_OUT
  ATLAS_MAVPROXY_QGC_OUT
  ATLAS_MAVSDK_SERVER_BIN
  ATLAS_MAVSDK_GRPC_ADDR
  ATLAS_PX4_SYSTEM_ADDRESS
  ATLAS_GROUND_STATION_LISTEN_ADDR
  ATLAS_GROUND_STATION_ADDR
  ATLAS_NATIVE_NVM_DIR
  ATLAS_NATIVE_NODE_BIN_DIR
  ATLAS_SITL_STATE_DIR
  ATLAS_SQLITE_PATH
  ATLAS_AGENT_STATE_DIR
  ATLAS_DRONE_NAME
  ATLAS_AGENT_VERSION
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --px4-dir)
      PX4_DIR="$2"
      PX4_VENV="${ATLAS_PX4_VENV:-"${PX4_DIR}/.venv/bin/activate"}"
      shift 2
      ;;
    --px4-model)
      PX4_MODEL="$2"
      if [[ "$ATLAS_GZ_FOLLOW_TARGET_EXPLICIT" -eq 0 ]]; then
        ATLAS_GZ_FOLLOW_TARGET="${PX4_MODEL#gz_}_0"
      fi
      shift 2
      ;;
    --world)
      PX4_WORLD="$2"
      shift 2
      ;;
    --mavlink-router)
      SITL_MAVLINK_ROUTER="$2"
      shift 2
      ;;
    --qgc-out)
      MAVPROXY_QGC_OUT="$2"
      shift 2
      ;;
    --state-dir)
      SITL_STATE_DIR="$2"
      ATLAS_SQLITE_PATH="${SITL_STATE_DIR}/native/atlas.db"
      ATLAS_AGENT_STATE_DIR="${SITL_STATE_DIR}/agent"
      shift 2
      ;;
    --skip-px4)
      SKIP_PX4=1
      shift
      ;;
    --skip-mavsdk)
      SKIP_MAVSDK=1
      shift
      ;;
    --skip-native)
      SKIP_NATIVE=1
      shift
      ;;
    --skip-agent)
      SKIP_AGENT=1
      shift
      ;;
    --skip-video)
      SKIP_VIDEO=1
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
      echo "Unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

log() {
  printf '[atlas-sitl] %s\n' "$*"
}

fail() {
  printf '[atlas-sitl] error: %s\n' "$*" >&2
  exit 1
}

apply_mavlink_defaults() {
  if [[ "$PX4_SYSTEM_ADDRESS_EXPLICIT" -eq 1 ]]; then
    return
  fi
  if [[ "$SITL_MAVLINK_ROUTER" == "mavproxy" ]]; then
    ATLAS_PX4_SYSTEM_ADDRESS="udpin://0.0.0.0:14541"
  else
    ATLAS_PX4_SYSTEM_ADDRESS="udpin://0.0.0.0:14540"
  fi
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "required command not found: $1"
  fi
}

require_path() {
  if [[ ! -e "$1" ]]; then
    fail "$2 not found: $1"
  fi
}

require_absolute_path() {
  if [[ "$1" != /* ]]; then
    fail "$2 must be an absolute path: $1"
  fi
}

tcp_port_from_addr() {
  printf '%s\n' "${1##*:}"
}

tcp_host_from_addr() {
  local host="${1%:*}"
  if [[ "$host" == "0.0.0.0" || "$host" == "::" || "$host" == "[::]" ]]; then
    host="127.0.0.1"
  fi
  printf '%s\n' "$host"
}

tcp_port_owner() {
  local listeners
  listeners="$(lsof -nP -iTCP:"$1" -sTCP:LISTEN 2>/dev/null || true)"
  printf '%s\n' "$listeners" | awk 'NR == 2 {print $1 " pid " $2}'
}

require_tcp_port_free() {
  local label="$1"
  local port="$2"
  local owner
  if [[ -z "$port" ]]; then
    fail "could not determine ${label} port"
  fi
  owner="$(tcp_port_owner "$port")"
  if [[ -n "$owner" ]]; then
    fail "${label} port ${port} is already in use by ${owner}. Stop that process or override the port."
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
  log "waiting for ${label} on ${host}:${port}"
  until nc -z "$host" "$port" >/dev/null 2>&1; do
    if [[ "$elapsed" -ge "$timeout_seconds" ]]; then
      fail "timed out waiting for ${label} on ${host}:${port}"
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
}

verify_rtsp_video() {
  local codec
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  log "verifying decodable H.264 video at ${SITL_VIDEO_RTSP_URL}"
  if ! codec="$(ffprobe \
    -rtsp_transport tcp \
    -rw_timeout 5000000 \
    -v error \
    -select_streams v:0 \
    -show_entries stream=codec_name \
    -of default=noprint_wrappers=1:nokey=1 \
    "$SITL_VIDEO_RTSP_URL" 2>/dev/null)"; then
    fail "SITL RTSP server started but did not provide decodable video; see ${LOG_DIR}/sitl-video.log"
  fi
  if [[ "$codec" != "h264" ]]; then
    fail "SITL RTSP stream reported unexpected codec '${codec:-none}'; expected h264"
  fi
}

wait_for_log() {
  local label="$1"
  local logfile="$2"
  local pattern="$3"
  local timeout_seconds="$4"
  local elapsed=0
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  log "waiting for ${label}"
  until grep -F "$pattern" "$logfile" >/dev/null 2>&1; do
    if [[ "$elapsed" -ge "$timeout_seconds" ]]; then
      fail "timed out waiting for ${label}; see ${logfile}"
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
}

native_runtime_prefix() {
  if [[ -n "$ATLAS_NATIVE_NODE_BIN_DIR" ]]; then
    printf 'export PATH="%s:$PATH" && ' "$ATLAS_NATIVE_NODE_BIN_DIR"
    return
  fi
  if [[ -s "${ATLAS_NATIVE_NVM_DIR}/nvm.sh" ]]; then
    printf 'export NVM_DIR="%s" && source "%s/nvm.sh" && nvm use --silent && ' \
      "$ATLAS_NATIVE_NVM_DIR" \
      "$ATLAS_NATIVE_NVM_DIR"
  fi
}

native_command() {
  printf '%senv ATLAS_GROUND_STATION_LISTEN_ADDR="%s" ATLAS_SQLITE_PATH="%s" ATLAS_VIDEO_RTSP_URL="%s" ATLAS_VIDEO_SOURCE_ID="%s" npm run tauri dev' \
    "$(native_runtime_prefix)" \
    "$ATLAS_GROUND_STATION_LISTEN_ADDR" \
    "$ATLAS_SQLITE_PATH" \
    "$ATLAS_VIDEO_RTSP_URL" \
    "$ATLAS_VIDEO_SOURCE_ID"
}

sitl_video_command() {
  printf 'env GZ_IP="%s" "%s" --topic "%s" --address "%s" --port "%s" --mount-path "%s" --fps "%s" --bitrate-kbps "%s" --frame-timeout "%s"' \
    "$ATLAS_GZ_IP" \
    "$ATLAS_SITL_VIDEO_BRIDGE" \
    "$ATLAS_SITL_VIDEO_TOPIC" \
    "$ATLAS_SITL_VIDEO_ADDRESS" \
    "$ATLAS_SITL_VIDEO_PORT" \
    "$ATLAS_SITL_VIDEO_PATH" \
    "$ATLAS_SITL_VIDEO_FPS" \
    "$ATLAS_SITL_VIDEO_BITRATE_KBPS" \
    "$ATLAS_SITL_VIDEO_FRAME_TIMEOUT_SECONDS"
}

build_sitl_video_bridge() {
  local packages
  packages="gz-transport13 gz-msgs10 gstreamer-1.0 gstreamer-app-1.0 gstreamer-rtsp-server-1.0"
  if [[ -x "$ATLAS_SITL_VIDEO_BRIDGE" && "$ATLAS_SITL_VIDEO_BRIDGE" -nt "$ATLAS_SITL_VIDEO_SOURCE" ]]; then
    return
  fi

  log "building Gazebo-to-RTSP video bridge"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '  c++ -std=c++17 -O2 -Wall -Wextra %q -o %q $(pkg-config --cflags --libs %s)\n' \
      "$ATLAS_SITL_VIDEO_SOURCE" \
      "$ATLAS_SITL_VIDEO_BRIDGE" \
      "$packages"
    return
  fi

  mkdir -p "$ATLAS_SITL_VIDEO_BUILD_DIR"
  # pkg-config emits the compiler arguments as separate shell words by design.
  # shellcheck disable=SC2046
  if ! c++ -std=c++17 -O2 -Wall -Wextra \
    "$ATLAS_SITL_VIDEO_SOURCE" \
    -o "$ATLAS_SITL_VIDEO_BRIDGE" \
    $(pkg-config --cflags --libs $packages); then
    fail "could not build the Gazebo-to-RTSP video bridge"
  fi
}

mavproxy_command() {
  local command
  command="env MPLCONFIGDIR=\"${LOG_DIR}/matplotlib\" \"${MAVPROXY_BIN}\" --master=\"${MAVPROXY_MASTER}\" --out=\"${MAVPROXY_MAVSDK_OUT}\""
  if [[ -n "$MAVPROXY_QGC_OUT" && "$MAVPROXY_QGC_OUT" != "none" ]]; then
    command="${command} --out=\"${MAVPROXY_QGC_OUT}\""
  fi
  printf '%s --non-interactive --no-state' "$command"
}

configure_gazebo_follow() {
  local request
  local attempt
  if [[ "$ATLAS_GZ_AUTO_FOLLOW" != "1" ]]; then
    log "Gazebo camera auto-follow disabled"
    return
  fi
  request="track_mode: FOLLOW, follow_target: {name: '${ATLAS_GZ_FOLLOW_TARGET}'}, follow_offset: {x: ${ATLAS_GZ_FOLLOW_OFFSET_X}, y: ${ATLAS_GZ_FOLLOW_OFFSET_Y}, z: ${ATLAS_GZ_FOLLOW_OFFSET_Z}}, follow_pgain: 1.0, track_pgain: 1.0"
  log "locking Gazebo camera to ${ATLAS_GZ_FOLLOW_TARGET}"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '  env GZ_IP=%q gz topic -t /gui/track -m gz.msgs.CameraTrack -p %q\n' \
      "$ATLAS_GZ_IP" \
      "$request"
    return
  fi
  # PX4 publishes the same message while the model is spawning. Republish after
  # the boot wait so the GUI CameraTracking plugin is definitely subscribed.
  for attempt in 1 2 3; do
    if env GZ_IP="$ATLAS_GZ_IP" gz topic -t /gui/track -m gz.msgs.CameraTrack -p "$request" >/dev/null 2>&1; then
      sleep 1
    else
      log "Gazebo camera follow attempt ${attempt} was not accepted"
    fi
  done
}

validate_native_runtime() {
  local version_check
  local node_version
  version_check="$(native_runtime_prefix)"'node -e '\''
const [major, minor] = process.versions.node.split(".").map(Number);
const ok = major > 22 || (major === 22 && minor >= 12) || (major === 20 && minor >= 19);
if (!ok) {
  console.error(`Node ${process.versions.node} is too old for Atlas Native.`);
  process.exit(1);
}
console.log(process.versions.node);
'\'''
  if ! node_version="$(cd "${ROOT_DIR}/atlas" && bash -lc "$version_check" 2>&1)"; then
    fail "Atlas Native requires Node 20.19+ or 22.12+. ${node_version}"
  fi
  log "using Atlas Native Node ${node_version}"
}

assert_prerequisites() {
  require_command bash
  require_command go
  require_command nc
  require_command lsof
  require_command grep
  require_absolute_path "$SITL_STATE_DIR" "SITL state directory"
  require_absolute_path "$ATLAS_SQLITE_PATH" "Atlas SQLite path"
  require_absolute_path "$ATLAS_AGENT_STATE_DIR" "Atlas Agent state directory"

  if [[ "$SKIP_VIDEO" -eq 0 ]]; then
    require_command c++
    require_command ffprobe
    require_command pkg-config
    require_path "$ATLAS_SITL_VIDEO_SOURCE" "SITL video bridge source"
    require_absolute_path "$ATLAS_SITL_VIDEO_BUILD_DIR" "SITL video build directory"
    if [[ "$ATLAS_SITL_VIDEO_TOPIC" != /* ]]; then
      fail "ATLAS_SITL_VIDEO_TOPIC must be an absolute Gazebo topic: ${ATLAS_SITL_VIDEO_TOPIC}"
    fi
    if [[ "$ATLAS_SITL_VIDEO_PATH" != /* ]]; then
      fail "ATLAS_SITL_VIDEO_PATH must begin with /: ${ATLAS_SITL_VIDEO_PATH}"
    fi
    if ! pkg-config --exists \
      gz-transport13 \
      gz-msgs10 \
      gstreamer-1.0 \
      gstreamer-app-1.0 \
      gstreamer-rtsp-server-1.0; then
      fail "SITL video requires Gazebo Transport, Gazebo Messages, GStreamer, and GStreamer RTSP Server development packages"
    fi
    require_tcp_port_free "SITL RTSP video" "$ATLAS_SITL_VIDEO_PORT"
  fi

  if [[ "$SKIP_NATIVE" -eq 0 ]]; then
    require_command cargo
    require_path "${ROOT_DIR}/atlas/node_modules" "Atlas Native dependencies"
    validate_native_runtime
    require_tcp_port_free "Atlas Native UI" "$ATLAS_NATIVE_UI_PORT"
    require_tcp_port_free "Atlas Native gRPC" "$(tcp_port_from_addr "$ATLAS_GROUND_STATION_LISTEN_ADDR")"
  fi

  if [[ "$SKIP_MAVSDK" -eq 0 ]]; then
    require_command "$MAVSDK_SERVER_BIN"
    require_tcp_port_free "mavsdk_server" "$MAVSDK_PORT"
  fi

  case "$SITL_MAVLINK_ROUTER" in
    none)
      if [[ -n "$MAVPROXY_QGC_OUT" && "$MAVPROXY_QGC_OUT" != "none" ]]; then
        fail "--qgc-out requires --mavlink-router mavproxy"
      fi
      ;;
    mavproxy)
      require_command "$MAVPROXY_BIN"
      ;;
    *)
      fail "unsupported MAVLink router mode: ${SITL_MAVLINK_ROUTER}"
      ;;
  esac

  if [[ "$SKIP_PX4" -eq 0 ]]; then
    require_command gz
    require_path "$PX4_DIR" "PX4 checkout"
    require_path "$PX4_VENV" "PX4 virtualenv activation script"
    require_path "${PX4_DIR}/Tools/simulation/gz/worlds/${PX4_WORLD}.sdf" "Gazebo world"
  fi
}

monitor_processes() {
  local index
  local pid
  local name
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  log "current Atlas SITL stack is running"
  log "  vehicle: ${PX4_MODEL}"
  log "  world:   ${PX4_WORLD}"
  if [[ "$SKIP_NATIVE" -eq 0 ]]; then
    log "  native:  gRPC ${ATLAS_GROUND_STATION_LISTEN_ADDR}"
    log "  sqlite:  ${ATLAS_SQLITE_PATH}"
  fi
  if [[ "$SKIP_AGENT" -eq 0 ]]; then
    log "  agent:   ${ATLAS_AGENT_STATE_DIR}"
  fi
  if [[ "$SKIP_VIDEO" -eq 0 ]]; then
    log "  video:   ${SITL_VIDEO_RTSP_URL}"
  fi
  log "  logs:    ${LOG_DIR}"
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

apply_mavlink_defaults
case "$ATLAS_SITL_VIDEO_ENABLED" in
  0)
    SKIP_VIDEO=1
    ;;
  1)
    ;;
  *)
    fail "ATLAS_SITL_VIDEO_ENABLED must be 0 or 1"
    ;;
esac
if [[ -z "$ATLAS_SITL_VIDEO_TOPIC" ]]; then
  ATLAS_SITL_VIDEO_TOPIC="/world/${PX4_WORLD}/model/${ATLAS_GZ_FOLLOW_TARGET}/link/camera_link/sensor/camera/image"
fi
SITL_VIDEO_RTSP_URL="rtsp://${ATLAS_SITL_VIDEO_ADDRESS}:${ATLAS_SITL_VIDEO_PORT}${ATLAS_SITL_VIDEO_PATH}"
if [[ "$SKIP_VIDEO" -eq 0 && -z "$ATLAS_VIDEO_RTSP_URL" ]]; then
  ATLAS_VIDEO_RTSP_URL="$SITL_VIDEO_RTSP_URL"
fi
if [[ "$SKIP_VIDEO" -eq 0 && -z "$ATLAS_VIDEO_SOURCE_ID" ]]; then
  ATLAS_VIDEO_SOURCE_ID="sitl-gimbal"
fi
assert_prerequisites

NATIVE_GRPC_PORT="$(tcp_port_from_addr "$ATLAS_GROUND_STATION_LISTEN_ADDR")"
NATIVE_GRPC_HOST="$(tcp_host_from_addr "$ATLAS_GROUND_STATION_LISTEN_ADDR")"
ATLAS_GROUND_STATION_ADDR="${ATLAS_GROUND_STATION_ADDR:-${NATIVE_GRPC_HOST}:${NATIVE_GRPC_PORT}}"

log "using PX4_DIR=${PX4_DIR}"
log "using PX4 model=${PX4_MODEL}"
log "using Gazebo world=${PX4_WORLD}"
log "using Gazebo transport IP=${ATLAS_GZ_IP}"
log "using MAVLink router=${SITL_MAVLINK_ROUTER}"
if [[ "$SITL_MAVLINK_ROUTER" == "mavproxy" ]]; then
  log "using MAVProxy master=${MAVPROXY_MASTER}"
  log "using MAVProxy MAVSDK output=${MAVPROXY_MAVSDK_OUT}"
  log "using MAVProxy QGC output=${MAVPROXY_QGC_OUT}"
fi
log "using PX4 system address=${ATLAS_PX4_SYSTEM_ADDRESS}"
log "using SITL state=${SITL_STATE_DIR}"
if [[ "$SKIP_VIDEO" -eq 0 ]]; then
  log "using Gazebo camera topic=${ATLAS_SITL_VIDEO_TOPIC}"
  log "publishing SITL video=${SITL_VIDEO_RTSP_URL}"
  log "using Atlas video source=${ATLAS_VIDEO_RTSP_URL} (${ATLAS_VIDEO_SOURCE_ID})"
fi
log "using logs=${LOG_DIR}"

if [[ "$DRY_RUN" -eq 0 ]]; then
  mkdir -p "$(dirname "$ATLAS_SQLITE_PATH")" "$ATLAS_AGENT_STATE_DIR"
fi

if [[ "$SKIP_PX4" -eq 0 ]]; then
  start_process \
    "px4-sitl" \
    "$PX4_DIR" \
    "source \"${PX4_VENV}\" && env GZ_IP=\"${ATLAS_GZ_IP}\" PX4_GZ_WORLD=\"${PX4_WORLD}\" make \"${PX4_TARGET}\" \"${PX4_MODEL}\""
  if [[ "$DRY_RUN" -eq 0 ]]; then
    log "giving PX4 ${PX4_BOOT_WAIT_SECONDS}s to publish MAVLink"
    sleep "$PX4_BOOT_WAIT_SECONDS"
  fi
  configure_gazebo_follow
fi

if [[ "$SKIP_VIDEO" -eq 0 ]]; then
  build_sitl_video_bridge
  start_process "sitl-video" "$ROOT_DIR" "$(sitl_video_command)"
  wait_for_tcp \
    "SITL RTSP video" \
    "$ATLAS_SITL_VIDEO_ADDRESS" \
    "$ATLAS_SITL_VIDEO_PORT" \
    "$STARTUP_TIMEOUT_SECONDS"
  verify_rtsp_video
fi

if [[ "$SITL_MAVLINK_ROUTER" == "mavproxy" ]]; then
  start_process "mavproxy" "$ROOT_DIR" "$(mavproxy_command)"
fi

if [[ "$SKIP_MAVSDK" -eq 0 ]]; then
  start_process \
    "mavsdk-server" \
    "$ROOT_DIR" \
    "\"${MAVSDK_SERVER_BIN}\" -p \"${MAVSDK_PORT}\" \"${ATLAS_PX4_SYSTEM_ADDRESS}\""
  wait_for_tcp "mavsdk_server" "127.0.0.1" "$MAVSDK_PORT" "$STARTUP_TIMEOUT_SECONDS"
fi

if [[ "$SKIP_NATIVE" -eq 0 ]]; then
  start_process "atlas-native" "${ROOT_DIR}/atlas" "$(native_command)"
  wait_for_tcp "Atlas Native UI" "localhost" "$ATLAS_NATIVE_UI_PORT" "$STARTUP_TIMEOUT_SECONDS"
  wait_for_tcp "Atlas Native gRPC" "$NATIVE_GRPC_HOST" "$NATIVE_GRPC_PORT" "$STARTUP_TIMEOUT_SECONDS"
fi

if [[ "$SKIP_AGENT" -eq 0 ]]; then
  start_process \
    "atlas-agent" \
    "${ROOT_DIR}/atlas-agent" \
    "env ATLAS_GROUND_STATION_ADDR=\"${ATLAS_GROUND_STATION_ADDR}\" ATLAS_AGENT_STATE_DIR=\"${ATLAS_AGENT_STATE_DIR}\" ATLAS_MAVSDK_GRPC_ADDR=\"${ATLAS_MAVSDK_GRPC_ADDR}\" ATLAS_DRONE_NAME=\"${ATLAS_DRONE_NAME}\" ATLAS_AGENT_VERSION=\"${ATLAS_AGENT_VERSION}\" ATLAS_VEHICLE_TYPE=\"${ATLAS_VEHICLE_TYPE}\" ATLAS_FLIGHT_CONTROLLER_TRANSPORT=\"udp\" ATLAS_FLIGHT_CONTROLLER_ENDPOINT=\"${ATLAS_PX4_SYSTEM_ADDRESS}\" ATLAS_FLIGHT_CONTROLLER_BAUD_RATE=\"0\" go run ./cmd/atlas-agent"
  wait_for_log \
    "Atlas Agent registration with Native" \
    "${LOG_DIR}/atlas-agent.log" \
    "registered with Atlas Native" \
    "$STARTUP_TIMEOUT_SECONDS"
fi

monitor_processes
