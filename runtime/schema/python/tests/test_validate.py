"""Validate registry + missing_validators tests."""

from __future__ import annotations

import json

from superschematic_schema_runtime import (
    RuntimeOptions,
    ScalarValidatorRegistry,
    ValidationError,
    create_default_scalar_validator_registry,
    load_type,
    missing_validators,
    parse_schema,
    validate_type,
)


def test_default_registry_has_canonical_scalars():
    reg = create_default_scalar_validator_registry()
    names = reg.names()
    assert "Contact.Email" in names
    assert "Identity.UUID" in names
    assert "Design.Color" in names


def test_missing_validators_reports_gap():
    schema = parse_schema(
        {
            "definitions": {
                "Mystery.Foo": {
                    "type": "string",
                    "x-typeMapping": {"python": "str"},
                    "x-hasCustomValidate": True,
                },
            }
        }
    )
    assert missing_validators(schema) == ["Mystery.Foo"]


def test_missing_validators_empty_when_all_registered():
    reg = ScalarValidatorRegistry()
    reg.register("Mystery.Foo", lambda v: [])
    schema = parse_schema(
        {
            "definitions": {
                "Mystery.Foo": {
                    "type": "string",
                    "x-typeMapping": {"python": "str"},
                    "x-hasCustomValidate": True,
                },
            }
        }
    )
    assert missing_validators(schema, reg) == []


def test_validate_custom_registry_used():
    calls: list[str] = []

    def validator(value: str) -> list[ValidationError]:
        calls.append(value)
        return [ValidationError(validator="custom", message="forced")]

    reg = ScalarValidatorRegistry()
    reg.register("Mystery.Foo", validator)
    schema = parse_schema(
        {
            "definitions": {
                "Mystery.Foo": {
                    "type": "string",
                    "x-typeMapping": {"python": "str"},
                    "x-hasCustomValidate": True,
                },
                "T": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {"x": {"$ref": "#/definitions/Mystery.Foo"}},
                },
            }
        }
    )
    errs = validate_type(
        schema, "T", {"x": "hello"}, options=__import__("superschematic_schema_runtime", fromlist=["RuntimeOptions"]).RuntimeOptions(validate_registry=reg)
    )
    assert "x" in errs
    assert calls == ["hello"]


def test_validate_field_level_constraints():
    schema = parse_schema(
        {
            "definitions": {
                "T": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {
                        "name": {
                            "type": "string",
                            "x-validateMinLength": 3,
                            "x-validateMaxLength": 5,
                        },
                        "age": {
                            "type": "integer",
                            "x-validateMin": 0,
                            "x-validateMax": 100,
                        },
                    },
                },
            }
        }
    )
    errs = validate_type(schema, "T", {"name": "ab", "age": 150})
    assert "name" in errs
    assert "age" in errs


def test_validate_malformed_scalar_reported_once():
    """The registered validator re-checks what the schema's pattern checks,
    so it only sees a value the pattern accepts: one failing value, one error."""
    calls: list[str] = []

    def validator(value: str) -> list[ValidationError]:
        calls.append(value)
        return [ValidationError(validator="pattern", message="core rejects " + value)]

    reg = ScalarValidatorRegistry()
    reg.register("Mystery.Code", validator)
    schema = parse_schema(
        {
            "definitions": {
                "Mystery.Code": {
                    "type": "string",
                    "pattern": "^[a-z]+$",
                    "x-typeMapping": {"python": "str"},
                },
                "T": {
                    "x-kind": "type",
                    "type": "object",
                    "properties": {"x": {"$ref": "#/definitions/Mystery.Code"}},
                },
            }
        }
    )
    options = __import__(
        "superschematic_schema_runtime", fromlist=["RuntimeOptions"]
    ).RuntimeOptions(validate_registry=reg)

    errs = validate_type(schema, "T", {"x": "NOT-A-CODE"}, options=options)
    assert [e.validator for e in errs["x"]] == ["pattern"]
    assert calls == []

    errs = validate_type(schema, "T", {"x": "abc"}, options=options)
    assert [(e.validator, e.message) for e in errs["x"]] == [("pattern", "core rejects abc")]
    assert calls == ["abc"]


def _any_json_schema():
    """A scalar whose json_schema type mapping is "any", named and typed so
    neither its name nor its String primitive and pattern can decide."""
    return parse_schema(
        {
            "definitions": {
                "Mystery.Blob": {
                    "type": "string",
                    "pattern": "^[a-z]+$",
                    "x-typeMapping": {"json_schema": "any"},
                    "x-hasCustomValidate": True,
                },
                "Doc": {
                    "x-kind": "type",
                    "type": "object",
                    "required": ["body"],
                    "properties": {
                        "body": {"$ref": "#/definitions/Mystery.Blob"},
                        "parts": {"type": "array", "items": {"$ref": "#/definitions/Mystery.Blob"}},
                    },
                },
            }
        }
    )


def _validator_names(errs) -> dict[str, list[str]]:
    return {key: [e.validator for e in value] for key, value in errs.items()}


def test_validate_any_json_scalar_accepts_every_json_value_but_null():
    calls: list[str] = []

    def validator(value: str) -> list[ValidationError]:
        calls.append(value)
        return [ValidationError(validator="pattern", message="a string validator ran")]

    reg = ScalarValidatorRegistry()
    reg.register("Mystery.Blob", validator)
    options = RuntimeOptions(validate_registry=reg)
    schema = _any_json_schema()
    for value in [{"k": 1, "none": None}, [1, "two", None], "NOT JSON", "", 42, 1.5, False]:
        errs = validate_type(schema, "Doc", {"body": value, "parts": [value]}, options=options)
        assert _validator_names(errs) == {}, value
    assert calls == []
    errs = validate_type(schema, "Doc", {"body": None, "parts": [1, None]}, options=options)
    assert _validator_names(errs) == {"body": ["required"], "parts[1]": ["required"]}
    assert _validator_names(validate_type(schema, "Doc", {}, options=options)) == {"body": ["required"]}


def test_validate_any_json_scalar_refuses_a_value_json_cannot_carry():
    cyclic: list = []
    cyclic.append(cyclic)
    schema = _any_json_schema()
    for value in [{1, 2}, (1, 2), {1: "value"}, float("nan"), float("inf"), object(), cyclic]:
        errs = validate_type(schema, "Doc", {"body": value})
        assert _validator_names(errs) == {"body": ["type"]}, value


def test_load_any_json_scalar_keeps_the_value():
    payload = '{"body": {"k": [1, null]}, "parts": [[1], "s", 2, true]}'
    for strict in (False, True):
        result = load_type(_any_json_schema(), "Doc", payload, RuntimeOptions(strict=strict))
        assert _validator_names(result.errors) == {}
        assert result.data == json.loads(payload)
