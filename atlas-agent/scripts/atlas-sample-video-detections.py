#!/usr/bin/env python3
"""Generate Atlas Frame NDJSON from a recorded video for tracker smoke tests.

OpenCV's built-in HOG person detector is intentionally used so this harness has
no model download. Its detections are test stimuli, not Hailo/YOLO acceptance
evidence and not a measure of production detector accuracy.
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, TextIO

import cv2
import numpy as np


class CameraMotionEstimator:
    METHOD = "SPARSE_OPTICAL_FLOW"

    def __init__(self, max_dimension: int = 320, max_features: int = 300) -> None:
        self.max_dimension = max_dimension
        self.max_features = max_features
        self.previous_gray: Any = None
        self.previous_points: Any = None

    def estimate(self, bgr_frame: Any) -> dict[str, Any] | None:
        height, width = bgr_frame.shape[:2]
        scale = min(1.0, self.max_dimension / max(height, width))
        resized = cv2.resize(bgr_frame, (round(width * scale), round(height * scale)), interpolation=cv2.INTER_AREA) if scale < 1 else bgr_frame
        gray = cv2.cvtColor(resized, cv2.COLOR_BGR2GRAY)
        current_points = cv2.goodFeaturesToTrack(
            gray, maxCorners=self.max_features, qualityLevel=0.01,
            minDistance=3, blockSize=3, useHarrisDetector=False,
        )
        if self.previous_gray is None or self.previous_points is None or len(self.previous_points) < 6:
            self.previous_gray = gray.copy()
            self.previous_points = current_points
            return None
        tracked, status, _errors = cv2.calcOpticalFlowPyrLK(
            self.previous_gray, gray, self.previous_points, None,
            winSize=(21, 21), maxLevel=3,
            criteria=(cv2.TERM_CRITERIA_EPS | cv2.TERM_CRITERIA_COUNT, 30, 0.01),
        )
        previous_points = self.previous_points
        self.previous_gray = gray.copy()
        self.previous_points = current_points
        if tracked is None or status is None:
            return None
        mask = status.reshape(-1).astype(bool)
        previous = previous_points.reshape(-1, 2)[mask]
        current = tracked.reshape(-1, 2)[mask]
        finite = np.isfinite(previous).all(axis=1) & np.isfinite(current).all(axis=1)
        previous, current = previous[finite], current[finite]
        if len(previous) < 6:
            return None
        affine, inliers = cv2.estimateAffinePartial2D(
            previous, current, method=cv2.RANSAC, ransacReprojThreshold=3.0,
            maxIters=1_000, confidence=0.99, refineIters=10,
        )
        if affine is None or inliers is None or not np.isfinite(affine).all():
            return None
        motion_height, motion_width = gray.shape
        determinant = float(np.linalg.det(affine[:, :2]))
        translation_x, translation_y = float(affine[0, 2]), float(affine[1, 2])
        if abs(determinant) < 0.5 or abs(determinant) > 2.0:
            return None
        if abs(translation_x) > motion_width * 0.5 or abs(translation_y) > motion_height * 0.5:
            return None
        inlier_count = int(inliers.reshape(-1).sum())
        confidence = max(0.0, min(1.0, (inlier_count / len(previous)) * min(1.0, inlier_count / 30.0)))
        return {
            "method": self.METHOD,
            "homography": [
                float(affine[0, 0]), float(affine[0, 1]) * motion_height / motion_width, translation_x / motion_width,
                float(affine[1, 0]) * motion_width / motion_height, float(affine[1, 1]), translation_y / motion_height,
                0.0, 0.0, 1.0,
            ],
            "confidence": confidence,
        }


def person_detections(frame: Any, hog: Any, max_width: int) -> list[dict[str, Any]]:
    original_height, original_width = frame.shape[:2]
    scale = min(1.0, max_width / original_width)
    working = cv2.resize(frame, (round(original_width * scale), round(original_height * scale)), interpolation=cv2.INTER_AREA) if scale < 1 else frame
    boxes, weights = hog.detectMultiScale(working, winStride=(8, 8), padding=(8, 8), scale=1.05)
    if len(boxes) == 0:
        return []
    scores = [1.0 / (1.0 + math.exp(-float(weight))) for weight in weights]
    keep = cv2.dnn.NMSBoxes(boxes.tolist(), scores, 0.0, 0.45)
    detections: list[dict[str, Any]] = []
    for index in np.array(keep).reshape(-1) if len(keep) else []:
        x, y, width, height = [float(value) for value in boxes[int(index)]]
        detections.append({
            "classId": 0,
            "classLabel": "person",
            "confidence": scores[int(index)],
            "boundingBox": {
                "x": max(0.0, min(1.0, x / working.shape[1])),
                "y": max(0.0, min(1.0, y / working.shape[0])),
                "width": max(0.0, min(1.0 - x / working.shape[1], width / working.shape[1])),
                "height": max(0.0, min(1.0 - y / working.shape[0], height / working.shape[0])),
            },
        })
    return detections


def rfc3339(base: datetime, seconds: float) -> str:
    return (base + timedelta(seconds=seconds)).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def generate(args: argparse.Namespace, output: TextIO) -> tuple[int, int, int]:
    capture = cv2.VideoCapture(str(args.input))
    if not capture.isOpened():
        raise RuntimeError(f"could not open video: {args.input}")
    fps = float(capture.get(cv2.CAP_PROP_FPS))
    if not math.isfinite(fps) or fps <= 0:
        fps = 30.0
    hog = cv2.HOGDescriptor()
    hog.setSVMDetector(cv2.HOGDescriptor_getDefaultPeopleDetector())
    motion_estimator = CameraMotionEstimator(args.cmc_max_dimension, args.cmc_max_features)
    base = datetime(2026, 1, 1, tzinfo=timezone.utc)
    source_frame = 0
    emitted = 0
    detection_count = 0
    cmc_count = 0
    while True:
        ok, frame = capture.read()
        if not ok:
            break
        source_frame += 1
        if (source_frame - 1) % args.stride != 0:
            continue
        if args.max_frames and emitted >= args.max_frames:
            break
        emitted += 1
        height, width = frame.shape[:2]
        detections = person_detections(frame, hog, args.max_width)
        motion = motion_estimator.estimate(frame)
        detection_count += len(detections)
        cmc_count += int(motion is not None)
        seconds = (source_frame - 1) / fps
        payload: dict[str, Any] = {
            "sourceId": args.source_id,
            "streamEpoch": args.stream_epoch,
            "frameId": str(emitted),
            "observedAt": rfc3339(base, seconds),
            "sourcePtsNs": round(seconds * 1_000_000_000),
            "imageWidth": width,
            "imageHeight": height,
            "model": {"name": "opencv-hog-person-smoke", "version": cv2.__version__},
            "inferenceLatencyMs": 0.0,
            "detections": detections,
        }
        if motion is not None:
            payload["cameraMotion"] = motion
        output.write(json.dumps(payload, separators=(",", ":")) + "\n")
    capture.release()
    return emitted, detection_count, cmc_count


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--stride", type=int, default=5)
    parser.add_argument("--max-frames", type=int, default=0)
    parser.add_argument("--max-width", type=int, default=960)
    parser.add_argument("--cmc-max-dimension", type=int, default=320)
    parser.add_argument("--cmc-max-features", type=int, default=300)
    parser.add_argument("--source-id", default="sample-video")
    parser.add_argument("--stream-epoch", default="sample-video-1")
    args = parser.parse_args()
    if args.stride < 1 or args.max_frames < 0 or args.max_width < 128:
        parser.error("stride must be positive, max-frames non-negative, and max-width at least 128")
    return args


def main() -> int:
    args = parse_args()
    output: TextIO = args.output.open("w", encoding="utf-8") if args.output else sys.stdout
    try:
        frames, detections, cmc = generate(args, output)
    finally:
        if args.output:
            output.close()
    print(f"detector=opencv_hog frames={frames} detections={detections} cmc_frames={cmc}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
