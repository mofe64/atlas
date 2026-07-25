#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DIST_DIR="${ATLAS_RELEASE_OUTPUT_DIR:-${AGENT_DIR}/dist}"

usage() {
  cat <<'EOF'
Usage:
  packaging/release.sh build VERSION [--replace]
  packaging/release.sh transfer VERSION USER@HOST

Build creates one Linux-arm64 Atlas Agent Debian package. It does not run the
Agent or spatial test suites, build a spatial image, create checksums/manifests,
or qualify the package. Run source tests separately before building. After a
successful build it removes older Atlas Agent packages from the output
directory, leaving only the new version.

Transfer copies only the Agent Debian package to /tmp on the Pi. Use the root
scripts/transfer-onboard-release.sh helper to transfer a selected Agent/Spatial
pair. Installation and smoke checks remain explicit landed-and-disarmed
operations.

Environment:
  ATLAS_RELEASE_OUTPUT_DIR  Override the package directory (default: dist).
EOF
}

fail() {
  printf 'atlas release: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

validate_version() {
  [[ "$1" =~ ^[0-9A-Za-z.+:~_-]+$ ]] || {
    printf 'atlas release: invalid version: %s\n' "$1" >&2
    exit 2
  }
}

agent_package_name() {
  printf 'atlas-agent_%s_arm64.deb\n' "$1"
}

build_release() {
  local version="$1"
  local option="${2:-}"
  local replace=false
  local package_name
  local package_path
  local staging_dir

  validate_version "${version}"
  case "${option}" in
    "")
      ;;
    --replace)
      replace=true
      ;;
    *)
      printf 'atlas release: unknown build option: %s\n' "${option}" >&2
      exit 2
      ;;
  esac

  package_name="$(agent_package_name "${version}")"
  package_path="${DIST_DIR}/${package_name}"
  mkdir -p "${DIST_DIR}"
  if [[ -e "${package_path}" && "${replace}" != true ]]; then
    fail "${package_name} already exists; use --replace to rebuild this version"
  fi

  staging_dir="$(mktemp -d "${DIST_DIR}/.release-${version}.XXXXXX")"
  cleanup_release_staging() {
    rm -rf -- "${staging_dir}"
  }
  trap cleanup_release_staging EXIT

  printf 'atlas release: building Agent package %s\n' "${package_name}"
  ATLAS_RELEASE_VERSION="${version}" \
  ATLAS_PACKAGE_OUTPUT_DIR="${staging_dir}" \
    "${AGENT_DIR}/packaging/build-deb.sh"

  [[ -f "${staging_dir}/${package_name}" ]] ||
    fail "package builder did not create ${package_name}"
  mv -f "${staging_dir}/${package_name}" "${package_path}"
  find "${DIST_DIR}" \
    -maxdepth 1 \
    -type f \
    -name 'atlas-agent_*_arm64.deb' \
    ! -name "${package_name}" \
    -delete

  cleanup_release_staging
  trap - EXIT
  printf 'atlas release: created %s\n' "${package_path}"
}

transfer_release() {
  local version="$1"
  local destination="$2"
  local package_name
  local package_path

  validate_version "${version}"
  [[ "${destination}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$ ]] || {
    printf 'atlas release: destination must be USER@HOST, got %s\n' \
      "${destination}" >&2
    exit 2
  }
  require_command scp
  package_name="$(agent_package_name "${version}")"
  package_path="${DIST_DIR}/${package_name}"
  [[ -f "${package_path}" ]] || fail "package is missing: ${package_path}"

  scp "${package_path}" "${destination}:/tmp/"
  printf 'atlas release: transferred %s to %s:/tmp/\n' \
    "${package_name}" "${destination}"
  printf 'atlas release: install and smoke-check explicitly on the landed Pi\n'
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
