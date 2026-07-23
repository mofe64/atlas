#!/usr/bin/env bash
set -euo pipefail

CONTEXT_DIR="/usr/share/atlas-agent/spatial-runtime"
IMAGE=""
BUILD_LOCAL=false

usage() {
  printf '%s\n' 'Usage: atlas-spatial-setup --image IMAGE [--build-local] [--context DIR]'
  printf '%s\n' 'Ensures Docker and the Atlas spatial runtime image are ready on Ubuntu 24.04.'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --image)
      [[ $# -ge 2 ]] || {
        printf '%s\n' '--image requires a value' >&2
        exit 2
      }
      IMAGE="$2"
      shift 2
      ;;
    --build-local)
      BUILD_LOCAL=true
      shift
      ;;
    --context)
      [[ $# -ge 2 ]] || {
        printf '%s\n' '--context requires a value' >&2
        exit 2
      }
      CONTEXT_DIR="$2"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

[[ "${EUID}" -eq 0 ]] || {
  printf '%s\n' 'atlas-spatial-setup must run as root' >&2
  exit 1
}
[[ "${IMAGE}" =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] || {
  printf '%s\n' '--image must be a valid Docker image reference' >&2
  exit 2
}

if ! command -v docker >/dev/null 2>&1; then
  printf '%s\n' '[atlas-spatial] installing Docker from the Ubuntu repository'
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io
fi

systemctl enable --now docker.service
if docker image inspect "${IMAGE}" >/dev/null 2>&1; then
  printf '[atlas-spatial] image already available: %s\n' "${IMAGE}"
  exit 0
fi

IMAGE_ARCHIVE="${CONTEXT_DIR}/atlas-spatial-runtime.tar"
if [[ -r "${IMAGE_ARCHIVE}" ]]; then
  printf '[atlas-spatial] loading packaged image archive: %s\n' "${IMAGE_ARCHIVE}"
  docker load --input "${IMAGE_ARCHIVE}"
fi
if docker image inspect "${IMAGE}" >/dev/null 2>&1; then
  exit 0
fi

if [[ "${BUILD_LOCAL}" != true ]]; then
  printf 'spatial image is unavailable: %s\n' "${IMAGE}" >&2
  printf '%s\n' 'Provide a release image archive or retry with --build-local.' >&2
  exit 1
fi
[[ -r "${CONTEXT_DIR}/packaging/Dockerfile" \
  && -d "${CONTEXT_DIR}/packaging/depthai" \
  && -d "${CONTEXT_DIR}/ros2_ws/src" ]] || {
  printf 'bundled spatial build context is incomplete: %s\n' "${CONTEXT_DIR}" >&2
  exit 1
}

printf '[atlas-spatial] building %s locally; Docker will reuse the patched DepthAI layers when unchanged\n' "${IMAGE}"
docker build \
  --file "${CONTEXT_DIR}/packaging/Dockerfile" \
  --build-arg "ATLAS_SPATIAL_VERSION=${IMAGE##*:}" \
  --tag "${IMAGE}" \
  "${CONTEXT_DIR}"
docker image inspect "${IMAGE}" >/dev/null
printf '[atlas-spatial] image ready: %s\n' "${IMAGE}"
