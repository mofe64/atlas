#!/usr/bin/env bash
set -euo pipefail

SERVICES=(
  atlas-mediamtx.service
  atlas-video-agent.service
  atlas-mavlink-router.service
  atlas-mavsdk.service
  atlas-agent.service
)

printf '[atlas-onboard-status] services\n'
sudo systemctl --no-pager --full status "${SERVICES[@]}" || true

printf '\n[atlas-onboard-status] recent logs\n'
for service in "${SERVICES[@]}"; do
  printf '\n--- %s ---\n' "$service"
  sudo journalctl -u "$service" -n 40 --no-pager || true
done

printf '\n[atlas-onboard-status] network\n'
ip -br addr || true
ip route || true

printf '\n[atlas-onboard-status] mavlink router config\n'
if [[ -f "${HOME}/.config/atlas-agent/mavlink-router/main.conf" ]]; then
  sed -n '1,120p' "${HOME}/.config/atlas-agent/mavlink-router/main.conf" || true
else
  printf 'missing: %s\n' "${HOME}/.config/atlas-agent/mavlink-router/main.conf"
fi

printf '\n[atlas-onboard-status] serial devices\n'
for dev in /dev/serial/by-id/* /dev/serial/by-path/* /dev/ttyUSB* /dev/ttyACM* /dev/serial0 /dev/serial1 /dev/ttyAMA0 /dev/ttyAMA1 /dev/ttyS0; do
  if [[ -e "$dev" ]]; then
    printf '%s -> %s\n' "$dev" "$(readlink -f "$dev")"
  fi
done

printf '\n[atlas-onboard-status] mavsdk_server\n'
if [[ -f "${HOME}/.config/atlas-agent/onboard.env" ]]; then
  # shellcheck disable=SC1090
  source "${HOME}/.config/atlas-agent/onboard.env"
fi
if [[ -n "${ATLAS_MAVSDK_SERVER_BIN:-}" ]]; then
  ls -l "$ATLAS_MAVSDK_SERVER_BIN" || true
fi
command -v mavsdk_server || true

printf '\n[atlas-onboard-status] rtsp port\n'
ss -lntp | grep ':8554' || true

printf '\n[atlas-onboard-status] hailo\n'
if command -v hailortcli >/dev/null 2>&1; then
  hailortcli fw-control identify || true
else
  lspci | grep -i hailo || true
fi
