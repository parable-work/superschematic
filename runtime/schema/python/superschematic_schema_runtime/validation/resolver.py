"""Schema input resolution: accept dict / bytes / str / Schema and return Schema."""

from __future__ import annotations


from .types import Schema, SchemaInput


def resolve_schema(schema: SchemaInput) -> Schema:
    """Coerce ``schema`` into a :class:`Schema` instance.

    Strings and bytes are parsed as JSON; dicts are parsed as JSON-Schema-shaped
    payloads (see ``superschematic_schema_runtime.json.reader.parse_schema``);
    :class:`Schema` instances are returned as-is.
    """
    if isinstance(schema, Schema):
        return schema
    # Local import to avoid a cycle through the ``json`` sub-package.
    from ..json.reader import parse_schema, parse_schema_json

    if isinstance(schema, (bytes, str)):
        return parse_schema_json(schema)
    if isinstance(schema, dict):
        return parse_schema(schema)
    raise TypeError(
        f"resolve_schema: unsupported schema input type: {type(schema).__name__}"
    )


__all__ = ["resolve_schema"]
