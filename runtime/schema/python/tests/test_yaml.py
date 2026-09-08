"""YAML payload pathway."""

from __future__ import annotations

import pytest

yaml = pytest.importorskip("yaml")

from superschematic_schema_runtime import has_errors, load_type_yaml  # noqa: E402


def test_load_type_yaml_happy(connector_schema):
    payload = b"""
email: alice@example.com
password: hunter2hunter2
count: '7'
"""
    result = load_type_yaml(connector_schema, "Connector", payload)
    assert not has_errors(result.errors)
    assert result.data["email"] == "alice@example.com"
    assert result.data["count"] == 7


def test_load_type_yaml_bad_payload(connector_schema):
    result = load_type_yaml(connector_schema, "Connector", b"::: not valid yaml :::\n  - x")
    # PyYAML accepts many things; the failure mode is either a top-level
    # validation error or non-object payload -- either way errors should fire.
    assert has_errors(result.errors)
