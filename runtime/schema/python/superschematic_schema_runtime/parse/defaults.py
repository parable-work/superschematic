"""Default-value parsing.

``FieldDef.default_value`` holds the schema-declared default as a string; this module
converts it into the target primitive when the field is absent during parse.
"""

from __future__ import annotations

import math
from typing import Any

from ..validation.types import FieldDef, ScalarDef


def primitive_for_builtin(name: str) -> str:
    if name in ("Int", "Float", "Boolean"):
        return name
    if name in ("String", "ID"):
        return "String"
    return ""


def apply_default(field: FieldDef, scalar: ScalarDef | None) -> tuple[Any, bool]:
    """Return ``(value, ok)`` for ``field``'s default.

    ``ok=False`` means the default string was not parseable for the declared
    type and the caller should emit a ``{"validator": "default"}`` error.
    """
    raw = field.default_value
    if raw is None:
        return None, False
    primitive = scalar.primitive if scalar is not None else primitive_for_builtin(
        field.type_ref.name
    )

    if primitive == "Int":
        try:
            parsed = float(raw)
        except ValueError:
            return None, False
        if not math.isfinite(parsed):
            return None, False
        return int(parsed), True
    if primitive == "Float":
        try:
            parsed = float(raw)
        except ValueError:
            return None, False
        if not math.isfinite(parsed):
            return None, False
        return parsed, True
    if primitive == "Boolean":
        if raw in ("true", "1"):
            return True, True
        if raw in ("false", "0"):
            return False, True
        return None, False
    return raw, True


__all__ = ["apply_default", "primitive_for_builtin"]
