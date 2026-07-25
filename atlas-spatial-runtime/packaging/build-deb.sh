#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPATIAL_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

VERSION="${ATLAS_SPATIAL_RELEASE_VERSION:-0.1.0-dev}"
OUTPUT_DIR="${ATLAS_SPATIAL_PACKAGE_OUTPUT_DIR:-${SPATIAL_DIR}/dist}"
DEPTHAI_VERSION="$(
  sed -n 's/^  "depthai==\([^"]*\)",$/\1/p' "${SPATIAL_DIR}/pyproject.toml"
)"
NUMPY_VERSION="$(
  sed -n 's/^  "numpy==\([^"]*\)",$/\1/p' "${SPATIAL_DIR}/pyproject.toml"
)"

usage() {
  printf '%s\n' 'Usage: packaging/build-deb.sh'
  printf '%s\n' 'Builds atlas-spatial-runtime_<version>_arm64.deb.'
  printf '%s\n' 'Environment: ATLAS_SPATIAL_RELEASE_VERSION, ATLAS_SPATIAL_PACKAGE_OUTPUT_DIR'
}

fail() {
  printf 'atlas spatial package: %s\n' "$*" >&2
  exit 1
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi

[[ "${VERSION}" =~ ^[0-9A-Za-z.+:~_-]+$ ]] ||
  fail "invalid Debian package version: ${VERSION}"
[[ -n "${DEPTHAI_VERSION}" ]] ||
  fail "pyproject.toml must pin the DepthAI dependency"
[[ -n "${NUMPY_VERSION}" ]] ||
  fail "pyproject.toml must pin the NumPy dependency"

for command in dpkg-deb install python3 sed; do
  command -v "${command}" >/dev/null 2>&1 ||
    fail "missing required build command: ${command}"
done
python3 -m pip --version >/dev/null 2>&1 ||
  fail "python3 pip is required to resolve the ARM64 runtime"

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "${BUILD_DIR}"' EXIT
PACKAGE_ROOT="${BUILD_DIR}/atlas-spatial-runtime"
SITE_PACKAGES="${PACKAGE_ROOT}/opt/atlas-spatial-runtime/lib/python3.12/site-packages"
RUNTIME_BIN="${PACKAGE_ROOT}/opt/atlas-spatial-runtime/bin"
WHEELHOUSE="${BUILD_DIR}/wheelhouse"

install -d \
  "${PACKAGE_ROOT}/DEBIAN" \
  "${SITE_PACKAGES}" \
  "${RUNTIME_BIN}" \
  "${PACKAGE_ROOT}/usr/bin" \
  "${PACKAGE_ROOT}/usr/lib/systemd/system" \
  "${PACKAGE_ROOT}/usr/lib/udev/rules.d" \
  "${WHEELHOUSE}" \
  "${OUTPUT_DIR}"

printf '[atlas-spatial-package] resolving Linux ARM64 runtime dependencies\n'
python3 -m pip download \
  --dest "${WHEELHOUSE}" \
  --implementation cp \
  --python-version 3.12 \
  --abi cp312 \
  --platform manylinux_2_28_aarch64 \
  --only-binary=:all: \
  --no-deps \
  "depthai==${DEPTHAI_VERSION}" \
  "numpy==${NUMPY_VERSION}"

python3 -m pip install \
  --target "${SITE_PACKAGES}" \
  --implementation cp \
  --python-version 3.12 \
  --abi cp312 \
  --platform manylinux_2_28_aarch64 \
  --only-binary=:all: \
  --no-compile \
  --no-deps \
  --no-index \
  --find-links "${WHEELHOUSE}" \
  "depthai==${DEPTHAI_VERSION}" \
  "numpy==${NUMPY_VERSION}"

cp -R "${SPATIAL_DIR}/src/atlas_spatial_runtime" "${SITE_PACKAGES}/"
find "${SITE_PACKAGES}" -type d -name '__pycache__' -prune -exec rm -rf {} +
find "${SITE_PACKAGES}" -type f -name '*.pyc' -delete
rm -rf "${SITE_PACKAGES}/.cache" "${SITE_PACKAGES}/bin"
find "${SITE_PACKAGES}" \
  -maxdepth 1 \
  -type f \
  -name 'depthai.cpython-*-aarch64-linux-gnu.so' \
  ! -name 'depthai.cpython-312-aarch64-linux-gnu.so' \
  -delete

write_launcher() {
  local name="$1"
  local module="$2"
  local visibility="$3"
  local target="${RUNTIME_BIN}/${name}"

  {
    printf '%s\n' '#!/bin/sh'
    printf '%s\n' 'set -eu'
    printf '%s\n' 'export PYTHONPATH="/opt/atlas-spatial-runtime/lib/python3.12/site-packages${PYTHONPATH:+:${PYTHONPATH}}"'
    printf 'exec /usr/bin/python3 -m %s "$@"\n' "${module}"
  } > "${target}"
  chmod 0755 "${target}"
  if [[ "${visibility}" == "public" ]]; then
    ln -s "/opt/atlas-spatial-runtime/bin/${name}" \
      "${PACKAGE_ROOT}/usr/bin/${name}"
  fi
}

write_launcher atlas-spatial-runtime atlas_spatial_runtime.runtime public
write_launcher atlas-spatial-probe atlas_spatial_runtime.probe private
write_launcher atlas-spatial-check atlas_spatial_runtime.diagnostics private

install -m 0644 \
  "${SCRIPT_DIR}/atlas-spatial-runtime.service" \
  "${PACKAGE_ROOT}/usr/lib/systemd/system/atlas-spatial-runtime.service"
install -m 0644 \
  "${SCRIPT_DIR}/99-atlas-depth-camera.rules" \
  "${PACKAGE_ROOT}/usr/lib/udev/rules.d/99-atlas-depth-camera.rules"

install -m 0755 "${SCRIPT_DIR}/debian/postinst" "${PACKAGE_ROOT}/DEBIAN/postinst"
install -m 0755 "${SCRIPT_DIR}/debian/prerm" "${PACKAGE_ROOT}/DEBIAN/prerm"
install -m 0755 "${SCRIPT_DIR}/debian/postrm" "${PACKAGE_ROOT}/DEBIAN/postrm"
sed "s/@VERSION@/${VERSION}/g" \
  "${SCRIPT_DIR}/debian/control.in" \
  > "${PACKAGE_ROOT}/DEBIAN/control"

PACKAGE_PATH="${OUTPUT_DIR}/atlas-spatial-runtime_${VERSION}_arm64.deb"
dpkg-deb --root-owner-group --build "${PACKAGE_ROOT}" "${PACKAGE_PATH}"
printf '[atlas-spatial-package] created %s\n' "${PACKAGE_PATH}"
