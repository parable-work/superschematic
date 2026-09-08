"""Lenient/strict scalar value coercion.

Returns ``(value, ok)``; ``ok=False`` triggers a ``{"validator": "type"}`` error at
the field path. In lenient mode, numeric strings and ``"true"``/``"false"``
literals are accepted.
"""

from __future__ import annotations

import math
from typing import Any

_MIN_SAFE_INTEGER = -9007199254740991
_MAX_SAFE_INTEGER = 9007199254740991


def coerce_int(value: Any, strict: bool) -> tuple[int, bool]:
    if isinstance(value, bool):
        return 0, False
    if isinstance(value, int):
        return (
            (value, True)
            if _MIN_SAFE_INTEGER <= value <= _MAX_SAFE_INTEGER
            else (0, False)
        )
    if isinstance(value, float):
        if not math.isfinite(value) or not value.is_integer():
            return 0, False
        parsed = int(value)
        return (
            (parsed, True)
            if _MIN_SAFE_INTEGER <= parsed <= _MAX_SAFE_INTEGER
            else (0, False)
        )
    if isinstance(value, str):
        if strict:
            return 0, False
        trimmed = value.strip()
        if not trimmed:
            return 0, False
        try:
            parsed = int(trimmed)
        except ValueError:
            return 0, False
        return (
            (parsed, True)
            if _MIN_SAFE_INTEGER <= parsed <= _MAX_SAFE_INTEGER
            else (0, False)
        )
    return 0, False


def coerce_float(value: Any, strict: bool) -> tuple[float, bool]:
    if isinstance(value, bool):
        return (1.0 if value else 0.0), True
    if isinstance(value, (int, float)):
        as_float = float(value)
        if not math.isfinite(as_float):
            return 0.0, False
        return as_float, True
    if isinstance(value, str):
        if strict:
            return 0.0, False
        trimmed = value.strip()
        if not trimmed:
            return 0.0, False
        try:
            parsed = float(trimmed)
        except ValueError:
            return 0.0, False
        if not math.isfinite(parsed):
            return 0.0, False
        return parsed, True
    return 0.0, False


def coerce_bool(value: Any, strict: bool) -> tuple[bool, bool]:
    if isinstance(value, bool):
        return value, True
    if isinstance(value, str):
        if strict:
            return False, False
        trimmed = value.strip().lower()
        if trimmed == "true":
            return True, True
        if trimmed == "false":
            return False, True
    return False, False


__all__ = ["coerce_bool", "coerce_float", "coerce_int"]
