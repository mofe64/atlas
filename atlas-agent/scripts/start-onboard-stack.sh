#!/usr/bin/env bash
set -euo pipefail

ACTION="start"
DRY_RUN=0
SERVICES=(
  atlas-mediamtx.service
  atlas-mavlink-router.service
  atlas-mavsdk.service
  atlas-agent.service
  atlas-video-agent.service
)

usage() {
  cat <<EOF
Usage: scripts/start-onboard-stack.sh [options]

Starts or stops the Atlas onboard systemd stack.

Options:
  --stop       Stop services in reverse dependency order.
  --restart    Restart services.
  --dry-run    Print systemctl commands without running them.
  -h, --help   Show this help.
EOF
}

run() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ %s\n' "$*"
    return
  fi
  "$@"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stop)
      ACTION="stop"
      shift
      ;;
    --restart)
      ACTION="restart"
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
      printf 'unknown option: %s\n' "$1" >&2
      exit 1
      ;;
  esac
done

if [[ "$ACTION" == "stop" ]]; then
  for ((idx=${#SERVICES[@]}-1; idx>=0; idx--)); do
    run sudo systemctl stop "${SERVICES[$idx]}"
  done
  exit 0
fi

run sudo systemctl daemon-reload
run sudo systemctl "$ACTION" "${SERVICES[@]}"
run sudo systemctl --no-pager --full status "${SERVICES[@]}"
