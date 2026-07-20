#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ATLAS_TEST_SITL_MAVSDK_ADDR="${ATLAS_TEST_SITL_MAVSDK_ADDR:-127.0.0.1:50051}"
ATLAS_TEST_SITL_RTSP_URL="${ATLAS_TEST_SITL_RTSP_URL:-${ATLAS_VIDEO_RTSP_URL:-rtsp://127.0.0.1:8554/main.264}}"
ATLAS_RUST_TOOLCHAIN="${ATLAS_RUST_TOOLCHAIN:-1.97.0}"

export ATLAS_TEST_SITL_MAVSDK_ADDR
export ATLAS_VIDEO_RTSP_URL="${ATLAS_TEST_SITL_RTSP_URL}"
export ATLAS_VIDEO_SOURCE_ID="${ATLAS_VIDEO_SOURCE_ID:-sitl-gimbal}"

exec rustup run "${ATLAS_RUST_TOOLCHAIN}" cargo test \
  --manifest-path "${ROOT_DIR}/atlas/src-tauri/Cargo.toml" \
  ground_station::tests::sitl_flies_response_patterns_with_continuous_video \
  -- \
  --ignored \
  --nocapture \
  --test-threads=1
