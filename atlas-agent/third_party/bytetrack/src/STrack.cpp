#include "STrack.h"

#include <algorithm>
#include <array>
#include <cmath>

namespace {

constexpr float kMinimumBoxDimension = 1e-3F;
constexpr float kMinimumHomogeneousScale = 1e-6F;

bool transform_point(const CAMERA_MOTION& homography, float x, float y, float& transformed_x, float& transformed_y)
{
	const float denominator = homography(2, 0) * x + homography(2, 1) * y + homography(2, 2);
	if (!std::isfinite(denominator) || std::abs(denominator) < kMinimumHomogeneousScale)
	{
		return false;
	}
	transformed_x = (homography(0, 0) * x + homography(0, 1) * y + homography(0, 2)) / denominator;
	transformed_y = (homography(1, 0) * x + homography(1, 1) * y + homography(1, 2)) / denominator;
	return std::isfinite(transformed_x) && std::isfinite(transformed_y);
}

bool transform_box(const CAMERA_MOTION& homography, float center_x, float center_y, float aspect, float height,
	float& transformed_center_x, float& transformed_center_y, float& transformed_aspect, float& transformed_height)
{
	height = std::max(std::abs(height), kMinimumBoxDimension);
	const float width = std::max(std::abs(aspect * height), kMinimumBoxDimension);
	const float left = center_x - width / 2.0F;
	const float right = center_x + width / 2.0F;
	const float top = center_y - height / 2.0F;
	const float bottom = center_y + height / 2.0F;
	const std::array<std::array<float, 2>, 4> corners{{
		{{left, top}}, {{right, top}}, {{right, bottom}}, {{left, bottom}},
	}};
	float minimum_x = 0.0F;
	float maximum_x = 0.0F;
	float minimum_y = 0.0F;
	float maximum_y = 0.0F;
	for (std::size_t index = 0; index < corners.size(); ++index)
	{
		float x = 0.0F;
		float y = 0.0F;
		if (!transform_point(homography, corners[index][0], corners[index][1], x, y))
		{
			return false;
		}
		if (index == 0)
		{
			minimum_x = maximum_x = x;
			minimum_y = maximum_y = y;
		}
		else
		{
			minimum_x = std::min(minimum_x, x);
			maximum_x = std::max(maximum_x, x);
			minimum_y = std::min(minimum_y, y);
			maximum_y = std::max(maximum_y, y);
		}
	}
	const float transformed_width = std::max(maximum_x - minimum_x, kMinimumBoxDimension);
	transformed_height = std::max(maximum_y - minimum_y, kMinimumBoxDimension);
	transformed_center_x = (minimum_x + maximum_x) / 2.0F;
	transformed_center_y = (minimum_y + maximum_y) / 2.0F;
	transformed_aspect = transformed_width / transformed_height;
	return std::isfinite(transformed_center_x) && std::isfinite(transformed_center_y) &&
		std::isfinite(transformed_aspect) && std::isfinite(transformed_height);
}

bool transform_state(const KAL_MEAN& state, const CAMERA_MOTION& homography, KAL_MEAN& transformed)
{
	float center_x = 0.0F;
	float center_y = 0.0F;
	float aspect = 0.0F;
	float height = 0.0F;
	if (!transform_box(homography, state(0), state(1), state(2), state(3), center_x, center_y, aspect, height))
	{
		return false;
	}

	float future_center_x = 0.0F;
	float future_center_y = 0.0F;
	float future_aspect = 0.0F;
	float future_height = 0.0F;
	if (!transform_box(homography, state(0) + state(4), state(1) + state(5),
		state(2) + state(6), state(3) + state(7), future_center_x, future_center_y,
		future_aspect, future_height))
	{
		return false;
	}

	transformed << center_x, center_y, aspect, height,
		future_center_x - center_x, future_center_y - center_y,
		future_aspect - aspect, future_height - height;
	return transformed.allFinite();
}

bool transform_covariance(const KAL_MEAN& state, const KAL_COVA& covariance,
	const CAMERA_MOTION& homography, KAL_COVA& transformed)
{
	Eigen::Matrix<float, 8, 8, Eigen::RowMajor> jacobian;
	for (int column = 0; column < 8; ++column)
	{
		const float epsilon = std::max(1e-3F, std::abs(state(column)) * 1e-4F);
		KAL_MEAN plus = state;
		KAL_MEAN minus = state;
		plus(column) += epsilon;
		minus(column) -= epsilon;
		KAL_MEAN transformed_plus;
		KAL_MEAN transformed_minus;
		if (!transform_state(plus, homography, transformed_plus) ||
			!transform_state(minus, homography, transformed_minus))
		{
			return false;
		}
		for (int row = 0; row < 8; ++row)
		{
			jacobian(row, column) = (transformed_plus(row) - transformed_minus(row)) / (2.0F * epsilon);
		}
	}
	transformed = jacobian * covariance * jacobian.transpose();
	transformed = ((transformed + transformed.transpose()) * 0.5F).eval();
	transformed.diagonal().array() += 1e-6F;
	return transformed.allFinite();
}

} // namespace

int STrack::next_track_id = 0;

STrack::STrack(vector<float> tlwh_, float score, int detection_index)
{
	_tlwh.resize(4);
	_tlwh.assign(tlwh_.begin(), tlwh_.end());

	is_activated = false;
	track_id = 0;
	state = TrackState::New;
	
	tlwh.resize(4);
	tlbr.resize(4);

	static_tlwh();
	static_tlbr();
	frame_id = 0;
	tracklet_len = 0;
	this->score = score;
	this->detection_index = detection_index;
	start_frame = 0;
}

STrack::~STrack()
{
}

void STrack::activate(byte_kalman::KalmanFilter &kalman_filter, int frame_id)
{
	this->kalman_filter = kalman_filter;
	this->track_id = this->next_id();

	vector<float> _tlwh_tmp(4);
	_tlwh_tmp[0] = this->_tlwh[0];
	_tlwh_tmp[1] = this->_tlwh[1];
	_tlwh_tmp[2] = this->_tlwh[2];
	_tlwh_tmp[3] = this->_tlwh[3];
	vector<float> xyah = tlwh_to_xyah(_tlwh_tmp);
	DETECTBOX xyah_box;
	xyah_box[0] = xyah[0];
	xyah_box[1] = xyah[1];
	xyah_box[2] = xyah[2];
	xyah_box[3] = xyah[3];
	auto mc = this->kalman_filter.initiate(xyah_box);
	this->mean = mc.first;
	this->covariance = mc.second;

	static_tlwh();
	static_tlbr();

	this->tracklet_len = 0;
	this->state = TrackState::Tracked;
	if (frame_id == 1)
	{
		this->is_activated = true;
	}
	//this->is_activated = true;
	this->frame_id = frame_id;
	this->start_frame = frame_id;
}

void STrack::re_activate(STrack &new_track, int frame_id, bool new_id)
{
	vector<float> xyah = tlwh_to_xyah(new_track.tlwh);
	DETECTBOX xyah_box;
	xyah_box[0] = xyah[0];
	xyah_box[1] = xyah[1];
	xyah_box[2] = xyah[2];
	xyah_box[3] = xyah[3];
	auto mc = this->kalman_filter.update(this->mean, this->covariance, xyah_box);
	this->mean = mc.first;
	this->covariance = mc.second;

	static_tlwh();
	static_tlbr();

	this->tracklet_len = 0;
	this->state = TrackState::Tracked;
	this->is_activated = true;
	this->frame_id = frame_id;
	this->score = new_track.score;
	this->detection_index = new_track.detection_index;
	if (new_id)
		this->track_id = next_id();
}

void STrack::update(STrack &new_track, int frame_id)
{
	this->frame_id = frame_id;
	this->tracklet_len++;

	vector<float> xyah = tlwh_to_xyah(new_track.tlwh);
	DETECTBOX xyah_box;
	xyah_box[0] = xyah[0];
	xyah_box[1] = xyah[1];
	xyah_box[2] = xyah[2];
	xyah_box[3] = xyah[3];

	auto mc = this->kalman_filter.update(this->mean, this->covariance, xyah_box);
	this->mean = mc.first;
	this->covariance = mc.second;

	static_tlwh();
	static_tlbr();

	this->state = TrackState::Tracked;
	this->is_activated = true;

	this->score = new_track.score;
	this->detection_index = new_track.detection_index;
}

void STrack::static_tlwh()
{
	if (this->state == TrackState::New)
	{
		tlwh[0] = _tlwh[0];
		tlwh[1] = _tlwh[1];
		tlwh[2] = _tlwh[2];
		tlwh[3] = _tlwh[3];
		return;
	}

	tlwh[0] = mean[0];
	tlwh[1] = mean[1];
	tlwh[2] = mean[2];
	tlwh[3] = mean[3];

	tlwh[2] *= tlwh[3];
	tlwh[0] -= tlwh[2] / 2;
	tlwh[1] -= tlwh[3] / 2;
}

void STrack::static_tlbr()
{
	tlbr.clear();
	tlbr.assign(tlwh.begin(), tlwh.end());
	tlbr[2] += tlbr[0];
	tlbr[3] += tlbr[1];
}

vector<float> STrack::tlwh_to_xyah(vector<float> tlwh_tmp)
{
	vector<float> tlwh_output = tlwh_tmp;
	tlwh_output[0] += tlwh_output[2] / 2;
	tlwh_output[1] += tlwh_output[3] / 2;
	tlwh_output[2] /= tlwh_output[3];
	return tlwh_output;
}

vector<float> STrack::to_xyah()
{
	return tlwh_to_xyah(tlwh);
}

vector<float> STrack::tlbr_to_tlwh(vector<float> &tlbr)
{
	tlbr[2] -= tlbr[0];
	tlbr[3] -= tlbr[1];
	return tlbr;
}

void STrack::mark_lost()
{
	state = TrackState::Lost;
}

void STrack::mark_removed()
{
	state = TrackState::Removed;
}

int STrack::next_id()
{
	next_track_id++;
	return next_track_id;
}

void STrack::reset_id()
{
	next_track_id = 0;
}

int STrack::end_frame()
{
	return this->frame_id;
}

void STrack::multi_predict(vector<STrack*> &stracks, byte_kalman::KalmanFilter &kalman_filter)
{
	for (int i = 0; i < stracks.size(); i++)
	{
		if (stracks[i]->state != TrackState::Tracked)
		{
			stracks[i]->mean[7] = 0;
		}
		kalman_filter.predict(stracks[i]->mean, stracks[i]->covariance);
		stracks[i]->static_tlwh();
		stracks[i]->static_tlbr();
	}
}

void STrack::multi_camera_motion(vector<STrack*> &stracks, const CAMERA_MOTION &homography)
{
	for (STrack* track : stracks)
	{
		KAL_MEAN transformed_mean;
		KAL_COVA transformed_covariance;
		if (!transform_state(track->mean, homography, transformed_mean) ||
			!transform_covariance(track->mean, track->covariance, homography, transformed_covariance))
		{
			continue;
		}
		track->mean = transformed_mean;
		track->covariance = transformed_covariance;
		track->static_tlwh();
		track->static_tlbr();
	}
}
