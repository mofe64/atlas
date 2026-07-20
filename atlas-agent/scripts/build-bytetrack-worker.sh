#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT_PATH="${1:-${AGENT_DIR}/dist/atlas-bytetrack-worker}"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "${BUILD_DIR}"' EXIT

for command in cmake ctest install; do
  command -v "${command}" >/dev/null 2>&1 || {
    printf 'missing required build command: %s\n' "${command}" >&2
    exit 1
  }
done

cmake \
  -S "${AGENT_DIR}/third_party/bytetrack" \
  -B "${BUILD_DIR}" \
  -DCMAKE_BUILD_TYPE=Release
cmake --build "${BUILD_DIR}" --parallel
ctest --test-dir "${BUILD_DIR}" --output-on-failure
mkdir -p "$(dirname "${OUTPUT_PATH}")"
install -m 0755 "${BUILD_DIR}/atlas-bytetrack-worker" "${OUTPUT_PATH}"
printf 'created %s\n' "${OUTPUT_PATH}"
