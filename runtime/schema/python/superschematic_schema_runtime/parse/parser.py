"""Runtime parse entry points.

Public surface: ``parse_type`` / ``parse_input`` (map -> coerced + normalized +
default-filled map), ``parse_type_json`` / ``parse_input_json`` (decode JSON
bytes/string then dispatch).
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

from .. import ValidationError
from ..errors import ValidationErrors, append_field_error, new_validation_errors
from ..validation.resolver import resolve_schema
from ..validation.types import Schema, SchemaInput
from .registry import (
    ScalarNormalizeRegistry,
    ScalarParseRegistry,
    create_default_scalar_normalize_registry,
    create_default_scalar_parse_registry,
)
from .walk import WalkContext, walk_type_def


@dataclass
class ParseOptions:
    strict: bool = False
    parse_registry: ScalarParseRegistry | None = None
    normalize_registry: ScalarNormalizeRegistry | None = None


@dataclass
class ParseResult:
    data: dict[str, Any]
    errors: ValidationErrors


_default_parse_registry: ScalarParseRegistry | None = None
_default_normalize_registry: ScalarNormalizeRegistry | None = None


def _get_default_parse_registry() -> ScalarParseRegistry:
    global _default_parse_registry
    if _default_parse_registry is None:
        _default_parse_registry = create_default_scalar_parse_registry()
    return _default_parse_registry


def _get_default_normalize_registry() -> ScalarNormalizeRegistry:
    global _default_normalize_registry
    if _default_normalize_registry is None:
        _default_normalize_registry = create_default_scalar_normalize_registry()
    return _default_normalize_registry


def _ctx(schema: Schema, options: ParseOptions | None) -> WalkContext:
    opts = options if options is not None else ParseOptions()
    return WalkContext(
        schema=schema,
        parse_registry=opts.parse_registry if opts.parse_registry is not None else _get_default_parse_registry(),
        normalize_registry=opts.normalize_registry
        if opts.normalize_registry is not None
        else _get_default_normalize_registry(),
        strict=opts.strict,
    )


def _unknown_type_error(name: str, kind: str) -> ParseResult:
    errs = new_validation_errors()
    append_field_error(
        errs, "", ValidationError(validator="type", message=f'unknown {kind} "{name}"')
    )
    return ParseResult(data={}, errors=errs)


def _object_from_value(value: Any) -> dict[str, Any]:
    return value if isinstance(value, dict) else {}


def parse_type(
    schema: SchemaInput,
    type_name: str,
    data: Any,
    options: ParseOptions | None = None,
) -> ParseResult:
    """Parse ``data`` against ``schema.types[type_name]``. Map-in/map-out;
    applies defaults, coerces primitives, runs normalize/parse hooks.
    """
    resolved = resolve_schema(schema)
    td = resolved.types.get(type_name)
    if td is None:
        return _unknown_type_error(type_name, "type")
    out, errs = walk_type_def(_ctx(resolved, options), td, _object_from_value(data))
    return ParseResult(data=out, errors=errs)


def parse_input(
    schema: SchemaInput,
    input_name: str,
    data: Any,
    options: ParseOptions | None = None,
) -> ParseResult:
    """Parse ``data`` against ``schema.inputs[input_name]``."""
    resolved = resolve_schema(schema)
    td = resolved.inputs.get(input_name)
    if td is None:
        return _unknown_type_error(input_name, "input")
    out, errs = walk_type_def(_ctx(resolved, options), td, _object_from_value(data))
    return ParseResult(data=out, errors=errs)


def parse_type_map(
    schema: SchemaInput,
    type_name: str,
    data: dict[str, Any],
    options: ParseOptions | None = None,
) -> ParseResult:
    return parse_type(schema, type_name, data, options)


def parse_input_map(
    schema: SchemaInput,
    input_name: str,
    data: dict[str, Any],
    options: ParseOptions | None = None,
) -> ParseResult:
    return parse_input(schema, input_name, data, options)


def _decode_json(payload: str | bytes) -> tuple[Any, ValidationErrors]:
    errors = new_validation_errors()
    if isinstance(payload, bytes):
        try:
            text = payload.decode("utf-8")
        except UnicodeDecodeError as exc:
            append_field_error(errors, "", ValidationError(validator="json", message=str(exc)))
            return None, errors
    else:
        text = payload
    try:
        return json.loads(text), errors
    except json.JSONDecodeError as exc:
        append_field_error(errors, "", ValidationError(validator="json", message=str(exc)))
        return None, errors


def parse_type_json(
    schema: SchemaInput,
    type_name: str,
    payload: str | bytes,
    options: ParseOptions | None = None,
) -> ParseResult:
    value, errs = _decode_json(payload)
    if errs:
        return ParseResult(data={}, errors=errs)
    if not isinstance(value, dict):
        e = new_validation_errors()
        append_field_error(
            e, "", ValidationError(validator="json", message="expected JSON object payload")
        )
        return ParseResult(data={}, errors=e)
    return parse_type(schema, type_name, value, options)


def parse_input_json(
    schema: SchemaInput,
    input_name: str,
    payload: str | bytes,
    options: ParseOptions | None = None,
) -> ParseResult:
    value, errs = _decode_json(payload)
    if errs:
        return ParseResult(data={}, errors=errs)
    if not isinstance(value, dict):
        e = new_validation_errors()
        append_field_error(
            e, "", ValidationError(validator="json", message="expected JSON object payload")
        )
        return ParseResult(data={}, errors=e)
    return parse_input(schema, input_name, value, options)


__all__ = [
    "ParseOptions",
    "ParseResult",
    "parse_input",
    "parse_input_json",
    "parse_input_map",
    "parse_type",
    "parse_type_json",
    "parse_type_map",
]
