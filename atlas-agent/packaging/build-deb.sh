#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

VERSION="${ATLAS_RELEASE_VERSION:-0.1.0-dev}"
OUTPUT_DIR="${ATLAS_PACKAGE_OUTPUT_DIR:-${AGENT_DIR}/dist}"
CACHE_DIR="${ATLAS_PACKAGE_CACHE_DIR:-${HOME}/.cache/atlas-packaging}"
MAVSDK_VERSION="${ATLAS_MAVSDK_VERSION:-v3.17.1}"
MAVSDK_ASSET="mavsdk_server_linux-arm64-musl"
MAVSDK_SHA256="${ATLAS_MAVSDK_SHA256:-dea8b9b30cbc2bbe35550b46244625c42c380ddfce9cbd47cd19d4cae66e2f2b}"
MAVSDK_URL="https://github.com/mavlink/MAVSDK/releases/download/${MAVSDK_VERSION}/${MAVSDK_ASSET}"
MODEL_SOURCE="${ATLAS_HEF_MODEL_PATH:-}"
MODEL_ACCELERATOR="${ATLAS_MODEL_ACCELERATOR:-hailo-8l}"
MODEL_PACKAGE_URL="${ATLAS_MODEL_PACKAGE_URL:-https://archive.raspberrypi.com/debian/pool/main/r/rpicam-apps/rpicam-apps-hailo-postprocess_1.9.0-1~bpo12+1_arm64.deb}"
MODEL_PACKAGE_SHA256="${ATLAS_MODEL_PACKAGE_SHA256:-a255a8fd7cb7237fcc9c3e067bda892b45db57c066456edd75f332ebe783711a}"

usage() {
  printf '%s\n' 'Usage: packaging/build-deb.sh'
  printf '%s\n' 'Builds atlas-agent_<version>_arm64.deb with pinned MAVSDK and Hailo-8L model artifacts.'
  printf '%s\n' 'Environment: ATLAS_RELEASE_VERSION, ATLAS_PACKAGE_OUTPUT_DIR, ATLAS_HEF_MODEL_PATH'
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

for command in go dpkg-deb curl install sed sha256sum; do
  command -v "$command" >/dev/null 2>&1 || {
    printf 'missing required build command: %s\n' "$command" >&2
    exit 1
  }
done

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "${BUILD_DIR}"' EXIT
PACKAGE_ROOT="${BUILD_DIR}/atlas-agent"
mkdir -p \
  "${PACKAGE_ROOT}/DEBIAN" \
  "${PACKAGE_ROOT}/usr/bin" \
  "${PACKAGE_ROOT}/usr/libexec/atlas-agent" \
  "${PACKAGE_ROOT}/usr/lib/systemd/system" \
  "${PACKAGE_ROOT}/usr/share/atlas-agent/models" \
  "${CACHE_DIR}" \
  "${OUTPUT_DIR}"

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
install -m 0644 "${SCRIPT_DIR}/systemd/atlas-agent.service" "${PACKAGE_ROOT}/usr/lib/systemd/system/atlas-agent.service"
install -m 0644 "${SCRIPT_DIR}/systemd/atlas-mavsdk.service" "${PACKAGE_ROOT}/usr/lib/systemd/system/atlas-mavsdk.service"
install -m 0755 "${SCRIPT_DIR}/debian/postinst" "${PACKAGE_ROOT}/DEBIAN/postinst"
install -m 0755 "${SCRIPT_DIR}/debian/prerm" "${PACKAGE_ROOT}/DEBIAN/prerm"
install -m 0755 "${SCRIPT_DIR}/debian/postrm" "${PACKAGE_ROOT}/DEBIAN/postrm"
sed "s/@VERSION@/${VERSION}/g" "${SCRIPT_DIR}/debian/control.in" > "${PACKAGE_ROOT}/DEBIAN/control"

{
  printf 'ATLAS_RELEASE_VERSION="%s"\n' "${VERSION}"
  printf 'ATLAS_MAVSDK_VERSION="%s"\n' "${MAVSDK_VERSION}"
  printf 'ATLAS_MAVSDK_SHA256="%s"\n' "${MAVSDK_SHA256}"
  printf 'ATLAS_MODEL_ACCELERATOR="%s"\n' "${MODEL_ACCELERATOR}"
  printf 'ATLAS_MODEL_SHA256="%s"\n' "${MODEL_SHA256}"
} > "${PACKAGE_ROOT}/usr/share/atlas-agent/release.env"
chmod 0644 "${PACKAGE_ROOT}/usr/share/atlas-agent/release.env"

PACKAGE_PATH="${OUTPUT_DIR}/atlas-agent_${VERSION}_arm64.deb"
dpkg-deb --root-owner-group --build "${PACKAGE_ROOT}" "${PACKAGE_PATH}"
(
  cd "${OUTPUT_DIR}"
  sha256sum "$(basename "${PACKAGE_PATH}")" > "$(basename "${PACKAGE_PATH}").sha256"
)
printf '[atlas-package] created %s\n' "${PACKAGE_PATH}"
