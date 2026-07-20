"""Conversions at the provider boundary; independent from ROS for testing."""

from __future__ import annotations

import sys

import numpy


def millimetres_to_float_metres(data: bytes, width: int, height: int, step: int, big_endian: bool) -> tuple[bytes, bool]:
    if width <= 0 or height <= 0 or step < width * 2 or len(data) < height * step:
        raise ValueError("invalid 16-bit depth image dimensions")
    byte_order = ">" if big_endian else "<"
    millimetres = numpy.ndarray(
        shape=(height, width),
        dtype=numpy.dtype(byte_order + "u2"),
        buffer=data,
        strides=(step, 2),
    )
    metres = millimetres.astype(numpy.float32)
    metres *= 0.001
    metres[millimetres == 0] = numpy.nan
    return metres.tobytes(order="C"), sys.byteorder == "big"
