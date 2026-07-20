#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SPATIAL_RUNTIME_DIR="$(cd "${AGENT_DIR}/../atlas-spatial-runtime" && pwd)"
MAVSDK_PIN_FILE="${SCRIPT_DIR}/mavsdk.env"

[[ -r "${MAVSDK_PIN_FILE}" ]] || {
  printf 'MAVSDK release contract is missing: %s\n' "${MAVSDK_PIN_FILE}" >&2
  exit 1
}
# shellcheck source=mavsdk.env
source "${MAVSDK_PIN_FILE}"
: "${ATLAS_MAVSDK_VERSION:?mavsdk.env must define ATLAS_MAVSDK_VERSION}"
: "${ATLAS_MAVSDK_SHA256:?mavsdk.env must define ATLAS_MAVSDK_SHA256}"
: "${ATLAS_MAVSDK_PROTO_COMMIT:?mavsdk.env must define ATLAS_MAVSDK_PROTO_COMMIT}"

VERSION="${ATLAS_RELEASE_VERSION:-0.1.0-dev}"
OUTPUT_DIR="${ATLAS_PACKAGE_OUTPUT_DIR:-${AGENT_DIR}/dist}"
CACHE_DIR="${ATLAS_PACKAGE_CACHE_DIR:-${HOME}/.cache/atlas-packaging}"
MAVSDK_VERSION="${ATLAS_MAVSDK_VERSION}"
MAVSDK_ASSET="mavsdk_server_linux-arm64-musl"
MAVSDK_SHA256="${ATLAS_MAVSDK_SHA256}"
MAVSDK_URL="https://github.com/mavlink/MAVSDK/releases/download/${MAVSDK_VERSION}/${MAVSDK_ASSET}"
MODEL_SOURCE="${ATLAS_HEF_MODEL_PATH:-}"
MODEL_ACCELERATOR="${ATLAS_MODEL_ACCELERATOR:-hailo-8l}"
MODEL_PACKAGE_URL="${ATLAS_MODEL_PACKAGE_URL:-https://archive.raspberrypi.com/debian/pool/main/r/rpicam-apps/rpicam-apps-hailo-postprocess_1.9.0-1~bpo12+1_arm64.deb}"
MODEL_PACKAGE_SHA256="${ATLAS_MODEL_PACKAGE_SHA256:-a255a8fd7cb7237fcc9c3e067bda892b45db57c066456edd75f332ebe783711a}"
BYTETRACK_WORKER_BIN="${ATLAS_BYTETRACK_WORKER_BIN:-}"
SPATIAL_IMAGE="${ATLAS_SPATIAL_CONTAINER_IMAGE:-atlas-spatial-runtime:${VERSION}}"

usage() {
  printf '%s\n' 'Usage: packaging/build-deb.sh'
  printf '%s\n' 'Builds atlas-agent_<version>_arm64.deb with MAVSDK, Hailo, and spatial-runtime artifacts.'
  printf '%s\n' 'Environment: ATLAS_RELEASE_VERSION, ATLAS_PACKAGE_OUTPUT_DIR, ATLAS_HEF_MODEL_PATH'
  printf '%s\n' 'Optional overrides: ATLAS_BYTETRACK_WORKER_BIN, ATLAS_SPATIAL_CONTAINER_IMAGE'
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi

if [[ ! "${VERSION}" =~ ^[0-9A-Za-z.+:~_-]+$ ]]; then
  printf 'invalid Debian package version: %s\n' "${VERSION}" >&2
  exit 1
fi
case "${MODEL_ACCELERATOR}" in
  hailo-8|hailo-8l)
    ;;
  *)
    printf 'ATLAS_MODEL_ACCELERATOR must be hailo-8 or hailo-8l\n' >&2
    exit 1
    ;;
esac

for command in go git dpkg-deb curl cp file find grep install sed sha256sum uname; do
  command -v "$command" >/dev/null 2>&1 || {
    printf 'missing required build command: %s\n' "$command" >&2
    exit 1
  }
done

is_linux_arm64_worker() {
  local worker_path="$1"
  local description
  [[ -s "${worker_path}" && -x "${worker_path}" ]] || return 1
  description="$(file -b "${worker_path}")"
  [[ "${description}" == *"ELF 64-bit"* && ( "${description}" == *"ARM aarch64"* || "${description}" == *"ARM64"* ) ]] &&
    grep -aFq 'atlas-bytetrack-worker' "${worker_path}"
}

build_linux_arm64_worker() {
  local output_path="$1"
  local host_os
  local host_arch
  host_os="$(uname -s)"
  host_arch="$(uname -m)"

  if [[ "${host_os}" == "Linux" && ( "${host_arch}" == "aarch64" || "${host_arch}" == "arm64" ) ]]; then
    printf '[atlas-package] building ByteTrack worker natively for Linux arm64\n'
    "${AGENT_DIR}/scripts/build-bytetrack-worker.sh" "${output_path}"
    return
  fi

  if command -v aarch64-linux-gnu-g++ >/dev/null 2>&1; then
    command -v cmake >/dev/null 2>&1 || {
      printf 'cmake is required to cross-build the ByteTrack worker\n' >&2
      exit 1
    }
    local worker_build_dir="${BUILD_DIR}/bytetrack-worker-cross"
    printf '[atlas-package] cross-building ByteTrack worker with aarch64-linux-gnu-g++\n'
    cmake \
      -S "${AGENT_DIR}/third_party/bytetrack" \
      -B "${worker_build_dir}" \
      -DCMAKE_BUILD_TYPE=Release \
      -DBUILD_TESTING=OFF \
      -DCMAKE_SYSTEM_NAME=Linux \
      -DCMAKE_SYSTEM_PROCESSOR=aarch64 \
      -DCMAKE_CXX_COMPILER="$(command -v aarch64-linux-gnu-g++)"
    cmake --build "${worker_build_dir}" --parallel --target atlas-bytetrack-worker
    install -m 0755 "${worker_build_dir}/atlas-bytetrack-worker" "${output_path}"
    return
  fi

  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 && docker buildx version >/dev/null 2>&1; then
    local docker_output_dir="${BUILD_DIR}/bytetrack-worker-docker"
    printf '[atlas-package] building ByteTrack worker in a Linux arm64 Docker container\n'
    mkdir -p "${docker_output_dir}"
    docker buildx build \
      --platform linux/arm64 \
      --file "${AGENT_DIR}/packaging/bytetrack/Dockerfile" \
      --output "type=local,dest=${docker_output_dir}" \
      "${AGENT_DIR}"
    install -m 0755 "${docker_output_dir}/atlas-bytetrack-worker" "${output_path}"
    return
  fi

  printf '%s\n' 'cannot create the Linux arm64 ByteTrack worker on this host' >&2
  printf '%s\n' 'build on Linux arm64, install an aarch64-linux-gnu C++ cross-compiler, or start Docker with Buildx support' >&2
  exit 1
}

resolve_bytetrack_worker() {
  local candidate
  if [[ -n "${BYTETRACK_WORKER_BIN}" ]]; then
    is_linux_arm64_worker "${BYTETRACK_WORKER_BIN}" || {
      printf 'ATLAS_BYTETRACK_WORKER_BIN is not an executable Linux arm64 binary: %s\n' "${BYTETRACK_WORKER_BIN}" >&2
      exit 1
    }
    printf '[atlas-package] using ByteTrack worker override %s\n' "${BYTETRACK_WORKER_BIN}"
    return
  fi

  for candidate in \
    "${OUTPUT_DIR}/atlas-bytetrack-worker-linux-arm64" \
    "${AGENT_DIR}/dist/atlas-bytetrack-worker-linux-arm64" \
    "${AGENT_DIR}/dist/atlas-bytetrack-worker"; do
    if is_linux_arm64_worker "${candidate}"; then
      BYTETRACK_WORKER_BIN="${candidate}"
      printf '[atlas-package] found cached ByteTrack worker %s\n' "${BYTETRACK_WORKER_BIN}"
      return
    fi
  done

  BYTETRACK_WORKER_BIN="${OUTPUT_DIR}/atlas-bytetrack-worker-linux-arm64"
  build_linux_arm64_worker "${BYTETRACK_WORKER_BIN}"
  is_linux_arm64_worker "${BYTETRACK_WORKER_BIN}" || {
    printf 'generated ByteTrack worker is not an executable Linux arm64 binary: %s\n' "${BYTETRACK_WORKER_BIN}" >&2
    exit 1
  }
}

MAVSDK_PROTO_DIR="${AGENT_DIR}/../third_party/mavsdk-proto"
MAVSDK_SCHEMA_MARKER="${AGENT_DIR}/internal/mavsdkpb/schema.commit"
ACTUAL_PROTO_COMMIT="$(git -C "${MAVSDK_PROTO_DIR}" rev-parse HEAD)"
if [[ "${ACTUAL_PROTO_COMMIT}" != "${ATLAS_MAVSDK_PROTO_COMMIT}" ]]; then
  printf 'MAVSDK schema mismatch: %s requires proto %s, checkout has %s\n' \
    "${MAVSDK_VERSION}" "${ATLAS_MAVSDK_PROTO_COMMIT}" "${ACTUAL_PROTO_COMMIT}" >&2
  exit 1
fi
[[ -r "${MAVSDK_SCHEMA_MARKER}" ]] || {
  printf 'generated MAVSDK schema marker is missing; run scripts/generate-mavsdk-go.sh\n' >&2
  exit 1
}
IFS= read -r GENERATED_PROTO_COMMIT < "${MAVSDK_SCHEMA_MARKER}"
if [[ "${GENERATED_PROTO_COMMIT}" != "${ATLAS_MAVSDK_PROTO_COMMIT}" ]]; then
  printf 'generated MAVSDK client uses proto %s; expected %s; run scripts/generate-mavsdk-go.sh\n' \
    "${GENERATED_PROTO_COMMIT}" "${ATLAS_MAVSDK_PROTO_COMMIT}" >&2
  exit 1
fi

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "${BUILD_DIR}"' EXIT
PACKAGE_ROOT="${BUILD_DIR}/atlas-agent"
mkdir -p \
  "${PACKAGE_ROOT}/DEBIAN" \
  "${PACKAGE_ROOT}/usr/bin" \
  "${PACKAGE_ROOT}/usr/sbin" \
  "${PACKAGE_ROOT}/usr/libexec/atlas-agent" \
  "${PACKAGE_ROOT}/usr/lib/systemd/system" \
  "${PACKAGE_ROOT}/usr/lib/udev/rules.d" \
  "${PACKAGE_ROOT}/usr/share/atlas-agent/hailo-container" \
  "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime" \
  "${PACKAGE_ROOT}/usr/share/atlas-agent/models" \
  "${PACKAGE_ROOT}/usr/share/doc/atlas-agent/third-party/bytetrack" \
  "${CACHE_DIR}" \
  "${OUTPUT_DIR}"

resolve_bytetrack_worker

printf '[atlas-package] building linux/arm64 binaries\n'
(
  cd "${AGENT_DIR}"
  RELEASE_LDFLAGS="-s -w -X github.com/sunnyside/atlas/atlas-agent/internal/buildinfo.Version=${VERSION}"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "${RELEASE_LDFLAGS}" -o "${PACKAGE_ROOT}/usr/bin/atlas-agent" ./cmd/atlas-agent
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "${RELEASE_LDFLAGS}" -o "${PACKAGE_ROOT}/usr/bin/atlas-setup" ./cmd/atlas-setup
)

MAVSDK_CACHE="${CACHE_DIR}/${MAVSDK_ASSET}_${MAVSDK_VERSION}"
if [[ ! -f "${MAVSDK_CACHE}" ]]; then
  printf '[atlas-package] downloading MAVSDK %s\n' "${MAVSDK_VERSION}"
  curl -fL "${MAVSDK_URL}" -o "${MAVSDK_CACHE}.tmp"
  mv "${MAVSDK_CACHE}.tmp" "${MAVSDK_CACHE}"
fi
printf '%s  %s\n' "${MAVSDK_SHA256}" "${MAVSDK_CACHE}" | sha256sum -c -
install -m 0755 "${MAVSDK_CACHE}" "${PACKAGE_ROOT}/usr/libexec/atlas-agent/mavsdk_server"

if [[ -z "${MODEL_SOURCE}" ]]; then
  MODEL_DEB="${CACHE_DIR}/$(basename "${MODEL_PACKAGE_URL}")"
  if [[ ! -f "${MODEL_DEB}" ]]; then
    printf '[atlas-package] downloading pinned Hailo-8L model package\n'
    curl -fL "${MODEL_PACKAGE_URL}" -o "${MODEL_DEB}.tmp"
    mv "${MODEL_DEB}.tmp" "${MODEL_DEB}"
  fi
  printf '%s  %s\n' "${MODEL_PACKAGE_SHA256}" "${MODEL_DEB}" | sha256sum -c -
  MODEL_EXTRACT="${BUILD_DIR}/model-package"
  mkdir -p "${MODEL_EXTRACT}"
  dpkg-deb -x "${MODEL_DEB}" "${MODEL_EXTRACT}"
  MODEL_SOURCE="${MODEL_EXTRACT}/usr/share/hailo-models/yolov6n_h8l.hef"
fi
[[ -s "${MODEL_SOURCE}" ]] || {
  printf 'Hailo HEF model is missing or empty: %s\n' "${MODEL_SOURCE}" >&2
  exit 1
}
install -m 0644 "${MODEL_SOURCE}" "${PACKAGE_ROOT}/usr/share/atlas-agent/models/objects.hef"
MODEL_SHA256="$(sha256sum "${MODEL_SOURCE}" | awk '{print $1}')"

install -m 0755 "${AGENT_DIR}/scripts/atlas-hailort-adapter.py" "${PACKAGE_ROOT}/usr/libexec/atlas-agent/atlas-hailort-adapter"
install -m 0755 "${BYTETRACK_WORKER_BIN}" "${PACKAGE_ROOT}/usr/libexec/atlas-agent/atlas-bytetrack-worker"
printf '[atlas-package] included FoundationVision ByteTrack worker with Atlas CMC extension\n'
install -m 0644 "${AGENT_DIR}/third_party/bytetrack/LICENSE" "${PACKAGE_ROOT}/usr/share/doc/atlas-agent/third-party/bytetrack/LICENSE"
install -m 0644 "${AGENT_DIR}/third_party/bytetrack/README.atlas.md" "${PACKAGE_ROOT}/usr/share/doc/atlas-agent/third-party/bytetrack/README.atlas.md"
install -m 0755 "${AGENT_DIR}/scripts/setup-hailo-ubuntu.sh" "${PACKAGE_ROOT}/usr/sbin/atlas-hailo-setup"
install -m 0755 "${AGENT_DIR}/scripts/setup-spatial-ubuntu.sh" "${PACKAGE_ROOT}/usr/sbin/atlas-spatial-setup"
install -m 0644 "${SCRIPT_DIR}/hailo/Dockerfile" "${PACKAGE_ROOT}/usr/share/atlas-agent/hailo-container/Dockerfile"
install -m 0755 "${SCRIPT_DIR}/hailo/atlas-hailo-container-check" "${PACKAGE_ROOT}/usr/share/atlas-agent/hailo-container/atlas-hailo-container-check"
install -m 0755 "${SCRIPT_DIR}/hailo/atlas-hailo-container-run" "${PACKAGE_ROOT}/usr/libexec/atlas-agent/atlas-hailo-container-run"
install -m 0755 "${SCRIPT_DIR}/spatial/atlas-spatial-container-run" "${PACKAGE_ROOT}/usr/libexec/atlas-agent/atlas-spatial-container-run"
install -m 0755 "${SCRIPT_DIR}/spatial/atlas-spatial-runtime-check" "${PACKAGE_ROOT}/usr/libexec/atlas-agent/atlas-spatial-runtime-check"
install -m 0644 "${SCRIPT_DIR}/spatial/99-atlas-depth-camera.rules" "${PACKAGE_ROOT}/usr/lib/udev/rules.d/99-atlas-depth-camera.rules"
mkdir -p "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime/packaging" "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime/ros2_ws"
install -m 0644 "${SPATIAL_RUNTIME_DIR}/README.md" "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime/README.md"
install -m 0644 "${SPATIAL_RUNTIME_DIR}/spatial-runtime.env" "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime/spatial-runtime.env"
install -m 0644 "${SPATIAL_RUNTIME_DIR}/packaging/Dockerfile" "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime/packaging/Dockerfile"
install -m 0755 "${SPATIAL_RUNTIME_DIR}/packaging/entrypoint.sh" "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime/packaging/entrypoint.sh"
cp -a "${SPATIAL_RUNTIME_DIR}/ros2_ws/src" "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime/ros2_ws/src"
find "${PACKAGE_ROOT}/usr/share/atlas-agent/spatial-runtime/ros2_ws/src" -type d -name __pycache__ -prune -exec rm -rf {} +
install -m 0755 "${AGENT_DIR}/scripts/atlas-hailort-adapter.py" "${PACKAGE_ROOT}/usr/share/atlas-agent/hailo-container/atlas-hailort-adapter.py"
install -m 0644 "${SCRIPT_DIR}/systemd/atlas-agent.service" "${PACKAGE_ROOT}/usr/lib/systemd/system/atlas-agent.service"
install -m 0644 "${SCRIPT_DIR}/systemd/atlas-hailo-adapter.service" "${PACKAGE_ROOT}/usr/lib/systemd/system/atlas-hailo-adapter.service"
install -m 0644 "${SCRIPT_DIR}/systemd/atlas-mavsdk.service" "${PACKAGE_ROOT}/usr/lib/systemd/system/atlas-mavsdk.service"
install -m 0644 "${SCRIPT_DIR}/systemd/atlas-spatial-runtime.service" "${PACKAGE_ROOT}/usr/lib/systemd/system/atlas-spatial-runtime.service"
install -m 0755 "${SCRIPT_DIR}/debian/postinst" "${PACKAGE_ROOT}/DEBIAN/postinst"
install -m 0755 "${SCRIPT_DIR}/debian/prerm" "${PACKAGE_ROOT}/DEBIAN/prerm"
install -m 0755 "${SCRIPT_DIR}/debian/postrm" "${PACKAGE_ROOT}/DEBIAN/postrm"
sed "s/@VERSION@/${VERSION}/g" "${SCRIPT_DIR}/debian/control.in" > "${PACKAGE_ROOT}/DEBIAN/control"

{
  printf 'ATLAS_RELEASE_VERSION="%s"\n' "${VERSION}"
  printf 'ATLAS_MAVSDK_VERSION="%s"\n' "${MAVSDK_VERSION}"
  printf 'ATLAS_MAVSDK_SHA256="%s"\n' "${MAVSDK_SHA256}"
  printf 'ATLAS_MAVSDK_PROTO_COMMIT="%s"\n' "${ATLAS_MAVSDK_PROTO_COMMIT}"
  printf 'ATLAS_MODEL_ACCELERATOR="%s"\n' "${MODEL_ACCELERATOR}"
  printf 'ATLAS_MODEL_SHA256="%s"\n' "${MODEL_SHA256}"
  printf 'ATLAS_SPATIAL_CONTAINER_IMAGE="%s"\n' "${SPATIAL_IMAGE}"
  printf 'ATLAS_SPATIAL_CONTRACT_VERSION="1"\n'
} > "${PACKAGE_ROOT}/usr/share/atlas-agent/release.env"
chmod 0644 "${PACKAGE_ROOT}/usr/share/atlas-agent/release.env"

PACKAGE_PATH="${OUTPUT_DIR}/atlas-agent_${VERSION}_arm64.deb"
dpkg-deb --root-owner-group --build "${PACKAGE_ROOT}" "${PACKAGE_PATH}"
(
  cd "${OUTPUT_DIR}"
  sha256sum "$(basename "${PACKAGE_PATH}")" > "$(basename "${PACKAGE_PATH}").sha256"
)
printf '[atlas-package] created %s\n' "${PACKAGE_PATH}"
