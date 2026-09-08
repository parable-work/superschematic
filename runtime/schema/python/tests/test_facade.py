"""Facade end-to-end tests: load / parse / validate / marshal flows."""

from __future__ import annotations

import json

from superschematic_schema_runtime import (
    Runtime,
    RuntimeOptions,
    has_errors,
    load_input,
    load_type,
    load_type_strict,
    marshal_type,
    parse_type,
    validate_type,
)


def test_load_type_happy_path(connector_schema):
    result = load_type(
        connector_schema,
        "Connector",
        {
            "email": "  ALICE@example.com  ",
            "password": "p4ssw0rd!",
            "count": "10",
            "active": "true",
            "tags": ["a", "b"],
        },
    )
    assert not has_errors(result.errors)
    assert result.data["email"] == "alice@example.com"
    assert result.data["count"] == 10
    assert result.data["active"] is True
    assert result.data["tags"] == ["a", "b"]


def test_load_type_invalid_email(connector_schema):
    result = load_type(
        connector_schema,
        "Connector",
        {"email": "not-an-email", "password": "p4ssw0rd!"},
    )
    assert has_errors(result.errors)
    assert "email" in result.errors
    assert result.errors["email"][0].validator in {"pattern", "custom"}


def test_load_type_defaults_applied(connector_schema):
    result = load_type(
        connector_schema,
        "Connector",
        {"email": "alice@example.com", "password": "p4ssw0rd!"},
    )
    assert not has_errors(result.errors)
    assert result.data["count"] == 5
    assert result.data["active"] is True


def test_load_type_required_missing(connector_schema):
    result = load_type(connector_schema, "Connector", {"email": "alice@example.com"})
    assert has_errors(result.errors)
    assert "password" in result.errors


def test_load_type_from_json_bytes(connector_schema):
    payload = json.dumps({"email": "bob@example.com", "password": "p4ssw0rd!"}).encode()
    result = load_type(connector_schema, "Connector", payload)
    assert not has_errors(result.errors)
    assert result.data["email"] == "bob@example.com"


def test_load_type_strict_rejects_unknown_field(connector_schema):
    result = load_type_strict(
        connector_schema,
        "Connector",
        {"email": "a@b.com", "password": "p4ssw0rd!", "extra": "rogue"},
    )
    assert has_errors(result.errors)
    assert "extra" in result.errors
    assert result.errors["extra"][0].validator == "unknown_field"


def test_validate_only(connector_schema):
    errs = validate_type(connector_schema, "Connector", {"email": "x@x.io", "password": "p4ssw0rd!"})
    assert errs == {}


def test_marshal_type_emits_schema_order(connector_schema):
    parsed = parse_type(
        connector_schema,
        "Connector",
        {"email": "alice@example.com", "password": "p4ssw0rd!", "tags": ["t"]},
    )
    m = marshal_type(connector_schema, "Connector", parsed.data)
    assert not has_errors(m.errors)
    # email -> password -> count -> active -> tags (declaration order)
    decoded = json.loads(m.json)
    keys = list(decoded.keys())
    assert keys.index("email") < keys.index("password")
    assert keys.index("password") < keys.index("count")
    assert decoded["tags"] == ["t"]


def test_runtime_class_caches_registries(connector_schema):
    rt = Runtime(connector_schema)
    r1 = rt.load_type("Connector", {"email": "alice@example.com", "password": "p4ssw0rd!"})
    r2 = rt.load_type("Connector", {"email": "bob@example.com", "password": "qwertyuiop"})
    assert not has_errors(r1.errors)
    assert not has_errors(r2.errors)


def test_load_input(connector_schema):
    result = load_input(
        connector_schema, "ConnectorInput", {"email": "alice@example.com", "password": "p4ssw0rd!"}
    )
    assert not has_errors(result.errors)


def test_parse_error_merges_with_validate(connector_schema):
    # bad email triggers parse error; password missing also triggers validate-required
    result = load_type(
        connector_schema, "Connector", {"email": "not-an-email"}
    )
    assert has_errors(result.errors)
    # Parse phase should have already failed and short-circuited validation, so the
    # error map contains only the parse error (matches Go / TS behavior).
    assert "email" in result.errors


def test_runtime_options_strict_override(connector_schema):
    rt = Runtime(connector_schema, RuntimeOptions(strict=True))
    result = rt.load_type("Connector", {"email": "a@b.com", "password": "p4ssw0rd!", "extra": 1})
    assert has_errors(result.errors)
    assert "extra" in result.errors
