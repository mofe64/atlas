"""Small unit conversions at the depth-provider boundary."""

from __future__ import annotations

import numpy as np


def millimetres_to_metres(depth_mm: np.ndarray) -> np.ndarray:
    """Convert a depth array for a consumer that explicitly needs metres.

    The native runtime retains uint16 millimetres. This conversion is kept at
    the consumer edge so every frame is not expanded to float32 unnecessarily.
    Zero is the DepthAI invalid-depth sentinel and becomes NaN.
    """

    source = np.asarray(depth_mm)
    if source.dtype != np.uint16 or source.ndim != 2:
        raise ValueError("depth must be a two-dimensional uint16 array")
    result = source.astype(np.float32)
    result *= 0.001
    result[source == 0] = np.nan
    return result

