"""Lenient vs strict coercion (parse/coerce.py)."""

from __future__ import annotations

import pytest

from psgen_schema_runtime.parse.coerce import coerce_bool, coerce_float, coerce_int


@pytest.mark.parametrize(
    "value,strict,expected",
    [
        (5, False, (5, True)),
        (5.7, False, (0, False)),
        ("10", False, (10, True)),
        ("10.9", False, (0, False)),
        (9007199254740992, False, (0, False)),
        ("9007199254740992", False, (0, False)),
        ("", False, (0, False)),
        ("abc", False, (0, False)),
        ("10", True, (0, False)),  # strict rejects numeric strings
        (None, False, (0, False)),
        (True, False, (0, False)),
    ],
)
def test_coerce_int(value, strict, expected):
    assert coerce_int(value, strict) == expected


@pytest.mark.parametrize(
    "value,strict,expected",
    [
        (5.7, False, (5.7, True)),
        ("3.14", False, (3.14, True)),
        ("3.14", True, (0.0, False)),
        ("nan", False, (0.0, False)),
    ],
)
def test_coerce_float(value, strict, expected):
    assert coerce_float(value, strict) == expected


@pytest.mark.parametrize(
    "value,strict,expected",
    [
        (True, False, (True, True)),
        (False, False, (False, True)),
        ("true", False, (True, True)),
        (" TRUE ", False, (True, True)),
        ("false", False, (False, True)),
        ("true", True, (False, False)),
        ("yes", False, (False, False)),
        (1, False, (False, False)),  # numbers are NOT coerced to bool
    ],
)
def test_coerce_bool(value, strict, expected):
    assert coerce_bool(value, strict) == expected
