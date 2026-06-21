#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

PX4_DIR="${ATLAS_PX4_DIR:-"${ROOT_DIR}/../PX4-Autopilot"}"
PX4_VENV="${ATLAS_PX4_VENV:-"${PX4_DIR}/.venv/bin/activate"}"
PX4_TARGET="${ATLAS_PX4_TARGET:-px4_sitl}"
PX4_MODEL="${ATLAS_PX4_MODEL:-gz_x500}"
PX4_BOOT_WAIT_SECONDS="${ATLAS_PX4_BOOT_WAIT_SECONDS:-20}"

MAVSDK_SERVER_BIN="${ATLAS_MAVSDK_SERVER_BIN:-mavsdk_server}"
MAVSDK_PORT="${ATLAS_MAVSDK_PORT:-50051}"
ATLAS_MAVSDK_GRPC_ADDR="${ATLAS_MAVSDK_GRPC_ADDR:-127.0.0.1:${MAVSDK_PORT}}"
ATLAS_PX4_SYSTEM_ADDRESS="${ATLAS_PX4_SYSTEM_ADDRESS:-udpin://0.0.0.0:14540}"

ATLAS_BACKEND_ADDR="${ATLAS_BACKEND_ADDR:-:8080}"
ATLAS_BACKEND_URL="${ATLAS_BACKEND_URL:-http://127.0.0.1:8080}"
ATLAS_AGENT_GRPC_ADDR="${ATLAS_AGENT_GRPC_ADDR:-127.0.0.1:9090}"
ATLAS_STORE="${ATLAS_STORE:-postgres}"
ATLAS_DATABASE_URL="${ATLAS_DATABASE_URL:-postgres://atlas:atlas@127.0.0.1:5432/atlas?sslmode=disable}"
ATLAS_SQLITE_PATH="${ATLAS_SQLITE_PATH:-"${ROOT_DIR}/.atlas-run/atlas.db"}"
ATLAS_DB_HOST="${ATLAS_DB_HOST:-127.0.0.1}"
ATLAS_DB_PORT="${ATLAS_DB_PORT:-5432}"
ATLAS_DB_NAME="${ATLAS_DB_NAME:-atlas}"
ATLAS_DB_USER="${ATLAS_DB_USER:-atlas}"
ATLAS_DB_PASSWORD="${ATLAS_DB_PASSWORD:-atlas}"
ATLAS_DB_COMPOSE_SERVICE="${ATLAS_DB_COMPOSE_SERVICE:-postgres}"

ATLAS_AGENT_ID="${ATLAS_AGENT_ID:-agent-001}"
ATLAS_DRONE_ID="${ATLAS_DRONE_ID:-drone-001}"
ATLAS_DRONE_NAME="${ATLAS_DRONE_NAME:-Training Quad 1}"
ATLAS_AGENT_VERSION="${ATLAS_AGENT_VERSION:-0.1.0-dev}"

ATLAS_UI_HOST="${ATLAS_UI_HOST:-127.0.0.1}"
ATLAS_UI_PORT="${ATLAS_UI_PORT:-5173}"
ATLAS_UI_NVM_DIR="${ATLAS_UI_NVM_DIR:-"${HOME}/.nvm"}"
ATLAS_UI_NODE_BIN_DIR="${ATLAS_UI_NODE_BIN_DIR:-}"

LOG_DIR="${ATLAS_RUN_LOG_DIR:-"${ROOT_DIR}/.atlas-run/logs/$(date +%Y%m%d-%H%M%S)"}"

SKIP_PX4=0
SKIP_MAVSDK=0
SKIP_BACKEND=0
SKIP_AGENT=0
SKIP_UI=0
DRY_RUN=0

PIDS=()
NAMES=()
POSTGRES_STARTED=0

usage() {
  cat <<EOF
Usage: scripts/start-sitl.sh [options]

Starts the Atlas local SITL stack:
  datastore -> PX4 SITL -> mavsdk_server -> atlas-backend -> atlas-agent -> atlas-ui

Options:
  --px4-dir PATH       PX4-Autopilot checkout. Default: ${PX4_DIR}
  --px4-model MODEL    PX4 Gazebo model. Default: ${PX4_MODEL}
  --skip-px4           Do not start PX4 SITL.
  --skip-mavsdk        Do not start mavsdk_server.
  --skip-backend       Do not start atlas-backend.
  --skip-agent         Do not start atlas-agent.
  --skip-ui            Do not start atlas-ui.
  --dry-run            Print commands without starting processes.
  -h, --help           Show this help.

Useful environment overrides:
  ATLAS_PX4_DIR
  ATLAS_PX4_VENV
  ATLAS_PX4_MODEL
  ATLAS_MAVSDK_SERVER_BIN
  ATLAS_MAVSDK_GRPC_ADDR
  ATLAS_PX4_SYSTEM_ADDRESS
  ATLAS_BACKEND_ADDR
  ATLAS_BACKEND_URL
  ATLAS_AGENT_GRPC_ADDR
  ATLAS_STORE
  ATLAS_DATABASE_URL
  ATLAS_SQLITE_PATH
  ATLAS_DB_HOST
  ATLAS_DB_PORT
  ATLAS_DB_NAME
  ATLAS_DB_USER
  ATLAS_DB_PASSWORD
  ATLAS_UI_PORT
  ATLAS_UI_NVM_DIR
  ATLAS_UI_NODE_BIN_DIR
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
    --skip-backend)
      SKIP_BACKEND=1
      shift
      ;;
    --skip-agent)
      SKIP_AGENT=1
      shift
      ;;
    --skip-ui)
      SKIP_UI=1
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

tcp_port_from_addr() {
  local addr="$1"

  printf '%s\n' "${addr##*:}"
}

tcp_port_owner() {
  local port="$1"
  local listeners

  listeners="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)"
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

  stop_postgres
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

wait_for_http() {
  local label="$1"
  local url="$2"
  local timeout_seconds="$3"
  local elapsed=0

  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  log "waiting for ${label} at ${url}"
  until curl -fsS "$url" >/dev/null 2>&1; do
    if [[ "$elapsed" -ge "$timeout_seconds" ]]; then
      fail "timed out waiting for ${label} at ${url}"
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
}

compose_command() {
  if docker compose version >/dev/null 2>&1; then
    printf 'docker compose'
    return
  fi

  if command -v docker-compose >/dev/null 2>&1; then
    printf 'docker-compose'
    return
  fi

  fail "Docker Compose is required for ATLAS_STORE=postgres"
}

start_postgres() {
  local compose

  if [[ "$ATLAS_STORE" != "postgres" ]]; then
    log "using ATLAS_STORE=${ATLAS_STORE}; skipping postgres startup"
    return
  fi

  require_command docker
  compose="$(compose_command)"

  log "starting postgres via docker compose"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '  (cd %q && %s up -d %q)\n' "$ROOT_DIR" "$compose" "$ATLAS_DB_COMPOSE_SERVICE"
    printf '  (cd %q && %s exec -T -e %q %q psql -U %q -d %q -v ON_ERROR_STOP=1 < atlas-backend/migrations/001_initial_atlas_store.sql)\n' \
      "$ROOT_DIR" \
      "$compose" \
      "PGPASSWORD=${ATLAS_DB_PASSWORD}" \
      "$ATLAS_DB_COMPOSE_SERVICE" \
      "$ATLAS_DB_USER" \
      "$ATLAS_DB_NAME"
    return
  fi

  (cd "$ROOT_DIR" && $compose up -d "$ATLAS_DB_COMPOSE_SERVICE")
  POSTGRES_STARTED=1
  wait_for_tcp "postgres" "$ATLAS_DB_HOST" "$ATLAS_DB_PORT" 60
  run_migrations
}

stop_postgres() {
  if [[ "$POSTGRES_STARTED" -ne 1 ]]; then
    return
  fi

  log "bringing down postgres via docker compose"
  if docker compose version >/dev/null 2>&1; then
    (cd "$ROOT_DIR" && docker compose down) || log "failed to bring down postgres"
    return
  fi

  if command -v docker-compose >/dev/null 2>&1; then
    (cd "$ROOT_DIR" && docker-compose down) || log "failed to bring down postgres"
    return
  fi

  log "Docker Compose unavailable; postgres may still be running"
}

run_migrations() {
  local compose
  local migration

  compose="$(compose_command)"
  log "applying atlas backend migrations"
  for migration in "${ROOT_DIR}"/atlas-backend/migrations/*.sql; do
    if [[ ! -f "$migration" ]]; then
      fail "no backend migrations found"
    fi
    log "  migration: ${migration##*/}"
    (cd "$ROOT_DIR" && $compose exec -T -e "PGPASSWORD=${ATLAS_DB_PASSWORD}" "$ATLAS_DB_COMPOSE_SERVICE" psql -U "$ATLAS_DB_USER" -d "$ATLAS_DB_NAME" -v ON_ERROR_STOP=1 < "$migration" >/dev/null)
  done
}

ui_runtime_prefix() {
  if [[ -n "$ATLAS_UI_NODE_BIN_DIR" ]]; then
    printf 'export PATH="%s:$PATH" && ' "$ATLAS_UI_NODE_BIN_DIR"
    return
  fi

  if [[ -s "${ATLAS_UI_NVM_DIR}/nvm.sh" ]]; then
    printf 'export NVM_DIR="%s" && source "%s/nvm.sh" && nvm use --silent && ' \
      "$ATLAS_UI_NVM_DIR" \
      "$ATLAS_UI_NVM_DIR"
  fi
}

ui_command() {
  printf '%snpm run dev -- --host "%s" --port "%s" --strictPort' \
    "$(ui_runtime_prefix)" \
    "$ATLAS_UI_HOST" \
    "$ATLAS_UI_PORT"
}

validate_ui_runtime() {
  local version_check
  local node_version

  version_check="$(ui_runtime_prefix)"'node -e '\''
const [major, minor] = process.versions.node.split(".").map(Number);
const ok = major > 22 || (major === 22 && minor >= 12) || (major === 20 && minor >= 19);
if (!ok) {
  console.error(`Node ${process.versions.node} is too old for Atlas UI. Use Node 22.13.1 from atlas-ui/.nvmrc.`);
  process.exit(1);
}
console.log(process.versions.node);
'\'''

  if ! node_version="$(cd "${ROOT_DIR}/atlas-ui" && bash -lc "$version_check" 2>&1)"; then
    fail "Atlas UI requires Node 20.19+ or 22.12+. ${node_version}"
  fi

  log "using Atlas UI Node ${node_version}"
}

assert_prerequisites() {
  require_command bash
  require_command go
  require_command nc
  require_command curl
  require_command lsof

  if [[ "$ATLAS_STORE" != "postgres" && "$ATLAS_STORE" != "sqlite" ]]; then
    fail "ATLAS_STORE must be postgres or sqlite"
  fi

  if [[ "$SKIP_BACKEND" -eq 0 && "$ATLAS_STORE" == "postgres" ]]; then
    require_command docker
    compose_command >/dev/null
  fi

  if [[ "$SKIP_UI" -eq 0 ]]; then
    require_path "${ROOT_DIR}/atlas-ui/node_modules" "atlas-ui dependencies"
    validate_ui_runtime
    require_tcp_port_free "atlas-ui" "$ATLAS_UI_PORT"
  fi

  if [[ "$SKIP_MAVSDK" -eq 0 ]]; then
    require_command "$MAVSDK_SERVER_BIN"
    require_tcp_port_free "mavsdk_server" "$MAVSDK_PORT"
  fi

  if [[ "$SKIP_BACKEND" -eq 0 ]]; then
    require_tcp_port_free "atlas-backend HTTP" "$(tcp_port_from_addr "$ATLAS_BACKEND_ADDR")"
    require_tcp_port_free "atlas-backend agent gRPC" "$(tcp_port_from_addr "$ATLAS_AGENT_GRPC_ADDR")"
  fi

  if [[ "$SKIP_PX4" -eq 0 ]]; then
    require_path "$PX4_DIR" "PX4 checkout"
    require_path "$PX4_VENV" "PX4 virtualenv activation script"
  fi
}

monitor_processes() {
  local index
  local pid
  local name

  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  log "stack is running"
  log "  backend: ${ATLAS_BACKEND_URL}"
  log "  ui:      http://${ATLAS_UI_HOST}:${ATLAS_UI_PORT}"
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

assert_prerequisites

log "using PX4_DIR=${PX4_DIR}"
log "using logs=${LOG_DIR}"

if [[ "$SKIP_BACKEND" -eq 0 ]]; then
  start_postgres
fi

if [[ "$SKIP_PX4" -eq 0 ]]; then
  start_process \
    "px4-sitl" \
    "$PX4_DIR" \
    "source \"${PX4_VENV}\" && make \"${PX4_TARGET}\" \"${PX4_MODEL}\""

  if [[ "$DRY_RUN" -eq 0 ]]; then
    log "giving PX4 ${PX4_BOOT_WAIT_SECONDS}s to publish MAVLink"
    sleep "$PX4_BOOT_WAIT_SECONDS"
  fi
fi

if [[ "$SKIP_MAVSDK" -eq 0 ]]; then
  start_process \
    "mavsdk-server" \
    "$ROOT_DIR" \
    "\"${MAVSDK_SERVER_BIN}\" -p \"${MAVSDK_PORT}\" \"${ATLAS_PX4_SYSTEM_ADDRESS}\""
  wait_for_tcp "mavsdk_server" "127.0.0.1" "$MAVSDK_PORT" 30
fi

if [[ "$SKIP_BACKEND" -eq 0 ]]; then
  start_process \
    "atlas-backend" \
    "${ROOT_DIR}/atlas-backend" \
    "env ATLAS_BACKEND_ADDR=\"${ATLAS_BACKEND_ADDR}\" ATLAS_AGENT_GRPC_ADDR=\"${ATLAS_AGENT_GRPC_ADDR}\" ATLAS_STORE=\"${ATLAS_STORE}\" ATLAS_DATABASE_URL=\"${ATLAS_DATABASE_URL}\" ATLAS_SQLITE_PATH=\"${ATLAS_SQLITE_PATH}\" go run ./cmd/atlas-backend"
  wait_for_http "atlas-backend" "${ATLAS_BACKEND_URL}/healthz" 30
fi

if [[ "$SKIP_AGENT" -eq 0 ]]; then
  start_process \
    "atlas-agent" \
    "${ROOT_DIR}/atlas-agent" \
    "env ATLAS_BACKEND_URL=\"${ATLAS_BACKEND_URL}\" ATLAS_AGENT_ID=\"${ATLAS_AGENT_ID}\" ATLAS_DRONE_ID=\"${ATLAS_DRONE_ID}\" ATLAS_DRONE_NAME=\"${ATLAS_DRONE_NAME}\" ATLAS_AGENT_VERSION=\"${ATLAS_AGENT_VERSION}\" ATLAS_AGENT_GRPC_ADDR=\"${ATLAS_AGENT_GRPC_ADDR}\" ATLAS_MAVSDK_GRPC_ADDR=\"${ATLAS_MAVSDK_GRPC_ADDR}\" ATLAS_PX4_SYSTEM_ADDRESS=\"${ATLAS_PX4_SYSTEM_ADDRESS}\" go run ./cmd/atlas-agent"
fi

if [[ "$SKIP_UI" -eq 0 ]]; then
  start_process \
    "atlas-ui" \
    "${ROOT_DIR}/atlas-ui" \
    "$(ui_command)"
  wait_for_tcp "atlas-ui" "$ATLAS_UI_HOST" "$ATLAS_UI_PORT" 30
fi

monitor_processes
