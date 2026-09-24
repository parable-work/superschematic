"""Import and run the pydantic package pygen generates for lists of lists.

The package is the committed pygen golden for the fixture-nested-arrays
service; the pygen golden test keeps it byte-equal to the generator output.
Pydantic reports an element error at ``(field, i, j)``; validate_all reports
the list rules at ``field[i]`` and ``field[i][j]``.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import List

import pytest
from pydantic import ValidationError

GOLDEN = (
    Path(__file__).resolve().parents[4]
    / "internal/generator/pygen/testdata/golden/fixture-nested-arrays"
)
sys.path.insert(0, str(GOLDEN))
# The golden tree must hold only generator output: the pygen golden test
# fails on any extra file, a __pycache__ directory included.
_write_bytecode = sys.dont_write_bytecode
sys.dont_write_bytecode = True
try:
    from schemas_types_fixture_nested_arrays import Drawing, Point, Shade
finally:
    sys.dont_write_bytecode = _write_bytecode


def _payload(**overrides):
    payload = {
        "labels": [["a", "b", "c"], [], ["d"]],
        "shades": [[], ["light", "dark"]],
        "polygons": [[{"x": 0.0, "y": 0.0}, {"x": 1.0, "y": 0.0}, {"x": 0.5, "y": 1.0}], []],
        "samples": [[0.5], [], [1.5, 2.5]],
    }
    payload.update(overrides)
    return payload


def _locs(excinfo):
    return [(error["loc"], error["type"]) for error in excinfo.value.errors()]


def _verdicts(model):
    return {key: [e["validator"] for e in entries] for key, entries in model.validate_all().errors.items()}


def test_fields_are_lists_of_lists():
    assert Drawing.model_fields["labels"].annotation == List[List[str]]
    assert Drawing.model_fields["shades"].annotation == List[List[Shade]]


def test_ragged_and_empty_lists_round_trip():
    payload = _payload()
    drawing = Drawing.from_json(json.dumps(payload))
    assert _verdicts(drawing) == {}
    assert drawing.labels == [["a", "b", "c"], [], ["d"]]
    assert drawing.shades == [[], ["light", "dark"]]
    assert drawing.polygons[0][2] == Point(x=0.5, y=1.0)
    assert json.loads(drawing.to_json()) == payload
    assert drawing.mask_secrets() == drawing

    empty = Drawing.from_dict(_payload(labels=[], shades=[], polygons=[], samples=[]))
    assert _verdicts(empty) == {}
    assert Drawing.from_dict(_payload(labels=[[]], samples=None)).samples is None


def test_null_inner_list_is_rejected():
    with pytest.raises(ValidationError) as excinfo:
        Drawing.from_dict(_payload(labels=[["a"], None]))
    assert _locs(excinfo) == [(("labels", 1), "list_type")]

    drawing = Drawing.from_dict(_payload())
    with pytest.raises(ValidationError) as excinfo:
        drawing.polygons = [None]
    assert _locs(excinfo) == [(("polygons", 0), "list_type")]

    # validate_all names the row when data bypassed validation.
    constructed = Drawing.model_construct(**{**_payload(), "labels": [["a"], None], "samples": [None]})
    verdicts = _verdicts(constructed)
    assert verdicts["labels[1]"] == ["required"]
    assert verdicts["samples[0]"] == ["required"]
    assert verdicts["samples"] == ["invalid"]


def test_bad_element_is_reported_at_both_indexes():
    with pytest.raises(ValidationError) as excinfo:
        Drawing.from_dict(_payload(labels=[["a"], ["b", 1]]))
    assert _locs(excinfo) == [(("labels", 1, 1), "string_type")]

    with pytest.raises(ValidationError) as excinfo:
        Drawing.from_dict(_payload(samples=[[1.5], [2.5, "x"]]))
    assert _locs(excinfo) == [(("samples", 1, 1), "float_type")]

    with pytest.raises(ValidationError) as excinfo:
        Drawing.from_dict(_payload(polygons=[[], [{"x": 1.0}]]))
    assert _locs(excinfo) == [(("polygons", 1, 0, "y"), "missing")]


def test_enum_wire_values_under_strict_validation():
    drawing = Drawing.model_validate(_payload(shades=[["dark"], ["light", "dark"]]), strict=True)
    assert drawing.shades == [["dark"], ["light", "dark"]]
    with pytest.raises(ValidationError) as excinfo:
        Drawing.model_validate(_payload(shades=[["dark"], ["light", "blue"]]), strict=True)
    assert [error["loc"] for error in excinfo.value.errors()] == [("shades", 1, 1)]


def test_list_bounds_apply_to_the_outer_list():
    wide = Drawing.from_dict(_payload(samples=[[float(i) for i in range(100)]] * 64))
    assert _verdicts(wide) == {}
    tall = Drawing.from_dict(_payload(samples=[[1.0]] * 65))
    assert _verdicts(tall) == {"samples": ["listMax"]}
