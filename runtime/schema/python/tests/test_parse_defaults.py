"""FieldDef.default_value application during parse."""

from __future__ import annotations

from psgen_schema_runtime import parse_schema, parse_type
from psgen_schema_runtime.parse.defaults import apply_default
from psgen_schema_runtime.parse.registry import create_default_scalar_parse_registry
from psgen_schema_runtime.validation.types import FieldDef, ScalarDef, TypeRef


def _field(name: str, type_name: str, default: str | None) -> FieldDef:
    return FieldDef(
        name=name,
        json_key=name,
        default_value=default,
        type_ref=TypeRef(name=type_name),
    )


def test_default_int():
    v, ok = apply_default(_field("n", "Int", "42"), None)
    assert ok and v == 42


def test_default_int_invalid():
    v, ok = apply_default(_field("n", "Int", "not-a-number"), None)
    assert not ok


def test_default_float():
    v, ok = apply_default(_field("n", "Float", "3.14"), None)
    assert ok and v == 3.14


def test_default_boolean():
    v, ok = apply_default(_field("b", "Boolean", "true"), None)
    assert ok and v is True
    v, ok = apply_default(_field("b", "Boolean", "0"), None)
    assert ok and v is False


def test_default_string_passthrough():
    v, ok = apply_default(_field("s", "String", "hello"), None)
    assert ok and v == "hello"


def test_default_scalar_kind_uses_scalar_primitive():
    scalar = ScalarDef(name="Custom", primitive="Int")
    v, ok = apply_default(_field("n", "Custom", "7"), scalar)
    assert ok and v == 7


def test_temporal_integer_units_have_default_parsers():
    registry = create_default_scalar_parse_registry()

    for unit in ("Milliseconds", "Seconds", "Minutes", "Hours", "Days"):
        assert registry.has(f"Temporal.{unit}")


def test_custom_integer_parser_enforces_scalar_bounds():
    schema = parse_schema(
        {
            "definitions": {
                "Finance.Money": {
                    "type": "integer",
                    "minimum": 0,
                    "maximum": 9007199254740991,
                    "x-hasCustomParse": True,
                },
                "Invoice": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "amount": {"$ref": "#/definitions/Finance.Money"},
                    },
                },
            },
        }
    )

    result = parse_type(schema, "Invoice", {"amount": -1})
    assert result.errors["amount"][0].validator == "parse"
    assert result.data["amount"] == -1


def test_default_applied_when_field_absent():
    schema = parse_schema(
        {
            "definitions": {
                "T": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "n": {"type": "integer", "default": "9"},
                        "s": {"type": "string", "default": "hi"},
                    },
                }
            }
        }
    )
    result = parse_type(schema, "T", {})
    assert result.data == {"n": 9, "s": "hi"}


def test_default_not_applied_when_field_present():
    schema = parse_schema(
        {
            "definitions": {
                "T": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "n": {"type": "integer", "default": "9"},
                    },
                }
            }
        }
    )
    result = parse_type(schema, "T", {"n": 1})
    assert result.data == {"n": 1}


def test_default_skipped_for_arrays():
    schema = parse_schema(
        {
            "definitions": {
                "T": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "items": {
                            "type": "array",
                            "items": {"type": "string"},
                            "default": "ignored",
                        },
                    },
                }
            }
        }
    )
    result = parse_type(schema, "T", {})
    assert result.data == {}  # array defaults are not applied
