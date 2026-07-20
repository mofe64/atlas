#include "BYTETracker.h"

#include <cstdlib>
#include <iostream>
#include <vector>

namespace {

Object detection(float x)
{
	Object object{};
	object.x = x;
	object.y = 128.0F;
	object.width = 64.0F;
	object.height = 128.0F;
	object.label = 0;
	object.detection_index = 0;
	object.prob = 0.9F;
	return object;
}

bool camera_motion_preserves_large_translation()
{
	STrack::reset_id();
	BYTETracker tracker;
	const std::vector<STrack> first = tracker.update({detection(64.0F)});
	CAMERA_MOTION motion = CAMERA_MOTION::Identity();
	motion(0, 2) = 160.0F;
	const std::vector<STrack> second = tracker.update({detection(224.0F)}, &motion);
	const std::vector<STrack> third = tracker.update({detection(384.0F)}, &motion);
	return first.size() == 1 && second.size() == 1 && third.size() == 1 &&
		first[0].track_id == second[0].track_id && second[0].track_id == third[0].track_id;
}

bool plain_bytetrack_does_not_match_disjoint_boxes()
{
	STrack::reset_id();
	BYTETracker tracker;
	const std::vector<STrack> first = tracker.update({detection(64.0F)});
	const std::vector<STrack> second = tracker.update({detection(224.0F)});
	return first.size() == 1 && (second.empty() || first[0].track_id != second[0].track_id);
}

} // namespace

int main()
{
	if (!camera_motion_preserves_large_translation()) {
		std::cerr << "CMC did not preserve the translated association\n";
		return EXIT_FAILURE;
	}
	if (!plain_bytetrack_does_not_match_disjoint_boxes()) {
		std::cerr << "plain ByteTrack unexpectedly matched disjoint boxes\n";
		return EXIT_FAILURE;
	}
	return EXIT_SUCCESS;
}
