"""Validate registry + missing_validators tests."""

from __future__ import annotations

from psgen_schema_runtime import (
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
        schema, "T", {"x": "hello"}, options=__import__("psgen_schema_runtime", fromlist=["RuntimeOptions"]).RuntimeOptions(validate_registry=reg)
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
