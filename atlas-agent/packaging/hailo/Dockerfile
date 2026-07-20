ARG BASE_IMAGE=docker.io/library/debian@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df
FROM ${BASE_IMAGE}

ARG HAILORT_VERSION=4.20.0-1
ARG HAILORT_SHA256=12058986a3cef2d3ccae98033e2ba0f57d63bbc1cf68d3541668329ea6a49e31
ARG PYHAILORT_VERSION=4.20.0-1
ARG PYHAILORT_SHA256=73fe05c39cd74fd6ac345696cbe08415fd0b84828d81a202491aa5d43deff1e7
ARG TAPPAS_VERSION=3.31.0+1-1
ARG TAPPAS_SHA256=52879a3027b78960c066c87bd230a63df744ed6cffe37c8adf7361ee70fd2998
ARG HAILO_DRIVER_VERSION=4.20.0
ARG HAILO_FIRMWARE_VERSION=4.20.0

LABEL org.opencontainers.image.title="Atlas Hailo perception runtime" \
      org.opencontainers.image.source="https://github.com/sunnyside/atlas" \
      io.atlas.hailo.driver.version="${HAILO_DRIVER_VERSION}" \
      io.atlas.hailo.firmware.version="${HAILO_FIRMWARE_VERSION}" \
      io.atlas.hailo.hailort.version="${HAILORT_VERSION}" \
      io.atlas.hailo.tappas.version="${TAPPAS_VERSION}"

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl python3-opencv python3-numpy; \
    curl -fsSL "https://archive.raspberrypi.com/debian/pool/main/h/hailort/hailort_${HAILORT_VERSION}_arm64.deb" -o /tmp/hailort.deb; \
    curl -fsSL "https://archive.raspberrypi.com/debian/pool/main/p/pyhailort/python3-hailort_${PYHAILORT_VERSION}_arm64.deb" -o /tmp/python3-hailort.deb; \
    curl -fsSL "https://archive.raspberrypi.com/debian/pool/main/h/hailo-tappas-core/hailo-tappas-core_${TAPPAS_VERSION}_arm64.deb" -o /tmp/hailo-tappas-core.deb; \
    printf '%s  %s\n' "${HAILORT_SHA256}" /tmp/hailort.deb | sha256sum -c -; \
    printf '%s  %s\n' "${PYHAILORT_SHA256}" /tmp/python3-hailort.deb | sha256sum -c -; \
    printf '%s  %s\n' "${TAPPAS_SHA256}" /tmp/hailo-tappas-core.deb | sha256sum -c -; \
    apt-get install -y --no-install-recommends /tmp/hailort.deb /tmp/python3-hailort.deb /tmp/hailo-tappas-core.deb; \
    rm -f /tmp/*.deb; \
    rm -rf /var/lib/apt/lists/*

COPY atlas-hailort-adapter.py /usr/local/bin/atlas-hailort-adapter
COPY atlas-hailo-container-check /usr/local/bin/atlas-hailo-container-check

RUN chmod 0755 /usr/local/bin/atlas-hailort-adapter /usr/local/bin/atlas-hailo-container-check

ENV HOME=/tmp \
    GST_REGISTRY=/tmp/gstreamer-registry.bin \
    LD_PRELOAD=/usr/lib/aarch64-linux-gnu/libgomp.so.1 \
    PYTHONDONTWRITEBYTECODE=1

HEALTHCHECK --interval=30s --timeout=20s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/atlas-hailo-container-check", "--software-only"]

ENTRYPOINT ["/usr/local/bin/atlas-hailort-adapter"]
