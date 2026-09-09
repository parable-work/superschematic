"""Schema-driven validation."""

from __future__ import annotations

import re
from typing import Any

from .. import ValidationError
from ..errors import (
    ValidationErrors,
    append_field_error,
    has_errors as _has_errors,
    new_validation_errors,
)
from .registry import ScalarValidatorRegistry, create_default_scalar_validator_registry
from .resolver import resolve_schema
from .types import FieldDef, ScalarDef, Schema, SchemaInput, TypeDef


_BUILTIN_SCALARS = frozenset({"String", "Int", "Float", "Boolean", "ID"})

_default_registry: ScalarValidatorRegistry | None = None


def _get_default_registry() -> ScalarValidatorRegistry:
    global _default_registry
    if _default_registry is None:
        _default_registry = create_default_scalar_validator_registry()
    return _default_registry


class ValidationOptions:
    """Options bag accepted by ``validate_type_errors`` / ``validate_input_errors``."""

    __slots__ = ("scalar_registry",)

    def __init__(self, scalar_registry: ScalarValidatorRegistry | None = None) -> None:
        self.scalar_registry = scalar_registry


def _resolve_registry(options: ValidationOptions | None) -> ScalarValidatorRegistry:
    if options is not None and options.scalar_registry is not None:
        return options.scalar_registry
    return _get_default_registry()


def _is_object(value: Any) -> bool:
    return isinstance(value, dict)


def _to_object(value: Any) -> dict[str, Any]:
    return value if _is_object(value) else {}


def _to_number(value: Any) -> float | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    return None


def _resolve_ref_kind(schema: Schema, type_name: str) -> str:
    if type_name in schema.scalars:
        return "scalar"
    if type_name in schema.enums:
        return "enum"
    if type_name in schema.types:
        return "type"
    if type_name in schema.inputs:
        return "input"
    return "builtin"


def _set_field_errors(
    errors: ValidationErrors, key: str, field_errors: list[ValidationError]
) -> None:
    if field_errors:
        errors[key] = list(field_errors)


def _add_nested_errors(
    errors: ValidationErrors, key: str, nested: ValidationErrors
) -> None:
    if _has_errors(nested):
        errors[key] = nested


def _validate_enum_value(
    schema: Schema, enum_name: str, value: Any
) -> list[ValidationError]:
    if not isinstance(value, str):
        return [ValidationError(validator="type", message=f"{enum_name} must be a string.")]
    enum_def = schema.enums.get(enum_name)
    if enum_def is None:
        return []
    for enum_value in enum_def.values:
        candidate = enum_value.serialized_as or enum_value.name
        if candidate == value:
            return []
    return [
        ValidationError(
            validator="enum", message=f"{enum_def.name} must be one of the allowed values."
        )
    ]


def _validate_string_constraints(
    scalar: ScalarDef,
    value: Any,
    apply_required: bool,
    registry: ScalarValidatorRegistry,
) -> list[ValidationError]:
    if not isinstance(value, str):
        if apply_required:
            return [ValidationError(validator="type", message=f"{scalar.name} must be a string.")]
        return []
    if value == "" and apply_required:
        return [ValidationError(validator="required", message=f"{scalar.name} is required.")]

    errors: list[ValidationError] = []

    if scalar.min_length > 0 and len(value) < scalar.min_length:
        errors.append(
            ValidationError(
                validator="minLength",
                message=f"{scalar.name} must be at least {scalar.min_length} characters.",
            )
        )
    if scalar.max_length > 0 and len(value) > scalar.max_length:
        errors.append(
            ValidationError(
                validator="maxLength",
                message=f"{scalar.name} must be at most {scalar.max_length} characters.",
            )
        )
    if scalar.pattern:
        try:
            if not re.search(scalar.pattern, value):
                errors.append(
                    ValidationError(
                        validator="pattern",
                        message=f"{scalar.name} has an invalid format.",
                    )
                )
        except re.error:
            errors.append(
                ValidationError(
                    validator="pattern", message=f"{scalar.name} has an invalid format."
                )
            )
    if scalar.reserved_words:
        if scalar.case_insensitive:
            is_reserved = any(
                rw.lower() == value.lower() for rw in scalar.reserved_words
            )
        else:
            is_reserved = value in scalar.reserved_words
        if is_reserved:
            errors.append(
                ValidationError(
                    validator="reservedWord",
                    message=f"{scalar.name} contains a reserved word.",
                )
            )

    if scalar.has_custom_validate or registry.has(scalar.name):
        scalar_fn = registry.get(scalar.name)
        if scalar_fn is not None:
            custom_errors = scalar_fn(value) or []
            errors.extend(custom_errors)

    return errors


def _validate_int_constraints(scalar: ScalarDef, value: Any) -> list[ValidationError]:
    parsed = _to_number(value)
    if parsed is None or not float(parsed).is_integer():
        return [ValidationError(validator="type", message=f"{scalar.name} must be a number.")]
    errors: list[ValidationError] = []
    if scalar.minimum is not None and parsed < scalar.minimum:
        errors.append(
            ValidationError(
                validator="min", message=f"{scalar.name} must be at least {scalar.minimum}."
            )
        )
    if scalar.maximum is not None and parsed > scalar.maximum:
        errors.append(
            ValidationError(
                validator="max", message=f"{scalar.name} must be at most {scalar.maximum}."
            )
        )
    return errors


def _validate_float_constraints(scalar: ScalarDef, value: Any) -> list[ValidationError]:
    parsed = _to_number(value)
    if parsed is None:
        return [ValidationError(validator="type", message=f"{scalar.name} must be a number.")]
    errors: list[ValidationError] = []
    if scalar.minimum is not None and parsed < scalar.minimum:
        errors.append(
            ValidationError(
                validator="min", message=f"{scalar.name} must be at least {scalar.minimum}."
            )
        )
    if scalar.maximum is not None and parsed > scalar.maximum:
        errors.append(
            ValidationError(
                validator="max", message=f"{scalar.name} must be at most {scalar.maximum}."
            )
        )
    return errors


def _validate_type_scalar_value(
    scalar: ScalarDef, value: Any, required: bool
) -> list[ValidationError]:
    if value is None:
        return (
            [ValidationError(validator="required", message=f"{scalar.name} is required.")]
            if required
            else []
        )
    if not isinstance(value, dict):
        return [ValidationError(validator="type", message=f"{scalar.name} must be an object.")]
    return []


def _validate_bytes_scalar_value(
    scalar: ScalarDef, value: Any, required: bool
) -> list[ValidationError]:
    if value is None:
        return (
            [ValidationError(validator="required", message=f"{scalar.name} is required.")]
            if required
            else []
        )
    if isinstance(value, (str, bytes, bytearray, memoryview)):
        return []
    return [ValidationError(validator="type", message=f"{scalar.name} must be bytes.")]


def _validate_scalar_value(
    scalar: ScalarDef,
    value: Any,
    required: bool,
    registry: ScalarValidatorRegistry,
) -> list[ValidationError]:
    if scalar.primitive == "Int":
        return _validate_int_constraints(scalar, value)
    if scalar.primitive == "Float":
        return _validate_float_constraints(scalar, value)
    if scalar.primitive == "Type":
        return _validate_type_scalar_value(scalar, value, required)
    if scalar.primitive == "Bytes":
        return _validate_bytes_scalar_value(scalar, value, required)
    return _validate_string_constraints(scalar, value, required, registry)


def _validate_field_level_string_constraints(
    field: FieldDef, value: str
) -> list[ValidationError]:
    out: list[ValidationError] = []
    if field.validate_min_length is not None and len(value) < field.validate_min_length:
        out.append(
            ValidationError(
                validator="minLength",
                message=f"{field.name} must be at least {field.validate_min_length} characters.",
            )
        )
    if field.validate_max_length is not None and len(value) > field.validate_max_length:
        out.append(
            ValidationError(
                validator="maxLength",
                message=f"{field.name} must be at most {field.validate_max_length} characters.",
            )
        )
    if field.validate_pattern:
        try:
            if not re.search(field.validate_pattern, value):
                out.append(
                    ValidationError(
                        validator="pattern",
                        message=f"{field.name} has an invalid format.",
                    )
                )
        except re.error:
            out.append(
                ValidationError(
                    validator="pattern", message=f"{field.name} has an invalid format."
                )
            )
    return out


def _validate_field_level_numeric_constraints(
    field: FieldDef, value: float
) -> list[ValidationError]:
    out: list[ValidationError] = []
    if field.validate_min is not None and value < field.validate_min:
        out.append(
            ValidationError(
                validator="min",
                message=f"{field.name} must be at least {field.validate_min}.",
            )
        )
    if field.validate_max is not None and value > field.validate_max:
        out.append(
            ValidationError(
                validator="max",
                message=f"{field.name} must be at most {field.validate_max}.",
            )
        )
    return out


def _has_field_level_constraints(field: FieldDef) -> bool:
    return (
        field.validate_min is not None
        or field.validate_max is not None
        or field.validate_min_length is not None
        or field.validate_max_length is not None
        or field.validate_pattern != ""
    )


def _apply_field_level_constraints(field: FieldDef, value: Any) -> list[ValidationError]:
    if isinstance(value, str):
        return _validate_field_level_string_constraints(field, value)
    numeric = _to_number(value)
    if numeric is not None:
        return _validate_field_level_numeric_constraints(field, numeric)
    return []


def _validate_single_field(
    schema: Schema,
    field: FieldDef,
    key: str,
    value: Any,
    errors: ValidationErrors,
    registry: ScalarValidatorRegistry,
) -> None:
    kind = _resolve_ref_kind(schema, field.type_ref.name)

    if kind == "scalar":
        scalar = schema.scalars.get(field.type_ref.name)
        if scalar is None:
            return
        field_errors = _validate_scalar_value(scalar, value, field.required, registry)
        if _has_field_level_constraints(field):
            field_errors.extend(_apply_field_level_constraints(field, value))
        _set_field_errors(errors, key, field_errors)
        return

    if kind == "enum":
        field_errors = _validate_enum_value(schema, field.type_ref.name, value)
        _set_field_errors(errors, key, field_errors)
        return

    if kind == "type":
        nested = schema.types.get(field.type_ref.name)
        if nested is not None and _is_object(value):
            nested_errors = _validate_type_def(schema, nested, value, registry)
            _add_nested_errors(errors, key, nested_errors)
        return

    if kind == "input":
        nested = schema.inputs.get(field.type_ref.name)
        if nested is not None and _is_object(value):
            nested_errors = _validate_type_def(schema, nested, value, registry)
            _add_nested_errors(errors, key, nested_errors)
        return

    # builtin
    if (
        field.required
        and field.type_ref.name in ("String", "ID")
        and isinstance(value, str)
        and value == ""
    ):
        append_field_error(
            errors,
            key,
            ValidationError(validator="required", message=f"{field.type_ref.name} is required."),
        )
        return
    if _has_field_level_constraints(field):
        field_errors = _apply_field_level_constraints(field, value)
        _set_field_errors(errors, key, field_errors)


def _validate_array_field(
    schema: Schema,
    field: FieldDef,
    key: str,
    value: Any,
    errors: ValidationErrors,
    registry: ScalarValidatorRegistry,
) -> None:
    if not isinstance(value, list):
        return
    kind = _resolve_ref_kind(schema, field.type_ref.name)
    for index, element in enumerate(value):
        element_key = f"{key}[{index}]"
        if element is None:
            if field.type_ref.elem_non_null:
                append_field_error(
                    errors,
                    element_key,
                    ValidationError(
                        validator="required", message=f"{field.type_ref.name} is required."
                    ),
                )
            continue
        if kind == "scalar":
            scalar = schema.scalars.get(field.type_ref.name)
            if scalar is None:
                continue
            element_errors = _validate_scalar_value(scalar, element, field.required, registry)
            _set_field_errors(errors, element_key, element_errors)
            continue
        if kind == "enum":
            element_errors = _validate_enum_value(schema, field.type_ref.name, element)
            _set_field_errors(errors, element_key, element_errors)
            continue
        if kind == "type":
            nested = schema.types.get(field.type_ref.name)
            if nested is not None and _is_object(element):
                nested_errors = _validate_type_def(schema, nested, element, registry)
                _add_nested_errors(errors, element_key, nested_errors)
            continue
        if kind == "input":
            nested = schema.inputs.get(field.type_ref.name)
            if nested is not None and _is_object(element):
                nested_errors = _validate_type_def(schema, nested, element, registry)
                _add_nested_errors(errors, element_key, nested_errors)


def _validate_field(
    schema: Schema,
    field: FieldDef,
    data: dict[str, Any],
    errors: ValidationErrors,
    registry: ScalarValidatorRegistry,
) -> None:
    key = field.json_key or field.name
    has_key = key in data
    value = data.get(key)

    if field.required:
        if not has_key or value is None:
            append_field_error(
                errors,
                key,
                ValidationError(validator="required", message=f"{field.name} is required."),
            )
            return
        if field.type_ref.is_array and (not isinstance(value, list) or len(value) == 0):
            append_field_error(
                errors,
                key,
                ValidationError(validator="required", message=f"{field.name} is required."),
            )
            return

    if not has_key or value is None:
        return

    if field.type_ref.is_array:
        if isinstance(value, list):
            if field.validate_list_min is not None and len(value) < field.validate_list_min:
                append_field_error(
                    errors,
                    key,
                    ValidationError(
                        validator="listMin",
                        message=f"{field.name} must have at least {field.validate_list_min} items.",
                    ),
                )
            if field.validate_list_max is not None and len(value) > field.validate_list_max:
                append_field_error(
                    errors,
                    key,
                    ValidationError(
                        validator="listMax",
                        message=f"{field.name} must have at most {field.validate_list_max} items.",
                    ),
                )
        _validate_array_field(schema, field, key, value, errors, registry)
        return

    _validate_single_field(schema, field, key, value, errors, registry)


def _validate_type_def(
    schema: Schema,
    type_def: TypeDef,
    data: dict[str, Any],
    registry: ScalarValidatorRegistry,
) -> ValidationErrors:
    errors = new_validation_errors()
    for field in type_def.fields:
        _validate_field(schema, field, data, errors, registry)
    return errors


def validate_type_errors(
    schema: SchemaInput,
    type_name: str,
    data: Any,
    options: ValidationOptions | None = None,
) -> ValidationErrors:
    """Validate ``data`` against ``schema.types[type_name]``; returns a (possibly empty)
    :data:`ValidationErrors`.
    """
    resolved = resolve_schema(schema)
    type_def = resolved.types.get(type_name)
    if type_def is None:
        return new_validation_errors()
    registry = _resolve_registry(options)
    return _validate_type_def(resolved, type_def, _to_object(data), registry)


def validate_input_errors(
    schema: SchemaInput,
    input_name: str,
    data: Any,
    options: ValidationOptions | None = None,
) -> ValidationErrors:
    """Validate ``data`` against ``schema.inputs[input_name]``."""
    resolved = resolve_schema(schema)
    type_def = resolved.inputs.get(input_name)
    if type_def is None:
        return new_validation_errors()
    registry = _resolve_registry(options)
    return _validate_type_def(resolved, type_def, _to_object(data), registry)


__all__ = [
    "ValidationOptions",
    "validate_type_errors",
    "validate_input_errors",
]
