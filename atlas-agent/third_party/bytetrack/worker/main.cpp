#include "BYTETracker.h"

#include <cmath>
#include <cstdlib>
#include <iostream>
#include <limits>
#include <map>
#include <memory>
#include <sstream>
#include <stdexcept>
#include <string>
#include <tuple>
#include <utility>
#include <vector>

namespace {

constexpr std::size_t kMaximumDetections = 1000;

struct Configuration {
	int frame_rate = 30;
	int track_buffer = 30;
	float track_threshold = 0.5F;
	float high_threshold = 0.6F;
	float match_threshold = 0.8F;
};

std::vector<std::string> split(const std::string& value, char delimiter)
{
	std::vector<std::string> fields;
	std::stringstream stream(value);
	std::string field;
	while (std::getline(stream, field, delimiter)) {
		fields.push_back(field);
	}
	if (!value.empty() && value.back() == delimiter) {
		fields.emplace_back();
	}
	return fields;
}

int parse_int(const std::string& value, const char* name)
{
	std::size_t consumed = 0;
	int parsed = 0;
	try {
		parsed = std::stoi(value, &consumed);
	} catch (const std::exception&) {
		throw std::runtime_error(std::string(name) + " must be an integer");
	}
	if (consumed != value.size()) {
		throw std::runtime_error(std::string(name) + " must be an integer");
	}
	return parsed;
}

float parse_float(const std::string& value, const char* name)
{
	std::size_t consumed = 0;
	float parsed = 0.0F;
	try {
		parsed = std::stof(value, &consumed);
	} catch (const std::exception&) {
		throw std::runtime_error(std::string(name) + " must be numeric");
	}
	if (consumed != value.size() || !std::isfinite(parsed)) {
		throw std::runtime_error(std::string(name) + " must be finite");
	}
	return parsed;
}

Configuration parse_arguments(int argc, char** argv)
{
	Configuration config;
	for (int index = 1; index < argc; index += 2) {
		if (index + 1 >= argc) {
			throw std::runtime_error(std::string("missing value for ") + argv[index]);
		}
		const std::string name = argv[index];
		const std::string value = argv[index + 1];
		if (name == "--frame-rate") {
			config.frame_rate = parse_int(value, "frame rate");
		} else if (name == "--track-buffer") {
			config.track_buffer = parse_int(value, "track buffer");
		} else if (name == "--track-threshold") {
			config.track_threshold = parse_float(value, "track threshold");
		} else if (name == "--high-threshold") {
			config.high_threshold = parse_float(value, "high threshold");
		} else if (name == "--match-threshold") {
			config.match_threshold = parse_float(value, "match threshold");
		} else {
			throw std::runtime_error("unknown argument: " + name);
		}
	}
	if (config.frame_rate < 1 || config.frame_rate > 240) {
		throw std::runtime_error("frame rate must be between 1 and 240");
	}
	if (config.track_buffer < 1 || config.track_buffer > 300) {
		throw std::runtime_error("track buffer must be between 1 and 300");
	}
	if (config.track_threshold < 0.0F || config.track_threshold > 1.0F ||
		config.high_threshold < 0.0F || config.high_threshold > 1.0F ||
		config.track_threshold >= config.high_threshold) {
		throw std::runtime_error("thresholds require 0 <= track < high <= 1");
	}
	if (config.match_threshold < 0.0F || config.match_threshold > 1.0F) {
		throw std::runtime_error("match threshold must be between 0 and 1");
	}
	return config;
}

std::string safe_error(std::string value)
{
	for (char& character : value) {
		if (character == '\t' || character == '\n' || character == '\r') {
			character = ' ';
		}
	}
	return value;
}

class Worker {
public:
	explicit Worker(Configuration config) : config_(std::move(config)) {}

	void run()
	{
		std::string line;
		while (std::getline(std::cin, line)) {
			handle(line);
		}
	}

private:
	void handle(const std::string& line)
	{
		const std::vector<std::string> fields = split(line, '\t');
		const std::string request_id = fields.size() > 2 ? fields[2] : "0";
		try {
			if (fields.size() < 3 || fields[0] != "v1") {
				throw std::runtime_error("unsupported worker protocol");
			}
			if (fields[1] == "reset") {
				if (fields.size() != 3) {
					throw std::runtime_error("reset request has unexpected fields");
				}
				trackers_.clear();
				STrack::reset_id();
				std::cout << "v1\treset_ok\t" << request_id << '\n' << std::flush;
				return;
			}
			if (fields[1] != "track") {
				if (fields[1] != "track_cmc") {
					throw std::runtime_error("unsupported worker operation");
				}
			}
			handle_track(fields, request_id, fields[1] == "track_cmc");
		} catch (const std::exception& error) {
			std::cout << "v1\terror\t" << request_id << '\t' << safe_error(error.what()) << '\n' << std::flush;
		}
	}

	void handle_track(const std::vector<std::string>& fields, const std::string& request_id, bool camera_motion_enabled)
	{
		const std::size_t header_size = camera_motion_enabled ? 7 : 6;
		if (fields.size() < header_size) {
			throw std::runtime_error("track request is incomplete");
		}
		const int image_width = parse_int(fields[3], "image width");
		const int image_height = parse_int(fields[4], "image height");
		const int count = parse_int(fields[5], "detection count");
		if (image_width < 1 || image_height < 1) {
			throw std::runtime_error("image dimensions must be positive");
		}
		if (count < 0 || static_cast<std::size_t>(count) > kMaximumDetections ||
			fields.size() != static_cast<std::size_t>(count) + header_size) {
			throw std::runtime_error("invalid detection count");
		}

		CAMERA_MOTION camera_motion;
		bool apply_camera_motion = false;
		if (camera_motion_enabled && fields[6] != "none") {
			const std::vector<std::string> values = split(fields[6], ',');
			if (values.size() != 9) {
				throw std::runtime_error("camera motion must contain nine fields");
			}
			CAMERA_MOTION normalized;
			for (int row = 0; row < 3; ++row) {
				for (int column = 0; column < 3; ++column) {
					normalized(row, column) = parse_float(values[row * 3 + column], "camera motion");
				}
			}
			CAMERA_MOTION scale = CAMERA_MOTION::Identity();
			CAMERA_MOTION inverse_scale = CAMERA_MOTION::Identity();
			scale(0, 0) = static_cast<float>(image_width);
			scale(1, 1) = static_cast<float>(image_height);
			inverse_scale(0, 0) = 1.0F / static_cast<float>(image_width);
			inverse_scale(1, 1) = 1.0F / static_cast<float>(image_height);
			camera_motion = scale * normalized * inverse_scale;
			if (!camera_motion.allFinite()) {
				throw std::runtime_error("camera motion transform is invalid");
			}
			apply_camera_motion = true;
		}

		std::map<int, std::vector<Object>> grouped;
		for (int index = 0; index < count; ++index) {
			const std::vector<std::string> detection = split(fields[index + header_size], ',');
			if (detection.size() != 7) {
				throw std::runtime_error("detection record must contain seven fields");
			}
			Object object{};
			object.detection_index = parse_int(detection[0], "detection index");
			object.label = parse_int(detection[1], "class id");
			object.prob = parse_float(detection[2], "confidence");
			object.x = parse_float(detection[3], "box x");
			object.y = parse_float(detection[4], "box y");
			object.width = parse_float(detection[5], "box width");
			object.height = parse_float(detection[6], "box height");
			if (object.detection_index < 0 || object.detection_index >= count ||
				object.prob < 0.0F || object.prob > 1.0F || object.x < 0.0F || object.y < 0.0F ||
				object.width <= 0.0F || object.height <= 0.0F ||
				object.x + object.width > static_cast<float>(image_width) + 0.01F ||
				object.y + object.height > static_cast<float>(image_height) + 0.01F) {
				throw std::runtime_error("detection is outside the frame or has invalid confidence");
			}
			grouped[object.label].push_back(object);
		}

		for (const auto& entry : grouped) {
			if (trackers_.find(entry.first) == trackers_.end()) {
				trackers_[entry.first] = std::make_unique<BYTETracker>(
					config_.frame_rate, config_.track_buffer, config_.track_threshold,
					config_.high_threshold, config_.match_threshold);
			}
		}

		std::vector<std::tuple<int, int, int>> associations;
		for (auto& entry : trackers_) {
			const auto detections = grouped.find(entry.first);
			const std::vector<Object> empty;
			const std::vector<Object>& objects = detections == grouped.end() ? empty : detections->second;
			const CAMERA_MOTION* motion = apply_camera_motion ? &camera_motion : nullptr;
			for (const STrack& track : entry.second->update(objects, motion)) {
				associations.emplace_back(track.detection_index, entry.first, track.track_id);
			}
		}

		std::cout << "v1\tresult\t" << request_id << '\t' << associations.size();
		for (const auto& association : associations) {
			std::cout << '\t' << std::get<0>(association) << ',' << std::get<1>(association)
				<< ',' << std::get<2>(association);
		}
		std::cout << '\n' << std::flush;
	}

	Configuration config_;
	std::map<int, std::unique_ptr<BYTETracker>> trackers_;
};

} // namespace

int main(int argc, char** argv)
{
	try {
		Worker(parse_arguments(argc, argv)).run();
		return EXIT_SUCCESS;
	} catch (const std::exception& error) {
		std::cerr << "atlas-bytetrack-worker: " << error.what() << '\n';
		return EXIT_FAILURE;
	}
}
