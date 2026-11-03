"""Serialize / marshal tests: schema order, nil-slice normalization, mask, strict."""

from __future__ import annotations

import json

from superschematic_schema_runtime import (
    marshal_type,
    parse_schema,
    parse_type,
    type_to_map,
)
from superschematic_schema_runtime.parse import ParseOptions
from superschematic_schema_runtime.serialize import SerializeOptions


def _schema():
    return parse_schema(
        {
            "definitions": {
                "T": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "a": {"type": "string"},
                        "b": {"type": "string"},
                        "tags": {"type": "array", "items": {"type": "string"}},
                        "secret": {"type": "string", "x-secret": True},
                    },
                    "required": ["a"],
                }
            }
        }
    )


def test_type_to_map_drops_unknown_in_lenient():
    schema = _schema()
    out = type_to_map(schema, "T", {"a": "1", "b": "2", "extra": "x"})
    assert "extra" not in out.data
    assert out.data == {"a": "1", "b": "2"}


def test_strict_reports_unknown_field():
    schema = _schema()
    out = type_to_map(schema, "T", {"a": "1", "extra": "x"}, options=SerializeOptions(strict=True))
    assert "extra" in out.errors
    assert out.errors["extra"][0].validator == "unknown_field"


def test_strict_drops_unknown_field_of_an_allowing_type():
    # D87: a type that is @allowUnknownFields accepts and drops a key it
    # does not declare in strict mode too.
    schema = parse_schema(
        {
            "definitions": {
                "Hook": {
                    "x-kind": "type",
                    "x-allowUnknownFields": True,
                    "type": "object",
                    "properties": {"item_id": {"type": "string"}},
                    "required": ["item_id"],
                }
            }
        }
    )
    assert schema.types["Hook"].allow_unknown_fields
    payload = {"item_id": "a", "environment": "sandbox"}
    out = type_to_map(schema, "Hook", payload, options=SerializeOptions(strict=True))
    assert out.errors == {}
    assert out.data == {"item_id": "a"}
    parsed = parse_type(schema, "Hook", payload, options=ParseOptions(strict=True))
    assert parsed.errors == {}
    assert parsed.data == {"item_id": "a"}
    refused = parse_type(schema, "Hook", {"item_id": 7, "environment": "x"}, options=ParseOptions(strict=True))
    assert "item_id" in refused.errors and "environment" not in refused.errors


def test_marshal_preserves_schema_order():
    schema = _schema()
    # Provide keys out of order -- output JSON should follow schema field order.
    out = marshal_type(schema, "T", {"tags": ["x"], "b": "B", "a": "A"})
    decoded = json.loads(out.json)
    assert list(decoded.keys()) == ["a", "b", "tags"]


def test_nil_array_becomes_empty_list():
    schema = _schema()
    out = marshal_type(schema, "T", {"a": "1", "tags": None})
    decoded = json.loads(out.json)
    assert decoded["tags"] == []


def test_mask_secrets_via_serialize_option():
    schema = _schema()
    out = type_to_map(
        schema, "T", {"a": "1", "secret": "value"}, options=SerializeOptions(mask_secrets=True)
    )
    assert out.data["secret"] is None  # optional secret -> null


def test_unknown_type_returns_error():
    schema = _schema()
    out = type_to_map(schema, "Nope", {})
    assert "" in out.errors
    assert out.errors[""][0].validator == "type"
