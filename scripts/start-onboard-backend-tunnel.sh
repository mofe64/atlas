#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.onboard-tunnel.yml"
WAIT_SECONDS="${ATLAS_TUNNEL_WAIT_SECONDS:-60}"

usage() {
  cat <<EOF
Usage: scripts/start-onboard-backend-tunnel.sh

Starts the non-SITL Atlas backend stack with Postgres and an ngrok TCP tunnel.

Required:
  NGROK_AUTHTOKEN        ngrok auth token for creating the TCP tunnel.

Optional:
  NGROK_TCP_URL          reserved ngrok TCP URL, for example tcp://1.tcp.ngrok.io:12345.
  ATLAS_TUNNEL_WAIT_SECONDS
                         seconds to wait for ngrok to publish the TCP URL. Default: ${WAIT_SECONDS}
EOF
}

log() {
  printf '[atlas-onboard-tunnel] %s\n' "$*"
}

fail() {
  printf '[atlas-onboard-tunnel] error: %s\n' "$*" >&2
  exit 1
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

  fail "Docker Compose is required"
}

extract_tcp_addr() {
  sed -nE 's/.*tcp:\/\/([^[:space:]"'"'"']+).*/\1/p'
}

read_tunnel_addr_from_logs() {
  local logs
  logs="$(cd "$ROOT_DIR" && $compose -f "$COMPOSE_FILE" logs --no-log-prefix --tail=120 atlas-ngrok-tcp 2>/dev/null || true)"
  printf '%s\n' "$logs" | extract_tcp_addr | tail -n 1
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

if [[ -z "${NGROK_AUTHTOKEN:-}" ]]; then
  fail "set NGROK_AUTHTOKEN before starting the tunnel"
fi

compose="$(compose_command)"
log "starting Postgres, migrations, backend, and ngrok TCP tunnel"
(cd "$ROOT_DIR" && $compose -f "$COMPOSE_FILE" up -d postgres migrate atlas-backend atlas-ngrok-tcp)

log "waiting for ngrok TCP endpoint"

deadline=$((SECONDS + WAIT_SECONDS))
public_addr=""
while [[ "$SECONDS" -lt "$deadline" ]]; do
  public_addr="$(read_tunnel_addr_from_logs)"
  if [[ -n "$public_addr" ]]; then
    break
  fi
  sleep 1
done

if [[ -z "$public_addr" ]]; then
  (cd "$ROOT_DIR" && $compose -f "$COMPOSE_FILE" logs --tail=80 atlas-ngrok-tcp || true)
  fail "ngrok did not publish a TCP endpoint within ${WAIT_SECONDS}s; see atlas-ngrok-tcp logs above"
fi

log "backend HTTP: http://127.0.0.1:${ATLAS_BACKEND_HTTP_PORT:-8080}"
log "backend gRPC tunnel: ${public_addr}"

cat <<EOF

Pass this to the onboard Pi installer:

  atlas-agent/scripts/install-onboard-pi.sh --ground-grpc ${public_addr}

For an already-installed Pi, set this in the onboard env file and restart atlas-agent:

  ATLAS_VEHICLE_AGENT_GRPC_ADDR="${public_addr}"

EOF
