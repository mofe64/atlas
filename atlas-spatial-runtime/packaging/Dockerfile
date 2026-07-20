FROM ros:jazzy-ros-base-noble

ARG DEBIAN_FRONTEND=noninteractive
ARG ATLAS_SPATIAL_VERSION=0.1.0-dev

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        python3-colcon-common-extensions \
        python3-numpy \
        ros2-testing-apt-source \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ros-jazzy-depth-image-proc \
        ros-jazzy-depthai-ros-v3 \
        ros-jazzy-rosbag2-storage-mcap \
    && rm -rf /var/lib/apt/lists/*

COPY ros2_ws/src /workspace/src
RUN . /opt/ros/jazzy/setup.sh \
    && cd /workspace \
    && colcon build --merge-install --install-base /opt/atlas-spatial-runtime

COPY packaging/entrypoint.sh /usr/local/bin/atlas-spatial-entrypoint
RUN chmod 0755 /usr/local/bin/atlas-spatial-entrypoint \
    && printf '%s\n' "${ATLAS_SPATIAL_VERSION}" > /opt/atlas-spatial-runtime/VERSION

ENV ROS_DISTRO=jazzy \
    ROS_LOCALHOST_ONLY=1 \
    DEPTHAI_TELEMETRY=0 \
    ATLAS_SPATIAL_PROVIDER=synthetic \
    ATLAS_SPATIAL_SOURCE_ID=front-depth \
    ATLAS_SPATIAL_SOCKET_PATH=/run/atlas-agent/spatial.sock

ENTRYPOINT ["/usr/local/bin/atlas-spatial-entrypoint"]
