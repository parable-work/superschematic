"""The cross-language validation corpus, run through the Python runtime.

``runtime/schema/testdata/validation_parity.json`` holds one vector table
with the expected verdicts; the generated-validator parity harness
(``internal/generator/parity``) writes it, and the Go and TypeScript runtime
suites assert the same rows. The Python runtime reads the schema JSON form,
so the schema comes from ``validation_parity.document.json``, which the
TypeScript suite keeps equal to the corpus IR.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from superschematic_schema_runtime import parse_schema, validate_type

_TESTDATA = Path(__file__).resolve().parents[2] / "testdata"
_CORPUS = json.loads((_TESTDATA / "validation_parity.json").read_text())
_SCHEMA = parse_schema(json.loads((_TESTDATA / "validation_parity.document.json").read_text()))


def _flatten(errors, prefix: str = "", out: dict | None = None) -> dict:
    """Map errors to path -> sorted validator names; nested objects are dotted."""
    out = {} if out is None else out
    for key, value in errors.items():
        path = f"{prefix}.{key}" if prefix else key
        if isinstance(value, dict):
            _flatten(value, path, out)
        else:
            out[path] = sorted(e.validator for e in value)
    return out


@pytest.mark.parametrize("vector", _CORPUS["vectors"], ids=lambda v: v["name"])
def test_validation_parity(vector):
    got = _flatten(validate_type(_SCHEMA, vector["type"], vector["payload"]))
    assert got == vector["want"]
