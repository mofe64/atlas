"""Direct DepthAI v3 provider for an RGB-aligned OAK stereo depth stream."""

from __future__ import annotations

from typing import Any

import numpy as np

from .provider import CameraCalibration, DepthFrame, ProviderInfo, host_arrival_ns


class DepthAIProvider:
    def __init__(
        self,
        *,
        width: int,
        height: int,
        fps: float,
        frame_id: str,
        expected_device_id: str = "",
        configured_model: str = "",
        configured_connection: str = "unknown",
    ) -> None:
        self._width = width
        self._height = height
        self._fps = fps
        self._frame_id = frame_id
        self._expected_device_id = expected_device_id
        self._pipeline: Any = None
        self._queue: Any = None
        self._info = ProviderInfo(
            "depthai", expected_device_id, configured_model, configured_connection
        )

    @property
    def info(self) -> ProviderInfo:
        return self._info

    def start(self) -> None:
        try:
            import depthai as dai
        except ImportError as error:
            raise RuntimeError(
                "DepthAI provider is selected but the depthai package is not installed"
            ) from error

        pipeline = dai.Pipeline()
        device = pipeline.getDefaultDevice()
        actual_id = str(device.getDeviceInfo().getDeviceId())
        if self._expected_device_id and actual_id != self._expected_device_id:
            raise RuntimeError(
                "the connected DepthAI device does not match "
                f"ATLAS_SPATIAL_DEVICE_ID (expected {self._expected_device_id}, got {actual_id})"
            )

        rgb = pipeline.create(dai.node.Camera).build(dai.CameraBoardSocket.CAM_A)
        left = pipeline.create(dai.node.Camera).build(dai.CameraBoardSocket.CAM_B)
        right = pipeline.create(dai.node.Camera).build(dai.CameraBoardSocket.CAM_C)
        stereo = pipeline.create(dai.node.StereoDepth)
        stereo.setDefaultProfilePreset(dai.node.StereoDepth.PresetMode.ROBOTICS)
        stereo.setLeftRightCheck(True)
        stereo.setSubpixel(True)

        rgb_output = rgb.requestOutput(
            size=(self._width, self._height),
            fps=self._fps,
            enableUndistortion=True,
        )
        left_output = left.requestOutput(size=(640, 400), fps=self._fps)
        right_output = right.requestOutput(size=(640, 400), fps=self._fps)
        left_output.link(stereo.left)
        right_output.link(stereo.right)

        platform = device.getPlatform()
        if platform == dai.Platform.RVC4:
            align = pipeline.create(dai.node.ImageAlign)
            stereo.depth.link(align.input)
            rgb_output.link(align.inputAlignTo)
            depth_output = align.outputAligned
        else:
            rgb_output.link(stereo.inputAlignTo)
            depth_output = stereo.depth

        queue = depth_output.createOutputQueue(maxSize=1, blocking=False)
        pipeline.start()

        model = self._read_model(device) or self._info.model or "DepthAI camera"
        connection = _usb_speed(device.getUsbSpeed())
        self._pipeline = pipeline
        self._queue = queue
        self._info = ProviderInfo("depthai", actual_id, model, connection)

    def try_read(self) -> DepthFrame | None:
        if self._queue is None:
            raise RuntimeError("DepthAI provider has not been started")
        message = self._queue.tryGet()
        if message is None:
            return None
        depth = np.asarray(message.getFrame())
        if depth.dtype != np.uint16:
            raise RuntimeError(f"DepthAI returned unexpected depth dtype {depth.dtype}")
        transformation = message.getTransformation()
        intrinsics = transformation.getIntrinsicMatrix()
        timestamp = message.getTimestamp()
        capture_ns = int(timestamp.total_seconds() * 1_000_000_000)
        height, width = depth.shape
        calibration = CameraCalibration(
            frame_id=self._frame_id,
            width=width,
            height=height,
            fx=float(intrinsics[0][0]),
            fy=float(intrinsics[1][1]),
            cx=float(intrinsics[0][2]),
            cy=float(intrinsics[1][2]),
            distortion_model=str(transformation.getDistortionModel()),
        )
        return DepthFrame(
            depth_mm=depth,
            capture_monotonic_ns=capture_ns,
            arrival_monotonic_ns=host_arrival_ns(),
            calibration=calibration,
        )

    def close(self) -> None:
        pipeline, self._pipeline = self._pipeline, None
        self._queue = None
        if pipeline is not None:
            pipeline.stop()
            pipeline.wait()

    @staticmethod
    def _read_model(device: Any) -> str:
        try:
            eeprom = device.readCalibration2().getEepromData()
        except Exception:
            return ""
        return str(eeprom.productName or eeprom.boardName or "")


def _usb_speed(value: Any) -> str:
    rendered = str(value).lower()
    if "super" in rendered:
        return "usb3"
    if "high" in rendered:
        return "usb2"
    return "unknown"
