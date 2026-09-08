"""Runtime validation error types and helpers.

``ValidationErrors`` is a ``dict`` whose values are either a leaf
``list[ValidationError]`` or a nested ``ValidationErrors`` for compound paths.
"""

from __future__ import annotations

from typing import Any

from parable_scalars import ValidationError


ValidationErrors = dict[str, Any]


def new_validation_errors() -> ValidationErrors:
    """Allocate an empty :data:`ValidationErrors` container."""
    return {}


def has_errors(errs: ValidationErrors | None) -> bool:
    """Return ``True`` if ``errs`` contains any leaf :class:`ValidationError`."""
    if not errs:
        return False
    for value in errs.values():
        if isinstance(value, list):
            if value:
                return True
        elif isinstance(value, dict):
            if has_errors(value):
                return True
    return False


def merge_validation_errors(
    dst: ValidationErrors, src: ValidationErrors | None
) -> ValidationErrors:
    """Merge ``src`` into ``dst`` (mutating ``dst``) and return ``dst``.

    Leaf lists are concatenated; nested dicts are merged recursively.
    """
    if not src:
        return dst
    for key, value in src.items():
        existing = dst.get(key)
        if isinstance(value, list):
            if existing is None:
                dst[key] = list(value)
            elif isinstance(existing, list):
                existing.extend(value)
            else:
                # Type mismatch: overwrite with the leaf list rather than crash.
                dst[key] = list(value)
        elif isinstance(value, dict):
            if isinstance(existing, dict):
                merge_validation_errors(existing, value)
            else:
                dst[key] = merge_validation_errors({}, value)
    return dst


def append_field_error(
    errs: ValidationErrors, field_path: str, error: ValidationError
) -> None:
    """Append ``error`` under ``errs[field_path]`` (leaf list)."""
    existing = errs.get(field_path)
    if isinstance(existing, list):
        existing.append(error)
    else:
        errs[field_path] = [error]


__all__ = [
    "ValidationError",
    "ValidationErrors",
    "new_validation_errors",
    "has_errors",
    "merge_validation_errors",
    "append_field_error",
]
