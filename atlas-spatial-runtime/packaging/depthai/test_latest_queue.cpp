#include "AtlasLatestQueue.hpp"

#include <cassert>
#include <cstddef>
#include <deque>
#include <vector>

namespace {

template <typename Value>
class FakeBoundedQueue {
   public:
    explicit FakeBoundedQueue(std::size_t capacity) : capacity_(capacity) {}

    bool try_push(const Value& value) {
        if(values_.size() >= capacity_) return false;
        values_.push_back(value);
        return true;
    }

    bool try_push(Value&& value) {
        if(values_.size() >= capacity_) return false;
        values_.push_back(std::move(value));
        return true;
    }

    bool try_pop(Value& value) {
        if(values_.empty()) return false;
        value = std::move(values_.front());
        values_.pop_front();
        return true;
    }

    std::vector<Value> values() const {
        return {values_.begin(), values_.end()};
    }

   private:
    std::size_t capacity_;
    std::deque<Value> values_;
};

}  // namespace

int main() {
    FakeBoundedQueue<int> queue(2);

    const auto first = atlas::depthai_patch::tryPushLatest(queue, 10);
    const auto second = atlas::depthai_patch::tryPushLatest(queue, 20);
    assert(first.accepted && second.accepted);
    assert(first.dropped() == 0 && second.dropped() == 0);
    assert((queue.values() == std::vector<int>{10, 20}));

    const auto overloaded = atlas::depthai_patch::tryPushLatest(queue, 30);
    assert(overloaded.accepted);
    assert(overloaded.evictedOldest);
    assert(!overloaded.droppedIncoming);
    assert(overloaded.dropped() == 1);
    assert((queue.values() == std::vector<int>{20, 30}));

    FakeBoundedQueue<int> disabled(0);
    const auto rejected = atlas::depthai_patch::tryPushLatest(disabled, 40);
    assert(!rejected.accepted);
    assert(!rejected.evictedOldest);
    assert(rejected.droppedIncoming);
    assert(rejected.dropped() == 1);

    return 0;
}
