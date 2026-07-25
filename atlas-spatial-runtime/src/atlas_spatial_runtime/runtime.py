"""Native Spatial process: provider ownership, freshness, and local health."""

from __future__ import annotations

import logging
import os
import signal
import threading
import time

from . import PROTOCOL_VERSION
from .aircraft_profile import AircraftProfile, load_aircraft_profile
from .depthai_provider import DepthAIProvider
from .health_contract import SpatialHealthState
from .health_server import HealthServer
from .provider import DepthProvider
from .synthetic_provider import SyntheticProvider


LOG = logging.getLogger("atlas-spatial-runtime")


def _environment_bool(name: str, default: bool) -> bool:
    value = os.environ.get(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


def _positive_int(name: str, default: int) -> int:
    value = int(os.environ.get(name, str(default)))
    if value <= 0:
        raise ValueError(f"{name} must be positive")
    return value


def _positive_float(name: str, default: float) -> float:
    value = float(os.environ.get(name, str(default)))
    if value <= 0:
        raise ValueError(f"{name} must be positive")
    return value


def load_runtime_profile() -> AircraftProfile:
    profile_path = os.environ.get("ATLAS_AIRCRAFT_PROFILE_PATH", "").strip()
    profile_id = os.environ.get("ATLAS_AIRCRAFT_PROFILE_ID", "").strip()
    if not profile_path:
        raise ValueError("ATLAS_AIRCRAFT_PROFILE_PATH is required")
    profile = load_aircraft_profile(profile_path)
    if not profile_id or profile.profile_id != profile_id:
        raise ValueError(
            "ATLAS_AIRCRAFT_PROFILE_ID must match the selected aircraft profile"
        )
    return profile


def build_provider(profile: AircraftProfile | None = None) -> DepthProvider:
    profile = profile or load_runtime_profile()
    provider = os.environ.get("ATLAS_SPATIAL_PROVIDER", "").strip().lower()
    if not provider:
        raise ValueError("ATLAS_SPATIAL_PROVIDER is required")
    configured_device_id = os.environ.get("ATLAS_SPATIAL_DEVICE_ID", "").strip()
    if (
        provider == "depthai"
        and configured_device_id
        and configured_device_id != profile.depth_camera.device_id
    ):
        raise ValueError("ATLAS_SPATIAL_DEVICE_ID does not match the aircraft profile")
    width = _positive_int("ATLAS_SPATIAL_WIDTH", 640)
    height = _positive_int("ATLAS_SPATIAL_HEIGHT", 400)
    fps = _positive_float("ATLAS_SPATIAL_FPS", 20.0)
    frame_id = os.environ.get("ATLAS_SPATIAL_FRAME_ID", "").strip()
    if not frame_id:
        raise ValueError("ATLAS_SPATIAL_FRAME_ID is required")
    if provider == "synthetic":
        return SyntheticProvider(
            width=width, height=height, fps=fps, frame_id=frame_id
        )
    if provider == "depthai":
        return DepthAIProvider(
            width=width,
            height=height,
            fps=fps,
            frame_id=frame_id,
            expected_device_id=profile.depth_camera.device_id,
            configured_model=os.environ.get("ATLAS_SPATIAL_MODEL", "").strip(),
            configured_connection=os.environ.get(
                "ATLAS_SPATIAL_USB_TRANSPORT", "unknown"
            ).strip(),
        )
    raise ValueError(f"unsupported ATLAS_SPATIAL_PROVIDER: {provider}")


def run(
    provider: DepthProvider, stop: threading.Event, profile: AircraftProfile
) -> None:
    source_id = os.environ.get("ATLAS_SPATIAL_SOURCE_ID", "front-depth").strip()
    if not source_id:
        raise ValueError("ATLAS_SPATIAL_SOURCE_ID is required")
    socket_path = os.environ.get(
        "ATLAS_SPATIAL_SOCKET_PATH", "/run/atlas-agent/spatial.sock"
    )
    stale_after_ms = _positive_float("ATLAS_SPATIAL_STALE_AFTER_MS", 1000.0)
    fail_after_ms = _positive_float("ATLAS_SPATIAL_FAIL_AFTER_MS", 5000.0)
    startup_grace_ms = _positive_float("ATLAS_SPATIAL_STARTUP_GRACE_MS", 30000.0)

    state = SpatialHealthState(
        provider=provider.info.provider,
        source_id=source_id,
        device=provider.info,
        profile_id=profile.profile_id,
    )
    server = HealthServer(
        socket_path, lambda: state.snapshot(stale_after_ms=stale_after_ms)
    )
    started_ns = time.monotonic_ns()
    try:
        provider.start()
        state.set_device(provider.info)
        server.start()
        LOG.info(
            "provider=%s device=%s model=%s connection=%s frame=%s",
            provider.info.provider,
            provider.info.device_id,
            provider.info.model,
            provider.info.connection,
            os.environ.get("ATLAS_SPATIAL_FRAME_ID", "oak_rgb_camera_optical_frame"),
        )
        while not stop.is_set():
            frame = provider.try_read()
            if frame is not None:
                state.observe(frame)
                continue
            now_ns = time.monotonic_ns()
            age_ms = (now_ns - started_ns) / 1_000_000.0
            last_arrival_ns = state.last_arrival_ns()
            if age_ms > startup_grace_ms and last_arrival_ns is None:
                raise RuntimeError("depth provider produced no frames during startup")
            if last_arrival_ns is not None:
                last_age_ms = (now_ns - last_arrival_ns) / 1_000_000.0
                if last_age_ms > fail_after_ms:
                    raise RuntimeError(
                        f"depth provider remained stale for {last_age_ms:.0f} ms"
                    )
            stop.wait(0.005)
    except Exception as error:
        state.set_error(str(error))
        raise
    finally:
        server.close()
        provider.close()


def main() -> None:
    logging.basicConfig(
        level=os.environ.get("ATLAS_SPATIAL_LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    if not _environment_bool("ATLAS_SPATIAL_ENABLED", True):
        LOG.info("spatial runtime is disabled")
        return
    requested_contract = os.environ.get(
        "ATLAS_SPATIAL_CONTRACT_VERSION", PROTOCOL_VERSION
    )
    if requested_contract != PROTOCOL_VERSION:
        raise SystemExit(
            f"unsupported Atlas Spatial contract {requested_contract}; "
            f"runtime implements {PROTOCOL_VERSION}"
        )
    stop = threading.Event()

    def stop_handler(_signum: int, _frame: object) -> None:
        stop.set()

    signal.signal(signal.SIGINT, stop_handler)
    signal.signal(signal.SIGTERM, stop_handler)
    profile = load_runtime_profile()
    run(build_provider(profile), stop, profile)


if __name__ == "__main__":
    main()
