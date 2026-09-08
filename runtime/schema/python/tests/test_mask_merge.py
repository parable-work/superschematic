"""mask + merge tests."""

from __future__ import annotations

from superschematic_schema_runtime import mask_type, merge_type, parse_schema


def _schema():
    return parse_schema(
        {
            "definitions": {
                "Inner": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "secret": {"type": "string", "x-secret": True},
                        "public": {"type": "string"},
                    },
                    "required": ["secret"],
                },
                "T": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "password": {"type": "string", "x-secret": True},
                        "items": {
                            "type": "array",
                            "items": {"$ref": "#/definitions/Inner"},
                        },
                    },
                    "required": ["password"],
                },
            }
        }
    )


def test_mask_zeroes_required_string_secret():
    schema = _schema()
    masked = mask_type(schema, "T", {"password": "shhh"})
    assert masked == {"password": ""}


def test_mask_recurses_into_nested_arrays():
    schema = _schema()
    masked = mask_type(
        schema,
        "T",
        {
            "password": "x",
            "items": [{"secret": "a", "public": "ok"}, {"secret": "b", "public": "ok2"}],
        },
    )
    assert masked["items"] == [
        {"secret": "", "public": "ok"},
        {"secret": "", "public": "ok2"},
    ]


def test_merge_restores_secret_when_new_is_empty():
    schema = _schema()
    merged = merge_type(
        schema,
        "T",
        {"password": ""},
        {"password": "existing-secret"},
    )
    assert merged == {"password": "existing-secret"}


def test_merge_keeps_new_password_when_non_empty():
    schema = _schema()
    merged = merge_type(
        schema,
        "T",
        {"password": "fresh"},
        {"password": "old"},
    )
    assert merged == {"password": "fresh"}


def test_merge_recurses_into_nested():
    schema = _schema()
    merged = merge_type(
        schema,
        "T",
        {"password": "p", "items": [{"secret": "", "public": "new"}]},
        {"password": "p", "items": [{"secret": "stored", "public": "old"}]},
    )
    assert merged["items"][0]["secret"] == "stored"
    assert merged["items"][0]["public"] == "new"
