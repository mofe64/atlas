FROM ubuntu:24.04 AS build

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        cmake \
        g++ \
        libeigen3-dev \
        ninja-build \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /source
COPY third_party/bytetrack/ ./

RUN cmake -S . -B /build -G Ninja -DCMAKE_BUILD_TYPE=Release -DBUILD_TESTING=ON \
    && cmake --build /build --parallel \
    && ctest --test-dir /build --output-on-failure

FROM scratch AS export
COPY --from=build /build/atlas-bytetrack-worker /atlas-bytetrack-worker
