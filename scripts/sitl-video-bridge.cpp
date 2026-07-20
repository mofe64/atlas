#include <gst/app/gstappsrc.h>
#include <gst/gst.h>
#include <gst/rtsp-server/rtsp-server.h>
#include <glib-unix.h>

#include <gz/msgs/image.pb.h>
#include <gz/transport/Node.hh>

#include <charconv>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>

namespace {

struct Config {
  std::string topic;
  std::string address{"127.0.0.1"};
  std::string port{"8554"};
  std::string mount_path{"/main.264"};
  std::uint32_t frames_per_second{30};
  std::uint32_t bitrate_kbps{2500};
  std::uint32_t frame_timeout_seconds{45};
};

struct VideoFormat {
  std::string gst_format;
  std::uint32_t bytes_per_pixel;
};

struct BridgeState {
  std::mutex mutex;
  std::condition_variable first_frame_received;
  bool source_ready{false};
  std::uint32_t width{0};
  std::uint32_t height{0};
  std::uint32_t bytes_per_pixel{0};
  std::uint32_t frames_per_second{30};
  std::uint64_t frame_number{0};
  std::string gst_format;
  GstAppSrc *app_source{nullptr};
};

void usage(const char *program) {
  std::cerr
      << "Usage: " << program << " --topic TOPIC [options]\n\n"
      << "Streams a Gazebo gz.msgs.Image topic through a local RTSP server.\n\n"
      << "Options:\n"
      << "  --topic TOPIC            Gazebo image topic (required)\n"
      << "  --address ADDRESS        RTSP bind address (default: 127.0.0.1)\n"
      << "  --port PORT              RTSP TCP port (default: 8554)\n"
      << "  --mount-path PATH        RTSP mount path (default: /main.264)\n"
      << "  --fps FPS                Stream frame rate (default: 30)\n"
      << "  --bitrate-kbps RATE      H.264 bitrate in kbit/s (default: 2500)\n"
      << "  --frame-timeout SECONDS  First-frame timeout (default: 45)\n"
      << "  -h, --help               Show this help\n";
}

bool parse_unsigned(std::string_view raw, std::uint32_t minimum,
                    std::uint32_t maximum, std::uint32_t &value) {
  std::uint32_t parsed = 0;
  const auto result =
      std::from_chars(raw.data(), raw.data() + raw.size(), parsed);
  if (result.ec != std::errc{} || result.ptr != raw.data() + raw.size() ||
      parsed < minimum || parsed > maximum) {
    return false;
  }
  value = parsed;
  return true;
}

std::optional<Config> parse_arguments(int argc, char **argv) {
  Config config;
  for (int index = 1; index < argc; ++index) {
    const std::string argument = argv[index];
    if (argument == "-h" || argument == "--help") {
      usage(argv[0]);
      return std::nullopt;
    }
    if (index + 1 >= argc) {
      std::cerr << "missing value for " << argument << '\n';
      usage(argv[0]);
      return std::nullopt;
    }
    const std::string value = argv[++index];
    if (argument == "--topic") {
      config.topic = value;
    } else if (argument == "--address") {
      config.address = value;
    } else if (argument == "--port") {
      std::uint32_t port = 0;
      if (!parse_unsigned(value, 1, 65535, port)) {
        std::cerr << "--port must be between 1 and 65535\n";
        return std::nullopt;
      }
      config.port = value;
    } else if (argument == "--mount-path") {
      config.mount_path = value;
    } else if (argument == "--fps") {
      if (!parse_unsigned(value, 1, 60, config.frames_per_second)) {
        std::cerr << "--fps must be between 1 and 60\n";
        return std::nullopt;
      }
    } else if (argument == "--bitrate-kbps") {
      if (!parse_unsigned(value, 100, 50000, config.bitrate_kbps)) {
        std::cerr << "--bitrate-kbps must be between 100 and 50000\n";
        return std::nullopt;
      }
    } else if (argument == "--frame-timeout") {
      if (!parse_unsigned(value, 1, 600, config.frame_timeout_seconds)) {
        std::cerr << "--frame-timeout must be between 1 and 600\n";
        return std::nullopt;
      }
    } else {
      std::cerr << "unknown option: " << argument << '\n';
      usage(argv[0]);
      return std::nullopt;
    }
  }

  if (config.topic.empty() || config.topic.front() != '/') {
    std::cerr << "--topic must be an absolute Gazebo topic\n";
    return std::nullopt;
  }
  if (config.address.empty()) {
    std::cerr << "--address cannot be empty\n";
    return std::nullopt;
  }
  if (config.mount_path.empty() || config.mount_path.front() != '/') {
    std::cerr << "--mount-path must begin with /\n";
    return std::nullopt;
  }
  return config;
}

std::optional<VideoFormat> video_format(gz::msgs::PixelFormatType format) {
  switch (format) {
    case gz::msgs::L_INT8:
      return VideoFormat{"GRAY8", 1};
    case gz::msgs::RGB_INT8:
      return VideoFormat{"RGB", 3};
    case gz::msgs::RGBA_INT8:
      return VideoFormat{"RGBA", 4};
    case gz::msgs::BGR_INT8:
      return VideoFormat{"BGR", 3};
    case gz::msgs::BGRA_INT8:
      return VideoFormat{"BGRA", 4};
    default:
      return std::nullopt;
  }
}

void publish_frame(BridgeState &state, const gz::msgs::Image &image) {
  const auto format = video_format(image.pixel_format_type());
  if (!format || image.width() == 0 || image.height() == 0) {
    return;
  }

  const std::size_t row_bytes =
      static_cast<std::size_t>(image.width()) * format->bytes_per_pixel;
  const std::size_t source_stride = image.step() == 0 ? row_bytes : image.step();
  const std::size_t source_bytes =
      source_stride * static_cast<std::size_t>(image.height());
  if (source_stride < row_bytes || image.data().size() < source_bytes) {
    return;
  }

  GstAppSrc *app_source = nullptr;
  std::uint64_t frame_number = 0;
  std::uint32_t frames_per_second = 30;
  {
    std::lock_guard<std::mutex> lock(state.mutex);
    if (!state.source_ready) {
      state.width = image.width();
      state.height = image.height();
      state.bytes_per_pixel = format->bytes_per_pixel;
      state.gst_format = format->gst_format;
      state.source_ready = true;
      state.first_frame_received.notify_one();
    }

    if (state.width != image.width() || state.height != image.height() ||
        state.bytes_per_pixel != format->bytes_per_pixel ||
        state.gst_format != format->gst_format || state.app_source == nullptr) {
      return;
    }
    app_source = GST_APP_SRC(gst_object_ref(state.app_source));
    frame_number = state.frame_number++;
    frames_per_second = state.frames_per_second;
  }

  GstBuffer *buffer = gst_buffer_new_allocate(
      nullptr, row_bytes * static_cast<std::size_t>(image.height()), nullptr);
  if (buffer == nullptr) {
    gst_object_unref(app_source);
    return;
  }

  GstMapInfo mapped{};
  if (!gst_buffer_map(buffer, &mapped, GST_MAP_WRITE)) {
    gst_buffer_unref(buffer);
    gst_object_unref(app_source);
    return;
  }
  for (std::uint32_t row = 0; row < image.height(); ++row) {
    std::memcpy(mapped.data + static_cast<std::size_t>(row) * row_bytes,
                image.data().data() + static_cast<std::size_t>(row) * source_stride,
                row_bytes);
  }
  gst_buffer_unmap(buffer, &mapped);
  GST_BUFFER_PTS(buffer) =
      gst_util_uint64_scale(frame_number, GST_SECOND, frames_per_second);
  GST_BUFFER_DTS(buffer) = GST_BUFFER_PTS(buffer);
  GST_BUFFER_DURATION(buffer) =
      gst_util_uint64_scale(1, GST_SECOND, frames_per_second);

  const GstFlowReturn result = gst_app_src_push_buffer(app_source, buffer);
  if (result != GST_FLOW_OK && result != GST_FLOW_FLUSHING) {
    std::cerr << "GStreamer rejected a camera frame: "
              << gst_flow_get_name(result) << '\n';
  }
  gst_object_unref(app_source);
}

void configure_media(GstRTSPMediaFactory *, GstRTSPMedia *media,
                     gpointer user_data) {
  auto &state = *static_cast<BridgeState *>(user_data);
  GstElement *pipeline = gst_rtsp_media_get_element(media);
  GstElement *element = gst_bin_get_by_name_recurse_up(GST_BIN(pipeline), "source");
  gst_object_unref(pipeline);
  if (element == nullptr) {
    std::cerr << "RTSP pipeline did not create its appsrc element\n";
    return;
  }

  GstCaps *caps = nullptr;
  {
    std::lock_guard<std::mutex> lock(state.mutex);
    caps = gst_caps_new_simple("video/x-raw", "format", G_TYPE_STRING,
                               state.gst_format.c_str(), "width", G_TYPE_INT,
                               static_cast<int>(state.width), "height", G_TYPE_INT,
                               static_cast<int>(state.height), "framerate",
                               GST_TYPE_FRACTION,
                               static_cast<int>(state.frames_per_second), 1,
                               nullptr);
  }
  gst_app_src_set_caps(GST_APP_SRC(element), caps);
  gst_caps_unref(caps);

  {
    std::lock_guard<std::mutex> lock(state.mutex);
    if (state.app_source != nullptr) {
      gst_object_unref(state.app_source);
    }
    state.frame_number = 0;
    state.app_source = GST_APP_SRC(element);
  }
}

std::optional<std::string> encoder_pipeline(std::uint32_t bitrate_kbps,
                                            std::uint32_t frames_per_second) {
  GstElementFactory *factory = gst_element_factory_find("x264enc");
  if (factory != nullptr) {
    gst_object_unref(factory);
    return "x264enc tune=zerolatency speed-preset=ultrafast bitrate=" +
           std::to_string(bitrate_kbps) + " key-int-max=" +
           std::to_string(frames_per_second) + " byte-stream=true";
  }

  factory = gst_element_factory_find("vtenc_h264");
  if (factory != nullptr) {
    gst_object_unref(factory);
    return "vtenc_h264 realtime=true allow-frame-reordering=false bitrate=" +
           std::to_string(bitrate_kbps);
  }

  factory = gst_element_factory_find("openh264enc");
  if (factory != nullptr) {
    gst_object_unref(factory);
    return "openh264enc bitrate=" + std::to_string(bitrate_kbps * 1000);
  }
  return std::nullopt;
}

gboolean stop_main_loop(gpointer user_data) {
  g_main_loop_quit(static_cast<GMainLoop *>(user_data));
  return G_SOURCE_REMOVE;
}

}  // namespace

int main(int argc, char **argv) {
  const auto config = parse_arguments(argc, argv);
  if (!config) {
    return argc > 1 && (std::string(argv[1]) == "-h" ||
                        std::string(argv[1]) == "--help")
               ? 0
               : 2;
  }

  gst_init(nullptr, nullptr);
  const auto encoder =
      encoder_pipeline(config->bitrate_kbps, config->frames_per_second);
  if (!encoder) {
    std::cerr << "no supported GStreamer H.264 encoder found; install x264enc, "
                 "vtenc_h264, or openh264enc\n";
    return 1;
  }
  for (const char *element : {"appsrc", "videoconvert", "h264parse",
                              "rtph264pay"}) {
    GstElementFactory *factory = gst_element_factory_find(element);
    if (factory == nullptr) {
      std::cerr << "required GStreamer element not found: " << element << '\n';
      return 1;
    }
    gst_object_unref(factory);
  }

  BridgeState state;
  state.frames_per_second = config->frames_per_second;
  gz::transport::Node node;
  if (!node.Subscribe<gz::msgs::Image>(
          config->topic,
          [&state](const gz::msgs::Image &image) { publish_frame(state, image); })) {
    std::cerr << "could not subscribe to Gazebo camera topic " << config->topic
              << '\n';
    return 1;
  }

  std::cerr << "waiting for Gazebo camera frames on " << config->topic << '\n';
  {
    std::unique_lock<std::mutex> lock(state.mutex);
    if (!state.first_frame_received.wait_for(
            lock, std::chrono::seconds(config->frame_timeout_seconds),
            [&state] { return state.source_ready; })) {
      std::cerr << "timed out waiting for a supported Gazebo camera frame\n";
      return 1;
    }
  }

  GstRTSPServer *server = gst_rtsp_server_new();
  g_object_set(server, "address", config->address.c_str(), "service",
               config->port.c_str(), nullptr);
  GstRTSPMountPoints *mounts = gst_rtsp_server_get_mount_points(server);
  GstRTSPMediaFactory *factory = gst_rtsp_media_factory_new();
  const std::string launch =
      "( appsrc name=source is-live=true block=false format=time "
      "do-timestamp=false max-buffers=2 leaky-type=downstream ! queue "
      "max-size-buffers=2 leaky=downstream ! videoconvert ! "
      "video/x-raw,format=I420 ! " + *encoder +
      " ! h264parse config-interval=-1 ! rtph264pay name=pay0 pt=96 "
      "config-interval=1 )";
  gst_rtsp_media_factory_set_launch(factory, launch.c_str());
  gst_rtsp_media_factory_set_shared(factory, TRUE);
  g_signal_connect(factory, "media-configure", G_CALLBACK(configure_media),
                   &state);
  gst_rtsp_mount_points_add_factory(mounts, config->mount_path.c_str(), factory);
  g_object_unref(mounts);

  if (gst_rtsp_server_attach(server, nullptr) == 0) {
    std::cerr << "could not bind RTSP server to " << config->address << ':'
              << config->port << '\n';
    g_object_unref(server);
    return 1;
  }

  GMainLoop *main_loop = g_main_loop_new(nullptr, FALSE);
  g_unix_signal_add(SIGINT, stop_main_loop, main_loop);
  g_unix_signal_add(SIGTERM, stop_main_loop, main_loop);
  std::cerr << "RTSP stream ready at rtsp://" << config->address << ':'
            << config->port << config->mount_path << " (" << state.width << 'x'
            << state.height << " @ " << config->frames_per_second << " fps)\n";
  g_main_loop_run(main_loop);

  {
    std::lock_guard<std::mutex> lock(state.mutex);
    if (state.app_source != nullptr) {
      gst_object_unref(state.app_source);
      state.app_source = nullptr;
    }
  }
  g_main_loop_unref(main_loop);
  g_object_unref(server);
  return 0;
}
