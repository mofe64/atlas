#include "AtlasMonotonicImuQueue.hpp"

#include <algorithm>
#include <cassert>
#include <cstdint>
#include <mutex>
#include <thread>
#include <vector>

int main() {
    atlas::depthai_patch::MonotonicImuQueue gate;
    std::vector<std::int64_t> emitted;

    auto timestamp = [](std::int64_t value) { return value; };
    auto emit = [&](std::int64_t value, std::int64_t timestampNs) {
        assert(value == timestampNs);
        emitted.push_back(value);
    };

    const auto first = gate.forward<std::int64_t>({40, 44, 42, 44, 48}, timestamp, emit);
    assert((emitted == std::vector<std::int64_t>{40, 42, 44, 48}));
    assert(first.forwarded == 4);
    assert(first.dropped == 1);
    assert(first.reordered == 5);

    const auto second = gate.forward<std::int64_t>({47, 52, 50}, timestamp, emit);
    assert((emitted == std::vector<std::int64_t>{40, 42, 44, 48, 50, 52}));
    assert(second.forwarded == 2);
    assert(second.dropped == 1);
    assert(second.totalDropped == 2);
    assert(second.lastForwardedTimestampNs == 52);

    atlas::depthai_patch::MonotonicImuQueue concurrentGate;
    std::vector<std::int64_t> concurrentEmitted;
    std::mutex outputMutex;
    auto concurrentEmit = [&](std::int64_t value, std::int64_t) {
        std::lock_guard<std::mutex> lock(outputMutex);
        concurrentEmitted.push_back(value);
    };
    std::thread older([&] { concurrentGate.forward<std::int64_t>({100, 104, 108}, timestamp, concurrentEmit); });
    std::thread newer([&] { concurrentGate.forward<std::int64_t>({106, 110, 112}, timestamp, concurrentEmit); });
    older.join();
    newer.join();
    assert(std::is_sorted(concurrentEmitted.begin(), concurrentEmitted.end()));
    assert(std::adjacent_find(concurrentEmitted.begin(), concurrentEmitted.end()) == concurrentEmitted.end());

    return 0;
}
