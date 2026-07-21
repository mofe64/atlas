FROM ros:jazzy-ros-base-noble AS depthai-libusb-builder

ARG DEBIAN_FRONTEND=noninteractive
ARG DEPTHAI_LIBUSB_REF=d631db2d91ce72f79ac296e3ff724eee98ad0c46

# The ROS Jazzy DepthAI v3 binary links Ubuntu's udev-backed libusb. Inside a
# container it can miss the OAK's firmware re-enumeration and fail with
# X_LINK_DEVICE_NOT_FOUND. Build the exact libusb revision pinned by DepthAI
# 3.6.1, using its netlink backend, so the final image can override only this
# dependency without forking the ROS driver or the DepthAI core.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        cmake \
        git \
    && git clone https://github.com/luxonis/libusb.git /src/libusb \
    && git -C /src/libusb checkout "${DEPTHAI_LIBUSB_REF}" \
    && test "$(git -C /src/libusb rev-parse HEAD)" = "${DEPTHAI_LIBUSB_REF}" \
    && cmake \
        -S /src/libusb \
        -B /src/libusb-build \
        -DBUILD_SHARED_LIBS=ON \
        -DBUILD_TESTS=OFF \
        -DBUILD_EXAMPLES=OFF \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_LIBDIR=lib \
        -DCMAKE_INSTALL_PREFIX=/opt/atlas-depthai-libusb \
        -DWITH_UDEV=OFF \
    && cmake --build /src/libusb-build --parallel \
    && cmake --install /src/libusb-build \
    && cmake -E create_symlink \
        libusb-1.0.so \
        /opt/atlas-depthai-libusb/lib/libusb-1.0.so.0

FROM ros:jazzy-ros-base-noble

ARG DEBIAN_FRONTEND=noninteractive
ARG ATLAS_SPATIAL_VERSION=0.1.0-dev
ARG DEPTHAI_CORE_VERSION=3.6.1
ARG DEPTHAI_ROS_VERSION=3.2.1

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
    && core_version="$(dpkg-query -W -f='${Version}' ros-jazzy-depthai-v3)" \
    && driver_version="$(dpkg-query -W -f='${Version}' ros-jazzy-depthai-ros-driver-v3)" \
    && case "${core_version}" in "${DEPTHAI_CORE_VERSION}-"*) ;; *) printf 'unsupported DepthAI core package: %s\n' "${core_version}" >&2; exit 1;; esac \
    && case "${driver_version}" in "${DEPTHAI_ROS_VERSION}-"*) ;; *) printf 'unsupported DepthAI ROS driver package: %s\n' "${driver_version}" >&2; exit 1;; esac \
    && rm -rf /var/lib/apt/lists/*

COPY --from=depthai-libusb-builder /opt/atlas-depthai-libusb /opt/atlas-depthai-libusb

ENV LD_LIBRARY_PATH=/opt/atlas-depthai-libusb/lib

# Fail the image build if the DepthAI core does not resolve the private libusb.
# This protects the workaround from silently disappearing during ROS upgrades.
RUN depthai_core="$(find /opt/ros/jazzy/lib -name libdepthai_v3-core.so -print -quit)" \
    && test -n "${depthai_core}" \
    && ldd "${depthai_core}" \
        | grep -F '/opt/atlas-depthai-libusb/lib/libusb-1.0.so.0'

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
