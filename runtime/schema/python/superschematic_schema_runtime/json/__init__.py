"""JSON Schema reader sub-package."""

from .reader import SchemaParseError, parse_schema, parse_schema_json

__all__ = ["SchemaParseError", "parse_schema", "parse_schema_json"]
