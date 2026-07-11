#!/usr/bin/env python3
"""Atlas onboard video/inference runner.

The MVP keeps video transport local to the Pi:
  A8 RTSP -> GStreamer/Hailo overlay pipeline -> MediaMTX RTSP publish
"""

from __future__ import annotations

import argparse
import os
import shlex
import shutil
import signal
import subprocess
import sys
from pathlib import Path


DEFAULT_YOLO_POSTPROCESS_SO = "/usr/lib/aarch64-linux-gnu/hailo/tappas/post_processes/libyolo_hailortpp_post.so"
DEFAULT_YOLO_POSTPROCESS_FUNCTION = "filter"


def env(key: str, default: str = "") -> str:
    return os.environ.get(key, default).strip()


def input_codec_for_url(input_url: str) -> str:
    configured = env("ATLAS_A8_RTP_CODEC", "auto").lower()
    if configured in {"auto", "h264", "h265"}:
        return configured
    if configured == "":
        return "auto"
    if configured not in {"auto", "h264", "h265"}:
        raise SystemExit("ATLAS_A8_RTP_CODEC must be one of: auto, h264, h265")
    return "auto"


def input_decode_stages(codec: str) -> list[str]:
    if codec == "auto":
        return [
            "application/x-rtp,media=video",
            "!",
            "decodebin",
        ]

    if codec == "h265":
        return [
            "rtph265depay",
            "!",
            "h265parse",
            "!",
            "avdec_h265",
        ]

    return [
        "rtph264depay",
        "!",
        "h264parse",
        "!",
        "avdec_h264",
    ]


def leaky_queue(name: str) -> list[str]:
    return [
        "queue",
        f"name={name}",
        "leaky=downstream",
        "max-size-buffers=1",
        "max-size-bytes=0",
        "max-size-time=0",
    ]


def build_default_pipeline() -> list[str]:
    input_url = env("ATLAS_A8_RTSP_URL", "rtsp://192.168.144.25:8554/main.264")
    output_url = env("ATLAS_PROCESSED_RTSP_URL", "rtsp://127.0.0.1:8554/atlas")
    model_path = env("ATLAS_PERCEPTION_MODEL_PATH")
    postprocess_so = env("ATLAS_PERCEPTION_POSTPROCESS_SO", DEFAULT_YOLO_POSTPROCESS_SO)
    postprocess_function = env("ATLAS_PERCEPTION_POSTPROCESS_FUNCTION", DEFAULT_YOLO_POSTPROCESS_FUNCTION)
    postprocess_config = env("ATLAS_PERCEPTION_POSTPROCESS_CONFIG")
    pipeline_mode = env("ATLAS_VIDEO_PIPELINE_MODE", "hailo").lower()
    bitrate = env("ATLAS_VIDEO_BITRATE_KBPS", "2500")
    width = env("ATLAS_PERCEPTION_WIDTH", "640")
    height = env("ATLAS_PERCEPTION_HEIGHT", "640")
    input_transport = env("ATLAS_A8_RTSP_TRANSPORT", "tcp").lower()
    input_latency_ms = env("ATLAS_A8_RTSP_LATENCY_MS", "50")
    key_int_max = env("ATLAS_VIDEO_KEY_INT_MAX", "15")

    if pipeline_mode not in {"hailo", "passthrough"}:
        raise SystemExit("ATLAS_VIDEO_PIPELINE_MODE must be one of: hailo, passthrough")
    if pipeline_mode == "hailo" and not model_path:
        raise SystemExit("ATLAS_PERCEPTION_MODEL_PATH is required for the Hailo pipeline")
    if pipeline_mode == "hailo" and not Path(model_path).is_file():
        raise SystemExit(f"ATLAS_PERCEPTION_MODEL_PATH does not exist: {model_path}")
    if pipeline_mode == "hailo" and not Path(postprocess_so).is_file():
        raise SystemExit(
            "ATLAS_PERCEPTION_POSTPROCESS_SO does not exist: "
            f"{postprocess_so}; Hailo YOLO boxes require the TAPPAS postprocess library"
        )
    if pipeline_mode == "hailo" and postprocess_config and not Path(postprocess_config).is_file():
        raise SystemExit(f"ATLAS_PERCEPTION_POSTPROCESS_CONFIG does not exist: {postprocess_config}")
    if input_transport not in {"tcp", "udp"}:
        raise SystemExit("ATLAS_A8_RTSP_TRANSPORT must be one of: tcp, udp")

    codec = input_codec_for_url(input_url)
    pipeline = [
        "gst-launch-1.0",
        "-e",
        "rtspsrc",
        f"location={input_url}",
        f"protocols={input_transport}",
        f"latency={input_latency_ms}",
        "drop-on-latency=true",
        "do-retransmission=false",
        "!",
        *input_decode_stages(codec),
        "!",
        *leaky_queue("atlas_decode_drop"),
        "!",
        "videoconvert",
        "!",
        "videoscale",
        "!",
        f"video/x-raw,format=RGB,width={width},height={height}",
    ]

    if pipeline_mode == "hailo":
        pipeline += [
            "!",
            *leaky_queue("atlas_hailo_drop"),
            "!",
            "hailonet",
            f"hef-path={model_path}",
            "!",
            "hailofilter",
            f"so-path={postprocess_so}",
            f"function-name={postprocess_function}",
            "qos=false",
        ]

        if postprocess_config:
            pipeline += [
                f"config-path={postprocess_config}",
            ]

        pipeline += [
            "!",
            "hailooverlay",
        ]

    pipeline += [
        "!",
        *leaky_queue("atlas_encode_drop"),
        "!",
        "videoconvert",
        "!",
        "x264enc",
        "tune=zerolatency",
        "speed-preset=ultrafast",
        f"bitrate={bitrate}",
        f"key-int-max={key_int_max}",
        "bframes=0",
        "!",
        "h264parse",
        "config-interval=1",
        "!",
        "rtspclientsink",
        f"location={output_url}",
        "protocols=tcp",
    ]
    return pipeline


def build_command() -> list[str]:
    override = env("ATLAS_VIDEO_AGENT_COMMAND")
    if override:
        return ["/bin/bash", "-lc", override]

    gst = shutil.which("gst-launch-1.0")
    if not gst:
        raise SystemExit("gst-launch-1.0 is required; install GStreamer packages first")

    pipeline = build_default_pipeline()
    pipeline[0] = gst
    return pipeline


def run(args: argparse.Namespace) -> int:
    command = build_command()
    if args.dry_run:
        print(" ".join(shlex.quote(part) for part in command))
        return 0

    process = subprocess.Popen(command)

    def stop_process(signum: int, _frame: object) -> None:
        if process.poll() is None:
            process.terminate()

    signal.signal(signal.SIGTERM, stop_process)
    signal.signal(signal.SIGINT, stop_process)

    return process.wait()


def main() -> int:
    parser = argparse.ArgumentParser(description="Run Atlas onboard Hailo video pipeline")
    parser.add_argument("--dry-run", action="store_true", help="print pipeline without starting")
    return run(parser.parse_args())


if __name__ == "__main__":
    sys.exit(main())
