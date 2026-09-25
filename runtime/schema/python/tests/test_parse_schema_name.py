"""The schema name parse_schema reads from the document root."""

from __future__ import annotations

from superschematic_schema_runtime import parse_schema


def test_name_is_read_before_title():
    # A document that carries both keeps its identifier in `name`; `title`
    # is then a display title and does not replace it.
    schema = parse_schema({"name": "connector-data", "title": "Connector data", "definitions": {}})
    assert schema.name == "connector-data"


def test_title_names_a_document_without_name():
    schema = parse_schema({"title": "parity-fixture", "definitions": {}})
    assert schema.name == "parity-fixture"


def test_name_is_empty_without_name_or_title():
    schema = parse_schema({"definitions": {}})
    assert schema.name == ""
