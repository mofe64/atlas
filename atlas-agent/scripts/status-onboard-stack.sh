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

printf '\n[atlas-onboard-status] perception model\n'
if [[ -n "${ATLAS_PERCEPTION_MODEL_PATH:-}" ]]; then
  ls -lh "$ATLAS_PERCEPTION_MODEL_PATH" || true
else
  printf 'ATLAS_PERCEPTION_MODEL_PATH is not set\n'
fi

printf '\n[atlas-onboard-status] rtsp port\n'
ss -lntp | grep ':8554' || true

printf '\n[atlas-onboard-status] local atlas rtsp stream\n'
if command -v ffprobe >/dev/null 2>&1; then
  timeout 8s ffprobe -rtsp_transport tcp \
    -v error \
    -select_streams v:0 \
    -show_entries stream=codec_name,width,height \
    -of default=noprint_wrappers=1 \
    rtsp://127.0.0.1:8554/atlas || true
else
  printf 'ffprobe not found\n'
fi

printf '\n[atlas-onboard-status] video agent log tail\n'
tail -n 80 "${HOME}/.local/state/atlas-agent/logs/atlas-video-agent.log" 2>/dev/null || true

printf '\n[atlas-onboard-status] hailo\n'
if command -v modprobe >/dev/null 2>&1; then
  sudo modprobe hailo_pci || true
fi
lsmod | grep -E '(^hailo|hailo_pci)' || true
modinfo hailo_pci 2>/dev/null | sed -n '1,80p' || true
find /sys/class -maxdepth 2 -iname '*hailo*' -print || true
if command -v lspci >/dev/null 2>&1; then
  lspci -nn | grep -i -E 'hailo|1e60' || true
fi
if command -v hailortcli >/dev/null 2>&1; then
  hailortcli fw-control identify || true
fi
