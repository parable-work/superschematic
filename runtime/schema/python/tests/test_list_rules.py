"""List rules for T[] and T[][] in the validate phase.

A required list means present, not non-empty; listMin and listMax bound the
outer list; a field's own constraints apply to every element and every
innermost element; an element and an inner list are never null; a non-list
inner value is a type error.
"""

from __future__ import annotations

import pytest

from superschematic_schema_runtime import parse_schema, validate_type


def _grid(items):
    return {"type": "array", "items": {"type": "array", "items": items}}


@pytest.fixture
def schema():
    return parse_schema(
        {
            "definitions": {
                "Code": {"type": "string", "x-typeMapping": {"python": "str"}},
                "Rules": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "tags": {
                            "type": "array",
                            "items": {"type": "string"},
                            "x-validateListMin": 1,
                            "x-validateListMax": 3,
                            "x-validateMaxLength": 4,
                        },
                        "rows": _grid({"type": "string"}),
                        "cells": {
                            **_grid({"type": "string"}),
                            "x-validateListMin": 1,
                            "x-validateListMax": 2,
                            "x-validateMinLength": 2,
                            "x-validatePattern": "^[a-z]+$",
                        },
                        "codes": {
                            "type": "array",
                            "items": {"$ref": "#/definitions/Code"},
                            "x-validateMaxLength": 4,
                        },
                        "codeGrid": {**_grid({"$ref": "#/definitions/Code"}), "x-validateMaxLength": 4},
                        "scores": {
                            "type": "array",
                            "items": {"type": "number"},
                            "x-validateMin": 0,
                            "x-validateMax": 1,
                        },
                    },
                    "required": ["tags", "rows"],
                },
            }
        }
    )


def _verdicts(errors):
    return {key: sorted(e.validator for e in value) for key, value in errors.items()}


def _base(**extra):
    return {"tags": ["a"], "rows": [], **extra}


def test_required_list_is_present_not_non_empty(schema):
    assert _verdicts(validate_type(schema, "Rules", {"tags": [], "rows": []})) == {"tags": ["listMin"]}
    assert _verdicts(validate_type(schema, "Rules", _base())) == {}
    assert _verdicts(validate_type(schema, "Rules", {})) == {"tags": ["required"], "rows": ["required"]}
    assert _verdicts(validate_type(schema, "Rules", {"tags": "a", "rows": []})) == {"tags": ["required"]}


def test_list_bounds_apply_to_the_outer_list(schema):
    assert _verdicts(validate_type(schema, "Rules", _base(tags=["a", "b", "c", "d"]))) == {"tags": ["listMax"]}
    assert _verdicts(validate_type(schema, "Rules", _base(cells=[]))) == {"cells": ["listMin"]}
    assert _verdicts(validate_type(schema, "Rules", _base(cells=[[], [], []]))) == {"cells": ["listMax"]}
    assert _verdicts(validate_type(schema, "Rules", _base(cells=[["ab", "cd", "ef", "gh"]]))) == {}


def test_field_constraints_apply_to_every_element(schema):
    assert _verdicts(validate_type(schema, "Rules", _base(tags=["ok", "toolong"]))) == {
        "tags[1]": ["maxLength"]
    }
    assert _verdicts(validate_type(schema, "Rules", _base(cells=[["ab"], ["x", "AB"]]))) == {
        "cells[1][0]": ["minLength"],
        "cells[1][1]": ["pattern"],
    }
    assert _verdicts(
        validate_type(schema, "Rules", _base(codes=["abcde"], codeGrid=[["ab", "abcde"]]))
    ) == {"codes[0]": ["maxLength"], "codeGrid[0][1]": ["maxLength"]}
    assert _verdicts(validate_type(schema, "Rules", _base(scores=[0.5, -1, 2]))) == {
        "scores[1]": ["min"],
        "scores[2]": ["max"],
    }


def test_an_element_is_never_null(schema):
    errors = validate_type(schema, "Rules", _base(tags=["a", None], codes=[None], codeGrid=[[None]]))
    assert _verdicts(errors) == {
        "tags[1]": ["required"],
        "codes[0]": ["required"],
        "codeGrid[0][0]": ["required"],
    }


def test_null_and_non_list_inner_lists(schema):
    errors = validate_type(schema, "Rules", _base(rows=[None, "a", []]))
    assert [(e.validator, e.message) for e in errors["rows[0]"]] == [("required", "required field")]
    assert [(e.validator, e.message) for e in errors["rows[1]"]] == [("type", "expected an array")]
    assert "rows[2]" not in errors
