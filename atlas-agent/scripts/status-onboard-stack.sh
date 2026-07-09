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

printf '\n[atlas-onboard-status] rtsp port\n'
ss -lntp | grep ':8554' || true

printf '\n[atlas-onboard-status] hailo\n'
if command -v hailortcli >/dev/null 2>&1; then
  hailortcli fw-control identify || true
else
  lspci | grep -i hailo || true
fi
