"""Scalar parse / normalize registries.

Pulls the default function set from the generated ``_generated_default_registry``
module so the registry composition is identical across calls and codegen runs.
"""

from __future__ import annotations

from typing import Callable

from .. import ValidationError
from .._generated_default_registry import (
    DEFAULT_NORMALIZE_FUNCTIONS,
    DEFAULT_PARSE_FUNCTIONS,
)
from ..validation.types import Schema


ScalarParseFunc = Callable[[str], tuple[str, list[ValidationError]]]
ScalarNormalizeFunc = Callable[[str], str]


def _wrap_parse(fn: Callable[[str], str], label: str) -> ScalarParseFunc:
    """Adapt a ``parse_<name>(str) -> str`` (raises ``ValueError``) to the
    runtime contract that returns ``(value, errors)``."""

    def adapter(input_value: str) -> tuple[str, list[ValidationError]]:
        try:
            return fn(input_value), []
        except ValueError as exc:
            return input_value, [
                ValidationError(validator="parse", message=f"invalid {label}: {exc}")
            ]

    return adapter


class ScalarParseRegistry:
    """Registry of scalar parse functions keyed by canonical name."""

    def __init__(self) -> None:
        self._fns: dict[str, ScalarParseFunc] = {}

    def register(self, name: str, fn: ScalarParseFunc) -> None:
        self._fns[name] = fn

    def unregister(self, name: str) -> bool:
        return self._fns.pop(name, None) is not None

    def get(self, name: str) -> ScalarParseFunc | None:
        return self._fns.get(name)

    def has(self, name: str) -> bool:
        return name in self._fns

    def names(self) -> list[str]:
        return sorted(self._fns)

    def missing_parsers(self, schema: Schema) -> list[str]:
        return sorted(
            name
            for name, scalar in schema.scalars.items()
            if scalar.has_custom_parse and not self.has(name)
        )


class ScalarNormalizeRegistry:
    """Registry of scalar normalize functions keyed by canonical name."""

    def __init__(self) -> None:
        self._fns: dict[str, ScalarNormalizeFunc] = {}

    def register(self, name: str, fn: ScalarNormalizeFunc) -> None:
        self._fns[name] = fn

    def unregister(self, name: str) -> bool:
        return self._fns.pop(name, None) is not None

    def get(self, name: str) -> ScalarNormalizeFunc | None:
        return self._fns.get(name)

    def has(self, name: str) -> bool:
        return name in self._fns

    def names(self) -> list[str]:
        return sorted(self._fns)

    def missing_normalizers(self, schema: Schema) -> list[str]:
        return sorted(
            name
            for name, scalar in schema.scalars.items()
            if scalar.has_custom_normalize and not self.has(name)
        )


def create_default_scalar_parse_registry() -> ScalarParseRegistry:
    """Build a parse registry pre-populated with every scalar that ships a
    custom ``parse_<name>``."""
    registry = ScalarParseRegistry()
    for canonical_name, fn in DEFAULT_PARSE_FUNCTIONS.items():
        # Human label for the error message: the segment after the dot.
        label = canonical_name.split(".")[-1] if "." in canonical_name else canonical_name
        registry.register(canonical_name, _wrap_parse(fn, label))
    return registry


def create_default_scalar_normalize_registry() -> ScalarNormalizeRegistry:
    """Build a normalize registry pre-populated with every scalar that ships a
    custom ``normalize_<name>``."""
    registry = ScalarNormalizeRegistry()
    for canonical_name, fn in DEFAULT_NORMALIZE_FUNCTIONS.items():
        registry.register(canonical_name, fn)
    return registry


__all__ = [
    "ScalarNormalizeFunc",
    "ScalarNormalizeRegistry",
    "ScalarParseFunc",
    "ScalarParseRegistry",
    "create_default_scalar_normalize_registry",
    "create_default_scalar_parse_registry",
]
