#pragma once

#include <cstddef>
#include <utility>

namespace atlas::depthai_patch {

struct LatestQueuePushResult {
    bool accepted = false;
    bool evictedOldest = false;
    bool droppedIncoming = false;

    std::size_t dropped() const {
        return (evictedOldest ? 1U : 0U) + (droppedIncoming ? 1U : 0U);
    }
};

// A real-time estimator must not backpressure the camera transport. Prefer the
// newest observation when a bounded processing queue is full: evict one stale
// observation, then retry without ever waiting for the consumer.
template <typename Queue, typename Value>
LatestQueuePushResult tryPushLatest(Queue& queue, Value value) {
    LatestQueuePushResult result;
    if(queue.try_push(value)) {
        result.accepted = true;
        return result;
    }

    Value evicted{};
    result.evictedOldest = queue.try_pop(evicted);
    result.accepted = queue.try_push(std::move(value));
    result.droppedIncoming = !result.accepted;
    return result;
}

}  // namespace atlas::depthai_patch
