FROM ros:jazzy-ros-base-noble AS depthai-libusb-builder

ARG DEBIAN_FRONTEND=noninteractive
ARG DEPTHAI_LIBUSB_REF=d631db2d91ce72f79ac296e3ff724eee98ad0c46

# Ubuntu's udev-backed libusb can lose the OAK while firmware changes its USB
# identity. Build DepthAI's pinned libusb revision with its netlink backend so
# discovery survives that re-enumeration inside the container.
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

FROM ros:jazzy-ros-base-noble AS depthai-vio-builder

ARG DEBIAN_FRONTEND=noninteractive
ARG DEPTHAI_CORE_VERSION=3.6.1
ARG DEPTHAI_CORE_DEBIAN_REVISION=2noble
ARG DEPTHAI_CORE_SOURCE_SHA256=f889d96a3458f7b9589db73f5ad1b33bee362a03171720aa6021b5f4132cbc60
ARG DEPTHAI_CORE_ATLAS_REVISION=atlas2

# Rebuild the exact ROS source package with the two narrow Basalt input fixes.
# The distinct +atlas2 revision makes the deviation visible to dpkg and doctor.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        devscripts \
        dpkg-dev \
        patch \
        ros2-testing-apt-source \
    && apt-get update \
    && apt-get build-dep -y ros-jazzy-depthai-v3

WORKDIR /src
RUN apt-get source ros-jazzy-depthai-v3 \
    && printf '%s  %s\n' \
        "${DEPTHAI_CORE_SOURCE_SHA256}" \
        "ros-jazzy-depthai-v3_${DEPTHAI_CORE_VERSION}.orig.tar.gz" \
        | sha256sum -c -

COPY packaging/depthai/AtlasMonotonicImuQueue.hpp /src/AtlasMonotonicImuQueue.hpp
COPY packaging/depthai/AtlasLatestQueue.hpp /src/AtlasLatestQueue.hpp
COPY packaging/depthai/test_monotonic_imu_queue.cpp /src/test_monotonic_imu_queue.cpp
COPY packaging/depthai/test_latest_queue.cpp /src/test_latest_queue.cpp
COPY packaging/depthai/depthai-v3.6.1-basalt-imu-order.patch /src/depthai-v3.6.1-basalt-imu-order.patch

RUN c++ -std=c++17 -pthread -Wall -Wextra -Werror \
        -I /src \
        /src/test_monotonic_imu_queue.cpp \
        -o /src/test-monotonic-imu-queue \
    && /src/test-monotonic-imu-queue \
    && c++ -std=c++17 -Wall -Wextra -Werror \
        -I /src \
        /src/test_latest_queue.cpp \
        -o /src/test-latest-queue \
    && /src/test-latest-queue \
    && test "$(dpkg-parsechangelog \
        --file "/src/ros-jazzy-depthai-v3-${DEPTHAI_CORE_VERSION}/debian/changelog" \
        --show-field Version)" = "${DEPTHAI_CORE_VERSION}-${DEPTHAI_CORE_DEBIAN_REVISION}" \
    && cp \
        /src/AtlasMonotonicImuQueue.hpp \
        "/src/ros-jazzy-depthai-v3-${DEPTHAI_CORE_VERSION}/src/basalt/AtlasMonotonicImuQueue.hpp" \
    && cp \
        /src/AtlasLatestQueue.hpp \
        "/src/ros-jazzy-depthai-v3-${DEPTHAI_CORE_VERSION}/src/basalt/AtlasLatestQueue.hpp" \
    && patch \
        --directory "/src/ros-jazzy-depthai-v3-${DEPTHAI_CORE_VERSION}" \
        --strip 1 \
        --input /src/depthai-v3.6.1-basalt-imu-order.patch \
    && sed -i \
        "1s/${DEPTHAI_CORE_VERSION}-${DEPTHAI_CORE_DEBIAN_REVISION}/${DEPTHAI_CORE_VERSION}-${DEPTHAI_CORE_DEBIAN_REVISION}+${DEPTHAI_CORE_ATLAS_REVISION}/" \
        "/src/ros-jazzy-depthai-v3-${DEPTHAI_CORE_VERSION}/debian/changelog" \
    && cd "/src/ros-jazzy-depthai-v3-${DEPTHAI_CORE_VERSION}" \
    && DEB_BUILD_OPTIONS=nocheck dpkg-buildpackage --build=binary --no-sign

FROM ros:jazzy-ros-base-noble AS atlas-upstream-depthai

ARG DEBIAN_FRONTEND=noninteractive
ARG DEPTHAI_CORE_VERSION=3.6.1
ARG DEPTHAI_CORE_DEBIAN_REVISION=2noble
ARG DEPTHAI_ROS_VERSION=3.2.1
ARG IMU_TOOLS_VERSION=2.1.5
ARG RTABMAP_ROS_VERSION=0.23.7

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        python3-colcon-common-extensions \
        python3-numpy \
        ros2-testing-apt-source \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ros-jazzy-imu-filter-madgwick \
        ros-jazzy-depth-image-proc \
        ros-jazzy-depthai-ros-v3 \
        ros-jazzy-rtabmap-odom \
    && rm -rf /var/lib/apt/lists/*

RUN core_version="$(dpkg-query -W -f='${Version}' ros-jazzy-depthai-v3)" \
    && driver_version="$(dpkg-query -W -f='${Version}' ros-jazzy-depthai-ros-driver-v3)" \
    && imu_filter_version="$(dpkg-query -W -f='${Version}' ros-jazzy-imu-filter-madgwick)" \
    && rtabmap_odom_version="$(dpkg-query -W -f='${Version}' ros-jazzy-rtabmap-odom)" \
    && case "${core_version}" in "${DEPTHAI_CORE_VERSION}-${DEPTHAI_CORE_DEBIAN_REVISION}"|"${DEPTHAI_CORE_VERSION}-${DEPTHAI_CORE_DEBIAN_REVISION}".*) ;; *) printf 'unsupported standard DepthAI core package: %s\n' "${core_version}" >&2; exit 1;; esac \
    && case "${driver_version}" in "${DEPTHAI_ROS_VERSION}-"*) ;; *) printf 'unsupported DepthAI ROS driver package: %s\n' "${driver_version}" >&2; exit 1;; esac \
    && case "${imu_filter_version}" in "${IMU_TOOLS_VERSION}-"*) ;; *) printf 'unsupported Madgwick package: %s\n' "${imu_filter_version}" >&2; exit 1;; esac \
    && case "${rtabmap_odom_version}" in "${RTABMAP_ROS_VERSION}-"*) ;; *) printf 'unsupported RTAB-Map odometry package: %s\n' "${rtabmap_odom_version}" >&2; exit 1;; esac

# The normal release path uses the unmodified ROS package and Ubuntu libusb.
# Keep the accepted Atlas-patched dependency stage in source only while the
# standard path is being qualified; deleting it requires explicit approval.
FROM atlas-upstream-depthai AS atlas-standard-depthai

RUN depthai_core="$(find /opt/ros/jazzy/lib -name libdepthai_v3-core.so -print -quit)" \
    && test -n "${depthai_core}" \
    && ldd "${depthai_core}" \
        | grep -F 'libusb-1.0.so.0' \
    && ! ldd "${depthai_core}" \
        | grep -F '/opt/atlas-depthai-libusb'

FROM atlas-upstream-depthai AS atlas-patched-depthai

ARG DEPTHAI_CORE_VERSION=3.6.1
ARG DEPTHAI_CORE_DEBIAN_REVISION=2noble
ARG DEPTHAI_CORE_ATLAS_REVISION=atlas2

COPY --from=depthai-vio-builder \
    /src/ros-jazzy-depthai-v3_3.6.1-2noble+atlas2_arm64.deb \
    /tmp/ros-jazzy-depthai-v3-atlas.deb

RUN dpkg --install /tmp/ros-jazzy-depthai-v3-atlas.deb \
    && rm /tmp/ros-jazzy-depthai-v3-atlas.deb \
    && core_version="$(dpkg-query -W -f='${Version}' ros-jazzy-depthai-v3)" \
    && driver_version="$(dpkg-query -W -f='${Version}' ros-jazzy-depthai-ros-driver-v3)" \
    && test "${core_version}" = "${DEPTHAI_CORE_VERSION}-${DEPTHAI_CORE_DEBIAN_REVISION}+${DEPTHAI_CORE_ATLAS_REVISION}" \
    && case "${driver_version}" in "${DEPTHAI_ROS_VERSION}-"*) ;; *) printf 'unsupported DepthAI ROS driver package: %s\n' "${driver_version}" >&2; exit 1;; esac \
    && strings /opt/ros/jazzy/lib/*/libdepthai_v3-core.so \
        | grep -F 'ATLAS_DEPTHAI_VIO_IMU_ORDER' \
    && strings /opt/ros/jazzy/lib/*/libdepthai_v3-core.so \
        | grep -F 'ATLAS_DEPTHAI_VIO_IMAGE_BACKPRESSURE'

COPY --from=depthai-libusb-builder /opt/atlas-depthai-libusb /opt/atlas-depthai-libusb

ENV LD_LIBRARY_PATH=/opt/atlas-depthai-libusb/lib

RUN depthai_core="$(find /opt/ros/jazzy/lib -name libdepthai_v3-core.so -print -quit)" \
    && test -n "${depthai_core}" \
    && ldd "${depthai_core}" \
        | grep -F '/opt/atlas-depthai-libusb/lib/libusb-1.0.so.0'

FROM atlas-standard-depthai AS atlas-runtime

ARG ATLAS_SPATIAL_VERSION=0.1.0-dev

# Normal Atlas changes start here, after the expensive native dependency
# layers. Docker reuses those layers unless the pinned inputs or patches change.
COPY ros2_ws/src /workspace/src
RUN . /opt/ros/jazzy/setup.sh \
    && cd /workspace \
    && colcon build --merge-install --install-base /opt/atlas-spatial-runtime

COPY packaging/entrypoint.sh /usr/local/bin/atlas-spatial-entrypoint
RUN chmod 0755 /usr/local/bin/atlas-spatial-entrypoint \
    && printf '%s\n' "${ATLAS_SPATIAL_VERSION}" > /opt/atlas-spatial-runtime/VERSION

LABEL org.opencontainers.image.title="Atlas Spatial Runtime" \
      io.sunnyside.atlas.spatial-version="${ATLAS_SPATIAL_VERSION}"

ENV ROS_DISTRO=jazzy \
    ROS_LOCALHOST_ONLY=1 \
    DEPTHAI_TELEMETRY=0 \
    ATLAS_SPATIAL_PROVIDER=synthetic \
    ATLAS_SPATIAL_SOURCE_ID=front-depth \
    ATLAS_SPATIAL_SOCKET_PATH=/run/atlas-agent/spatial.sock \
    ATLAS_SPATIAL_CLOUD_SOCKET_PATH=/run/atlas-agent/spatial-cloud.sock

ENTRYPOINT ["/usr/local/bin/atlas-spatial-entrypoint"]
