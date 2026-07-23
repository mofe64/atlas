#pragma once

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <limits>
#include <mutex>
#include <utility>
#include <vector>

namespace atlas::depthai_patch {

struct ImuOrderingResult {
    std::size_t forwarded = 0;
    std::size_t dropped = 0;
    std::size_t reordered = 0;
    std::uint64_t totalForwarded = 0;
    std::uint64_t totalDropped = 0;
    std::uint64_t totalReordered = 0;
    std::int64_t lastForwardedTimestampNs = std::numeric_limits<std::int64_t>::min();
};

// Serializes DepthAI's possibly concurrent IMU callbacks, orders every batch,
// and prevents a duplicate or regressive timestamp from reaching Basalt. The
// gate deliberately drops a late sample instead of changing its timestamp:
// inventing time would corrupt preintegration, while one missing measurement
// is a bounded estimator degradation that can be surfaced by health metrics.
class MonotonicImuQueue {
   public:
    template <typename Packet, typename Timestamp, typename Forward>
    ImuOrderingResult forward(std::vector<Packet> packets, Timestamp timestamp, Forward emit) {
        std::lock_guard<std::mutex> lock(mutex_);
        ImuOrderingResult result;

        const bool requiresReordering = !std::is_sorted(
            packets.begin(), packets.end(), [&](const Packet& left, const Packet& right) { return timestamp(left) < timestamp(right); });
        if(requiresReordering) {
            std::stable_sort(
                packets.begin(), packets.end(), [&](const Packet& left, const Packet& right) { return timestamp(left) < timestamp(right); });
            result.reordered = packets.size();
            totalReordered_ += result.reordered;
        }

        for(auto& packet : packets) {
            const std::int64_t packetTimestampNs = timestamp(packet);
            if(packetTimestampNs <= lastForwardedTimestampNs_) {
                result.dropped++;
                totalDropped_++;
                continue;
            }
            emit(packet, packetTimestampNs);
            lastForwardedTimestampNs_ = packetTimestampNs;
            result.forwarded++;
            totalForwarded_++;
        }

        result.totalForwarded = totalForwarded_;
        result.totalDropped = totalDropped_;
        result.totalReordered = totalReordered_;
        result.lastForwardedTimestampNs = lastForwardedTimestampNs_;
        return result;
    }

   private:
    std::mutex mutex_;
    std::int64_t lastForwardedTimestampNs_ = std::numeric_limits<std::int64_t>::min();
    std::uint64_t totalForwarded_ = 0;
    std::uint64_t totalDropped_ = 0;
    std::uint64_t totalReordered_ = 0;
};

}  // namespace atlas::depthai_patch
