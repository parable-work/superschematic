"""Schema-aware merge for secret fields.

When a config form comes back with a secret field zeroed (because the client never
received the raw value), replace those zero values with the existing stored
value before writing. Non-secret fields always take the new value.
"""

from __future__ import annotations

from typing import Any

from ..mask import deep_copy_map, deep_copy_value
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


def _is_zero_value(value: Any) -> bool:
    if value is None:
        return True
    if isinstance(value, str):
        return value == ""
    if isinstance(value, bool):
        return value is False
    if isinstance(value, (int, float)):
        return value == 0
    if isinstance(value, list):
        return len(value) == 0
    if isinstance(value, dict):
        return len(value) == 0
    return False


def _merge_array_field(
    schema: Schema,
    field: FieldDef,
    kind: str,
    new_arr: list[Any],
    existing_arr: list[Any],
) -> list[Any]:
    merged: list[Any] = []
    for index, new_elem in enumerate(new_arr):
        if new_elem is None:
            merged.append(None)
            continue
        existing_elem = existing_arr[index] if index < len(existing_arr) else None
        if kind == "type":
            td = schema.types.get(field.type_ref.name)
            if td is not None and _is_object(new_elem) and _is_object(existing_elem):
                merged.append(_merge_type_def(schema, td, new_elem, existing_elem))
                continue
        elif kind == "input":
            td = schema.inputs.get(field.type_ref.name)
            if td is not None and _is_object(new_elem) and _is_object(existing_elem):
                merged.append(_merge_type_def(schema, td, new_elem, existing_elem))
                continue
        merged.append(deep_copy_value(new_elem))
    return merged


def _merge_nested_array_field(
    schema: Schema,
    field: FieldDef,
    kind: str,
    new_rows: list[Any],
    existing_rows: list[Any],
) -> list[Any]:
    """Merge a list of lists row by row, pairing inner lists by index."""
    merged: list[Any] = []
    for index, new_row in enumerate(new_rows):
        existing_row = existing_rows[index] if index < len(existing_rows) else None
        if isinstance(new_row, list) and isinstance(existing_row, list):
            merged.append(_merge_array_field(schema, field, kind, new_row, existing_row))
            continue
        merged.append(deep_copy_value(new_row))
    return merged


def _merge_type_def(
    schema: Schema,
    td: TypeDef,
    new_data: dict[str, Any],
    existing_data: dict[str, Any],
) -> dict[str, Any]:
    merged = deep_copy_map(new_data)
    for field in td.fields:
        key = _field_key(field)
        new_exists = key in new_data
        existing_exists = key in existing_data
        new_val = new_data.get(key)
        existing_val = existing_data.get(key)

        if field.secret:
            if (not new_exists or _is_zero_value(new_val)) and existing_exists:
                merged[key] = deep_copy_value(existing_val)
            continue

        if not new_exists or new_val is None:
            continue
        if existing_val is None:
            continue

        kind = _resolve_ref_kind(schema, field.type_ref)
        if field.type_ref.is_array:
            if isinstance(new_val, list) and isinstance(existing_val, list):
                if field.type_ref.is_array_of_arrays:
                    merged[key] = _merge_nested_array_field(
                        schema, field, kind, new_val, existing_val
                    )
                else:
                    merged[key] = _merge_array_field(schema, field, kind, new_val, existing_val)
            continue

        if kind == "type":
            nested = schema.types.get(field.type_ref.name)
            if nested is not None and _is_object(new_val) and _is_object(existing_val):
                merged[key] = _merge_type_def(schema, nested, new_val, existing_val)
        elif kind == "input":
            nested = schema.inputs.get(field.type_ref.name)
            if nested is not None and _is_object(new_val) and _is_object(existing_val):
                merged[key] = _merge_type_def(schema, nested, new_val, existing_val)
    return merged


def merge_type(
    schema: SchemaInput,
    type_name: str,
    new_data: dict[str, Any] | None,
    existing_data: dict[str, Any] | None,
) -> dict[str, Any] | None:
    if new_data is None:
        return None
    if existing_data is None:
        return deep_copy_map(new_data)
    resolved = resolve_schema(schema)
    td = resolved.types.get(type_name)
    if td is None:
        return deep_copy_map(new_data)
    return _merge_type_def(resolved, td, new_data, existing_data)


def merge_input(
    schema: SchemaInput,
    input_name: str,
    new_data: dict[str, Any] | None,
    existing_data: dict[str, Any] | None,
) -> dict[str, Any] | None:
    if new_data is None:
        return None
    if existing_data is None:
        return deep_copy_map(new_data)
    resolved = resolve_schema(schema)
    td = resolved.inputs.get(input_name)
    if td is None:
        return deep_copy_map(new_data)
    return _merge_type_def(resolved, td, new_data, existing_data)


__all__ = ["merge_input", "merge_type"]
