"""Versioned, hash-addressed body/sensor transform contract."""

from __future__ import annotations

from copy import deepcopy
from hashlib import sha256
import json
import math
from pathlib import Path


SCHEMA = "atlas.transform-bundle/v1"
STATUSES = {"verified", "configured_unverified", "unmeasured"}
CONVENTIONS = {
    "handedness": "right-handed",
    "translation": "child origin expressed in parent-frame axes, metres",
    "rotation": "unit quaternion WXYZ rotating child-frame vectors into parent-frame vectors",
    "pointTransform": "p_parent = R_parent_from_child * p_child + translationM",
}


def canonical_json(value: dict) -> bytes:
    return json.dumps(value, allow_nan=False, separators=(",", ":"), sort_keys=True).encode("utf-8")


def bundle_hash(bundle: dict) -> str:
    value = deepcopy(bundle)
    value.pop("sha256", None)
    return "sha256:" + sha256(canonical_json(value)).hexdigest()


def validate_transform_bundle(bundle: dict) -> dict:
    if bundle.get("schema") != SCHEMA:
        raise ValueError("unsupported transform bundle schema")
    for field in ("bundleId", "aircraftId", "createdAt"):
        if not isinstance(bundle.get(field), str) or not bundle[field].strip():
            raise ValueError(f"transform bundle {field} is required")
    if bundle.get("conventions") != CONVENTIONS:
        raise ValueError("transform bundle conventions are missing or ambiguous")
    frames = bundle.get("frames")
    if not isinstance(frames, dict) or not frames:
        raise ValueError("transform bundle frames are required")
    transforms = bundle.get("transforms")
    if not isinstance(transforms, list) or not transforms:
        raise ValueError("transform bundle requires at least one transform")
    seen: set[tuple[str, str]] = set()
    for transform in transforms:
        parent = transform.get("parentFrame")
        child = transform.get("childFrame")
        if not isinstance(parent, str) or not parent or not isinstance(child, str) or not child or parent == child:
            raise ValueError("transform parentFrame and childFrame must be distinct non-empty strings")
        if not isinstance(frames.get(parent), str) or not frames[parent].strip():
            raise ValueError(f"transform frame {parent} requires a definition")
        if not isinstance(frames.get(child), str) or not frames[child].strip():
            raise ValueError(f"transform frame {child} requires a definition")
        edge = (parent, child)
        if edge in seen:
            raise ValueError(f"duplicate transform edge {parent} -> {child}")
        seen.add(edge)
        status = transform.get("status")
        if status not in STATUSES:
            raise ValueError(f"invalid transform status for {parent} -> {child}")
        translation, rotation = transform.get("translationM"), transform.get("rotationWXYZ")
        if status == "unmeasured":
            if translation is not None or rotation is not None:
                raise ValueError("unmeasured transforms must not contain invented geometry")
        else:
            _validate_vector(translation, ("x", "y", "z"), "translationM")
            _validate_vector(rotation, ("w", "x", "y", "z"), "rotationWXYZ")
            norm = math.sqrt(sum(float(rotation[key]) ** 2 for key in ("w", "x", "y", "z")))
            if abs(norm - 1.0) > 1e-6:
                raise ValueError("transform quaternion must be normalized")
        provenance = transform.get("provenance")
        if not isinstance(provenance, dict) or not isinstance(provenance.get("method"), str):
            raise ValueError("every transform requires provenance.method")
    expected_hash = bundle_hash(bundle)
    if "sha256" in bundle and bundle["sha256"] != expected_hash:
        raise ValueError("transform bundle hash does not match canonical contents")
    validated = deepcopy(bundle)
    validated["sha256"] = expected_hash
    return validated


def _validate_vector(value: object, keys: tuple[str, ...], name: str) -> None:
    if not isinstance(value, dict) or set(value) != set(keys):
        raise ValueError(f"{name} must contain exactly {', '.join(keys)}")
    if not all(isinstance(value[key], (int, float)) and math.isfinite(float(value[key])) for key in keys):
        raise ValueError(f"{name} values must be finite numbers")


def load_transform_bundle(path: str | Path) -> dict:
    return validate_transform_bundle(json.loads(Path(path).read_text(encoding="utf-8")))


def transform_status(bundle: dict, parent: str, child: str) -> str | None:
    for transform in bundle["transforms"]:
        if transform["parentFrame"] == parent and transform["childFrame"] == child:
            return str(transform["status"])
    return None
