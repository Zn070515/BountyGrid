"""Explicit public CAD units and SolidWorks boundary conversions."""

from __future__ import annotations

import math

from .errors import InvalidArgumentError


def _finite(value: float, name: str) -> float:
    if not math.isfinite(value):
        raise InvalidArgumentError(f"{name} must be finite", details={"value": value})
    return value


def mm_to_sw_m(value_mm: float) -> float:
    return _finite(value_mm, "value_mm") / 1000.0


def sw_m_to_mm(value_m: float) -> float:
    return _finite(value_m, "value_m") * 1000.0


def deg_to_sw_rad(value_deg: float) -> float:
    return math.radians(_finite(value_deg, "value_deg"))


def sw_rad_to_deg(value_rad: float) -> float:
    return math.degrees(_finite(value_rad, "value_rad"))
