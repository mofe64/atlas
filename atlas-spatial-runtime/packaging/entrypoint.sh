#!/bin/sh
set -eu

if [ "${ATLAS_SPATIAL_CONTRACT_VERSION:-1}" != 1 ]; then
    printf 'unsupported Atlas spatial contract version: %s\n' "${ATLAS_SPATIAL_CONTRACT_VERSION}" >&2
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
    "socket_path:=${ATLAS_SPATIAL_SOCKET_PATH:-/run/atlas-agent/spatial.sock}"

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
