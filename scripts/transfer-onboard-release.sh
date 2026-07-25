#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

usage() {
  cat <<'EOF'
Usage:
  scripts/transfer-onboard-release.sh AGENT_VERSION SPATIAL_VERSION USER@HOST

Transfers the selected Agent and Spatial Debian packages to /tmp on one host.
Both packages must already have been built by their component release commands.
EOF
}

fail() {
  printf 'atlas onboard transfer: %s\n' "$*" >&2
  exit 1
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" || "${1:-}" == "help" ]]; then
  usage
  exit 0
fi

[[ $# -eq 3 ]] || {
  usage >&2
  exit 2
}

AGENT_VERSION="$1"
SPATIAL_VERSION="$2"
DESTINATION="$3"
AGENT_PACKAGE="${ROOT_DIR}/atlas-agent/dist/atlas-agent_${AGENT_VERSION}_arm64.deb"
SPATIAL_PACKAGE="${ROOT_DIR}/atlas-spatial-runtime/dist/atlas-spatial-runtime_${SPATIAL_VERSION}_arm64.deb"

[[ "${AGENT_VERSION}" =~ ^[0-9A-Za-z.+:~_-]+$ ]] ||
  fail "invalid Agent version: ${AGENT_VERSION}"
[[ "${SPATIAL_VERSION}" =~ ^[0-9A-Za-z.+:~_-]+$ ]] ||
  fail "invalid Spatial version: ${SPATIAL_VERSION}"
[[ "${DESTINATION}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$ ]] ||
  fail "destination must be USER@HOST, got ${DESTINATION}"
command -v scp >/dev/null 2>&1 || fail "missing required command: scp"
[[ -f "${AGENT_PACKAGE}" ]] || fail "package is missing: ${AGENT_PACKAGE}"
[[ -f "${SPATIAL_PACKAGE}" ]] || fail "package is missing: ${SPATIAL_PACKAGE}"

scp "${AGENT_PACKAGE}" "${SPATIAL_PACKAGE}" "${DESTINATION}:/tmp/"
printf 'atlas onboard transfer: transferred Agent %s and Spatial %s to %s:/tmp/\n' \
  "${AGENT_VERSION}" "${SPATIAL_VERSION}" "${DESTINATION}"
printf 'atlas onboard transfer: on the landed Pi run:\n'
printf '  sudo apt install /tmp/%s /tmp/%s\n' \
  "$(basename "${AGENT_PACKAGE}")" "$(basename "${SPATIAL_PACKAGE}")"
printf '  sudo atlas-setup\n'
