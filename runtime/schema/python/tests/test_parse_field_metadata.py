"""Field-level title / x-placeholder parse into FieldDef."""

from __future__ import annotations

import json
from pathlib import Path

from superschematic_schema_runtime import parse_schema

_FIXTURE = (
    Path(__file__).resolve().parents[2] / "testdata" / "fielddef_legacy.json"
)
LEGACY_SCHEMA = json.loads(_FIXTURE.read_text())



def _fields():
    schema = parse_schema(LEGACY_SCHEMA)
    input_def = schema.inputs["ConnectorAuthInput"]
    return {field.name: field for field in input_def.fields}


def test_title_and_placeholder_parsed():
    fields = _fields()
    assert fields["client_domain"].title == "My Domain name"
    assert fields["client_domain"].placeholder == "acme"
    assert fields["client_domain"].validate_pattern == "^[A-Za-z0-9][A-Za-z0-9-]*$"


def test_title_and_placeholder_default_empty():
    fields = _fields()
    assert fields["clientSecret"].title == ""
    assert fields["clientSecret"].placeholder == ""
