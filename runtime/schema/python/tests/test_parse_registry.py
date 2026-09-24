"""Default scalar parse registry (parse/registry.py)."""

from __future__ import annotations

import pytest

from superschematic_schema_runtime.parse.registry import create_default_scalar_parse_registry


def test_string_map_parses_to_canonical_json_text():
    parse = create_default_scalar_parse_registry().get("Generic.StringMap")
    assert parse is not None

    assert parse('{ "tier": "gold", "region": "eu" }') == ('{"region":"eu","tier":"gold"}', [])
    assert parse("{}") == ("{}", [])


@pytest.mark.parametrize("invalid", ['{"region":null}', '{"region":1}', "not json"])
def test_string_map_rejects_non_string_maps(invalid):
    parse = create_default_scalar_parse_registry().get("Generic.StringMap")
    assert parse is not None

    unchanged, errors = parse(invalid)
    assert unchanged == invalid
    assert len(errors) == 1
    assert errors[0].validator == "parse"
