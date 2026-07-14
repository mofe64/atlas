#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_CONTEXT="$(cd "${SCRIPT_DIR}/../packaging/hailo" 2>/dev/null && pwd || true)"

DRY_RUN=0
ASSUME_YES=0
REPLACE_EXISTING=0
REBOOT_REQUIRED=0
COMMAND=install

HAILO_DRIVER_VERSION="4.20.0"
HAILO_DRIVER_PACKAGE_VERSION="4.20.0-1"
HAILO_DRIVER_SHA256="92b37ba188bdd97a672d3a6430bf3a8cc61856b79616b9677fdfd1bc25ef04ee"
HAILO_FIRMWARE_VERSION="4.20.0"
HAILO_FIRMWARE_PACKAGE_VERSION="4.20.0-1"
HAILO_FIRMWARE_SHA256="8fdcba5d10bd3861b88b750435c6aaead1151e70fbe79f699dda627957f1896c"
HAILORT_PACKAGE_VERSION="4.20.0-1"
HAILORT_PACKAGE_SHA256="12058986a3cef2d3ccae98033e2ba0f57d63bbc1cf68d3541668329ea6a49e31"
PYHAILORT_PACKAGE_VERSION="4.20.0-1"
PYHAILORT_PACKAGE_SHA256="73fe05c39cd74fd6ac345696cbe08415fd0b84828d81a202491aa5d43deff1e7"
HAILO_TAPPAS_PACKAGE_VERSION="3.31.0+1-1"
HAILO_TAPPAS_PACKAGE_SHA256="52879a3027b78960c066c87bd230a63df744ed6cffe37c8adf7361ee70fd2998"
HAILO_BASE_IMAGE="docker.io/library/debian@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df"
HAILO_CONTAINER_IMAGE_TAG="${ATLAS_HAILO_CONTAINER_IMAGE:-atlas-hailo-runtime:4.20.0-tappas-3.31.0}"
HAILO_CONTAINER_IMAGE="$HAILO_CONTAINER_IMAGE_TAG"
HAILO_CONTAINER_NAME="${ATLAS_HAILO_CONTAINER_NAME:-atlas-hailo-adapter}"
HAILO_CONTEXT_DIR="${ATLAS_HAILO_CONTAINER_CONTEXT:-/usr/share/atlas-agent/hailo-container}"
HAILO_CACHE_DIR="${ATLAS_HAILO_CACHE_DIR:-/var/cache/atlas-agent/hailo}"
HAILO_ENV_FILE="${ATLAS_HAILO_ENV_FILE:-/etc/atlas-agent/hailo-container.env}"
ARCHIVE_BASE="https://archive.raspberrypi.com/debian"
DRIVER_URL="${ARCHIVE_BASE}/pool/main/h/hailo-dkms/hailo-dkms_${HAILO_DRIVER_PACKAGE_VERSION}_all.deb"
FIRMWARE_URL="${ARCHIVE_BASE}/pool/main/h/hailofw/hailofw_${HAILO_FIRMWARE_PACKAGE_VERSION}_all.deb"
HOST_USERSPACE_PACKAGES=(hailo-all hailort python3-hailort hailo-tappas-core)
EXISTING_HOST_USERSPACE=()

usage() {
  cat <<EOF
Usage: atlas-hailo-setup [install|status] [options]

Installs the pinned Hailo host driver/firmware and builds the Atlas Hailo
userspace container for Ubuntu 24.04 arm64 on Raspberry Pi 5.

Options:
  --dry-run             Print actions without changing the computer.
  --yes                 Do not prompt before installing kernel/container components.
  --replace-existing    Replace another Hailo profile and remove host HailoRT/TAPPAS.
  --context PATH        Container build context. Default: ${HAILO_CONTEXT_DIR}
  --image IMAGE         Local image tag. Default: ${HAILO_CONTAINER_IMAGE_TAG}
  -h, --help            Show this help.

The script does not enable perception. After it completes (and after any
requested reboot), run sudo atlas-setup to select container-backed Hailo.
EOF
}

log() {
  printf '[atlas-hailo-setup] %s\n' "$*"
}

fail() {
  printf '[atlas-hailo-setup] error: %s\n' "$*" >&2
  exit 1
}

run() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+'
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

require_root() {
  if [[ "$DRY_RUN" -eq 0 && "${EUID}" -ne 0 ]]; then
    fail "run with sudo: sudo atlas-hailo-setup"
  fi
}

validate_platform() {
  local os_id=""
  local os_version=""
  if [[ -r /etc/os-release ]]; then
    os_id="$(. /etc/os-release; printf '%s' "${ID:-}")"
    os_version="$(. /etc/os-release; printf '%s' "${VERSION_ID:-}")"
  fi
  [[ "$os_id" == ubuntu && "$os_version" == 24.04 ]] || fail "supported host is Ubuntu 24.04; found ${os_id:-unknown} ${os_version:-unknown}"
  [[ "$(uname -m)" == aarch64 || "$(uname -m)" == arm64 ]] || fail "supported architecture is arm64; found $(uname -m)"
  if [[ -r /proc/device-tree/model ]]; then
    local board
    board="$(tr -d '\0' </proc/device-tree/model)"
    [[ "$board" == *"Raspberry Pi 5"* ]] || fail "supported board is Raspberry Pi 5; found ${board}"
  fi
}

installed_version() {
  local record
  record="$(dpkg-query -W -f='${Status}\t${Version}' "$1" 2>/dev/null || true)"
  if [[ "$record" == $'install ok installed\t'* ]]; then
    printf '%s' "${record#*$'\t'}"
  fi
}

check_existing_version() {
  local package="$1"
  local expected="$2"
  local actual
  actual="$(installed_version "$package")"
  if [[ -n "$actual" && "$actual" != "$expected" && "$REPLACE_EXISTING" -ne 1 ]]; then
    fail "${package} ${actual} is already installed; expected ${expected}. Reuse it if Atlas doctor passes, or rerun with --replace-existing for a controlled replacement"
  fi
  if [[ -n "$actual" && "$actual" != "$expected" ]]; then
    REBOOT_REQUIRED=1
  fi
}

check_existing_userspace() {
  local package
  local version
  for package in "${HOST_USERSPACE_PACKAGES[@]}"; do
    version="$(installed_version "$package")"
    if [[ -n "$version" ]]; then
      EXISTING_HOST_USERSPACE+=("$package")
    fi
  done
  if [[ "${#EXISTING_HOST_USERSPACE[@]}" -gt 0 && "$REPLACE_EXISTING" -ne 1 ]]; then
    fail "host Hailo userspace packages are installed (${EXISTING_HOST_USERSPACE[*]}). Keep using the native runtime if Atlas doctor passes, or rerun with --replace-existing to move HailoRT/TAPPAS into the pinned container"
  fi
}

download_verified() {
  local url="$1"
  local sha256="$2"
  local destination="$3"
  if [[ -f "$destination" ]] && printf '%s  %s\n' "$sha256" "$destination" | sha256sum -c - >/dev/null 2>&1; then
    log "using verified cache $(basename "$destination")"
    return
  fi
  run curl -fsSL "$url" -o "${destination}.tmp"
  if [[ "$DRY_RUN" -eq 0 ]]; then
    printf '%s  %s\n' "$sha256" "${destination}.tmp" | sha256sum -c - >/dev/null || fail "checksum failed for ${url}"
    mv "${destination}.tmp" "$destination"
  fi
}

prepare_driver_package() {
  local source_deb="$1"
  local patched_deb="$2"
  local workdir
  workdir="$(mktemp -d)"

  run dpkg-deb -R "$source_deb" "$workdir"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "would apply the pinned Ubuntu raspi-kernel warning patch to Hailo DKMS"
    run dpkg-deb -b "$workdir" "$patched_deb"
    rm -rf "$workdir"
    return
  fi

  local header="${workdir}/usr/src/hailo_pci-${HAILO_DRIVER_VERSION}/common/pcie_common.h"
  local kbuild="${workdir}/usr/src/hailo_pci-${HAILO_DRIVER_VERSION}/linux/pcie/Kbuild"
  [[ -f "$header" && -f "$kbuild" ]] || fail "unexpected Hailo DKMS package layout"
  if ! grep -q 'hailo_pcie_is_device_ready_for_boot' "$header"; then
    sed -i '/^bool hailo_pcie_is_firmware_loaded/i bool hailo_pcie_is_device_ready_for_boot(struct hailo_pcie_resources *resources);' "$header"
  fi
  if ! grep -q 'Wno-error=missing-prototypes' "$kbuild"; then
    sed -i '/ccflags-y[[:space:]]*+= -Werror/a ccflags-y      += -Wno-error=missing-prototypes' "$kbuild"
  fi
  dpkg-deb -b "$workdir" "$patched_deb" >/dev/null
  install -D -m 0644 "${workdir}/usr/src/hailo_pci-${HAILO_DRIVER_VERSION}/linux/pcie/51-hailo-udev.rules" /etc/udev/rules.d/51-hailo-udev.rules
  rm -rf "$workdir"
}

prepare_container_context() {
  if [[ -f "${HAILO_CONTEXT_DIR}/Dockerfile" &&
        -f "${HAILO_CONTEXT_DIR}/atlas-hailo-container-check" &&
        -f "${HAILO_CONTEXT_DIR}/atlas-hailort-adapter.py" ]]; then
    return
  fi

  local source_adapter="${SCRIPT_DIR}/atlas-hailort-adapter.py"
  [[ -f "${HAILO_CONTEXT_DIR}/Dockerfile" &&
     -f "${HAILO_CONTEXT_DIR}/atlas-hailo-container-check" &&
     -f "$source_adapter" ]] || fail "incomplete Hailo container context at ${HAILO_CONTEXT_DIR}"

  local staged_context="${HAILO_CACHE_DIR}/container-context"
  run install -d -m 0755 "$staged_context"
  run install -m 0644 "${HAILO_CONTEXT_DIR}/Dockerfile" "${staged_context}/Dockerfile"
  run install -m 0755 "${HAILO_CONTEXT_DIR}/atlas-hailo-container-check" "${staged_context}/atlas-hailo-container-check"
  run install -m 0755 "$source_adapter" "${staged_context}/atlas-hailort-adapter.py"
  HAILO_CONTEXT_DIR="$staged_context"
}

write_runtime_environment() {
  local temporary
  temporary="$(mktemp)"
  cat >"$temporary" <<EOF
# Generated by atlas-hailo-setup. Versions form one compatibility unit.
ATLAS_HAILO_CONTAINER_IMAGE="${HAILO_CONTAINER_IMAGE}"
ATLAS_HAILO_CONTAINER_IMAGE_TAG="${HAILO_CONTAINER_IMAGE_TAG}"
ATLAS_HAILO_CONTAINER_NAME="${HAILO_CONTAINER_NAME}"
ATLAS_HAILO_DRIVER_VERSION="${HAILO_DRIVER_VERSION}"
ATLAS_HAILO_DRIVER_PACKAGE_VERSION="${HAILO_DRIVER_PACKAGE_VERSION}"
ATLAS_HAILO_FIRMWARE_VERSION="${HAILO_FIRMWARE_VERSION}"
ATLAS_HAILO_FIRMWARE_PACKAGE_VERSION="${HAILO_FIRMWARE_PACKAGE_VERSION}"
ATLAS_HAILORT_PACKAGE_VERSION="${HAILORT_PACKAGE_VERSION}"
ATLAS_HAILO_TAPPAS_PACKAGE_VERSION="${HAILO_TAPPAS_PACKAGE_VERSION}"
EOF
  run install -D -m 0644 "$temporary" "$HAILO_ENV_FILE"
  rm -f "$temporary"
}

status() {
  log "host driver package: $(installed_version hailo-dkms || true)"
  log "host firmware package: $(installed_version hailofw || true)"
  if command -v modinfo >/dev/null 2>&1; then
    log "loaded driver candidate: $(modinfo -F version hailo_pci 2>/dev/null || printf missing)"
  fi
  if [[ -c /dev/hailo0 ]]; then
    log "device: /dev/hailo0 present"
  else
    log "device: /dev/hailo0 missing"
  fi
  if command -v docker >/dev/null 2>&1 && docker image inspect "$HAILO_CONTAINER_IMAGE_TAG" >/dev/null 2>&1; then
    log "container image: ${HAILO_CONTAINER_IMAGE_TAG} present"
  else
    log "container image: ${HAILO_CONTAINER_IMAGE} missing"
  fi
}

install_hailo() {
  validate_platform
  for command in apt-get install systemctl uname; do
    command -v "$command" >/dev/null 2>&1 || fail "missing required command: ${command}"
  done
  [[ -d "$HAILO_CONTEXT_DIR" ]] || {
    if [[ -n "$SOURCE_CONTEXT" && -d "$SOURCE_CONTEXT" ]]; then
      HAILO_CONTEXT_DIR="$SOURCE_CONTEXT"
    else
      fail "Hailo container context not found at ${HAILO_CONTEXT_DIR}"
    fi
  }

  check_existing_version hailo-dkms "$HAILO_DRIVER_PACKAGE_VERSION"
  check_existing_version hailofw "$HAILO_FIRMWARE_PACKAGE_VERSION"
  check_existing_userspace

  if [[ "$ASSUME_YES" -ne 1 && "$DRY_RUN" -ne 1 ]]; then
    printf 'Install pinned Hailo kernel components and container runtime'
    if [[ "${#EXISTING_HOST_USERSPACE[@]}" -gt 0 ]]; then
      printf ', removing host HailoRT/TAPPAS'
    fi
    printf '? [y/N]: '
    read -r answer
    [[ "${answer,,}" == y || "${answer,,}" == yes ]] || fail "installation cancelled"
  fi

  local headers="linux-headers-$(uname -r)"
  run apt-get update
  run apt-get install -y ca-certificates coreutils curl dkms docker.io dpkg-dev kmod sed udev "$headers"
  if [[ "$DRY_RUN" -eq 0 ]]; then
    for command in curl docker dpkg-deb modprobe sha256sum udevadm; do
      command -v "$command" >/dev/null 2>&1 || fail "dependency installation did not provide ${command}"
    done
  fi
  if [[ "${#EXISTING_HOST_USERSPACE[@]}" -gt 0 ]]; then
    run apt-get remove -y --purge "${EXISTING_HOST_USERSPACE[@]}"
  fi
  run systemctl enable --now docker.service
  run install -d -m 0755 "$HAILO_CACHE_DIR"
  prepare_container_context

  local driver_deb="${HAILO_CACHE_DIR}/hailo-dkms_${HAILO_DRIVER_PACKAGE_VERSION}_all.deb"
  local patched_driver_deb="${HAILO_CACHE_DIR}/hailo-dkms_${HAILO_DRIVER_PACKAGE_VERSION}_atlas-ubuntu_all.deb"
  local firmware_deb="${HAILO_CACHE_DIR}/hailofw_${HAILO_FIRMWARE_PACKAGE_VERSION}_all.deb"
  download_verified "$DRIVER_URL" "$HAILO_DRIVER_SHA256" "$driver_deb"
  download_verified "$FIRMWARE_URL" "$HAILO_FIRMWARE_SHA256" "$firmware_deb"
  prepare_driver_package "$driver_deb" "$patched_driver_deb"
  run apt-get install -y --allow-downgrades "$firmware_deb" "$patched_driver_deb"
  run udevadm control --reload-rules
  run udevadm trigger
  run modprobe hailo_pci

  run docker build --pull --platform linux/arm64 \
    --build-arg "BASE_IMAGE=${HAILO_BASE_IMAGE}" \
    --build-arg "HAILO_DRIVER_VERSION=${HAILO_DRIVER_VERSION}" \
    --build-arg "HAILO_FIRMWARE_VERSION=${HAILO_FIRMWARE_VERSION}" \
    --build-arg "HAILORT_VERSION=${HAILORT_PACKAGE_VERSION}" \
    --build-arg "HAILORT_SHA256=${HAILORT_PACKAGE_SHA256}" \
    --build-arg "PYHAILORT_VERSION=${PYHAILORT_PACKAGE_VERSION}" \
    --build-arg "PYHAILORT_SHA256=${PYHAILORT_PACKAGE_SHA256}" \
    --build-arg "TAPPAS_VERSION=${HAILO_TAPPAS_PACKAGE_VERSION}" \
    --build-arg "TAPPAS_SHA256=${HAILO_TAPPAS_PACKAGE_SHA256}" \
    --tag "$HAILO_CONTAINER_IMAGE_TAG" "$HAILO_CONTEXT_DIR"
  if [[ "$DRY_RUN" -eq 0 ]]; then
    HAILO_CONTAINER_IMAGE="$(docker image inspect --format '{{.Id}}' "$HAILO_CONTAINER_IMAGE_TAG")"
    [[ "$HAILO_CONTAINER_IMAGE" == sha256:* ]] || fail "could not resolve immutable container image id"
  fi
  run docker run --rm --network none --entrypoint /usr/local/bin/atlas-hailo-container-check "$HAILO_CONTAINER_IMAGE" --software-only
  write_runtime_environment
  run systemctl daemon-reload

  log "Hailo host and container runtime installed"
  if [[ "$DRY_RUN" -eq 0 && ( "$REBOOT_REQUIRED" -eq 1 || ! -c /dev/hailo0 ) ]]; then
    log "reboot required before the pinned driver, /dev/hailo0, and firmware can be verified"
    log "after reboot run: sudo atlas-setup && sudo atlas-setup doctor"
    exit 3
  fi
  log "run sudo atlas-setup to enable container-backed perception"
}

if [[ "${1:-}" == install || "${1:-}" == status ]]; then
  COMMAND="$1"
  shift
fi
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --yes) ASSUME_YES=1; shift ;;
    --replace-existing) REPLACE_EXISTING=1; shift ;;
    --context) [[ "$#" -ge 2 ]] || fail "--context requires a path"; HAILO_CONTEXT_DIR="$2"; shift 2 ;;
    --image) [[ "$#" -ge 2 ]] || fail "--image requires a value"; HAILO_CONTAINER_IMAGE_TAG="$2"; HAILO_CONTAINER_IMAGE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

case "$COMMAND" in
  install) require_root; install_hailo ;;
  status) status ;;
  *) fail "unknown command: ${COMMAND}" ;;
esac
