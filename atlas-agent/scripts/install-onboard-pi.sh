#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

DRY_RUN=0
CONFIGURE_ETH0=0
GROUND_GRPC_ADDR="${ATLAS_VEHICLE_AGENT_GRPC_ADDR:-192.168.144.50:9090}"
DRONE_ID="${ATLAS_DRONE_ID:-drone-001}"
DRONE_NAME="${ATLAS_DRONE_NAME:-Atlas Pi 5}"
VEHICLE_AGENT_ID="${ATLAS_VEHICLE_AGENT_ID:-agent-001}"
INSTALL_PREFIX="${ATLAS_ONBOARD_INSTALL_PREFIX:-/opt/atlas}"
ENV_DIR="${ATLAS_ONBOARD_ENV_DIR:-${HOME}/.config/atlas-agent}"
ENV_FILE="${ATLAS_ONBOARD_ENV_FILE:-${ENV_DIR}/onboard.env}"
LOG_DIR="${ATLAS_ONBOARD_LOG_DIR:-${HOME}/.local/state/atlas-agent/logs}"
MEDIAMTX_VERSION="${ATLAS_MEDIAMTX_VERSION:-v1.14.0}"
MEDIAMTX_ASSET_ARCH="${ATLAS_MEDIAMTX_ASSET_ARCH:-linux_arm64}"
MEDIAMTX_DIR="${ATLAS_MEDIAMTX_DIR:-${INSTALL_PREFIX}/mediamtx}"
MAVSDK_SERVER_VERSION="${ATLAS_MAVSDK_SERVER_VERSION:-v3.17.1}"
MAVSDK_SERVER_ASSET="${ATLAS_MAVSDK_SERVER_ASSET:-mavsdk_server_linux-arm64-musl}"
MAVSDK_SERVER_BIN="${ATLAS_MAVSDK_SERVER_BIN:-${INSTALL_PREFIX}/bin/mavsdk_server}"
MODEL_PATH="${ATLAS_PERCEPTION_MODEL_PATH:-${INSTALL_PREFIX}/models/yolov6n.hef}"
MODEL_PATH_EXPLICIT=0
if [[ -n "${ATLAS_PERCEPTION_MODEL_PATH:-}" ]]; then
  MODEL_PATH_EXPLICIT=1
fi
MODEL_SOURCE="${ATLAS_PERCEPTION_MODEL_SOURCE:-}"
HAILO_MODEL_CACHE_DIR="${ATLAS_HAILO_MODEL_CACHE_DIR:-${HOME}/hailo-models}"
HAILO_MODEL_DEB_URL="${ATLAS_HAILO_MODEL_DEB_URL:-${ATLAS_HAILO_RPI_ARCHIVE_BASE_URL:-https://archive.raspberrypi.com/debian}/pool/main/r/rpicam-apps/rpicam-apps-hailo-postprocess_1.9.0-1~bpo12+1_arm64.deb}"
HAILO_MODEL_DEB_SHA256="${ATLAS_HAILO_MODEL_DEB_SHA256:-a255a8fd7cb7237fcc9c3e067bda892b45db57c066456edd75f332ebe783711a}"
HAILO_MODEL_DEB_MEMBER="${ATLAS_HAILO_MODEL_DEB_MEMBER:-./usr/share/hailo-models/yolov6n_h8l.hef}"
HAILO_POSTPROCESS_SO="${ATLAS_PERCEPTION_POSTPROCESS_SO:-/usr/lib/aarch64-linux-gnu/hailo/tappas/post_processes/libyolo_hailortpp_post.so}"
HAILO_POSTPROCESS_FUNCTION="${ATLAS_PERCEPTION_POSTPROCESS_FUNCTION:-filter}"
HAILO_POSTPROCESS_CONFIG="${ATLAS_PERCEPTION_POSTPROCESS_CONFIG:-}"
VIDEO_PIPELINE_MODE="${ATLAS_VIDEO_PIPELINE_MODE:-hailo}"
A8_RTP_CODEC="${ATLAS_A8_RTP_CODEC:-auto}"
HAILO_HARDWARE="${ATLAS_HAILO_HARDWARE:-ai-kit}"
HAILO_INSTALL_MODE="${ATLAS_HAILO_INSTALL_MODE:-auto}"
HAILO_APT_PACKAGES="${ATLAS_HAILO_APT_PACKAGES:-}"
HAILO_DEB_DIR="${ATLAS_HAILO_DEB_DIR:-}"
DEFAULT_HAILO_DEB_DIR="${ATLAS_DEFAULT_HAILO_DEB_DIR:-${HOME}/hailo-debs}"
HAILO_DEB_SOURCE="${ATLAS_HAILO_DEB_SOURCE:-raspberrypi}"
HAILO_RPI_ARCHIVE_BASE_URL="${ATLAS_HAILO_RPI_ARCHIVE_BASE_URL:-https://archive.raspberrypi.com/debian}"
HAILO_RPI_SUITE="${ATLAS_HAILO_RPI_SUITE:-bookworm}"
HAILO_RPI_ARCH="${ATLAS_HAILO_RPI_ARCH:-arm64}"
HAILO_RPI_PACKAGE_SPECS=()
HAILO_REBOOT_REQUIRED=0
HAILO_VERIFY_DEFERRED=0
HAILO_REBOOT_REASON=""
MAVLINK_ROUTER_REPO="${ATLAS_MAVLINK_ROUTER_REPO:-https://github.com/mavlink-router/mavlink-router.git}"
MAVLINK_ROUTER_REF="${ATLAS_MAVLINK_ROUTER_REF:-master}"
MAVLINK_ROUTER_SOURCE_DIR="${ATLAS_MAVLINK_ROUTER_SOURCE_DIR:-${INSTALL_PREFIX}/src/mavlink-router}"
MAVLINK_ROUTER_SOURCE_MARKER="${MAVLINK_ROUTER_SOURCE_DIR}/.atlas-source-install"
MAVLINK_ROUTER_UART_DEVICE="${ATLAS_MAVLINK_ROUTER_UART_DEVICE:-/dev/serial0}"
MAVLINK_ROUTER_UART_BAUD="${ATLAS_MAVLINK_ROUTER_UART_BAUD:-921600}"
OS_RELEASE_ID="${ATLAS_ONBOARD_OS_ID:-unknown}"
OS_RELEASE_ID_LIKE="${ATLAS_ONBOARD_OS_ID_LIKE:-}"
OS_RELEASE_PRETTY_NAME="${ATLAS_ONBOARD_OS_PRETTY_NAME:-unknown}"
OS_RELEASE_VERSION_CODENAME="${ATLAS_ONBOARD_OS_VERSION_CODENAME:-}"

APT_PACKAGES=(
  curl
  git
  ca-certificates
  build-essential
  python3
  python3-venv
  python3-pip
  ffmpeg
  gstreamer1.0-tools
  gstreamer1.0-plugins-base
  gstreamer1.0-plugins-good
  gstreamer1.0-plugins-bad
  gstreamer1.0-plugins-ugly
  gstreamer1.0-libav
  gstreamer1.0-rtsp
  libgstreamer1.0-0
  libgstreamer-plugins-base1.0-0
  netcat-openbsd
  pciutils
  golang-go
)

MAVLINK_ROUTER_BUILD_PACKAGES=(
  git
  meson
  ninja-build
  pkg-config
  gcc
  g++
  systemd
)

usage() {
  cat <<EOF
Usage: scripts/install-onboard-pi.sh [options]

Installs/configures the Atlas onboard Raspberry Pi stack.

Options:
  --dry-run                 Print commands/files without changing the system.
  --configure-eth0          Write local-only static eth0 netplan config.
  --ground-grpc ADDR        Backend vehicle-agent gRPC address. Default: ${GROUND_GRPC_ADDR}
  --drone-id ID             Drone id. Default: ${DRONE_ID}
  --drone-name NAME         Drone display name. Default: ${DRONE_NAME}
  --vehicle-agent-id ID     Vehicle agent id. Default: ${VEHICLE_AGENT_ID}
  --install-prefix PATH     Install prefix. Default: ${INSTALL_PREFIX}
  --env-file PATH           Env file path. Default: ${ENV_FILE}
  --mavsdk-version VERSION  MAVSDK server release tag. Default: ${MAVSDK_SERVER_VERSION}
  --model-path PATH         Hailo HEF model path used by the video pipeline. Default: ${MODEL_PATH}
  --model-source PATH       Copy a local Hailo HEF model into --model-path instead of auto-downloading.
  --video-pipeline-mode MODE
                            Video pipeline mode: hailo or passthrough. Default: ${VIDEO_PIPELINE_MODE}
  --a8-rtp-codec CODEC      A8 RTSP RTP codec: auto, h264, or h265. Default: ${A8_RTP_CODEC}
  --hailo-hardware TYPE     Hailo hardware package family: ai-kit, ai-hat-plus, ai-hat-plus-2, or none.
                            Default: ${HAILO_HARDWARE}
  --hailo-install MODE      Hailo package install mode: auto, always, or never.
                            auto installs only when --video-pipeline-mode=hailo. Default: ${HAILO_INSTALL_MODE}
  --hailo-apt-packages LIST Exact Hailo apt packages to install, overriding OS defaults.
                            Default: auto-detect package names from the OS.
  --hailo-deb-dir PATH     Override local Ubuntu-compatible Hailo .deb package directory.
                            Ubuntu default when --hailo-apt-packages is not set: ${DEFAULT_HAILO_DEB_DIR}
  --hailo-deb-source SRC   Source used to populate --hailo-deb-dir on Ubuntu: raspberrypi or none.
                            Default: ${HAILO_DEB_SOURCE}
  --hailo-rpi-suite SUITE  Raspberry Pi archive suite for Hailo deb downloads. Default: ${HAILO_RPI_SUITE}
                            Ubuntu 24.04 should use bookworm; trixie requires newer Python/OpenCV/libc.
  --mavlink-device PATH     Pixhawk serial device. Default: ${MAVLINK_ROUTER_UART_DEVICE}
  --mavlink-baud RATE       Pixhawk serial baud. Default: ${MAVLINK_ROUTER_UART_BAUD}
  --mavlink-router-ref REF  Source ref used if mavlink-router apt package is unavailable. Default: ${MAVLINK_ROUTER_REF}
  -h, --help                Show this help.
EOF
}

log() {
  printf '[atlas-onboard-install] %s\n' "$*"
}

fail() {
  printf '[atlas-onboard-install] error: %s\n' "$*" >&2
  exit 1
}

require_value() {
  local option="$1"
  local value="${2:-}"
  if [[ -z "$value" || "$value" == --* ]]; then
    fail "${option} requires a value"
  fi
}

validate_video_config() {
  case "$VIDEO_PIPELINE_MODE" in
    hailo|passthrough)
      ;;
    *)
      fail "--video-pipeline-mode must be one of: hailo, passthrough"
      ;;
  esac

  case "$A8_RTP_CODEC" in
    auto|h264|h265)
      ;;
    *)
      fail "--a8-rtp-codec must be one of: auto, h264, h265"
      ;;
  esac

  case "$HAILO_HARDWARE" in
    ai-kit|ai-hat-plus|ai-hat-plus-2|none)
      ;;
    *)
      fail "--hailo-hardware must be one of: ai-kit, ai-hat-plus, ai-hat-plus-2, none"
      ;;
  esac

  case "$HAILO_INSTALL_MODE" in
    auto|always|never)
      ;;
    *)
      fail "--hailo-install must be one of: auto, always, never"
      ;;
  esac

  case "$HAILO_DEB_SOURCE" in
    raspberrypi|none)
      ;;
    *)
      fail "--hailo-deb-source must be one of: raspberrypi, none"
      ;;
  esac

  if [[ "$VIDEO_PIPELINE_MODE" == "hailo" && "$HAILO_HARDWARE" == "none" ]]; then
    fail "--video-pipeline-mode=hailo requires --hailo-hardware to be ai-kit, ai-hat-plus, or ai-hat-plus-2"
  fi

}

run() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ %s\n' "$*"
    return
  fi
  "$@"
}

run_shell() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ %s\n' "$*"
    return
  fi
  bash -lc "$*"
}

write_file() {
  local path="$1"
  local mode="$2"
  local content="$3"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf -- '--- %s (%s) ---\n%s\n' "$path" "$mode" "$content"
    return
  fi

  if [[ "$path" == /etc/* || "$path" == /opt/* ]]; then
    printf '%s\n' "$content" | sudo tee "$path" >/dev/null
    sudo chmod "$mode" "$path"
  else
    mkdir -p "$(dirname "$path")"
    printf '%s\n' "$content" >"$path"
    chmod "$mode" "$path"
  fi
}

load_os_release() {
  if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    source /etc/os-release
    OS_RELEASE_ID="${ATLAS_ONBOARD_OS_ID:-${ID:-unknown}}"
    OS_RELEASE_ID_LIKE="${ATLAS_ONBOARD_OS_ID_LIKE:-${ID_LIKE:-}}"
    OS_RELEASE_PRETTY_NAME="${ATLAS_ONBOARD_OS_PRETTY_NAME:-${PRETTY_NAME:-unknown}}"
    OS_RELEASE_VERSION_CODENAME="${ATLAS_ONBOARD_OS_VERSION_CODENAME:-${VERSION_CODENAME:-}}"
  fi
}

is_raspberry_pi_os() {
  [[ -r /etc/rpi-issue ]] && return 0
  [[ "${OS_RELEASE_ID}" == "raspbian" ]] && return 0
  case "$OS_RELEASE_PRETTY_NAME" in
    *"Raspberry Pi OS"*|*"raspberry pi os"*)
      return 0
      ;;
  esac
  return 1
}

is_ubuntu() {
  [[ "${OS_RELEASE_ID}" == "ubuntu" ]]
}

detect_platform() {
  load_os_release
  log "platform: $(uname -a)"
  log "os: ${OS_RELEASE_PRETTY_NAME}"
  if [[ "$(uname -m)" != "aarch64" && "$(uname -m)" != "arm64" ]]; then
    log "warning: expected arm64/aarch64 Raspberry Pi OS, got $(uname -m)"
  fi
  if [[ -r /proc/device-tree/model ]]; then
    local model
    model="$(tr -d '\0' </proc/device-tree/model)"
    log "board: ${model}"
    if [[ "$model" != *"Raspberry Pi 5"* ]]; then
      log "warning: expected Raspberry Pi 5 for AI HAT/Hailo MVP"
    fi
  else
    log "warning: /proc/device-tree/model not readable; cannot confirm Pi 5"
  fi
}

install_apt_packages() {
  log "installing apt packages"
  run sudo apt-get update
  run sudo apt-get install -y "${APT_PACKAGES[@]}"
}

recover_hailo_dpkg_state() {
  if [[ "$HAILO_INSTALL_MODE" == "auto" && "$VIDEO_PIPELINE_MODE" != "hailo" ]]; then
    return
  fi

  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ patch any already-unpacked /usr/src/hailo_pci-* DKMS source\n'
    printf '+ if hailo-dkms is half-configured, sudo dpkg --configure -a\n'
    return
  fi

  local hailo_dkms_status=""
  hailo_dkms_status="$(dpkg-query -W -f='${db:Status-Abbrev}' hailo-dkms 2>/dev/null || true)"
  if [[ -z "$hailo_dkms_status" ]]; then
    return
  fi

  patch_installed_hailo_dkms_sources
  if [[ "$hailo_dkms_status" == *F* || "$hailo_dkms_status" == *H* || "$hailo_dkms_status" == *U* ]]; then
    log "detected incomplete hailo-dkms package state (${hailo_dkms_status}); retrying dpkg configuration after DKMS source patch"
    if ! sudo dpkg --configure -a; then
      log "hailo-dkms recovery failed; collecting DKMS diagnostics"
      collect_hailo_dkms_diagnostics
      fail "hailo-dkms is still not configured after applying the Ubuntu kernel build patch"
    fi
    mark_hailo_reboot_required "hailo-dkms was configured during this installer run"
  fi
}

verify_gstreamer_elements() {
  log "verifying GStreamer video elements"
  local required_elements=(
    rtspsrc
    rtspclientsink
    decodebin
    rtph264depay
    h264parse
    avdec_h264
    videoconvert
    videoscale
    x264enc
  )

  if [[ "$A8_RTP_CODEC" == "auto" || "$A8_RTP_CODEC" == "h265" ]]; then
    required_elements+=(
      rtph265depay
      h265parse
      avdec_h265
    )
  fi

  if [[ "$DRY_RUN" -eq 1 ]]; then
    for element in "${required_elements[@]}"; do
      printf '+ gst-inspect-1.0 %s\n' "$element"
    done
    if [[ "$VIDEO_PIPELINE_MODE" == "hailo" ]]; then
      printf '+ gst-inspect-1.0 hailonet\n'
      printf '+ gst-inspect-1.0 hailofilter\n'
      printf '+ gst-inspect-1.0 hailooverlay\n'
    fi
    return
  fi

  local missing_elements=()
  for element in "${required_elements[@]}"; do
    if ! gst-inspect-1.0 "$element" >/dev/null 2>&1; then
      missing_elements+=("$element")
    fi
  done

  if [[ "${#missing_elements[@]}" -gt 0 ]]; then
    fail "missing required GStreamer elements: ${missing_elements[*]}"
  fi

  if [[ "$VIDEO_PIPELINE_MODE" == "hailo" ]]; then
    local missing_hailo_elements=()
    for element in hailonet hailofilter hailooverlay; do
      if ! gst-inspect-1.0 "$element" >/dev/null 2>&1; then
        missing_hailo_elements+=("$element")
      fi
    done
    if [[ "${#missing_hailo_elements[@]}" -gt 0 ]]; then
      fail "missing Hailo GStreamer elements: ${missing_hailo_elements[*]}; install Hailo runtime packages, then rerun the installer"
    fi
  fi
}

install_default_hailo_model() {
  local deb_name
  deb_name="$(basename "$HAILO_MODEL_DEB_URL")"
  local deb_path="${HAILO_MODEL_CACHE_DIR}/${deb_name}"
  local workdir=""
  local extracted_model=""

  command -v ar >/dev/null 2>&1 || fail "ar is required to extract the Hailo model package; install binutils/build-essential and rerun"
  command -v tar >/dev/null 2>&1 || fail "tar is required to extract the Hailo model package"
  if [[ -n "$HAILO_MODEL_DEB_SHA256" ]]; then
    command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required to verify the Hailo model package"
  fi

  run mkdir -p "$HAILO_MODEL_CACHE_DIR"
  if [[ -f "$deb_path" ]]; then
    log "Hailo model package already downloaded: ${deb_path}"
  else
    log "downloading Hailo YOLOv6n model package"
    run curl -fL "$HAILO_MODEL_DEB_URL" -o "$deb_path"
  fi

  if [[ -n "$HAILO_MODEL_DEB_SHA256" ]]; then
    printf '%s  %s\n' "$HAILO_MODEL_DEB_SHA256" "$deb_path" | sha256sum -c - >/dev/null || fail "checksum verification failed for ${deb_path}"
  fi

  workdir="$(mktemp -d /tmp/atlas-hailo-model.XXXXXX)"
  run_shell "cd '${workdir}' && ar -x '${deb_path}' data.tar.gz"
  run tar -xzf "${workdir}/data.tar.gz" -C "$workdir" "$HAILO_MODEL_DEB_MEMBER"

  extracted_model="${workdir}/${HAILO_MODEL_DEB_MEMBER#./}"
  [[ -s "$extracted_model" ]] || fail "Hailo model package did not contain a non-empty model at ${HAILO_MODEL_DEB_MEMBER}"

  run sudo mkdir -p "$(dirname "$MODEL_PATH")"
  run sudo install -m 0644 "$extracted_model" "$MODEL_PATH"
  run rm -rf "$workdir"
}

verify_hailo_model_file() {
  [[ -f "$MODEL_PATH" ]] || fail "Hailo model is missing: ${MODEL_PATH}"
  [[ -s "$MODEL_PATH" ]] || fail "Hailo model is empty: ${MODEL_PATH}"
  case "$MODEL_PATH" in
    *.hef)
      ;;
    *)
      log "warning: Hailo model path does not use the .hef extension: ${MODEL_PATH}"
      ;;
  esac

  if command -v hailortcli >/dev/null 2>&1 && hailortcli --help 2>/dev/null | grep -q 'parse-hef'; then
    hailortcli parse-hef "$MODEL_PATH" >/dev/null || fail "HailoRT could not parse HEF model: ${MODEL_PATH}"
  fi
}

install_hailo_model() {
  if [[ "$VIDEO_PIPELINE_MODE" != "hailo" ]]; then
    log "skipping Hailo model setup because video pipeline mode is ${VIDEO_PIPELINE_MODE}"
    return
  fi

  log "verifying Hailo model"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    if [[ -n "$MODEL_SOURCE" ]]; then
      printf '+ test -f %q\n' "$MODEL_SOURCE"
      printf '+ sudo mkdir -p %q\n' "$(dirname "$MODEL_PATH")"
      printf '+ sudo install -m 0644 %q %q\n' "$MODEL_SOURCE" "$MODEL_PATH"
    else
      printf '+ if %q is missing, mkdir -p %q\n' "$MODEL_PATH" "$HAILO_MODEL_CACHE_DIR"
      printf '+ if %q is missing, curl -fL %q -o %q\n' "$MODEL_PATH" "$HAILO_MODEL_DEB_URL" "${HAILO_MODEL_CACHE_DIR}/$(basename "$HAILO_MODEL_DEB_URL")"
      printf '+ verify SHA-256 for %q\n' "${HAILO_MODEL_CACHE_DIR}/$(basename "$HAILO_MODEL_DEB_URL")"
      printf '+ extract %q from model package\n' "$HAILO_MODEL_DEB_MEMBER"
      printf '+ sudo mkdir -p %q\n' "$(dirname "$MODEL_PATH")"
      printf '+ sudo install -m 0644 <extracted HEF> %q\n' "$MODEL_PATH"
    fi
    printf '+ test -s %q\n' "$MODEL_PATH"
    printf '+ hailortcli parse-hef %q || true\n' "$MODEL_PATH"
    return
  fi

  if [[ -n "$MODEL_SOURCE" ]]; then
    [[ -f "$MODEL_SOURCE" ]] || fail "Hailo model source not found: ${MODEL_SOURCE}"
    [[ -s "$MODEL_SOURCE" ]] || fail "Hailo model source is empty: ${MODEL_SOURCE}"
    case "$MODEL_SOURCE" in
      *.hef)
        ;;
      *)
        log "warning: Hailo model source does not use the .hef extension: ${MODEL_SOURCE}"
        ;;
    esac

    local source_real=""
    local target_real=""
    source_real="$(readlink -f "$MODEL_SOURCE" 2>/dev/null || true)"
    if [[ -e "$MODEL_PATH" ]]; then
      target_real="$(readlink -f "$MODEL_PATH" 2>/dev/null || true)"
    fi

    if [[ -n "$source_real" && -n "$target_real" && "$source_real" == "$target_real" ]]; then
      log "Hailo model source is already installed at ${MODEL_PATH}"
    else
      run sudo mkdir -p "$(dirname "$MODEL_PATH")"
      run sudo install -m 0644 "$MODEL_SOURCE" "$MODEL_PATH"
    fi
  elif [[ -s "$MODEL_PATH" ]]; then
    log "Hailo model already available: ${MODEL_PATH}"
  else
    install_default_hailo_model
  fi

  verify_hailo_model_file
  log "Hailo model available: ${MODEL_PATH}"
}

verify_hailo_postprocess() {
  if [[ "$VIDEO_PIPELINE_MODE" != "hailo" ]]; then
    log "skipping Hailo postprocess verification because video pipeline mode is ${VIDEO_PIPELINE_MODE}"
    return
  fi

  log "verifying Hailo YOLO postprocess"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ test -s %q\n' "$HAILO_POSTPROCESS_SO"
    printf '+ verify %q uses function %q\n' "$HAILO_POSTPROCESS_SO" "$HAILO_POSTPROCESS_FUNCTION"
    if [[ -n "$HAILO_POSTPROCESS_CONFIG" ]]; then
      printf '+ test -s %q\n' "$HAILO_POSTPROCESS_CONFIG"
    fi
    return
  fi

  [[ -f "$HAILO_POSTPROCESS_SO" ]] || fail "Hailo YOLO postprocess library is missing: ${HAILO_POSTPROCESS_SO}"
  [[ -s "$HAILO_POSTPROCESS_SO" ]] || fail "Hailo YOLO postprocess library is empty: ${HAILO_POSTPROCESS_SO}"
  if [[ -n "$HAILO_POSTPROCESS_CONFIG" ]]; then
    [[ -f "$HAILO_POSTPROCESS_CONFIG" ]] || fail "Hailo YOLO postprocess config is missing: ${HAILO_POSTPROCESS_CONFIG}"
    [[ -s "$HAILO_POSTPROCESS_CONFIG" ]] || fail "Hailo YOLO postprocess config is empty: ${HAILO_POSTPROCESS_CONFIG}"
  fi
  log "Hailo YOLO postprocess available: ${HAILO_POSTPROCESS_SO} (${HAILO_POSTPROCESS_FUNCTION})"
}

install_hailo_packages() {
  if [[ "$HAILO_INSTALL_MODE" == "never" ]]; then
    log "skipping Hailo package install (--hailo-install=never)"
    return
  fi

  if [[ "$HAILO_INSTALL_MODE" == "auto" && "$VIDEO_PIPELINE_MODE" != "hailo" ]]; then
    log "skipping Hailo package install because video pipeline mode is ${VIDEO_PIPELINE_MODE}"
    return
  fi

  if [[ "$HAILO_HARDWARE" == "none" ]]; then
    log "skipping Hailo package install because Hailo hardware is none"
    return
  fi

  if [[ -n "$HAILO_DEB_DIR" ]]; then
    install_hailo_deb_packages
    return
  fi

  local candidate_sets=()
  if [[ -n "$HAILO_APT_PACKAGES" ]]; then
    candidate_sets=("$HAILO_APT_PACKAGES")
  elif is_ubuntu; then
    HAILO_DEB_DIR="$DEFAULT_HAILO_DEB_DIR"
    install_hailo_deb_packages
    return
  elif is_raspberry_pi_os; then
    case "$HAILO_HARDWARE" in
      ai-kit|ai-hat-plus)
        candidate_sets=(
          "dkms hailo-all"
          "dkms hailo-dkms hailort hailo-tappas-core"
        )
        ;;
      ai-hat-plus-2)
        candidate_sets=("dkms hailo-h10-all")
        ;;
    esac
  elif [[ "$DRY_RUN" -eq 1 ]]; then
    log "dry-run: Hailo apt package selection depends on target /etc/os-release"
    printf '+ apt-cache policy hailo-all hailo-h10-all hailo-dkms hailort hailo-tappas-core hailort-pcie-driver-dkms\n'
    return
  else
    fail "unsupported OS for automatic Hailo package selection: ${OS_RELEASE_PRETTY_NAME}. Set ATLAS_HAILO_APT_PACKAGES with compatible Hailo package names or pass --hailo-deb-dir with matching arm64 Hailo .deb packages"
  fi

  log "checking Hailo apt packages for ${HAILO_HARDWARE} on ${OS_RELEASE_PRETTY_NAME}"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    local dry_run_packages=()
    read -r -a dry_run_packages <<< "${candidate_sets[0]}"
    printf '+ sudo apt-get install -y'
    printf ' %q' "${dry_run_packages[@]}"
    printf '\n'
    return
  fi

  local package_set
  local checked_sets=()
  for package_set in "${candidate_sets[@]}"; do
    local candidate_packages=()
    local missing_packages=()
    read -r -a candidate_packages <<< "$package_set"
    checked_sets+=("$package_set")
    for package_name in "${candidate_packages[@]}"; do
      if ! apt-cache show "$package_name" >/dev/null 2>&1; then
        missing_packages+=("$package_name")
      fi
    done
    if [[ "${#missing_packages[@]}" -eq 0 ]]; then
      run sudo apt-get install -y "${candidate_packages[@]}"
      if apt-cache show python3-hailort >/dev/null 2>&1; then
        run sudo apt-get install -y python3-hailort
      fi
      if hailo_driver_module_newer_than_boot; then
        mark_hailo_reboot_required "the Hailo PCIe kernel module was installed during this boot"
      fi
      return
    fi
  done

  local message="no complete Hailo apt package set is available in configured apt sources"
  if [[ "$VIDEO_PIPELINE_MODE" == "hailo" || "$HAILO_INSTALL_MODE" == "always" ]]; then
    fail "${message}. OS: ${OS_RELEASE_PRETTY_NAME}. Checked: ${checked_sets[*]}. Configure Hailo's Ubuntu-compatible apt source, set ATLAS_HAILO_APT_PACKAGES, or pass --hailo-deb-dir with matching Ubuntu arm64 Hailo .deb packages"
  fi
  log "warning: ${message}"
}

install_hailo_deb_packages() {
  log "installing Hailo local .deb packages from ${HAILO_DEB_DIR}"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ mkdir -p %q\n' "$HAILO_DEB_DIR"
    if [[ "$HAILO_DEB_SOURCE" == "raspberrypi" ]]; then
      select_hailo_raspberrypi_package_specs
      printf '+ curl -fsSL %q/dists/%q/main/binary-%q/Packages.gz | gzip -dc > /tmp/atlas-hailo-packages\n' "$HAILO_RPI_ARCHIVE_BASE_URL" "$HAILO_RPI_SUITE" "$HAILO_RPI_ARCH"
      local dry_run_package_spec
      for dry_run_package_spec in "${HAILO_RPI_PACKAGE_SPECS[@]}"; do
        printf '+ download Hailo package %q from %q into %q\n' "$dry_run_package_spec" "$HAILO_RPI_SUITE" "$HAILO_DEB_DIR"
      done
    fi
    install_dkms_support_packages
    if [[ "$HAILO_DEB_SOURCE" == "raspberrypi" ]]; then
      printf '+ sudo apt-get install -y <downloaded Hailo .deb files>\n'
    else
      printf '+ sudo apt-get install -y %q/*.deb\n' "$HAILO_DEB_DIR"
    fi
    printf '+ patch Hailo DKMS source for Ubuntu Raspberry Pi kernel warning handling\n'
    printf '+ chmod 0755 %q\n' "$HAILO_DEB_DIR"
    printf '+ chmod 0644 <selected Hailo .deb files>\n'
    printf '+ sudo ldconfig\n'
    return
  fi

  run mkdir -p "$HAILO_DEB_DIR"

  local deb_packages=()
  if [[ "$HAILO_DEB_SOURCE" == "raspberrypi" ]]; then
    download_hailo_raspberrypi_debs deb_packages
  fi

  if [[ "${#deb_packages[@]}" -eq 0 ]]; then
    while IFS= read -r -d '' deb_package; do
      deb_packages+=("$deb_package")
    done < <(find "$HAILO_DEB_DIR" -maxdepth 1 -type f -name '*.deb' -print0 | sort -z)
  fi

  if [[ "${#deb_packages[@]}" -eq 0 ]]; then
    fail "no Hailo .deb packages found in ${HAILO_DEB_DIR}. Required package family: Hailo driver, firmware, HailoRT, and Hailo TAPPAS/GStreamer plugins"
  fi

  prepare_hailo_dkms_deb_packages deb_packages
  patch_installed_hailo_dkms_sources
  run chmod 0755 "$HAILO_DEB_DIR"
  run chmod 0644 "${deb_packages[@]}"
  install_dkms_support_packages
  install_hailo_selected_debs "${deb_packages[@]}"
  run sudo ldconfig
}

select_hailo_raspberrypi_package_specs() {
  HAILO_RPI_PACKAGE_SPECS=()
  case "$HAILO_RPI_SUITE" in
    trixie|forky)
      if is_ubuntu; then
        fail "Raspberry Pi ${HAILO_RPI_SUITE} Hailo packages require newer Python/OpenCV/libc than Ubuntu 24.04 provides. Use --hailo-rpi-suite bookworm for this Ubuntu Pi."
      fi
      case "$HAILO_HARDWARE" in
        ai-kit|ai-hat-plus)
          HAILO_RPI_PACKAGE_SPECS=(hailo-all hailort-pcie-driver hailort python3-hailort hailo-tappas-core hailo-models)
          ;;
        ai-hat-plus-2)
          HAILO_RPI_PACKAGE_SPECS=(hailo-h10-all hailort-pcie-driver hailort python3-hailort hailo-tappas-core hailo-models)
          ;;
      esac
      ;;
    bookworm|bullseye)
      case "$HAILO_HARDWARE" in
        ai-kit|ai-hat-plus)
          HAILO_RPI_PACKAGE_SPECS=(
            hailofw=4.19.0-2
            hailo-dkms=4.18.0-2
            hailort=4.18.0
            hailo-tappas-core=3.29.1
          )
          ;;
        ai-hat-plus-2)
          fail "AI HAT+ 2 uses the Hailo-10 package family, which is not available in the Ubuntu-compatible bookworm package set. Use AI HAT+ Hailo-8 hardware or provide a matching package set with --hailo-deb-source none --hailo-deb-dir."
          ;;
      esac
      ;;
    *)
      fail "unsupported Raspberry Pi Hailo package suite: ${HAILO_RPI_SUITE}"
      ;;
  esac
}

install_dkms_support_packages() {
  local headers_package="linux-headers-$(uname -r)"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ sudo apt-get install -y dkms\n'
    printf '+ apt-cache show %q >/dev/null 2>&1 && sudo apt-get install -y %q || true\n' "$headers_package" "$headers_package"
    return
  fi

  run sudo apt-get install -y dkms
  if apt-cache show "$headers_package" >/dev/null 2>&1; then
    run sudo apt-get install -y "$headers_package"
  else
    log "warning: ${headers_package} is not available; Hailo DKMS driver build may fail without matching kernel headers"
  fi
}

install_hailo_selected_debs() {
  if sudo apt-get install -y "$@"; then
    if hailo_driver_module_newer_than_boot; then
      mark_hailo_reboot_required "the Hailo PCIe kernel module was installed during this boot"
    fi
    return
  fi

  log "Hailo package install failed; patching any unpacked DKMS source and retrying package configuration"
  patch_installed_hailo_dkms_sources
  if sudo dpkg --configure -a && sudo apt-get install -y "$@"; then
    if hailo_driver_module_newer_than_boot; then
      mark_hailo_reboot_required "the Hailo PCIe kernel module was installed during this boot"
    else
      mark_hailo_reboot_required "Hailo packages were configured during this installer run"
    fi
    return
  fi

  log "Hailo package install failed; collecting DKMS diagnostics"
  collect_hailo_dkms_diagnostics
  fail "Hailo package install failed. Read the DKMS make.log above; the usual causes are missing matching kernel headers or Hailo DKMS driver incompatibility with the running Ubuntu Raspberry Pi kernel."
}

collect_hailo_dkms_diagnostics() {
  run_shell "uname -a"
  run_shell "dpkg -l 'linux-headers-*' | grep '^ii' || true"
  run_shell "dkms status || true"
  run_shell "find /var/lib/dkms -path '*/hailo*/build/make.log' -print -exec tail -n 160 {} \\; || true"
}

hailo_runtime_required() {
  [[ "$VIDEO_PIPELINE_MODE" == "hailo" || "$HAILO_INSTALL_MODE" == "always" ]]
}

mark_hailo_reboot_required() {
  HAILO_REBOOT_REQUIRED=1
  if [[ -n "${1:-}" ]]; then
    HAILO_REBOOT_REASON="$1"
  fi
}

hailo_driver_module_path() {
  local module_path=""
  module_path="$(modinfo -n hailo_pci 2>/dev/null || true)"
  if [[ -n "$module_path" && "$module_path" != "(builtin)" ]]; then
    printf '%s\n' "$module_path"
    return
  fi

  find "/lib/modules/$(uname -r)" -type f \( -name 'hailo_pci.ko' -o -name 'hailo_pci.ko.*' \) -print -quit 2>/dev/null || true
}

hailo_driver_module_newer_than_boot() {
  [[ -e /proc/1 ]] || return 1

  local module_path
  module_path="$(hailo_driver_module_path)"
  [[ -n "$module_path" && -e "$module_path" ]] || return 1
  [[ "$module_path" -nt /proc/1 ]]
}

load_hailo_driver_module() {
  if lsmod | awk '{print $1}' | grep -qx 'hailo_pci'; then
    return 0
  fi

  if ! command -v modprobe >/dev/null 2>&1; then
    log "warning: modprobe not found; cannot load hailo_pci before verification"
    return 1
  fi

  log "loading Hailo PCIe kernel module"
  if sudo modprobe hailo_pci; then
    return 0
  fi

  log "warning: sudo modprobe hailo_pci failed; continuing to HailoRT diagnostics"
  return 1
}

collect_hailo_runtime_diagnostics() {
  log "collecting Hailo runtime diagnostics"
  run_shell "lsmod | grep -E '(^hailo|hailo_pci)' || true"
  run_shell "modinfo hailo_pci 2>/dev/null | sed -n '1,80p' || true"
  run_shell "find /sys/class -maxdepth 2 -iname '*hailo*' -print || true"
  run_shell "command -v lspci >/dev/null 2>&1 && lspci -nn | grep -i -E 'hailo|1e60' || true"
  run_shell "dmesg | grep -i -E 'hailo|pcie|pci' | tail -n 120 || true"
}

prepare_hailo_dkms_deb_packages() {
  local -n packages_ref="$1"
  local index

  for index in "${!packages_ref[@]}"; do
    case "$(basename "${packages_ref[$index]}")" in
      *.atlas-patched.deb)
        ;;
      hailo-dkms_*.deb)
        local patched_deb
        patch_hailo_dkms_deb_package "${packages_ref[$index]}" patched_deb
        packages_ref[$index]="$patched_deb"
        ;;
    esac
  done
}

patch_hailo_dkms_deb_package() {
  local deb_path="$1"
  local -n patched_deb_ref="$2"
  patched_deb_ref="$deb_path"

  if ! command -v dpkg-deb >/dev/null 2>&1; then
    log "warning: dpkg-deb not found; will patch Hailo DKMS source after package unpack if needed"
    return
  fi

  local deb_name
  deb_name="$(basename "$deb_path")"
  local package_version="${deb_name#hailo-dkms_}"
  package_version="${package_version%_all.deb}"
  local driver_version="${package_version%%-*}"
  local patched_deb="${deb_path%.deb}.atlas-patched.deb"

  if [[ -f "$patched_deb" && "$patched_deb" -nt "$deb_path" ]]; then
    patched_deb_ref="$patched_deb"
    return
  fi

  local workdir
  workdir="$(mktemp -d /tmp/atlas-hailo-dkms-deb.XXXXXX)"
  log "patching Hailo DKMS package for Ubuntu kernel build: ${deb_name}"
  dpkg-deb -R "$deb_path" "$workdir"
  patch_hailo_dkms_source_tree "${workdir}/usr/src/hailo_pci-${driver_version}" user
  dpkg-deb -b "$workdir" "$patched_deb" >/dev/null
  chmod 0644 "$patched_deb"
  patched_deb_ref="$patched_deb"
}

patch_installed_hailo_dkms_sources() {
  local source_dir
  for source_dir in /usr/src/hailo_pci-*; do
    [[ -d "$source_dir" ]] || continue
    patch_hailo_dkms_source_tree "$source_dir" sudo || true
  done
}

patch_hailo_dkms_source_tree() {
  local source_dir="$1"
  local mode="${2:-user}"
  local header="${source_dir}/common/pcie_common.h"
  local kbuild="${source_dir}/linux/pcie/Kbuild"
  local prototype='bool hailo_pcie_is_device_ready_for_boot(struct hailo_pcie_resources *resources);'

  if [[ ! -f "$header" ]]; then
    log "warning: Hailo DKMS header not found: ${header}"
    return 1
  fi

  if ! grep -q 'hailo_pcie_is_device_ready_for_boot' "$header"; then
    log "patching Hailo DKMS missing prototype in ${header}"
    if [[ "$mode" == "sudo" ]]; then
      sudo sed -i "/^bool hailo_pcie_is_firmware_loaded/i ${prototype}" "$header"
    else
      sed -i "/^bool hailo_pcie_is_firmware_loaded/i ${prototype}" "$header"
    fi
  fi

  if [[ -f "$kbuild" ]] && ! grep -q 'Wno-error=missing-prototypes' "$kbuild"; then
    log "patching Hailo DKMS Kbuild missing-prototypes warning policy in ${kbuild}"
    if [[ "$mode" == "sudo" ]]; then
      sudo sed -i '/ccflags-y[[:space:]]*+= -Werror/a ccflags-y      += -Wno-error=missing-prototypes' "$kbuild"
    else
      sed -i '/ccflags-y[[:space:]]*+= -Werror/a ccflags-y      += -Wno-error=missing-prototypes' "$kbuild"
    fi
  fi
}

download_hailo_raspberrypi_debs() {
  local -n output_packages="$1"
  local packages_index="/tmp/atlas-hailo-${HAILO_RPI_SUITE}-${HAILO_RPI_ARCH}-Packages"
  local packages_url="${HAILO_RPI_ARCHIVE_BASE_URL}/dists/${HAILO_RPI_SUITE}/main/binary-${HAILO_RPI_ARCH}/Packages.gz"
  select_hailo_raspberrypi_package_specs

  log "downloading Hailo package index from ${packages_url}"
  run_shell "curl -fsSL '${packages_url}' | gzip -dc > '${packages_index}'"

  local package_spec
  for package_spec in "${HAILO_RPI_PACKAGE_SPECS[@]}"; do
    local package_name="$package_spec"
    local package_version=""
    if [[ "$package_spec" == *=* ]]; then
      package_name="${package_spec%%=*}"
      package_version="${package_spec#*=}"
    fi

    local package_record
    package_record="$(deb_record_from_index "$packages_index" "$package_name" "$package_version")"
    if [[ -z "$package_record" ]]; then
      if [[ -n "$package_version" ]]; then
        fail "package ${package_name}=${package_version} was not found in ${packages_url}"
      fi
      fail "package ${package_name} was not found in ${packages_url}"
    fi

    local version="${package_record%%	*}"
    local filename="${package_record#*	}"
    local deb_url="${HAILO_RPI_ARCHIVE_BASE_URL}/${filename}"
    local deb_path="${HAILO_DEB_DIR}/$(basename "$filename")"

    if [[ -f "$deb_path" ]]; then
      log "Hailo package already downloaded: $(basename "$deb_path")"
    else
      log "downloading ${package_name} ${version}"
      run curl -fL "$deb_url" -o "$deb_path"
    fi
    run chmod 0644 "$deb_path"
    output_packages+=("$deb_path")
  done
}

deb_record_from_index() {
  local packages_index="$1"
  local package_name="$2"
  local requested_version="${3:-}"
  local best_version=""
  local best_filename=""
  local version
  local filename

  while IFS=$'\t' read -r version filename; do
    if [[ -n "$requested_version" && "$version" != "$requested_version" ]]; then
      continue
    fi
    if [[ -z "$best_version" ]] || dpkg --compare-versions "$version" gt "$best_version"; then
      best_version="$version"
      best_filename="$filename"
    fi
  done < <(
    awk -v target="$package_name" '
      /^Package: / { pkg=$2; version=""; filename="" }
      /^Version: / && pkg == target { version=$2 }
      /^Filename: / && pkg == target { print version "\t" $2 }
    ' "$packages_index"
  )

  if [[ -n "$best_version" && -n "$best_filename" ]]; then
    printf '%s\t%s\n' "$best_version" "$best_filename"
  fi
}

install_mavlink_router_from_source() {
  log "building mavlink-routerd from source"
  log "source: ${MAVLINK_ROUTER_REPO}@${MAVLINK_ROUTER_REF}"

  run sudo apt-get install -y "${MAVLINK_ROUTER_BUILD_PACKAGES[@]}"
  run sudo mkdir -p "$(dirname "$MAVLINK_ROUTER_SOURCE_DIR")"
  run sudo chown "$USER":"$USER" "$(dirname "$MAVLINK_ROUTER_SOURCE_DIR")"

  if [[ -d "${MAVLINK_ROUTER_SOURCE_DIR}/.git" ]]; then
    run_shell "cd '${MAVLINK_ROUTER_SOURCE_DIR}' && git fetch --depth 1 origin '${MAVLINK_ROUTER_REF}' && git checkout FETCH_HEAD && git submodule update --init --recursive"
  else
    run_shell "git clone --recursive --depth 1 --branch '${MAVLINK_ROUTER_REF}' '${MAVLINK_ROUTER_REPO}' '${MAVLINK_ROUTER_SOURCE_DIR}'"
  fi

  run_shell "cd '${MAVLINK_ROUTER_SOURCE_DIR}' && rm -rf build && meson setup build . --prefix=/usr --buildtype=release && ninja -C build && sudo ninja -C build install"
  run_shell "printf 'repo=%s\nref=%s\ninstalled_at=%s\n' '${MAVLINK_ROUTER_REPO}' '${MAVLINK_ROUTER_REF}' \"\$(date -u +%Y-%m-%dT%H:%M:%SZ)\" > '${MAVLINK_ROUTER_SOURCE_MARKER}'"
}

install_mavlink_router() {
  if command -v mavlink-routerd >/dev/null 2>&1; then
    log "mavlink-routerd already available"
    return
  fi

  log "checking mavlink-router apt package"
  if apt-cache show mavlink-router >/dev/null 2>&1; then
    run sudo apt-get install -y mavlink-router
    return
  fi

  log "mavlink-router apt package unavailable; falling back to source build"
  install_mavlink_router_from_source
}

verify_hailo() {
  if [[ "$HAILO_INSTALL_MODE" == "auto" && "$VIDEO_PIPELINE_MODE" != "hailo" ]]; then
    log "skipping Hailo runtime verification because video pipeline mode is ${VIDEO_PIPELINE_MODE}"
    return
  fi

  log "verifying Hailo device visibility"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+ sudo modprobe hailo_pci || true\n'
    printf '+ hailortcli fw-control identify\n'
    printf '+ lsmod | grep -E %q || true\n' '(^hailo|hailo_pci)'
    printf '+ modinfo hailo_pci || true\n'
    printf '+ find /sys/class -maxdepth 2 -iname %q -print || true\n' '*hailo*'
    printf '+ lspci -nn | grep -i -E %q || true\n' 'hailo|1e60'
    printf '+ dmesg | grep -i hailo || true\n'
    return
  fi

  if ! command -v hailortcli >/dev/null 2>&1; then
    run_shell "command -v lspci >/dev/null 2>&1 && lspci -nn | grep -i -E 'hailo|1e60' || true"
    if hailo_runtime_required; then
      fail "hailortcli not found; Hailo runtime is required for --video-pipeline-mode=hailo"
    fi
    log "warning: hailortcli not found; Hailo runtime may still need setup"
    return
  fi

  load_hailo_driver_module || true

  if hailortcli fw-control identify; then
    return
  fi

  collect_hailo_runtime_diagnostics

  if ! hailo_runtime_required; then
    log "warning: HailoRT could not identify the device; continuing because Hailo is not required for this install mode"
    return
  fi

  if [[ "$HAILO_REBOOT_REQUIRED" -eq 1 ]] || hailo_driver_module_newer_than_boot; then
    mark_hailo_reboot_required "${HAILO_REBOOT_REASON:-the Hailo PCIe kernel module was installed during this boot}"
    HAILO_VERIFY_DEFERRED=1
    log "Hailo runtime verification is deferred until after reboot: ${HAILO_REBOOT_REASON}"
    log "continuing Atlas install; reboot the Pi before starting the Hailo video stack"
    return
  fi

  fail "HailoRT could not identify the Hailo PCIe device. Confirm the AI HAT+ is seated and powered, reboot the Pi, then rerun this installer or ${SCRIPT_DIR}/status-onboard-stack.sh."
}

install_mediamtx() {
  log "installing MediaMTX into ${MEDIAMTX_DIR}"
  if [[ -x "${MEDIAMTX_DIR}/mediamtx" ]]; then
    log "MediaMTX already installed"
    return
  fi

  local asset="mediamtx_${MEDIAMTX_VERSION}_${MEDIAMTX_ASSET_ARCH}.tar.gz"
  local archive="/tmp/${asset}"
  local download_url="https://github.com/bluenviron/mediamtx/releases/download/${MEDIAMTX_VERSION}/${asset}"
  run sudo mkdir -p "$MEDIAMTX_DIR"
  run sudo chown "$USER":"$USER" "$MEDIAMTX_DIR"
  run rm -f "$archive"
  run curl -fL "$download_url" -o "$archive"
  run_shell "tar -tzf '${archive}' >/dev/null"
  run tar -xzf "$archive" -C "$MEDIAMTX_DIR"
}

build_atlas_agent() {
  log "building atlas-agent binary"
  run sudo mkdir -p "${INSTALL_PREFIX}/bin"
  run sudo chown "$USER":"$USER" "${INSTALL_PREFIX}/bin"
  run_shell "cd '${ROOT_DIR}' && go build -o '${INSTALL_PREFIX}/bin/atlas-agent' ./cmd/atlas-agent"
  run install -m 0755 "${SCRIPT_DIR}/atlas-video-agent.py" "${INSTALL_PREFIX}/bin/atlas-video-agent.py"
}

install_mavsdk_server() {
  if [[ -x "$MAVSDK_SERVER_BIN" ]]; then
    log "mavsdk_server already installed at ${MAVSDK_SERVER_BIN}"
    return
  fi
  if command -v mavsdk_server >/dev/null 2>&1; then
    MAVSDK_SERVER_BIN="$(command -v mavsdk_server)"
    log "mavsdk_server already available at ${MAVSDK_SERVER_BIN}"
    return
  fi

  local download_url="https://github.com/mavlink/MAVSDK/releases/download/${MAVSDK_SERVER_VERSION}/${MAVSDK_SERVER_ASSET}"
  local tmp_bin="/tmp/${MAVSDK_SERVER_ASSET}_${MAVSDK_SERVER_VERSION}"
  log "installing mavsdk_server from ${download_url}"
  run sudo mkdir -p "$(dirname "$MAVSDK_SERVER_BIN")"
  run sudo chown "$USER":"$USER" "$(dirname "$MAVSDK_SERVER_BIN")"
  run rm -f "$tmp_bin"
  run curl -fL "$download_url" -o "$tmp_bin"
  run install -m 0755 "$tmp_bin" "$MAVSDK_SERVER_BIN"
  if [[ "$DRY_RUN" -eq 0 ]]; then
    [[ -x "$MAVSDK_SERVER_BIN" ]] || fail "downloaded mavsdk_server is not executable"
  fi
}

write_env_file() {
  local content
  content="$(cat <<EOF
ATLAS_DRONE_ID="${DRONE_ID}"
ATLAS_DRONE_NAME="${DRONE_NAME}"
ATLAS_VEHICLE_AGENT_ID="${VEHICLE_AGENT_ID}"
ATLAS_VEHICLE_AGENT_GRPC_ADDR="${GROUND_GRPC_ADDR}"
ATLAS_MAVSDK_GRPC_ADDR="127.0.0.1:50051"
ATLAS_PX4_SYSTEM_ADDRESS="udpin://0.0.0.0:14540"
ATLAS_MAVLINK_OBSERVER_ENDPOINT="udp-server://0.0.0.0:14550"
ATLAS_MAVSDK_SERVER_BIN="${MAVSDK_SERVER_BIN}"
ATLAS_MAVLINK_ROUTER_UART_DEVICE="${MAVLINK_ROUTER_UART_DEVICE}"
ATLAS_MAVLINK_ROUTER_UART_BAUD="${MAVLINK_ROUTER_UART_BAUD}"
ATLAS_A8_RTSP_URL="rtsp://192.168.144.25:8554/main.264"
ATLAS_PROCESSED_RTSP_URL="rtsp://127.0.0.1:8554/atlas"
ATLAS_PERCEPTION_MODEL_PATH="${MODEL_PATH}"
ATLAS_PERCEPTION_POSTPROCESS_SO="${HAILO_POSTPROCESS_SO}"
ATLAS_PERCEPTION_POSTPROCESS_FUNCTION="${HAILO_POSTPROCESS_FUNCTION}"
ATLAS_PERCEPTION_POSTPROCESS_CONFIG="${HAILO_POSTPROCESS_CONFIG}"
ATLAS_PERCEPTION_ACCELERATOR="hailo"
ATLAS_VIDEO_PIPELINE_MODE="${VIDEO_PIPELINE_MODE}"
ATLAS_A8_RTP_CODEC="${A8_RTP_CODEC}"
ATLAS_A8_RTSP_TRANSPORT="${ATLAS_A8_RTSP_TRANSPORT:-tcp}"
ATLAS_A8_RTSP_LATENCY_MS="${ATLAS_A8_RTSP_LATENCY_MS:-50}"
ATLAS_VIDEO_KEY_INT_MAX="${ATLAS_VIDEO_KEY_INT_MAX:-15}"
ATLAS_PERCEPTION_SOURCE_ID="a8-main"
ATLAS_PERCEPTION_METADATA_PATH="${HOME}/.local/state/atlas-agent/perception/metadata.jsonl"
ATLAS_COMPANION_LOG_DIR="${LOG_DIR}"
ATLAS_MAVLINK_ROUTER_CONFIG_FILE="${HOME}/.config/atlas-agent/mavlink-router/main.conf"
EOF
)"
  write_file "$ENV_FILE" "0644" "$content"
}

configure_eth0() {
  if [[ "$CONFIGURE_ETH0" -ne 1 ]]; then
    log "eth0 static config skipped; rerun with --configure-eth0 to enable"
    return
  fi

  local content
  content="$(cat <<'EOF'
network:
  version: 2
  ethernets:
    eth0:
      dhcp4: false
      dhcp6: false
      addresses:
        - 192.168.144.168/24
      optional: true
EOF
)"
  write_file "/etc/netplan/99-siyi-eth0-local.yaml" "0644" "$content"
  log "eth0 netplan written; apply manually with: sudo netplan try"
}

write_systemd_units() {
  log "writing systemd units"
  local user_name="${SUDO_USER:-$USER}"
  local group_name
  group_name="$(id -gn "$user_name")"

  write_file "/etc/systemd/system/atlas-mediamtx.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas MediaMTX RTSP server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${user_name}
Group=${group_name}
WorkingDirectory=${MEDIAMTX_DIR}
ExecStart=${MEDIAMTX_DIR}/mediamtx
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-mediamtx.log
StandardError=append:${LOG_DIR}/atlas-mediamtx.log

[Install]
WantedBy=multi-user.target
EOF
)"

  write_file "/etc/systemd/system/atlas-mavlink-router.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas MAVLink Router
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${user_name}
Group=${group_name}
EnvironmentFile=${ENV_FILE}
ExecStart=/usr/bin/env bash -lc 'exec mavlink-routerd -c "\${ATLAS_MAVLINK_ROUTER_CONFIG_FILE}"'
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-mavlink-router.log
StandardError=append:${LOG_DIR}/atlas-mavlink-router.log

[Install]
WantedBy=multi-user.target
EOF
)"

  write_file "/etc/systemd/system/atlas-mavsdk.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas MAVSDK Server
After=atlas-mavlink-router.service
Requires=atlas-mavlink-router.service

[Service]
Type=simple
User=${user_name}
Group=${group_name}
EnvironmentFile=${ENV_FILE}
ExecStart=/usr/bin/env bash -lc 'exec "\${ATLAS_MAVSDK_SERVER_BIN}" -p "\${ATLAS_MAVSDK_GRPC_ADDR##*:}" "\${ATLAS_PX4_SYSTEM_ADDRESS}"'
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-mavsdk.log
StandardError=append:${LOG_DIR}/atlas-mavsdk.log

[Install]
WantedBy=multi-user.target
EOF
)"

  write_file "/etc/systemd/system/atlas-agent.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas Vehicle Agent
After=atlas-mavsdk.service
Requires=atlas-mavsdk.service

[Service]
Type=simple
User=${user_name}
Group=${group_name}
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_PREFIX}/bin/atlas-agent
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-agent.log
StandardError=append:${LOG_DIR}/atlas-agent.log

[Install]
WantedBy=multi-user.target
EOF
)"

  write_file "/etc/systemd/system/atlas-video-agent.service" "0644" "$(cat <<EOF
[Unit]
Description=Atlas Hailo Video Agent
After=atlas-mediamtx.service
Requires=atlas-mediamtx.service

[Service]
Type=simple
User=${user_name}
Group=${group_name}
WorkingDirectory=${LOG_DIR}
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_PREFIX}/bin/atlas-video-agent.py
Restart=always
RestartSec=3
StandardOutput=append:${LOG_DIR}/atlas-video-agent.log
StandardError=append:${LOG_DIR}/atlas-video-agent.log

[Install]
WantedBy=multi-user.target
EOF
)"

  if [[ "$DRY_RUN" -eq 0 ]]; then
    run mkdir -p "$LOG_DIR"
    run sudo systemctl daemon-reload
    run sudo systemctl enable atlas-mediamtx.service atlas-mavlink-router.service atlas-mavsdk.service atlas-agent.service atlas-video-agent.service
  fi
}

generate_mavlink_router_config() {
  local setup_args=(
    --device "${MAVLINK_ROUTER_UART_DEVICE}"
    --baud "${MAVLINK_ROUTER_UART_BAUD}"
    --env "${HOME}/.config/atlas-agent/mavlink-router/atlas-mavlink.env"
  )

  run "${SCRIPT_DIR}/setup-mavlink-router.sh" "${setup_args[@]}" --dry-run
  if [[ "$DRY_RUN" -eq 0 ]]; then
    "${SCRIPT_DIR}/setup-mavlink-router.sh" "${setup_args[@]}"
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --configure-eth0)
      CONFIGURE_ETH0=1
      shift
      ;;
    --ground-grpc)
      require_value "$1" "${2:-}"
      GROUND_GRPC_ADDR="$2"
      shift 2
      ;;
    --drone-id)
      require_value "$1" "${2:-}"
      DRONE_ID="$2"
      shift 2
      ;;
    --drone-name)
      require_value "$1" "${2:-}"
      DRONE_NAME="$2"
      shift 2
      ;;
    --vehicle-agent-id)
      require_value "$1" "${2:-}"
      VEHICLE_AGENT_ID="$2"
      shift 2
      ;;
    --install-prefix)
      require_value "$1" "${2:-}"
      INSTALL_PREFIX="$2"
      MEDIAMTX_DIR="${INSTALL_PREFIX}/mediamtx"
      MAVSDK_SERVER_BIN="${INSTALL_PREFIX}/bin/mavsdk_server"
      if [[ "$MODEL_PATH_EXPLICIT" -eq 0 ]]; then
        MODEL_PATH="${INSTALL_PREFIX}/models/yolov6n.hef"
      fi
      MAVLINK_ROUTER_SOURCE_DIR="${INSTALL_PREFIX}/src/mavlink-router"
      MAVLINK_ROUTER_SOURCE_MARKER="${MAVLINK_ROUTER_SOURCE_DIR}/.atlas-source-install"
      shift 2
      ;;
    --env-file)
      require_value "$1" "${2:-}"
      ENV_FILE="$2"
      ENV_DIR="$(dirname "$ENV_FILE")"
      shift 2
      ;;
    --mavsdk-version)
      require_value "$1" "${2:-}"
      MAVSDK_SERVER_VERSION="$2"
      shift 2
      ;;
    --model-path)
      require_value "$1" "${2:-}"
      MODEL_PATH="$2"
      MODEL_PATH_EXPLICIT=1
      shift 2
      ;;
    --model-source)
      require_value "$1" "${2:-}"
      MODEL_SOURCE="$2"
      shift 2
      ;;
    --video-pipeline-mode)
      require_value "$1" "${2:-}"
      VIDEO_PIPELINE_MODE="$2"
      shift 2
      ;;
    --a8-rtp-codec)
      require_value "$1" "${2:-}"
      A8_RTP_CODEC="$2"
      shift 2
      ;;
    --hailo-hardware)
      require_value "$1" "${2:-}"
      HAILO_HARDWARE="$2"
      shift 2
      ;;
    --hailo-install)
      require_value "$1" "${2:-}"
      HAILO_INSTALL_MODE="$2"
      shift 2
      ;;
    --hailo-apt-packages)
      require_value "$1" "${2:-}"
      HAILO_APT_PACKAGES="$2"
      shift 2
      ;;
    --hailo-deb-dir)
      require_value "$1" "${2:-}"
      HAILO_DEB_DIR="$2"
      shift 2
      ;;
    --hailo-deb-source)
      require_value "$1" "${2:-}"
      HAILO_DEB_SOURCE="$2"
      shift 2
      ;;
    --hailo-rpi-suite)
      require_value "$1" "${2:-}"
      HAILO_RPI_SUITE="$2"
      shift 2
      ;;
    --mavlink-device)
      require_value "$1" "${2:-}"
      MAVLINK_ROUTER_UART_DEVICE="$2"
      shift 2
      ;;
    --mavlink-baud)
      require_value "$1" "${2:-}"
      MAVLINK_ROUTER_UART_BAUD="$2"
      shift 2
      ;;
    --mavlink-router-ref)
      require_value "$1" "${2:-}"
      MAVLINK_ROUTER_REF="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

validate_video_config
detect_platform
recover_hailo_dpkg_state
install_apt_packages
install_hailo_packages
verify_hailo
verify_gstreamer_elements
install_hailo_model
verify_hailo_postprocess
install_mavlink_router
install_mediamtx
build_atlas_agent
install_mavsdk_server
write_env_file
configure_eth0
generate_mavlink_router_config
write_systemd_units

log "onboard install complete"
log "env file: ${ENV_FILE}"
if [[ "$HAILO_VERIFY_DEFERRED" -eq 1 ]]; then
  log "reboot required before Hailo runtime verification: ${HAILO_REBOOT_REASON}"
  log "after reboot: hailortcli fw-control identify && gst-inspect-1.0 hailonet hailofilter hailooverlay"
fi
log "start stack: ${SCRIPT_DIR}/start-onboard-stack.sh"
log "status: ${SCRIPT_DIR}/status-onboard-stack.sh"
