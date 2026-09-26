"""Per-field parse walker.

Applies defaults when absent, coerces primitives, runs normalize and parse
hooks, and recurses into nested types / arrays. Strict mode rejects unknown
fields with ``{"validator": "unknown_field"}``.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

from .. import ValidationError
from ..errors import (
    ValidationErrors,
    append_field_error,
    has_errors as _has_errors,
    new_validation_errors,
)
from ..validation.types import FieldDef, ScalarDef, Schema, TypeDef, TypeRef, json_shape_of
from .coerce import coerce_bool, coerce_float, coerce_int
from .defaults import apply_default
from .registry import ScalarNormalizeRegistry, ScalarParseRegistry


_BUILTIN_SCALARS = frozenset({"String", "Int", "Float", "Boolean", "ID"})


@dataclass
class WalkContext:
    schema: Schema
    parse_registry: ScalarParseRegistry
    normalize_registry: ScalarNormalizeRegistry
    strict: bool = False


def _is_object(value: Any) -> bool:
    return isinstance(value, dict)


def _resolve_ref_kind(schema: Schema, ref: TypeRef) -> str:
    if ref.name in schema.scalars:
        return "scalar"
    if ref.name in schema.enums:
        return "enum"
    if ref.name in schema.types:
        return "type"
    if ref.name in schema.inputs:
        return "input"
    return "builtin"


def field_key(field: FieldDef) -> str:
    return field.json_key or field.name


def _type_mismatch_message(scalar: ScalarDef) -> str:
    shape = scalar.structured_json_type()
    if shape:
        return f"expected a JSON {shape} or its JSON text"
    primitive = scalar.primitive
    if primitive == "Int":
        return "expected integer value"
    if primitive == "Float":
        return "expected numeric value"
    if primitive == "Boolean":
        return "expected boolean value"
    if primitive in ("String", ""):
        return "expected string value"
    return "type mismatch"


def _apply_scalar(ctx: WalkContext, scalar: ScalarDef, value: Any) -> tuple[Any, bool]:
    # Any JSON value is one, whatever the scalar's primitive; validation
    # checks it.
    if scalar.is_any_json():
        return value, True
    # A JSON object or array scalar takes its dict or list, or the value's
    # JSON text, which the String primitive's steps below read.
    shape = scalar.structured_json_type()
    if shape:
        if json_shape_of(value) == shape:
            return value, True
        if not isinstance(value, str):
            return value, False
    primitive = scalar.primitive
    if primitive == "Int":
        return coerce_int(value, ctx.strict)
    if primitive == "Float":
        return coerce_float(value, ctx.strict)
    if primitive == "Boolean":
        return coerce_bool(value, ctx.strict)
    if primitive in ("String", ""):
        if not isinstance(value, str):
            return value, False
        s = value
        if scalar.has_custom_normalize:
            fn = ctx.normalize_registry.get(scalar.name)
            if fn is not None:
                s = fn(s)
        return s, True
    return value, True


def _apply_scalar_parse(
    ctx: WalkContext,
    scalar: ScalarDef,
    value: Any,
    key: str,
    errors: ValidationErrors,
) -> Any:
    shape = scalar.structured_json_type()
    if shape:
        return _parse_structured_json(ctx, scalar, shape, value, key, errors)
    if not scalar.has_custom_parse:
        return value
    fn = ctx.parse_registry.get(scalar.name)
    if fn is None:
        return value
    parsed, errs = fn(value if isinstance(value, str) else str(value))
    if errs:
        errors[key] = list(errs)
        return value
    if scalar.primitive == "Int":
        coerced, ok = coerce_int(parsed, False)
        return coerced if ok else value
    return parsed


def _parse_structured_json(
    ctx: WalkContext,
    scalar: ScalarDef,
    shape: str,
    value: Any,
    key: str,
    errors: ValidationErrors,
) -> Any:
    """Parse a value of a JSON object or array scalar
    (``ScalarDef.structured_json_type``) into that dict or list, the value
    every generated type holds.

    A string is the value's JSON text. When the scalar has a registered parser
    (Generic.StringMap), the text, or the JSON text of a dict or list, goes
    through it and its canonical text is decoded; a parser error is the result.
    Text that does not decode to the declared shape is kept as it is, for
    validation to report.
    """
    text = value if isinstance(value, str) else None
    fn = ctx.parse_registry.get(scalar.name) if scalar.has_custom_parse else None
    if fn is not None:
        if text is None:
            try:
                text = json.dumps(value, separators=(",", ":"), allow_nan=False)
            except (TypeError, ValueError):
                return value
        parsed, errs = fn(text)
        if errs:
            errors[key] = list(errs)
            return value
        text = parsed
    if text is None:
        return value
    try:
        decoded = json.loads(text)
    except ValueError:
        return value
    return decoded if json_shape_of(decoded) == shape else value


def _apply_builtin(name: str, value: Any, strict: bool) -> tuple[Any, bool]:
    if name == "Int":
        return coerce_int(value, strict)
    if name == "Float":
        return coerce_float(value, strict)
    if name == "Boolean":
        return coerce_bool(value, strict)
    if name in ("String", "ID"):
        return (value, True) if isinstance(value, str) else (value, False)
    return value, True


def _walk_single_field(
    ctx: WalkContext,
    field: FieldDef,
    key: str,
    kind: str,
    value: Any,
    result: dict[str, Any],
    errors: ValidationErrors,
) -> None:
    if kind == "scalar":
        scalar = ctx.schema.scalars.get(field.type_ref.name)
        if scalar is None:
            result[key] = value
            return
        try:
            out, ok = _apply_scalar(ctx, scalar, value)
        except ValueError as exc:
            append_field_error(
                errors,
                key,
                ValidationError(validator="normalize", message=f"invalid {scalar.name}: {exc}"),
            )
            result[key] = value
            return
        if not ok:
            append_field_error(
                errors,
                key,
                ValidationError(validator="type", message=_type_mismatch_message(scalar)),
            )
            result[key] = value
            return
        result[key] = _apply_scalar_parse(ctx, scalar, out, key, errors)
        return
    if kind == "enum":
        result[key] = value
        return
    if kind == "builtin":
        out, ok = _apply_builtin(field.type_ref.name, value, ctx.strict)
        if not ok:
            append_field_error(
                errors,
                key,
                ValidationError(validator="type", message=f"expected {field.type_ref.name} value"),
            )
            result[key] = value
            return
        result[key] = out
        return
    if kind == "type":
        td = ctx.schema.types.get(field.type_ref.name)
        if td is None or not _is_object(value):
            append_field_error(
                errors, key, ValidationError(validator="type", message="expected object value")
            )
            result[key] = value
            return
        nested = walk_type_def(ctx, td, value)
        if _has_errors(nested[1]):
            errors[key] = nested[1]
        result[key] = nested[0]
        return
    if kind == "input":
        td = ctx.schema.inputs.get(field.type_ref.name)
        if td is None or not _is_object(value):
            append_field_error(
                errors, key, ValidationError(validator="type", message="expected object value")
            )
            result[key] = value
            return
        nested = walk_type_def(ctx, td, value)
        if _has_errors(nested[1]):
            errors[key] = nested[1]
        result[key] = nested[0]


def _walk_array_elem(
    ctx: WalkContext,
    field: FieldDef,
    kind: str,
    elem: Any,
    elem_key: str,
    errors: ValidationErrors,
) -> Any:
    if elem is None:
        return None
    if kind == "scalar":
        scalar = ctx.schema.scalars.get(field.type_ref.name)
        if scalar is None:
            return elem
        v, ok = _apply_scalar(ctx, scalar, elem)
        if not ok:
            append_field_error(
                errors,
                elem_key,
                ValidationError(validator="type", message=_type_mismatch_message(scalar)),
            )
            return elem
        return _apply_scalar_parse(ctx, scalar, v, elem_key, errors)
    if kind == "enum":
        return elem
    if kind == "builtin":
        v, ok = _apply_builtin(field.type_ref.name, elem, ctx.strict)
        if not ok:
            append_field_error(
                errors,
                elem_key,
                ValidationError(
                    validator="type", message=f"expected {field.type_ref.name} value"
                ),
            )
            return elem
        return v
    if kind in ("type", "input"):
        defs = ctx.schema.types if kind == "type" else ctx.schema.inputs
        td = defs.get(field.type_ref.name)
        if td is None or not _is_object(elem):
            append_field_error(
                errors,
                elem_key,
                ValidationError(validator="type", message="expected object value"),
            )
            return elem
        nested = walk_type_def(ctx, td, elem)
        if _has_errors(nested[1]):
            errors[elem_key] = nested[1]
        return nested[0]
    return elem


def _walk_array_field(
    ctx: WalkContext,
    field: FieldDef,
    key: str,
    kind: str,
    value: Any,
    result: dict[str, Any],
    errors: ValidationErrors,
) -> None:
    if not isinstance(value, list):
        append_field_error(
            errors, key, ValidationError(validator="type", message="expected array value")
        )
        result[key] = value
        return
    if not field.type_ref.is_array_of_arrays:
        result[key] = [
            _walk_array_elem(ctx, field, kind, elem, f"{key}[{index}]", errors)
            for index, elem in enumerate(value)
        ]
        return
    # A list of lists: walk every inner list. A null inner list passes
    # through; validation rejects it.
    out: list[Any] = []
    for index, row in enumerate(value):
        row_key = f"{key}[{index}]"
        if row is None:
            out.append(None)
            continue
        if not isinstance(row, list):
            append_field_error(
                errors, row_key, ValidationError(validator="type", message="expected an array")
            )
            out.append(row)
            continue
        out.append(
            [
                _walk_array_elem(ctx, field, kind, elem, f"{row_key}[{inner_index}]", errors)
                for inner_index, elem in enumerate(row)
            ]
        )
    result[key] = out


def _walk_field(
    ctx: WalkContext,
    field: FieldDef,
    raw: dict[str, Any],
    result: dict[str, Any],
    errors: ValidationErrors,
) -> None:
    key = field_key(field)
    present = key in raw
    value = raw.get(key) if present else None

    if not present:
        if field.default_value is None:
            return
        if field.type_ref.is_array:
            return
        kind = _resolve_ref_kind(ctx.schema, field.type_ref)
        if kind in ("type", "input"):
            return
        scalar = ctx.schema.scalars.get(field.type_ref.name) if kind == "scalar" else None
        v, ok = apply_default(field, scalar)
        if not ok:
            append_field_error(
                errors,
                key,
                ValidationError(
                    validator="default",
                    message="default value is not parseable for declared type",
                ),
            )
            return
        result[key] = v
        return

    if value is None:
        result[key] = None
        return

    kind = _resolve_ref_kind(ctx.schema, field.type_ref)
    if field.type_ref.is_array:
        _walk_array_field(ctx, field, key, kind, value, result, errors)
        return
    _walk_single_field(ctx, field, key, kind, value, result, errors)


def walk_type_def(
    ctx: WalkContext, td: TypeDef, raw: dict[str, Any]
) -> tuple[dict[str, Any], ValidationErrors]:
    result: dict[str, Any] = {}
    errors = new_validation_errors()
    known: set[str] = set()
    for field in td.fields:
        key = field_key(field)
        known.add(key)
        _walk_field(ctx, field, raw, result, errors)
    if ctx.strict:
        for k in raw.keys():
            if k in known:
                continue
            append_field_error(
                errors, k, ValidationError(validator="unknown_field", message="unknown field")
            )
    return result, errors


__all__ = ["WalkContext", "walk_type_def", "field_key"]
