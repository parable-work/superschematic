"""Schema-aware secret masking.

Replaces values of fields marked ``@secret`` with their zero-equivalent
before the payload leaves a server. Non-secret fields are deep-copied as-is.
"""

from __future__ import annotations

import copy
from typing import Any

from ..validation.resolver import resolve_schema
from ..validation.types import FieldDef, Schema, SchemaInput, TypeDef, TypeRef


_BUILTIN_SCALARS = frozenset({"String", "Int", "Float", "Boolean", "ID"})


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


def deep_copy_value(v: Any) -> Any:
    return copy.deepcopy(v)


def deep_copy_map(data: dict[str, Any]) -> dict[str, Any]:
    return {k: deep_copy_value(v) for k, v in data.items()}


def _zero_for_primitive(name: str) -> Any:
    if name in ("String", "ID"):
        return ""
    if name == "Int":
        return 0
    if name == "Float":
        return 0
    if name == "Boolean":
        return False
    return None


def _zero_secret_value(schema: Schema, field: FieldDef) -> Any:
    if not field.required:
        return None
    if field.type_ref.is_array:
        return []
    kind = _resolve_ref_kind(schema, field.type_ref)
    if kind in ("type", "input"):
        return {}
    if kind == "enum":
        return ""
    if kind == "scalar":
        scalar = schema.scalars.get(field.type_ref.name)
        return _zero_for_primitive(scalar.primitive) if scalar else None
    if kind == "builtin":
        return _zero_for_primitive(field.type_ref.name)
    return None


def _mask_array_field(
    schema: Schema, field: FieldDef, kind: str, arr: list[Any]
) -> list[Any]:
    out: list[Any] = []
    for elem in arr:
        if elem is None:
            out.append(None)
            continue
        if kind == "type":
            td = schema.types.get(field.type_ref.name)
            if td is not None and _is_object(elem):
                out.append(_mask_type_def(schema, td, elem))
                continue
        elif kind == "input":
            td = schema.inputs.get(field.type_ref.name)
            if td is not None and _is_object(elem):
                out.append(_mask_type_def(schema, td, elem))
                continue
        out.append(deep_copy_value(elem))
    return out


def _mask_field(schema: Schema, field: FieldDef, value: Any) -> Any:
    if value is None:
        return None
    if field.secret:
        return _zero_secret_value(schema, field)
    kind = _resolve_ref_kind(schema, field.type_ref)
    if field.type_ref.is_array:
        if not isinstance(value, list):
            return deep_copy_value(value)
        if field.type_ref.is_array_of_arrays:
            return [
                _mask_array_field(schema, field, kind, row)
                if isinstance(row, list)
                else deep_copy_value(row)
                for row in value
            ]
        return _mask_array_field(schema, field, kind, value)
    if kind == "type":
        td = schema.types.get(field.type_ref.name)
        if td is None or not _is_object(value):
            return deep_copy_value(value)
        return _mask_type_def(schema, td, value)
    if kind == "input":
        td = schema.inputs.get(field.type_ref.name)
        if td is None or not _is_object(value):
            return deep_copy_value(value)
        return _mask_type_def(schema, td, value)
    return deep_copy_value(value)


def _mask_type_def(schema: Schema, td: TypeDef, data: dict[str, Any]) -> dict[str, Any]:
    masked = deep_copy_map(data)
    for field in td.fields:
        key = _field_key(field)
        if key not in data:
            continue
        masked[key] = _mask_field(schema, field, data[key])
    return masked


def mask_type(
    schema: SchemaInput, type_name: str, data: dict[str, Any] | None
) -> dict[str, Any] | None:
    if data is None:
        return None
    resolved = resolve_schema(schema)
    td = resolved.types.get(type_name)
    if td is None:
        return deep_copy_map(data)
    return _mask_type_def(resolved, td, data)


def mask_input(
    schema: SchemaInput, input_name: str, data: dict[str, Any] | None
) -> dict[str, Any] | None:
    if data is None:
        return None
    resolved = resolve_schema(schema)
    td = resolved.inputs.get(input_name)
    if td is None:
        return deep_copy_map(data)
    return _mask_type_def(resolved, td, data)


__all__ = ["deep_copy_map", "deep_copy_value", "mask_input", "mask_type"]
