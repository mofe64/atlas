#!/bin/sh
set -eu

if [ "${ATLAS_SPATIAL_CONTRACT_VERSION:-1}" != 1 ]; then
    printf 'unsupported Atlas spatial contract version: %s\n' "${ATLAS_SPATIAL_CONTRACT_VERSION}" >&2
    exit 1
fi

if [ "${ATLAS_SPATIAL_PX4_VIO_FUSION_ENABLED:-false}" != false ] \
    || [ "${ATLAS_SPATIAL_MOVEMENT_ENABLED:-false}" != false ]; then
    printf '%s\n' 'PX4 VIO fusion and spatial-runtime movement must remain disabled' >&2
    exit 1
fi

# ROS-generated setup scripts intentionally probe optional variables such as
# AMENT_TRACE_SETUP_FILES. Source them without nounset, then restore our strict
# entrypoint policy for the Atlas-owned launch arguments below.
set +u
. /opt/ros/jazzy/setup.sh
. /opt/atlas-spatial-runtime/setup.sh
set -u

set -- \
    ros2 launch atlas_spatial_runtime spatial_runtime.launch.py \
    "provider:=${ATLAS_SPATIAL_PROVIDER:-synthetic}" \
    "source_id:=${ATLAS_SPATIAL_SOURCE_ID:-front-depth}" \
    "usb_transport:=${ATLAS_SPATIAL_USB_TRANSPORT:-unknown}" \
    "socket_path:=${ATLAS_SPATIAL_SOCKET_PATH:-/run/atlas-agent/spatial.sock}" \
    "cloud_socket_path:=${ATLAS_SPATIAL_CLOUD_SOCKET_PATH:-/run/atlas-agent/spatial-cloud.sock}" \
    "transform_bundle_path:=${ATLAS_SPATIAL_TRANSFORM_BUNDLE_PATH:-/var/lib/atlas-agent/spatial/transforms.v1.json}" \
    "vio_enabled:=${ATLAS_SPATIAL_VIO_ENABLED:-true}" \
    "live_cloud_enabled:=${ATLAS_SPATIAL_LIVE_CLOUD_ENABLED:-true}"

# ROS rejects an explicit empty launch assignment (for example device_id:=).
# Omit optional metadata when it is unknown so the launch file's empty default
# is used without inventing a provider-specific identity.
if [ -n "${ATLAS_SPATIAL_DEVICE_ID:-}" ]; then
    set -- "$@" "device_id:=${ATLAS_SPATIAL_DEVICE_ID}"
fi
if [ -n "${ATLAS_SPATIAL_MODEL:-}" ]; then
    set -- "$@" "model:=${ATLAS_SPATIAL_MODEL}"
fi

exec "$@"
