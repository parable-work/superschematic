"""Schema-aware serialization.

Produces a sanitized map (``type_to_map`` / ``input_to_map``) or canonical
JSON bytes (``marshal_type`` / ``marshal_input``) honoring schema field
declaration order.

- Unknown fields are dropped in lenient mode; reported as
  ``{"validator": "unknown_field"}`` when ``strict=True``.
- Missing / ``None`` arrays become ``[]`` in output (not ``null``).
- ``mask_secrets=True`` composes with :mod:`psgen_schema_runtime.mask` to
  zero ``@secret`` fields before serializing.
- JSON keys are emitted in :class:`TypeDef` declaration order so output is
  byte-stable across calls.
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
from ..mask import mask_input, mask_type
from ..validation.resolver import resolve_schema
from ..validation.types import FieldDef, Schema, SchemaInput, TypeDef, TypeRef


_BUILTIN_SCALARS = frozenset({"String", "Int", "Float", "Boolean", "ID"})


@dataclass
class SerializeOptions:
    strict: bool = False
    mask_secrets: bool = False


@dataclass
class SerializeResult:
    data: dict[str, Any]
    errors: ValidationErrors


@dataclass
class MarshalResult:
    json: bytes
    errors: ValidationErrors


@dataclass
class _Ctx:
    schema: Schema
    strict: bool
    mask_secrets: bool


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


def _field_key(field: FieldDef) -> str:
    return field.json_key or field.name


def _ctx_from_options(schema: Schema, options: SerializeOptions | None) -> _Ctx:
    opts = options if options is not None else SerializeOptions()
    return _Ctx(schema=schema, strict=opts.strict, mask_secrets=opts.mask_secrets)


def _unknown_type_error(name: str, kind: str) -> SerializeResult:
    errs = new_validation_errors()
    append_field_error(
        errs, "", ValidationError(validator="type", message=f'unknown {kind} "{name}"')
    )
    return SerializeResult(data={}, errors=errs)


def _apply_mask(
    ctx: _Ctx, data: dict[str, Any], kind: str, name: str
) -> dict[str, Any]:
    if not ctx.mask_secrets:
        return data
    masked = (
        mask_input(ctx.schema, name, data) if kind == "input" else mask_type(ctx.schema, name, data)
    )
    return masked if masked is not None else data


def _walk_elem(
    ctx: _Ctx,
    field: FieldDef,
    kind: str,
    elem: Any,
    errors: ValidationErrors,
    path: str,
) -> Any:
    if elem is None:
        return None
    if kind == "type":
        td = ctx.schema.types.get(field.type_ref.name)
        if td is None or not _is_object(elem):
            return elem
        nested_errs = new_validation_errors()
        result = _walk_type_def(ctx, td, elem, nested_errs)
        if _has_errors(nested_errs):
            errors[path] = nested_errs
        return result
    if kind == "input":
        td = ctx.schema.inputs.get(field.type_ref.name)
        if td is None or not _is_object(elem):
            return elem
        nested_errs = new_validation_errors()
        result = _walk_type_def(ctx, td, elem, nested_errs)
        if _has_errors(nested_errs):
            errors[path] = nested_errs
        return result
    return elem


def _walk_field(
    ctx: _Ctx, field: FieldDef, value: Any, errors: ValidationErrors, path: str
) -> Any:
    if value is None:
        return [] if field.type_ref.is_array else None
    kind = _resolve_ref_kind(ctx.schema, field.type_ref)
    if field.type_ref.is_array:
        if not isinstance(value, list):
            return value
        return [_walk_elem(ctx, field, kind, value[i], errors, f"{path}[{i}]") for i in range(len(value))]
    if kind == "type":
        nested = ctx.schema.types.get(field.type_ref.name)
        if nested is None or not _is_object(value):
            return value
        nested_errs = new_validation_errors()
        result = _walk_type_def(ctx, nested, value, nested_errs)
        if _has_errors(nested_errs):
            errors[path] = nested_errs
        return result
    if kind == "input":
        nested = ctx.schema.inputs.get(field.type_ref.name)
        if nested is None or not _is_object(value):
            return value
        nested_errs = new_validation_errors()
        result = _walk_type_def(ctx, nested, value, nested_errs)
        if _has_errors(nested_errs):
            errors[path] = nested_errs
        return result
    return value


def _walk_type_def(
    ctx: _Ctx, td: TypeDef, data: dict[str, Any], errors: ValidationErrors
) -> dict[str, Any]:
    out: dict[str, Any] = {}
    known: set[str] = set()
    for field in td.fields:
        key = _field_key(field)
        known.add(key)
        if key not in data:
            continue
        out[key] = _walk_field(ctx, field, data[key], errors, key)
    if ctx.strict:
        for k in data.keys():
            if k in known:
                continue
            append_field_error(
                errors, k, ValidationError(validator="unknown_field", message="unknown field")
            )
    return out


def _serialize(
    ctx: _Ctx, td: TypeDef, data: dict[str, Any], kind: str, name: str
) -> SerializeResult:
    errors = new_validation_errors()
    src = _apply_mask(ctx, data, kind, name)
    out = _walk_type_def(ctx, td, src, errors)
    return SerializeResult(data=out, errors=errors)


# --- Canonical JSON writing (schema-ordered keys) ---


def _write_elem_value(ctx: _Ctx, field: FieldDef, kind: str, elem: Any) -> Any:
    if elem is None:
        return None
    if kind == "type":
        td = ctx.schema.types.get(field.type_ref.name)
        if td is not None and _is_object(elem):
            return _write_object(ctx, td, elem)
    elif kind == "input":
        td = ctx.schema.inputs.get(field.type_ref.name)
        if td is not None and _is_object(elem):
            return _write_object(ctx, td, elem)
    return elem


def _write_field_value(ctx: _Ctx, field: FieldDef, value: Any) -> Any:
    if value is None:
        return [] if field.type_ref.is_array else None
    kind = _resolve_ref_kind(ctx.schema, field.type_ref)
    if field.type_ref.is_array:
        if not isinstance(value, list):
            return value
        return [_write_elem_value(ctx, field, kind, elem) for elem in value]
    if kind == "type":
        nested = ctx.schema.types.get(field.type_ref.name)
        if nested is not None and _is_object(value):
            return _write_object(ctx, nested, value)
    elif kind == "input":
        nested = ctx.schema.inputs.get(field.type_ref.name)
        if nested is not None and _is_object(value):
            return _write_object(ctx, nested, value)
    return value


def _write_object(ctx: _Ctx, td: TypeDef, sanitized: dict[str, Any]) -> dict[str, Any]:
    """Emit a dict whose key insertion order matches the TypeDef declaration."""
    out: dict[str, Any] = {}
    emitted: set[str] = set()
    for field in td.fields:
        key = _field_key(field)
        if key not in sanitized:
            continue
        emitted.add(key)
        out[key] = _write_field_value(ctx, field, sanitized[key])
    for key, value in sanitized.items():
        if key in emitted:
            continue
        out[key] = value
    return out


def _marshal(
    ctx: _Ctx, td: TypeDef, data: dict[str, Any], kind: str, name: str
) -> MarshalResult:
    result = _serialize(ctx, td, data, kind, name)
    if _has_errors(result.errors):
        return MarshalResult(json=b"", errors=result.errors)
    try:
        ordered = _write_object(ctx, td, result.data)
        return MarshalResult(
            json=json.dumps(ordered, separators=(",", ":")).encode("utf-8"),
            errors=result.errors,
        )
    except (TypeError, ValueError) as exc:
        errors = result.errors
        append_field_error(errors, "", ValidationError(validator="json", message=str(exc)))
        return MarshalResult(json=b"", errors=errors)


def type_to_map(
    schema: SchemaInput,
    type_name: str,
    data: dict[str, Any] | None,
    options: SerializeOptions | None = None,
) -> SerializeResult:
    resolved = resolve_schema(schema)
    td = resolved.types.get(type_name)
    if td is None:
        return _unknown_type_error(type_name, "type")
    return _serialize(_ctx_from_options(resolved, options), td, data or {}, "type", type_name)


def input_to_map(
    schema: SchemaInput,
    input_name: str,
    data: dict[str, Any] | None,
    options: SerializeOptions | None = None,
) -> SerializeResult:
    resolved = resolve_schema(schema)
    td = resolved.inputs.get(input_name)
    if td is None:
        return _unknown_type_error(input_name, "input")
    return _serialize(_ctx_from_options(resolved, options), td, data or {}, "input", input_name)


def marshal_type(
    schema: SchemaInput,
    type_name: str,
    data: dict[str, Any] | None,
    options: SerializeOptions | None = None,
) -> MarshalResult:
    resolved = resolve_schema(schema)
    td = resolved.types.get(type_name)
    if td is None:
        errs = new_validation_errors()
        append_field_error(
            errs, "", ValidationError(validator="type", message=f'unknown type "{type_name}"')
        )
        return MarshalResult(json=b"", errors=errs)
    return _marshal(_ctx_from_options(resolved, options), td, data or {}, "type", type_name)


def marshal_input(
    schema: SchemaInput,
    input_name: str,
    data: dict[str, Any] | None,
    options: SerializeOptions | None = None,
) -> MarshalResult:
    resolved = resolve_schema(schema)
    td = resolved.inputs.get(input_name)
    if td is None:
        errs = new_validation_errors()
        append_field_error(
            errs, "", ValidationError(validator="type", message=f'unknown input "{input_name}"')
        )
        return MarshalResult(json=b"", errors=errs)
    return _marshal(_ctx_from_options(resolved, options), td, data or {}, "input", input_name)


__all__ = [
    "MarshalResult",
    "SerializeOptions",
    "SerializeResult",
    "input_to_map",
    "marshal_input",
    "marshal_type",
    "type_to_map",
]
