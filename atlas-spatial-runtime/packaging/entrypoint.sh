#!/bin/sh
set -eu

if [ "${ATLAS_SPATIAL_CONTRACT_VERSION:-1}" != 1 ]; then
    printf 'unsupported Atlas spatial contract version: %s\n' "${ATLAS_SPATIAL_CONTRACT_VERSION}" >&2
    exit 1
fi

. /opt/ros/jazzy/setup.sh
. /opt/atlas-spatial-runtime/setup.sh

exec ros2 launch atlas_spatial_runtime spatial_runtime.launch.py \
    provider:="${ATLAS_SPATIAL_PROVIDER:-synthetic}" \
    source_id:="${ATLAS_SPATIAL_SOURCE_ID:-front-depth}" \
    device_id:="${ATLAS_SPATIAL_DEVICE_ID:-}" \
    model:="${ATLAS_SPATIAL_MODEL:-}" \
    usb_transport:="${ATLAS_SPATIAL_USB_TRANSPORT:-unknown}" \
    socket_path:="${ATLAS_SPATIAL_SOCKET_PATH:-/run/atlas-agent/spatial.sock}"
