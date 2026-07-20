cmake_minimum_required(VERSION 3.16)
project(atlas_bytetrack_worker LANGUAGES CXX)

include(CTest)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

find_path(EIGEN3_INCLUDE_DIR Eigen/Core PATH_SUFFIXES eigen3 REQUIRED)

add_executable(atlas-bytetrack-worker
    worker/main.cpp
    src/BYTETracker.cpp
    src/STrack.cpp
    src/kalmanFilter.cpp
    src/lapjv.cpp
    src/utils.cpp
)
target_include_directories(atlas-bytetrack-worker PRIVATE include "${EIGEN3_INCLUDE_DIR}")

install(TARGETS atlas-bytetrack-worker RUNTIME DESTINATION bin)

if(BUILD_TESTING)
    add_executable(atlas-bytetrack-cmc-test
        tests/bytetrack_cmc_test.cpp
        src/BYTETracker.cpp
        src/STrack.cpp
        src/kalmanFilter.cpp
        src/lapjv.cpp
        src/utils.cpp
    )
    target_include_directories(atlas-bytetrack-cmc-test PRIVATE include "${EIGEN3_INCLUDE_DIR}")
    add_test(NAME atlas-bytetrack-cmc-test COMMAND atlas-bytetrack-cmc-test)
endif()
