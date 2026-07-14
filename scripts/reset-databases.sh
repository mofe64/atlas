#!/usr/bin/env bash
set -euo pipefail

# This script resets only the current Atlas Backend and Native databases.
# It deliberately leaves the operating-system credential vault untouched.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="${ROOT_DIR}/atlas-backend"
COMPOSE_FILE="${BACKEND_DIR}/docker-compose.yml"

usage() {
  cat <<'EOF'
Usage: scripts/reset-databases.sh [--yes]

Deletes and recreates the local Atlas development databases:
  - Docker Compose PostgreSQL volume for atlas-backend
  - Native app SQLite database, WAL, and shared-memory files

Options:
  --yes       Skip the interactive confirmation.
  -h, --help  Show this help.

Environment:
  ATLAS_NATIVE_DATA_DIR  Override the native app data directory.
EOF
}

fail() {
  printf '[atlas-reset] error: %s\n' "$*" >&2
  exit 1
}

log() {
  printf '[atlas-reset] %s\n' "$*"
}

ASSUME_YES=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --yes)
      ASSUME_YES=1
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

command -v docker >/dev/null 2>&1 || fail "Docker is required"
docker compose version >/dev/null 2>&1 || fail "Docker Compose is required"
[[ -f "${COMPOSE_FILE}" ]] || fail "Compose file not found: ${COMPOSE_FILE}"

# Deleting SQLite files while Atlas is open can leave the running process using
# an unlinked database. Stop instead of trying to terminate the user's app.
if command -v pgrep >/dev/null 2>&1; then
  if pgrep -x atlas >/dev/null 2>&1 || pgrep -x Atlas >/dev/null 2>&1; then
    fail "Atlas is running; quit the native app before resetting its database"
  fi
fi

if [[ -n "${ATLAS_NATIVE_DATA_DIR:-}" ]]; then
  NATIVE_DATA_DIR="${ATLAS_NATIVE_DATA_DIR}"
else
  case "$(uname -s)" in
    Darwin)
      NATIVE_DATA_DIR="${HOME}/Library/Application Support/com.sunnyside.atlas"
      ;;
    Linux)
      NATIVE_DATA_DIR="${XDG_DATA_HOME:-${HOME}/.local/share}/com.sunnyside.atlas"
      ;;
    *)
      fail "unsupported platform; set ATLAS_NATIVE_DATA_DIR explicitly"
      ;;
  esac
fi

NATIVE_DB="${NATIVE_DATA_DIR}/atlas.db"

if [[ "${ASSUME_YES}" -ne 1 ]]; then
  cat <<EOF
This will permanently delete:
  - every local backend organization, user, and session;
  - the native SQLite cache at ${NATIVE_DB}.

The OS credential-vault entry is not deleted. Its old session will become invalid
because the backend sessions table is recreated.
EOF
  printf 'Type RESET to continue: '
  read -r confirmation
  [[ "${confirmation}" == "RESET" ]] || fail "reset cancelled"
fi

log "stopping the backend and deleting its PostgreSQL volume"
docker compose -f "${COMPOSE_FILE}" down --volumes --remove-orphans

log "deleting the native SQLite database and WAL sidecar files"
rm -f -- "${NATIVE_DB}" "${NATIVE_DB}-wal" "${NATIVE_DB}-shm"

log "rebuilding the backend and applying PostgreSQL migrations"
docker compose -f "${COMPOSE_FILE}" up --build --detach

log "reset complete"
log "the native SQLite migration will run automatically the next time Atlas starts"
