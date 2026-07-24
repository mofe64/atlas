#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
ATLAS_DIR="$(cd "${AGENT_DIR}/.." && pwd)"
SPATIAL_DIR="${ATLAS_DIR}/atlas-spatial-runtime"
DIST_DIR="${ATLAS_RELEASE_OUTPUT_DIR:-${AGENT_DIR}/dist}"

usage() {
  cat <<'EOF'
Usage:
  packaging/release.sh build VERSION [--replace] [--reuse-image]
  packaging/release.sh verify VERSION
  packaging/release.sh transfer VERSION USER@HOST

Build creates one matched Linux-arm64 Atlas release: the standard-DepthAI
spatial image archive, Agent Debian package, checksums, and release manifest.
Transfer verifies that complete set before copying it to /tmp on the Pi.
Reuse-image resumes packaging from an already-built local image and must only
be used when the spatial runtime source has not changed since that image build.

Environment:
  ATLAS_RELEASE_OUTPUT_DIR  Override the artifact directory (default: dist).
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

set_artifact_names() {
  local version="$1"
  AGENT_DEB="atlas-agent_${version}_arm64.deb"
  AGENT_DEB_SHA="${AGENT_DEB}.sha256"
  AGENT_BINARY_SHA="atlas-agent_${version}_arm64.binary.sha256"
  SPATIAL_ARCHIVE="atlas-spatial-runtime_${version}_arm64.tar.gz"
  SPATIAL_ARCHIVE_SHA="${SPATIAL_ARCHIVE}.sha256"
  RELEASE_MANIFEST="atlas-release_${version}_arm64.json"
}

verify_checksum_file() {
  local directory="$1"
  local checksum_file="$2"
  local expected_artifact="$3"
  local recorded_artifact

  recorded_artifact="$(awk 'NF {print $2; exit}' "${directory}/${checksum_file}")"
  [[ "${recorded_artifact}" == "${expected_artifact}" ]] ||
    fail "${checksum_file} must reference only ${expected_artifact}, got ${recorded_artifact:-empty}"
  [[ "${recorded_artifact}" != */* ]] ||
    fail "${checksum_file} contains a path; checksums must remain valid after transfer"
  (
    cd "${directory}"
    sha256sum -c "${checksum_file}"
  )
}

verify_release_directory() {
  local version="$1"
  local directory="$2"
  local image_reference="atlas-spatial-runtime:${version}"
  local artifact
  local package_version
  local packaged_image
  local packaged_binary_sha
  local recorded_binary_sha
  local agent_deb_sha
  local spatial_archive_sha

  set_artifact_names "${version}"
  for artifact in \
    "${AGENT_DEB}" \
    "${AGENT_DEB_SHA}" \
    "${AGENT_BINARY_SHA}" \
    "${SPATIAL_ARCHIVE}" \
    "${SPATIAL_ARCHIVE_SHA}" \
    "${RELEASE_MANIFEST}"; do
    [[ -f "${directory}/${artifact}" ]] ||
      fail "release artifact is missing: ${directory}/${artifact}"
  done

  verify_checksum_file "${directory}" "${AGENT_DEB_SHA}" "${AGENT_DEB}"
  verify_checksum_file \
    "${directory}" "${SPATIAL_ARCHIVE_SHA}" "${SPATIAL_ARCHIVE}"

  recorded_binary_sha="$(awk 'NF {print $1; exit}' "${directory}/${AGENT_BINARY_SHA}")"
  [[ "${recorded_binary_sha}" =~ ^[0-9a-f]{64}$ ]] ||
    fail "${AGENT_BINARY_SHA} does not contain one SHA-256 digest"

  package_version="$(dpkg-deb -f "${directory}/${AGENT_DEB}" Version)"
  [[ "${package_version}" == "${version}" ]] ||
    fail "${AGENT_DEB} contains version ${package_version}, expected ${version}"

  packaged_image="$(
    dpkg-deb --fsys-tarfile "${directory}/${AGENT_DEB}" |
      tar -xOf - ./usr/share/atlas-agent/release.env |
      sed -n 's/^ATLAS_SPATIAL_CONTAINER_IMAGE="\(.*\)"$/\1/p'
  )"
  [[ "${packaged_image}" == "${image_reference}" ]] ||
    fail "${AGENT_DEB} selects ${packaged_image:-no spatial image}, expected ${image_reference}"

  packaged_binary_sha="$(
    dpkg-deb --fsys-tarfile "${directory}/${AGENT_DEB}" |
      tar -xOf - ./usr/bin/atlas-agent |
      sha256sum |
      awk '{print $1}'
  )"
  [[ "${packaged_binary_sha}" == "${recorded_binary_sha}" ]] ||
    fail "${AGENT_BINARY_SHA} does not match the Agent binary in ${AGENT_DEB}"

  agent_deb_sha="$(sha256sum "${directory}/${AGENT_DEB}" | awk '{print $1}')"
  spatial_archive_sha="$(
    sha256sum "${directory}/${SPATIAL_ARCHIVE}" | awk '{print $1}'
  )"
  python3 - \
    "${directory}/${RELEASE_MANIFEST}" \
    "${version}" \
    "${image_reference}" \
    "${AGENT_DEB}" \
    "${agent_deb_sha}" \
    "${recorded_binary_sha}" \
    "${SPATIAL_ARCHIVE}" \
    "${spatial_archive_sha}" <<'PY'
import json
import sys

(
    manifest_path,
    version,
    image_reference,
    agent_name,
    agent_sha,
    binary_sha,
    spatial_name,
    spatial_sha,
) = sys.argv[1:]

with open(manifest_path, encoding="utf-8") as manifest_file:
    manifest = json.load(manifest_file)

assert manifest["schemaVersion"] == 1
assert manifest["version"] == version
assert manifest["spatialImage"]["reference"] == image_reference
assert manifest["spatialImage"]["id"].startswith("sha256:")
assert manifest["artifacts"]["agentDeb"] == {
    "file": agent_name,
    "sha256": agent_sha,
    "binarySha256": binary_sha,
}
assert manifest["artifacts"]["spatialArchive"] == {
    "file": spatial_name,
    "sha256": spatial_sha,
}
PY
  printf 'atlas release: verified %s in %s\n' "${version}" "${directory}"
}

build_release() {
  local version="$1"
  shift
  local replace=false
  local reuse_image=false
  local option
  local image_reference="atlas-spatial-runtime:${version}"
  local artifact
  local staging_dir
  local package_root
  local image_id
  local git_commit
  local source_tree_dirty=false
  local built_at
  local agent_deb_sha
  local agent_binary_sha
  local spatial_archive_sha

  for option in "$@"; do
    case "${option}" in
      --replace)
        replace=true
        ;;
      --reuse-image)
        reuse_image=true
        ;;
      *)
        printf 'atlas release: unknown build option: %s\n' "${option}" >&2
        exit 2
        ;;
    esac
  done
  validate_version "${version}"
  set_artifact_names "${version}"

  require_command docker
  require_command dpkg-deb
  require_command gzip
  require_command sha256sum
  require_command git
  require_command tar
  require_command python3
  docker info >/dev/null
  if [[ "${reuse_image}" != true ]]; then
    docker buildx version >/dev/null
  fi

  mkdir -p "${DIST_DIR}"
  if [[ "${replace}" != true ]]; then
    for artifact in \
      "${AGENT_DEB}" \
      "${AGENT_DEB_SHA}" \
      "${AGENT_BINARY_SHA}" \
      "${SPATIAL_ARCHIVE}" \
      "${SPATIAL_ARCHIVE_SHA}" \
      "${RELEASE_MANIFEST}"; do
      [[ ! -e "${DIST_DIR}/${artifact}" ]] ||
        fail "${artifact} already exists; use --replace to rebuild this version"
    done
  fi

  staging_dir="$(mktemp -d "${DIST_DIR}/.release-${version}.XXXXXX")"
  cleanup_release_staging() {
    rm -rf -- "${staging_dir}"
  }
  trap cleanup_release_staging EXIT

  if [[ "${reuse_image}" == true ]]; then
    docker image inspect "${image_reference}" >/dev/null 2>&1 ||
      fail "--reuse-image requested, but ${image_reference} is unavailable"
    [[ "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "${image_reference}")" == "linux/arm64" ]] ||
      fail "${image_reference} is not a Linux-arm64 image"
    printf 'atlas release: explicitly reusing local image %s\n' "${image_reference}"
  else
    printf 'atlas release: building %s from the canonical spatial Dockerfile\n' \
      "${image_reference}"
    docker buildx build \
      --platform linux/arm64 \
      --file "${SPATIAL_DIR}/packaging/Dockerfile" \
      --build-arg "ATLAS_SPATIAL_VERSION=${version}" \
      --tag "${image_reference}" \
      --load \
      "${SPATIAL_DIR}"
  fi

  printf 'atlas release: verifying standard DepthAI and spatial source tests\n'
  docker run --rm --entrypoint /bin/bash "${image_reference}" -lc '
    set +u
    . /opt/ros/jazzy/setup.sh
    . /opt/atlas-spatial-runtime/setup.sh
    set -u
    test "$(cat /opt/atlas-spatial-runtime/VERSION)" = "${1}"
    core_version="$(dpkg-query -W -f=\${Version} ros-jazzy-depthai-v3)"
    case "${core_version}" in
      3.6.1-2noble|3.6.1-2noble.*) ;;
      *) echo "unexpected standard DepthAI version: ${core_version}" >&2; exit 1 ;;
    esac
    dpkg-query -W \
      ros-jazzy-depthai-ros-driver-v3 \
      ros-jazzy-imu-filter-madgwick \
      ros-jazzy-rtabmap-odom
    core="$(find /opt/ros/jazzy/lib -name libdepthai_v3-core.so -print -quit)"
    test -n "${core}"
    ldd "${core}" | grep -F libusb-1.0.so.0
    ! ldd "${core}" | grep -F /opt/atlas-depthai-libusb
    ! strings "${core}" | grep -F ATLAS_DEPTHAI_
    grep -F "always_process_most_recent_frame: true" \
      /workspace/src/atlas_spatial_runtime/config/rtabmap_vio.yaml
    python3 -m pytest -q /workspace/src/atlas_spatial_runtime/test
  ' atlas-release-verifier "${version}"

  printf 'atlas release: archiving %s\n' "${image_reference}"
  docker save \
    --output "${staging_dir}/atlas-spatial-runtime_${version}_arm64.tar" \
    "${image_reference}"
  gzip -n "${staging_dir}/atlas-spatial-runtime_${version}_arm64.tar"
  (
    cd "${staging_dir}"
    sha256sum "${SPATIAL_ARCHIVE}" > "${SPATIAL_ARCHIVE_SHA}"
  )

  printf 'atlas release: building the matching Agent Debian package\n'
  ATLAS_RELEASE_VERSION="${version}" \
  ATLAS_PACKAGE_OUTPUT_DIR="${staging_dir}" \
  ATLAS_SPATIAL_CONTAINER_IMAGE="${image_reference}" \
    "${AGENT_DIR}/packaging/build-deb.sh"

  package_root="${staging_dir}/package-check"
  mkdir -p "${package_root}"
  dpkg-deb -x "${staging_dir}/${AGENT_DEB}" "${package_root}"
  sha256sum "${package_root}/usr/bin/atlas-agent" |
    awk '{print $1}' > "${staging_dir}/${AGENT_BINARY_SHA}"

  image_id="$(docker image inspect --format '{{.Id}}' "${image_reference}")"
  [[ "${image_id}" == sha256:* ]] ||
    fail "Docker returned a non-immutable image identity: ${image_id}"
  git_commit="$(git -C "${ATLAS_DIR}" rev-parse HEAD)"
  if [[ -n "$(git -C "${ATLAS_DIR}" status --porcelain --untracked-files=normal)" ]]; then
    source_tree_dirty=true
  fi
  built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  agent_deb_sha="$(
    sha256sum "${staging_dir}/${AGENT_DEB}" | awk '{print $1}'
  )"
  agent_binary_sha="$(awk 'NF {print $1; exit}' "${staging_dir}/${AGENT_BINARY_SHA}")"
  spatial_archive_sha="$(
    sha256sum "${staging_dir}/${SPATIAL_ARCHIVE}" | awk '{print $1}'
  )"

  {
    printf '{\n'
    printf '  "schemaVersion": 1,\n'
    printf '  "version": "%s",\n' "${version}"
    printf '  "builtAt": "%s",\n' "${built_at}"
    printf '  "gitCommit": "%s",\n' "${git_commit}"
    printf '  "sourceTreeDirty": %s,\n' "${source_tree_dirty}"
    printf '  "spatialImage": {\n'
    printf '    "reference": "%s",\n' "${image_reference}"
    printf '    "id": "%s"\n' "${image_id}"
    printf '  },\n'
    printf '  "artifacts": {\n'
    printf '    "agentDeb": {\n'
    printf '      "file": "%s",\n' "${AGENT_DEB}"
    printf '      "sha256": "%s",\n' "${agent_deb_sha}"
    printf '      "binarySha256": "%s"\n' "${agent_binary_sha}"
    printf '    },\n'
    printf '    "spatialArchive": {\n'
    printf '      "file": "%s",\n' "${SPATIAL_ARCHIVE}"
    printf '      "sha256": "%s"\n' "${spatial_archive_sha}"
    printf '    }\n'
    printf '  }\n'
    printf '}\n'
  } > "${staging_dir}/${RELEASE_MANIFEST}"

  verify_release_directory "${version}" "${staging_dir}"

  for artifact in \
    "${AGENT_DEB}" \
    "${AGENT_DEB_SHA}" \
    "${AGENT_BINARY_SHA}" \
    "${SPATIAL_ARCHIVE}" \
    "${SPATIAL_ARCHIVE_SHA}" \
    "${RELEASE_MANIFEST}"; do
    mv -f "${staging_dir}/${artifact}" "${DIST_DIR}/${artifact}"
  done
  if [[ -f "${staging_dir}/atlas-bytetrack-worker-linux-arm64" \
    && ! -f "${DIST_DIR}/atlas-bytetrack-worker-linux-arm64" ]]; then
    mv "${staging_dir}/atlas-bytetrack-worker-linux-arm64" \
      "${DIST_DIR}/atlas-bytetrack-worker-linux-arm64"
  fi

  verify_release_directory "${version}" "${DIST_DIR}"
  printf 'atlas release: complete manifest: %s/%s\n' \
    "${DIST_DIR}" "${RELEASE_MANIFEST}"

  # The EXIT trap is the failure-path safety net, but staging_dir is local to
  # this function. Clean it while that local still exists and disarm the trap
  # after a successful build; otherwise `set -u` turns an already-complete
  # release into a false failure as the script exits.
  cleanup_release_staging
  trap - EXIT
}

transfer_release() {
  local version="$1"
  local destination="$2"

  validate_version "${version}"
  [[ "${destination}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$ ]] || {
    printf 'atlas release: destination must be USER@HOST, got %s\n' \
      "${destination}" >&2
    exit 2
  }
  require_command scp
  require_command dpkg-deb
  require_command sha256sum
  require_command tar
  require_command python3
  set_artifact_names "${version}"
  verify_release_directory "${version}" "${DIST_DIR}"

  scp \
    "${DIST_DIR}/${AGENT_DEB}" \
    "${DIST_DIR}/${AGENT_DEB_SHA}" \
    "${DIST_DIR}/${AGENT_BINARY_SHA}" \
    "${DIST_DIR}/${SPATIAL_ARCHIVE}" \
    "${DIST_DIR}/${SPATIAL_ARCHIVE_SHA}" \
    "${DIST_DIR}/${RELEASE_MANIFEST}" \
    "${destination}:/tmp/"

  printf 'atlas release: transferred %s to %s:/tmp/\n' "${version}" "${destination}"
  printf 'atlas release: installation remains an explicit grounded-aircraft step\n'
}

command_name="${1:-}"
case "${command_name}" in
  build)
    [[ $# -ge 2 && $# -le 4 ]] || {
      usage >&2
      exit 2
    }
    build_release "$2" "${@:3}"
    ;;
  verify)
    [[ $# -eq 2 ]] || {
      usage >&2
      exit 2
    }
    validate_version "$2"
    require_command dpkg-deb
    require_command sha256sum
    require_command tar
    require_command python3
    verify_release_directory "$2" "${DIST_DIR}"
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
