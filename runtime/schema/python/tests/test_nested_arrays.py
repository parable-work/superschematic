"""Lists of lists (T[][]): read, parse, validate, serialize, mask and merge.

An inner list is never null and may be empty. Every innermost element is
handled as a T[] element is, and its errors are keyed ``field[i][j]``.
List bounds apply to the outer list.
"""

from __future__ import annotations

import json

import pytest

from superschematic_schema_runtime import (
    SchemaParseError,
    load_type,
    marshal_type,
    mask_type,
    merge_type,
    parse_schema,
    parse_type,
    type_to_map,
    validate_type,
)


def _nested(items):
    return {"type": "array", "items": {"type": "array", "items": items}}


@pytest.fixture
def schema():
    return parse_schema(
        {
            "definitions": {
                "Grid.Code": {
                    "type": "string",
                    "x-typeMapping": {"python": "str"},
                    "pattern": "^[a-z]+$",
                },
                "Shade": {"x-kind": "enum", "enum": ["light", "dark"]},
                "Point": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "x": {"type": "number"},
                        "y": {"type": "number"},
                        "token": {"type": "string", "x-secret": True},
                    },
                    "required": ["x", "y"],
                },
                "Drawing": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "labels": _nested({"type": "string"}),
                        "shades": _nested({"$ref": "#/definitions/Shade"}),
                        "polygons": _nested({"$ref": "#/definitions/Point"}),
                        "codes": _nested({"$ref": "#/definitions/Grid.Code"}),
                        "samples": {**_nested({"type": "integer"}), "x-validateListMax": 2},
                        "tags": {"type": "array", "items": {"type": "string"}},
                    },
                    "required": ["labels"],
                },
            }
        }
    )


def _validators(errors, key):
    return [e.validator for e in errors[key]]


def test_reader_marks_lists_of_lists(schema):
    fields = {f.name: f.type_ref for f in schema.types["Drawing"].fields}
    assert fields["labels"].is_array and fields["labels"].is_array_of_arrays
    assert fields["labels"].name == "String"
    assert fields["polygons"].name == "Point" and fields["polygons"].is_array_of_arrays
    assert fields["tags"].is_array and not fields["tags"].is_array_of_arrays


def test_reader_refuses_a_third_level():
    with pytest.raises(SchemaParseError, match=r"arrays nest at most two levels \(T\[\]\[\]\)"):
        parse_schema(
            {
                "definitions": {
                    "T": {
                        "x-kind": "type",
                        "type": "object",
                        "properties": {"cube": _nested({"type": "array", "items": {"type": "string"}})},
                    }
                }
            }
        )


def test_ragged_and_empty_lists_load(schema):
    data = {
        "labels": [["a", "b", "c"], [], ["d"]],
        "shades": [["light"], ["dark", "light"]],
        "polygons": [[{"x": 1, "y": 2}], []],
        "codes": [[], ["ab"]],
        "samples": [],
    }
    result = load_type(schema, "Drawing", data)
    assert result.errors == {}
    assert result.data == data


def test_required_list_of_lists_follows_the_list_rule(schema):
    # A required list means present, not non-empty.
    assert load_type(schema, "Drawing", {"labels": []}).errors == {}
    assert load_type(schema, "Drawing", {"labels": [[]]}).errors == {}
    errors = load_type(schema, "Drawing", {}).errors
    assert _validators(errors, "labels") == ["required"]


def test_null_inner_list_is_rejected(schema):
    result = load_type(schema, "Drawing", {"labels": [["a"], None], "polygons": [None]})
    assert _validators(result.errors, "labels[1]") == ["required"]
    assert result.errors["labels[1]"][0].message == "required field"
    assert _validators(result.errors, "polygons[0]") == ["required"]
    # Parse alone passes the null through; validation rejects it.
    assert parse_type(schema, "Drawing", {"labels": [None]}).errors == {}
    assert _validators(validate_type(schema, "Drawing", {"labels": [None]}), "labels[0]") == [
        "required"
    ]


def test_inner_value_that_is_not_a_list_is_a_type_error(schema):
    errors = load_type(schema, "Drawing", {"labels": [["a"], "b"]}).errors
    assert _validators(errors, "labels[1]") == ["type"]
    assert errors["labels[1]"][0].message == "expected an array"
    # Validation alone reports the same, as the Go and TypeScript runtimes do.
    errors = validate_type(schema, "Drawing", {"labels": [["a"], "b"]})
    assert _validators(errors, "labels[1]") == ["type"]
    assert errors["labels[1]"][0].message == "expected an array"


def test_elements_that_do_not_parse_are_reported_at_both_indexes(schema):
    errors = load_type(schema, "Drawing", {"labels": [["a", 1]], "samples": [[1], [2, "x"]]}).errors
    assert set(errors) == {"labels[0][1]", "samples[1][1]"}
    assert _validators(errors, "labels[0][1]") == ["type"]
    assert errors["samples[1][1]"][0].message == "expected Int value"


def test_invalid_elements_are_reported_at_both_indexes(schema):
    errors = load_type(
        schema,
        "Drawing",
        {
            "labels": [["a", None]],
            "shades": [["light"], ["dark", "blue"]],
            "polygons": [[{"x": 1, "y": 2}, {"x": 1}]],
            "codes": [["ab"], [], ["AB"]],
            "tags": ["ok", None],
        },
    ).errors
    assert set(errors) == {"labels[0][1]", "shades[1][1]", "polygons[0][1]", "codes[2][0]", "tags[1]"}
    # A null innermost element follows the T[] element rule, as tags[1] does.
    assert _validators(errors, "labels[0][1]") == ["required"]
    assert _validators(errors, "tags[1]") == ["required"]
    assert _validators(errors, "shades[1][1]") == ["enum"]
    assert _validators(errors["polygons[0][1]"], "y") == ["required"]
    assert _validators(errors, "codes[2][0]") == ["pattern"]


def test_list_bounds_apply_to_the_outer_list(schema):
    ok = load_type(schema, "Drawing", {"labels": [["a"]], "samples": [[1, 2, 3, 4, 5], [6]]})
    assert ok.errors == {}
    over = load_type(schema, "Drawing", {"labels": [["a"]], "samples": [[1], [2], [3]]})
    assert _validators(over.errors, "samples") == ["listMax"]


def test_serialize_walks_every_inner_list(schema):
    data = {
        "labels": [["a"], []],
        "polygons": [[{"y": 2, "x": 1, "extra": True}], []],
    }
    out = type_to_map(schema, "Drawing", data)
    assert out.errors == {}
    assert out.data["polygons"] == [[{"x": 1, "y": 2}], []]
    marshalled = marshal_type(schema, "Drawing", data)
    assert marshalled.errors == {}
    assert json.loads(marshalled.json) == {"labels": [["a"], []], "polygons": [[{"x": 1, "y": 2}], []]}
    assert marshalled.json.index(b'"x"') < marshalled.json.index(b'"y"')


def test_mask_and_merge_visit_every_inner_list(schema):
    stored = {
        "labels": [["a"]],
        "polygons": [[{"x": 1, "y": 2, "token": "s1"}], [], [{"x": 3, "y": 4, "token": "s2"}]],
    }
    masked = mask_type(schema, "Drawing", stored)
    assert masked["polygons"] == [[{"x": 1, "y": 2, "token": None}], [], [{"x": 3, "y": 4, "token": None}]]
    assert stored["polygons"][0][0]["token"] == "s1"

    submitted = {
        "labels": [["b"]],
        "polygons": [[{"x": 5, "y": 6, "token": ""}], [], [{"x": 7, "y": 8, "token": "new"}]],
    }
    merged = merge_type(schema, "Drawing", submitted, stored)
    assert merged["labels"] == [["b"]]
    assert merged["polygons"] == [
        [{"x": 5, "y": 6, "token": "s1"}],
        [],
        [{"x": 7, "y": 8, "token": "new"}],
    ]
