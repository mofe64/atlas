#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPATIAL_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DIST_DIR="${ATLAS_SPATIAL_RELEASE_OUTPUT_DIR:-${SPATIAL_DIR}/dist}"

usage() {
  cat <<'EOF'
Usage:
  packaging/release.sh build VERSION [--replace]
  packaging/release.sh transfer VERSION USER@HOST

Build runs the Spatial source tests and creates one self-contained Linux-arm64
Debian package. The package contains the native Python runtime and does not
download Python dependencies when it is installed on the Pi. After a successful
build, older Spatial packages are removed from the output directory.

Transfer copies only that package to /tmp on the Pi. Install Agent and Spatial
together before running atlas-setup on the landed, disarmed aircraft.

Environment:
  ATLAS_SPATIAL_RELEASE_OUTPUT_DIR  Override the package directory (default: dist).
EOF
}

fail() {
  printf 'atlas spatial release: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 ||
    fail "missing required command: $1"
}

validate_version() {
  [[ "$1" =~ ^[0-9A-Za-z.+:~_-]+$ ]] ||
    fail "invalid version: $1"
}

package_name() {
  printf 'atlas-spatial-runtime_%s_arm64.deb\n' "$1"
}

build_release() {
  local version="$1"
  local option="${2:-}"
  local replace=false
  local name
  local path
  local staging_dir

  validate_version "${version}"
  case "${option}" in
    "")
      ;;
    --replace)
      replace=true
      ;;
    *)
      fail "unknown build option: ${option}"
      ;;
  esac

  name="$(package_name "${version}")"
  path="${DIST_DIR}/${name}"
  mkdir -p "${DIST_DIR}"
  if [[ -e "${path}" && "${replace}" != true ]]; then
    fail "${name} already exists; use --replace to rebuild this version"
  fi

  printf 'atlas spatial release: testing source\n'
  "${SPATIAL_DIR}/scripts/test-source.sh"

  staging_dir="$(mktemp -d "${DIST_DIR}/.release-${version}.XXXXXX")"
  cleanup_release_staging() {
    rm -rf -- "${staging_dir}"
  }
  trap cleanup_release_staging EXIT

  printf 'atlas spatial release: building %s\n' "${name}"
  ATLAS_SPATIAL_RELEASE_VERSION="${version}" \
  ATLAS_SPATIAL_PACKAGE_OUTPUT_DIR="${staging_dir}" \
    "${SPATIAL_DIR}/packaging/build-deb.sh"

  [[ -f "${staging_dir}/${name}" ]] ||
    fail "package builder did not create ${name}"
  mv -f "${staging_dir}/${name}" "${path}"
  find "${DIST_DIR}" \
    -maxdepth 1 \
    -type f \
    -name 'atlas-spatial-runtime_*_arm64.deb' \
    ! -name "${name}" \
    -delete

  cleanup_release_staging
  trap - EXIT
  printf 'atlas spatial release: created %s\n' "${path}"
}

transfer_release() {
  local version="$1"
  local destination="$2"
  local name
  local path

  validate_version "${version}"
  [[ "${destination}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$ ]] ||
    fail "destination must be USER@HOST, got ${destination}"
  require_command scp
  name="$(package_name "${version}")"
  path="${DIST_DIR}/${name}"
  [[ -f "${path}" ]] || fail "package is missing: ${path}"

  scp "${path}" "${destination}:/tmp/"
  printf 'atlas spatial release: transferred %s to %s:/tmp/\n' \
    "${name}" "${destination}"
}

command_name="${1:-}"
case "${command_name}" in
  build)
    [[ $# -ge 2 && $# -le 3 ]] || {
      usage >&2
      exit 2
    }
    build_release "$2" "${3:-}"
    ;;
  transfer)
    [[ $# -eq 3 ]] || {
      usage >&2
      exit 2
    }
    transfer_release "$2" "$3"
    ;;
  --help|-h|help)
    usage
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
