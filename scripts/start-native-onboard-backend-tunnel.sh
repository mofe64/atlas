#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
COMPOSE_FILE="${ATLAS_ONBOARD_DB_COMPOSE_FILE:-${ROOT_DIR}/docker-compose.onboard-db.yml}"
RUN_DIR="${ATLAS_NATIVE_RUN_DIR:-${ROOT_DIR}/.atlas-run}"
LOG_DIR="${ATLAS_NATIVE_LOG_DIR:-${RUN_DIR}/logs}"
WAIT_SECONDS="${ATLAS_TUNNEL_WAIT_SECONDS:-60}"

ATLAS_DB_NAME="${ATLAS_DB_NAME:-atlas}"
ATLAS_DB_USER="${ATLAS_DB_USER:-atlas}"
ATLAS_DB_PASSWORD="${ATLAS_DB_PASSWORD:-atlas}"
ATLAS_POSTGRES_PORT="${ATLAS_POSTGRES_PORT:-5432}"
ATLAS_BACKEND_ADDR="${ATLAS_BACKEND_ADDR:-:8080}"
ATLAS_VEHICLE_AGENT_GRPC_ADDR="${ATLAS_VEHICLE_AGENT_GRPC_ADDR:-:9090}"
ATLAS_DATABASE_URL="${ATLAS_DATABASE_URL:-postgres://${ATLAS_DB_USER}:${ATLAS_DB_PASSWORD}@127.0.0.1:${ATLAS_POSTGRES_PORT}/${ATLAS_DB_NAME}?sslmode=disable}"
ATLAS_LOCAL_INPUTS_ENABLED="${ATLAS_LOCAL_INPUTS_ENABLED:-true}"
ATLAS_LOCAL_VIDEO_RTSP_URL="${ATLAS_LOCAL_VIDEO_RTSP_URL:-rtsp://192.168.144.168:8554/atlas}"
ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT="${ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT:-udp}"
ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE="${ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE:-256}"

backend_pid=""
ngrok_pid=""
public_addr=""

usage() {
  cat <<EOF
Usage: scripts/start-native-onboard-backend-tunnel.sh

Starts the non-SITL Atlas ground stack with:
  - Postgres and migrations in Docker Compose
  - atlas-backend as a native host Go process
  - ngrok TCP as a native host process

ngrok auth:
  Set NGROK_AUTHTOKEN, or authenticate the local ngrok CLI with:
    ngrok config add-authtoken <token>

Optional:
  NGROK_TCP_URL                    reserved ngrok TCP URL, for example tcp://1.tcp.ngrok.io:12345.
  ATLAS_BACKEND_ADDR               native backend HTTP listen addr. Default: ${ATLAS_BACKEND_ADDR}
  ATLAS_VEHICLE_AGENT_GRPC_ADDR    native backend gRPC listen addr. Default: ${ATLAS_VEHICLE_AGENT_GRPC_ADDR}
  ATLAS_DATABASE_URL               native backend Postgres URL. Default: ${ATLAS_DATABASE_URL}
  ATLAS_LOCAL_VIDEO_RTSP_URL       local RTSP source for backend WebRTC relay. Default: ${ATLAS_LOCAL_VIDEO_RTSP_URL}
  ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT RTSP transport from backend to Pi: udp or tcp. Default: ${ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT}
  ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE
                                    bounded RTP packet queue before WebRTC. Default: ${ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE}
  ATLAS_ONBOARD_DB_COMPOSE_FILE    DB-only Compose file. Default: ${COMPOSE_FILE}
  ATLAS_TUNNEL_WAIT_SECONDS        seconds to wait for ngrok to publish TCP URL. Default: ${WAIT_SECONDS}
EOF
}

log() {
  printf '[atlas-native-onboard] %s\n' "$*"
}

fail() {
  printf '[atlas-native-onboard] error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  local name="$1"
  if ! command -v "$name" >/dev/null 2>&1; then
    fail "${name} is required"
  fi
}

compose() {
  if docker compose version >/dev/null 2>&1; then
    docker compose "$@"
    return
  fi

  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose "$@"
    return
  fi

  fail "Docker Compose is required"
}

tcp_port_from_addr() {
  local addr="$1"
  printf '%s\n' "${addr##*:}"
}

port_is_listening() {
  local port="$1"
  if ! command -v nc >/dev/null 2>&1; then
    return 1
  fi
  nc -z 127.0.0.1 "$port" >/dev/null 2>&1
}

require_port_free() {
  local label="$1"
  local port="$2"
  if port_is_listening "$port"; then
    fail "${label} port ${port} is already in use; stop the existing backend/tunnel first"
  fi
}

wait_for_postgres() {
  local deadline=$((SECONDS + WAIT_SECONDS))
  while [[ "$SECONDS" -lt "$deadline" ]]; do
    if (cd "$ROOT_DIR" && compose -f "$COMPOSE_FILE" exec -T postgres pg_isready -U "$ATLAS_DB_USER" -d "$ATLAS_DB_NAME" >/dev/null 2>&1); then
      return
    fi
    sleep 1
  done
  fail "Postgres did not become ready within ${WAIT_SECONDS}s"
}

wait_for_http() {
  local label="$1"
  local url="$2"
  local deadline=$((SECONDS + WAIT_SECONDS))
  while [[ "$SECONDS" -lt "$deadline" ]]; do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return
    fi
    if [[ -n "$backend_pid" ]] && ! kill -0 "$backend_pid" >/dev/null 2>&1; then
      tail_log "$BACKEND_LOG"
      fail "${label} exited before becoming ready"
    fi
    sleep 1
  done
  tail_log "$BACKEND_LOG"
  fail "${label} did not become ready within ${WAIT_SECONDS}s"
}

extract_tcp_addr() {
  sed -nE "s/.*tcp:\/\/([^[:space:]\"']+).*/\1/p"
}

read_tunnel_addr_from_log() {
  if [[ ! -f "$NGROK_LOG" ]]; then
    return
  fi
  extract_tcp_addr <"$NGROK_LOG" | tail -n 1
}

read_tunnel_addr_from_api() {
  curl -fsS http://127.0.0.1:4040/api/tunnels 2>/dev/null | extract_tcp_addr | tail -n 1 || true
}

wait_for_tunnel() {
  local deadline=$((SECONDS + WAIT_SECONDS))
  while [[ "$SECONDS" -lt "$deadline" ]]; do
    public_addr="$(read_tunnel_addr_from_log)"
    if [[ -z "$public_addr" ]]; then
      public_addr="$(read_tunnel_addr_from_api)"
    fi
    if [[ -n "$public_addr" ]]; then
      return
    fi
    if [[ -n "$ngrok_pid" ]] && ! kill -0 "$ngrok_pid" >/dev/null 2>&1; then
      tail_log "$NGROK_LOG"
      fail "ngrok exited before publishing a TCP endpoint"
    fi
    sleep 1
  done
  tail_log "$NGROK_LOG"
  fail "ngrok did not publish a TCP endpoint within ${WAIT_SECONDS}s"
}

tail_log() {
  local log_file="$1"
  if [[ -f "$log_file" ]]; then
    printf '\n--- %s ---\n' "$log_file" >&2
    tail -n 80 "$log_file" >&2 || true
    printf -- '--- end log ---\n\n' >&2
  fi
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM

  if [[ -n "$ngrok_pid" ]] && kill -0 "$ngrok_pid" >/dev/null 2>&1; then
    log "stopping ngrok"
    kill "$ngrok_pid" >/dev/null 2>&1 || true
  fi

  if [[ -n "$backend_pid" ]] && kill -0 "$backend_pid" >/dev/null 2>&1; then
    log "stopping atlas-backend"
    kill "$backend_pid" >/dev/null 2>&1 || true
  fi

  if [[ -n "$ngrok_pid" ]]; then
    wait "$ngrok_pid" 2>/dev/null || true
  fi
  if [[ -n "$backend_pid" ]]; then
    wait "$backend_pid" 2>/dev/null || true
  fi

  exit "$status"
}

monitor_children() {
  while true; do
    sleep 1

    if ! kill -0 "$backend_pid" >/dev/null 2>&1; then
      local backend_status=0
      wait "$backend_pid" || backend_status=$?
      tail_log "$BACKEND_LOG"
      fail "atlas-backend exited with status ${backend_status}"
    fi

    if ! kill -0 "$ngrok_pid" >/dev/null 2>&1; then
      local ngrok_status=0
      wait "$ngrok_pid" || ngrok_status=$?
      tail_log "$NGROK_LOG"
      fail "ngrok exited with status ${ngrok_status}"
    fi
  done
}

case "${1:-}" in
  -h|--help)
    usage
    exit 0
    ;;
  "")
    ;;
  *)
    fail "unknown option: $1"
    ;;
esac

require_command docker
require_command go
require_command curl
require_command ngrok

http_port="$(tcp_port_from_addr "$ATLAS_BACKEND_ADDR")"
grpc_port="$(tcp_port_from_addr "$ATLAS_VEHICLE_AGENT_GRPC_ADDR")"

mkdir -p "$LOG_DIR"
BACKEND_LOG="${LOG_DIR}/atlas-backend-native.log"
NGROK_LOG="${LOG_DIR}/atlas-ngrok-native.log"

export ATLAS_DB_NAME
export ATLAS_DB_USER
export ATLAS_DB_PASSWORD
export ATLAS_POSTGRES_PORT

log "starting Docker Postgres"
(cd "$ROOT_DIR" && compose -f "$COMPOSE_FILE" up -d --remove-orphans postgres)

require_port_free "atlas-backend HTTP" "$http_port"
require_port_free "atlas-backend vehicle-agent gRPC" "$grpc_port"

log "waiting for Docker Postgres"
wait_for_postgres

log "applying migrations"
(cd "$ROOT_DIR" && compose -f "$COMPOSE_FILE" run --rm -T migrate)

trap cleanup EXIT INT TERM

log "starting native atlas-backend"
(
  cd "${ROOT_DIR}/atlas-backend"
  export ATLAS_BACKEND_ADDR
  export ATLAS_VEHICLE_AGENT_GRPC_ADDR
  export ATLAS_DATABASE_URL
  export ATLAS_LOCAL_INPUTS_ENABLED
  export ATLAS_LOCAL_VIDEO_RTSP_URL
  export ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT
  export ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE
  exec go run ./cmd/atlas-backend
) >"$BACKEND_LOG" 2>&1 &
backend_pid=$!

wait_for_http "atlas-backend" "http://127.0.0.1:${http_port}/healthz"

log "starting native ngrok TCP tunnel"
ngrok_args=(tcp --log=stdout --log-format=logfmt)
if [[ -n "${NGROK_AUTHTOKEN:-}" ]]; then
  ngrok_args+=(--authtoken "$NGROK_AUTHTOKEN")
fi
if [[ -n "${NGROK_TCP_URL:-}" ]]; then
  ngrok_args+=(--url "${NGROK_TCP_URL#tcp://}")
fi
ngrok_args+=("$grpc_port")

(
  cd "$ROOT_DIR"
  exec ngrok "${ngrok_args[@]}"
) >"$NGROK_LOG" 2>&1 &
ngrok_pid=$!

log "waiting for ngrok TCP endpoint"
wait_for_tunnel

log "backend HTTP: http://127.0.0.1:${http_port}"
log "backend local gRPC: 127.0.0.1:${grpc_port}"
log "backend gRPC tunnel: ${public_addr}"
log "backend log: ${BACKEND_LOG}"
log "ngrok log: ${NGROK_LOG}"

cat <<EOF

Pass this to the onboard Pi installer:

  atlas-agent/scripts/install-onboard-pi.sh --ground-grpc ${public_addr}

For an already-installed Pi, set this in the onboard env file and restart atlas-agent:

  ATLAS_VEHICLE_AGENT_GRPC_ADDR="${public_addr}"

Press Ctrl-C to stop native atlas-backend and ngrok. Docker Postgres is left running.

EOF

monitor_children
