"""Validate registry + missing_validators tests."""

from __future__ import annotations

from superschematic_schema_runtime import (
    ScalarValidatorRegistry,
    ValidationError,
    create_default_scalar_validator_registry,
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
