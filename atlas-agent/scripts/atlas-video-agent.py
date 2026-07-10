#!/usr/bin/env python3
"""Atlas onboard video/inference runner.

The MVP keeps video transport local to the Pi:
  A8 RTSP -> GStreamer/Hailo overlay pipeline -> MediaMTX RTSP publish

Detection metadata is emitted as JSONL for atlas-agent to forward on the
existing vehicle-agent gRPC stream. The script always emits health records.
If ATLAS_PERCEPTION_DETECTIONS_IN is set, detection JSONL records from that
file/FIFO are normalized and copied into ATLAS_PERCEPTION_METADATA_PATH.
"""

from __future__ import annotations

import argparse
import json
import os
import shlex
import shutil
import signal
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


def env(key: str, default: str = "") -> str:
    return os.environ.get(key, default).strip()


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


class HealthState:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.input_connected = False
        self.output_publishing = False
        self.model_loaded = False
        self.fps = 0.0
        self.dropped_frames = 0
        self.last_frame_at = ""
        self.last_detection_at = ""
        self.last_error = ""

    def update(self, **kwargs: Any) -> None:
        with self.lock:
            for key, value in kwargs.items():
                setattr(self, key, value)

    def snapshot(self) -> dict[str, Any]:
        with self.lock:
            return {
                "type": "health",
                "droneId": env("ATLAS_DRONE_ID", "drone-001"),
                "sourceId": env("ATLAS_PERCEPTION_SOURCE_ID", "a8-main"),
                "inputConnected": self.input_connected,
                "outputPublishing": self.output_publishing,
                "modelLoaded": self.model_loaded,
                "accelerator": env("ATLAS_PERCEPTION_ACCELERATOR", "hailo"),
                "fps": self.fps,
                "droppedFrames": self.dropped_frames,
                "lastFrameAt": self.last_frame_at,
                "lastDetectionAt": self.last_detection_at,
                "lastError": self.last_error,
                "modelName": env("ATLAS_PERCEPTION_MODEL_NAME", "yolov6n-hailo"),
                "modelVersion": env("ATLAS_PERCEPTION_MODEL_VERSION", "hef-mvp"),
            }


def append_jsonl(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(payload, separators=(",", ":")) + "\n")


def health_loop(path: Path, state: HealthState, stop: threading.Event) -> None:
    while not stop.is_set():
        append_jsonl(path, state.snapshot())
        stop.wait(float(env("ATLAS_PERCEPTION_HEALTH_INTERVAL_SEC", "1.0")))


def normalize_detection_event(raw: dict[str, Any]) -> dict[str, Any]:
    payload = dict(raw)
    payload["type"] = "event"
    payload.setdefault("droneId", env("ATLAS_DRONE_ID", "drone-001"))
    payload.setdefault("sourceId", env("ATLAS_PERCEPTION_SOURCE_ID", "a8-main"))
    payload.setdefault("observedAt", utc_now())
    payload.setdefault("modelName", env("ATLAS_PERCEPTION_MODEL_NAME", "yolov6n-hailo"))
    payload.setdefault("modelVersion", env("ATLAS_PERCEPTION_MODEL_VERSION", "hef-mvp"))
    payload.setdefault("inferenceLatencyMs", 0)
    payload.setdefault("detections", [])
    return payload


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


def detection_copy_loop(input_path: Path, output_path: Path, state: HealthState, stop: threading.Event) -> None:
    while not stop.is_set() and not input_path.exists():
        stop.wait(1)
    if stop.is_set():
        return

    with input_path.open("r", encoding="utf-8") as handle:
        handle.seek(0, os.SEEK_END)
        while not stop.is_set():
            line = handle.readline()
            if not line:
                stop.wait(0.25)
                continue
            try:
                payload = normalize_detection_event(json.loads(line))
            except Exception as exc:  # noqa: BLE001 - keep service alive on malformed detector output.
                state.update(last_error=f"detection metadata parse failed: {exc}")
                continue
            detections = payload.get("detections") or []
            if detections:
                state.update(last_detection_at=payload.get("observedAt", utc_now()))
            append_jsonl(output_path, payload)


def build_default_pipeline() -> list[str]:
    input_url = env("ATLAS_A8_RTSP_URL", "rtsp://192.168.144.25:8554/main.264")
    output_url = env("ATLAS_PROCESSED_RTSP_URL", "rtsp://127.0.0.1:8554/atlas")
    model_path = env("ATLAS_PERCEPTION_MODEL_PATH")
    postprocess_so = env("ATLAS_PERCEPTION_POSTPROCESS_SO")
    postprocess_function = env("ATLAS_PERCEPTION_POSTPROCESS_FUNCTION", "yolov6n")
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
        *leaky_queue("atlas_src_drop"),
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
        ]

        if postprocess_so:
            pipeline += [
                "!",
                "hailofilter",
                f"so-path={postprocess_so}",
                f"function-name={postprocess_function}",
                "qos=false",
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
    metadata_path = Path(env("ATLAS_PERCEPTION_METADATA_PATH", "~/.local/state/atlas-agent/perception/metadata.jsonl")).expanduser()
    detections_in = env("ATLAS_PERCEPTION_DETECTIONS_IN")
    state = HealthState()
    stop = threading.Event()

    command = build_command()
    if args.dry_run:
        print(" ".join(shlex.quote(part) for part in command))
        print(f"metadata: {metadata_path}")
        if detections_in:
            print(f"detections input: {Path(detections_in).expanduser()}")
        return 0

    threads = [
        threading.Thread(target=health_loop, args=(metadata_path, state, stop), daemon=True),
    ]
    if detections_in:
        threads.append(
            threading.Thread(
                target=detection_copy_loop,
                args=(Path(detections_in).expanduser(), metadata_path, state, stop),
                daemon=True,
            )
        )
    for thread in threads:
        thread.start()

    state.update(model_loaded=bool(env("ATLAS_PERCEPTION_MODEL_PATH")), input_connected=True)
    process = subprocess.Popen(command)

    def stop_process(signum: int, _frame: Any) -> None:
        stop.set()
        if process.poll() is None:
            process.terminate()

    signal.signal(signal.SIGTERM, stop_process)
    signal.signal(signal.SIGINT, stop_process)

    while process.poll() is None:
        state.update(output_publishing=True, last_frame_at=utc_now(), last_error="")
        time.sleep(1)

    stop.set()
    code = process.returncode or 0
    if code != 0:
        state.update(output_publishing=False, last_error=f"video pipeline exited with code {code}")
        append_jsonl(metadata_path, state.snapshot())
    return code


def main() -> int:
    parser = argparse.ArgumentParser(description="Run Atlas onboard Hailo video pipeline")
    parser.add_argument("--dry-run", action="store_true", help="print pipeline and metadata paths without starting")
    return run(parser.parse_args())


if __name__ == "__main__":
    sys.exit(main())
