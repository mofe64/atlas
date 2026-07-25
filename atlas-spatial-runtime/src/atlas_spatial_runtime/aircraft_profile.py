"""Load the selected aircraft's small physical payload description."""

from __future__ import annotations

from dataclasses import dataclass
import json
import math
from pathlib import Path
import re


IDENTIFIER = re.compile(r"^[a-z0-9][a-z0-9._-]{0,63}$")


@dataclass(frozen=True)
class DepthCamera:
    device_id: str
    translation_m: tuple[float, float, float]
    rotation_wxyz: tuple[float, float, float, float]


@dataclass(frozen=True)
class AircraftProfile:
    profile_id: str
    depth_camera: DepthCamera


def load_aircraft_profile(path: str | Path) -> AircraftProfile:
    source = Path(path)
    if not source.is_absolute():
        raise ValueError("aircraft profile path must be absolute")
    try:
        raw = json.loads(source.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ValueError(f"read aircraft profile {source}: {error}") from error
    profile = _object(raw, "aircraft profile")
    _exact_keys(profile, {"profileId", "payloads"}, "aircraft profile")
    profile_id = _identifier(profile["profileId"], "profileId")

    payloads = _object(profile["payloads"], "payloads")
    _exact_keys(payloads, {"depthCamera"}, "payloads")
    camera = _object(payloads["depthCamera"], "payloads.depthCamera")
    _exact_keys(camera, {"deviceId", "offsetToBody"}, "payloads.depthCamera")
    device_id = _text(camera["deviceId"], "payloads.depthCamera.deviceId")

    offset = _object(camera["offsetToBody"], "offsetToBody")
    _exact_keys(offset, {"translationM", "rotationWXYZ"}, "offsetToBody")
    return AircraftProfile(
        profile_id=profile_id,
        depth_camera=DepthCamera(
            device_id=device_id,
            translation_m=_vector(offset["translationM"], "translationM"),
            rotation_wxyz=_quaternion(offset["rotationWXYZ"]),
        ),
    )


def _object(value: object, name: str) -> dict:
    if not isinstance(value, dict):
        raise ValueError(f"{name} must be an object")
    return value


def _exact_keys(raw: dict, expected: set[str], name: str) -> None:
    missing = expected.difference(raw)
    unknown = set(raw).difference(expected)
    if missing:
        raise ValueError(f"{name} is missing fields: {sorted(missing)}")
    if unknown:
        raise ValueError(f"{name} has unknown fields: {sorted(unknown)}")


def _text(value: object, name: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{name} must be a non-empty string")
    return value.strip()


def _identifier(value: object, name: str) -> str:
    result = _text(value, name)
    if not IDENTIFIER.fullmatch(result):
        raise ValueError(f"{name} must be a lowercase identifier")
    return result


def _number(value: object, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{name} must be numeric")
    result = float(value)
    if not math.isfinite(result):
        raise ValueError(f"{name} must be finite")
    return result


def _vector(value: object, name: str) -> tuple[float, float, float]:
    raw = _object(value, name)
    _exact_keys(raw, {"x", "y", "z"}, name)
    return (
        _number(raw["x"], f"{name}.x"),
        _number(raw["y"], f"{name}.y"),
        _number(raw["z"], f"{name}.z"),
    )


def _quaternion(value: object) -> tuple[float, float, float, float]:
    raw = _object(value, "rotationWXYZ")
    _exact_keys(raw, {"w", "x", "y", "z"}, "rotationWXYZ")
    result = (
        _number(raw["w"], "rotationWXYZ.w"),
        _number(raw["x"], "rotationWXYZ.x"),
        _number(raw["y"], "rotationWXYZ.y"),
        _number(raw["z"], "rotationWXYZ.z"),
    )
    norm = math.sqrt(sum(component * component for component in result))
    if abs(norm - 1.0) > 1e-3:
        raise ValueError("rotationWXYZ must be normalized")
    return result
