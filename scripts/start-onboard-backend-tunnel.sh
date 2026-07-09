#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.onboard-tunnel.yml"
NGROK_WEB_PORT="${NGROK_WEB_PORT:-4040}"
WAIT_SECONDS="${ATLAS_TUNNEL_WAIT_SECONDS:-60}"

usage() {
  cat <<EOF
Usage: scripts/start-onboard-backend-tunnel.sh

Starts the non-SITL Atlas backend stack with Postgres and an ngrok TCP tunnel.

Required:
  NGROK_AUTHTOKEN        ngrok auth token for creating the TCP tunnel.

Optional:
  NGROK_TCP_URL          reserved ngrok TCP URL, for example tcp://1.tcp.ngrok.io:12345.
  NGROK_WEB_PORT         local ngrok inspection/API port. Default: ${NGROK_WEB_PORT}
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
  sed -nE 's/.*"public_url"[[:space:]]*:[[:space:]]*"tcp:\/\/([^"]+)".*/\1/p'
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

api_url="http://127.0.0.1:${NGROK_WEB_PORT}/api/tunnels"
log "waiting for ngrok TCP endpoint from ${api_url}"

deadline=$((SECONDS + WAIT_SECONDS))
public_addr=""
while [[ "$SECONDS" -lt "$deadline" ]]; do
  body="$(curl -fsS "$api_url" 2>/dev/null | tr -d '\n' || true)"
  public_addr="$(printf '%s\n' "$body" | extract_tcp_addr | head -n 1)"
  if [[ -n "$public_addr" ]]; then
    break
  fi
  sleep 1
done

if [[ -z "$public_addr" ]]; then
  fail "ngrok did not publish a TCP endpoint within ${WAIT_SECONDS}s; check: ${compose} -f ${COMPOSE_FILE} logs atlas-ngrok-tcp"
fi

log "backend HTTP: http://127.0.0.1:${ATLAS_BACKEND_HTTP_PORT:-8080}"
log "backend gRPC tunnel: ${public_addr}"

cat <<EOF

Pass this to the onboard Pi installer:

  atlas-agent/scripts/install-onboard-pi.sh --ground-grpc ${public_addr}

For an already-installed Pi, set this in the onboard env file and restart atlas-agent:

  ATLAS_VEHICLE_AGENT_GRPC_ADDR="${public_addr}"

EOF
