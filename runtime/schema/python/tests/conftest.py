"""Shared schema fixtures for runtime tests."""

from __future__ import annotations

import pytest

from superschematic_schema_runtime import parse_schema


@pytest.fixture
def connector_schema():
    """A small but realistic schema exercising scalars, defaults, secrets, and arrays."""
    return parse_schema(
        {
            "definitions": {
                "Contact.Email": {
                    "type": "string",
                    "x-typeMapping": {"python": "str"},
                    "x-hasCustomNormalize": True,
                                        "x-hasCustomValidate": True,
                    "pattern": "^[^@]+@[^@]+\\.[a-zA-Z]{2,}$",
                },
                "Auth.Password": {
                    "type": "string",
                    "x-typeMapping": {"python": "str"},
                },
                "Inner": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "label": {"type": "string"},
                    },
                },
                "Connector": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "email": {"$ref": "#/definitions/Contact.Email"},
                        "password": {
                            "$ref": "#/definitions/Auth.Password",
                            "x-secret": True,
                        },
                        "count": {"type": "integer", "default": "5"},
                        "active": {"type": "boolean", "default": "true"},
                        "tags": {"type": "array", "items": {"type": "string"}},
                        "inner": {"$ref": "#/definitions/Inner"},
                    },
                    "required": ["email", "password"],
                },
                "ConnectorInput": {
                    "x-kind": "input",
                    "type": "object",
                    "properties": {
                        "email": {"$ref": "#/definitions/Contact.Email"},
                        "password": {
                            "$ref": "#/definitions/Auth.Password",
                            "x-secret": True,
                        },
                    },
                    "required": ["email"],
                },
            },
        }
    )
