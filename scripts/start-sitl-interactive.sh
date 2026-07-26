#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SITL_LAUNCHER="${ROOT_DIR}/scripts/start-sitl.sh"
SAMPLE_VIDEO_DIR="${ROOT_DIR}/sampleVids"

fail() {
  printf '[atlas-sitl-select] error: %s\n' "$*" >&2
  exit 1
}

read_selection() {
  local prompt="$1"
  local default="$2"
  local maximum="$3"
  local selection

  while true; do
    printf '%s' "$prompt" >&2
    if ! IFS= read -r selection; then
      fail "input ended before a selection was made"
    fi
    selection="${selection:-$default}"
    if [[ "$selection" =~ ^[0-9]+$ ]] \
      && ((selection >= 1 && selection <= maximum)); then
      printf '%s\n' "$selection"
      return
    fi
    printf 'Please enter a number from 1 to %s.\n' "$maximum" >&2
  done
}

choose_sample_video() {
  local index
  local selection
  local selected_video
  local videos=()

  [[ -d "$SAMPLE_VIDEO_DIR" ]] \
    || fail "sample video directory not found: ${SAMPLE_VIDEO_DIR}"

  shopt -s nullglob nocaseglob
  videos=(
    "$SAMPLE_VIDEO_DIR"/*.mp4
    "$SAMPLE_VIDEO_DIR"/*.mov
    "$SAMPLE_VIDEO_DIR"/*.mkv
    "$SAMPLE_VIDEO_DIR"/*.avi
    "$SAMPLE_VIDEO_DIR"/*.webm
    "$SAMPLE_VIDEO_DIR"/*.m4v
  )
  shopt -u nullglob nocaseglob

  if [[ ${#videos[@]} -eq 0 ]]; then
    fail "no supported videos found in ${SAMPLE_VIDEO_DIR}; add an MP4, MOV, MKV, AVI, WebM, or M4V file"
  fi

  printf '\nAvailable sample videos:\n' >&2
  index=1
  for selected_video in "${videos[@]}"; do
    printf '  %d) %s\n' "$index" "$(basename "$selected_video")" >&2
    index=$((index + 1))
  done

  selection="$(read_selection "Select a sample video [1]: " 1 "${#videos[@]}")"
  printf '%s\n' "${videos[$((selection - 1))]}"
}

[[ -x "$SITL_LAUNCHER" ]] || fail "SITL launcher is not executable: ${SITL_LAUNCHER}"

printf 'Atlas SITL video source:\n'
printf '  1) Gazebo simulated gimbal camera\n'
printf '  2) Prerecorded video from sampleVids\n'

source_selection="$(read_selection "Select a video source [1]: " 1 2)"

case "$source_selection" in
  1)
    unset ATLAS_VIDEO_SOURCE ATLAS_VIDEO_RTSP_URL ATLAS_VIDEO_SOURCE_ID
    printf '\nStarting SITL with the Gazebo camera.\n'
    exec "$SITL_LAUNCHER" "$@"
    ;;
  2)
    selected_video="$(choose_sample_video)"
    export ATLAS_VIDEO_SOURCE="$selected_video"
    export ATLAS_VIDEO_SOURCE_ID="${ATLAS_VIDEO_SOURCE_ID:-sample-video}"
    unset ATLAS_VIDEO_RTSP_URL
    printf '\nStarting SITL with sample video: %s\n' "$(basename "$selected_video")"
    exec "$SITL_LAUNCHER" --skip-video "$@"
    ;;
esac
