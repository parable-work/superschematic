"""Top-level runtime facade.

Composes the parse / validate / serialize / mask / merge submodules into one
API a Python service uses end-to-end::

    load_*     = parse + validate (most common entrypoint)
    parse_*    = parse only (for staged pipelines)
    validate_* = validate already-parsed data
    marshal_*  = serialize map -> JSON bytes
    type_to_map / input_to_map = sanitized map output
    mask_*  / merge_* = secret handling
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from .errors import (
    ValidationErrors,
    has_errors,
    merge_validation_errors,
    new_validation_errors,
)
from .mask import mask_input as _mask_input
from .mask import mask_type as _mask_type
from .merge import merge_input as _merge_input
from .merge import merge_type as _merge_type
from .parse import (
    ParseOptions,
    ParseResult,
    ScalarNormalizeRegistry,
    ScalarParseRegistry,
    create_default_scalar_normalize_registry,
    create_default_scalar_parse_registry,
    parse_input as _parse_input,
    parse_input_json as _parse_input_json,
    parse_input_map as _parse_input_map,
    parse_type as _parse_type,
    parse_type_json as _parse_type_json,
    parse_type_map as _parse_type_map,
)
from .serialize import (
    MarshalResult,
    SerializeOptions,
    SerializeResult,
    input_to_map as _input_to_map,
    marshal_input as _marshal_input,
    marshal_type as _marshal_type,
    type_to_map as _type_to_map,
)
from .validation import (
    ScalarValidatorRegistry,
    ValidationOptions,
    create_default_scalar_validator_registry,
    missing_validators as _missing_validators,
    resolve_schema,
    validate_input_errors as _validate_input_errors,
    validate_type_errors as _validate_type_errors,
)
from .validation.types import Schema, SchemaInput


@dataclass
class LoadResult:
    data: dict[str, Any]
    errors: ValidationErrors


@dataclass
class RuntimeOptions:
    strict: bool = False
    mask_secrets: bool = False
    parse_registry: ScalarParseRegistry | None = None
    normalize_registry: ScalarNormalizeRegistry | None = None
    validate_registry: ScalarValidatorRegistry | None = None


def _parse_options_from(opts: RuntimeOptions | None) -> ParseOptions:
    if opts is None:
        return ParseOptions()
    return ParseOptions(
        strict=opts.strict,
        parse_registry=opts.parse_registry,
        normalize_registry=opts.normalize_registry,
    )


def _serialize_options_from(opts: RuntimeOptions | None) -> SerializeOptions:
    if opts is None:
        return SerializeOptions()
    return SerializeOptions(strict=opts.strict, mask_secrets=opts.mask_secrets)


def _validation_options_from(opts: RuntimeOptions | None) -> ValidationOptions:
    if opts is None:
        return ValidationOptions()
    return ValidationOptions(scalar_registry=opts.validate_registry)


def _with_errors(parse_result: ParseResult, validate_errs: ValidationErrors) -> LoadResult:
    if has_errors(parse_result.errors):
        if has_errors(validate_errs):
            merged = merge_validation_errors(dict(parse_result.errors), validate_errs)
            return LoadResult(data=parse_result.data, errors=merged)
        return LoadResult(data=parse_result.data, errors=parse_result.errors)
    return LoadResult(data=parse_result.data, errors=validate_errs)


class Runtime:
    """Reusable runtime that caches default registries across many calls."""

    def __init__(self, schema: SchemaInput, options: RuntimeOptions | None = None) -> None:
        self.schema: Schema = resolve_schema(schema)
        self._options = options if options is not None else RuntimeOptions()
        self._parse_registry = (
            self._options.parse_registry
            if self._options.parse_registry is not None
            else create_default_scalar_parse_registry()
        )
        self._normalize_registry = (
            self._options.normalize_registry
            if self._options.normalize_registry is not None
            else create_default_scalar_normalize_registry()
        )
        self._validate_registry = (
            self._options.validate_registry
            if self._options.validate_registry is not None
            else create_default_scalar_validator_registry()
        )

    def _resolve(self, call: RuntimeOptions | None) -> RuntimeOptions:
        if call is None:
            return RuntimeOptions(
                strict=self._options.strict,
                mask_secrets=self._options.mask_secrets,
                parse_registry=self._parse_registry,
                normalize_registry=self._normalize_registry,
                validate_registry=self._validate_registry,
            )
        return RuntimeOptions(
            strict=call.strict if call.strict is not None else self._options.strict,
            mask_secrets=call.mask_secrets
            if call.mask_secrets is not None
            else self._options.mask_secrets,
            parse_registry=call.parse_registry or self._parse_registry,
            normalize_registry=call.normalize_registry or self._normalize_registry,
            validate_registry=call.validate_registry or self._validate_registry,
        )

    def load_type(
        self, type_name: str, data: Any, options: RuntimeOptions | None = None
    ) -> LoadResult:
        return load_type(self.schema, type_name, data, self._resolve(options))

    def load_input(
        self, input_name: str, data: Any, options: RuntimeOptions | None = None
    ) -> LoadResult:
        return load_input(self.schema, input_name, data, self._resolve(options))

    def load_type_yaml(
        self, type_name: str, payload: str | bytes, options: RuntimeOptions | None = None
    ) -> LoadResult:
        return load_type_yaml(self.schema, type_name, payload, self._resolve(options))

    def load_input_yaml(
        self, input_name: str, payload: str | bytes, options: RuntimeOptions | None = None
    ) -> LoadResult:
        return load_input_yaml(self.schema, input_name, payload, self._resolve(options))

    def parse_type(
        self, type_name: str, data: Any, options: RuntimeOptions | None = None
    ) -> ParseResult:
        return _parse_type(
            self.schema, type_name, data, _parse_options_from(self._resolve(options))
        )

    def parse_input(
        self, input_name: str, data: Any, options: RuntimeOptions | None = None
    ) -> ParseResult:
        return _parse_input(
            self.schema, input_name, data, _parse_options_from(self._resolve(options))
        )

    def parse_type_map(
        self, type_name: str, data: dict[str, Any], options: RuntimeOptions | None = None
    ) -> ParseResult:
        return _parse_type_map(
            self.schema, type_name, data, _parse_options_from(self._resolve(options))
        )

    def parse_input_map(
        self, input_name: str, data: dict[str, Any], options: RuntimeOptions | None = None
    ) -> ParseResult:
        return _parse_input_map(
            self.schema, input_name, data, _parse_options_from(self._resolve(options))
        )

    def validate_type(
        self, type_name: str, data: Any, options: RuntimeOptions | None = None
    ) -> ValidationErrors:
        resolved = self._resolve(options)
        return _validate_type_errors(
            self.schema, type_name, data, ValidationOptions(scalar_registry=resolved.validate_registry)
        )

    def validate_input(
        self, input_name: str, data: Any, options: RuntimeOptions | None = None
    ) -> ValidationErrors:
        resolved = self._resolve(options)
        return _validate_input_errors(
            self.schema, input_name, data, ValidationOptions(scalar_registry=resolved.validate_registry)
        )

    def marshal_type(
        self, type_name: str, data: dict[str, Any], options: RuntimeOptions | None = None
    ) -> MarshalResult:
        return _marshal_type(
            self.schema, type_name, data, _serialize_options_from(self._resolve(options))
        )

    def marshal_input(
        self, input_name: str, data: dict[str, Any], options: RuntimeOptions | None = None
    ) -> MarshalResult:
        return _marshal_input(
            self.schema, input_name, data, _serialize_options_from(self._resolve(options))
        )

    def type_to_map(
        self, type_name: str, data: dict[str, Any], options: RuntimeOptions | None = None
    ) -> SerializeResult:
        return _type_to_map(
            self.schema, type_name, data, _serialize_options_from(self._resolve(options))
        )

    def input_to_map(
        self, input_name: str, data: dict[str, Any], options: RuntimeOptions | None = None
    ) -> SerializeResult:
        return _input_to_map(
            self.schema, input_name, data, _serialize_options_from(self._resolve(options))
        )

    def mask_type(self, type_name: str, data: dict[str, Any] | None) -> dict[str, Any] | None:
        return _mask_type(self.schema, type_name, data)

    def mask_input(self, input_name: str, data: dict[str, Any] | None) -> dict[str, Any] | None:
        return _mask_input(self.schema, input_name, data)

    def merge_type(
        self,
        type_name: str,
        new_data: dict[str, Any] | None,
        existing_data: dict[str, Any] | None,
    ) -> dict[str, Any] | None:
        return _merge_type(self.schema, type_name, new_data, existing_data)

    def merge_input(
        self,
        input_name: str,
        new_data: dict[str, Any] | None,
        existing_data: dict[str, Any] | None,
    ) -> dict[str, Any] | None:
        return _merge_input(self.schema, input_name, new_data, existing_data)

    def missing_validators(self) -> list[str]:
        return _missing_validators(self.schema, self._validate_registry)


# --- Module-level convenience functions ---


def _load_via(
    schema: SchemaInput,
    name: str,
    data: Any,
    options: RuntimeOptions | None,
    is_input: bool,
) -> LoadResult:
    resolved = resolve_schema(schema)
    parse_opts = _parse_options_from(options)
    validation_opts = _validation_options_from(options)
    if isinstance(data, (bytes, str)):
        parsed = (
            _parse_input_json(resolved, name, data, parse_opts)
            if is_input
            else _parse_type_json(resolved, name, data, parse_opts)
        )
    else:
        parsed = (
            _parse_input(resolved, name, data, parse_opts)
            if is_input
            else _parse_type(resolved, name, data, parse_opts)
        )
    if has_errors(parsed.errors):
        return LoadResult(data=parsed.data, errors=parsed.errors)
    validate_errs = (
        _validate_input_errors(resolved, name, parsed.data, validation_opts)
        if is_input
        else _validate_type_errors(resolved, name, parsed.data, validation_opts)
    )
    return _with_errors(parsed, validate_errs)


def load_type(
    schema: SchemaInput,
    type_name: str,
    data: Any,
    options: RuntimeOptions | None = None,
) -> LoadResult:
    """Parse + validate against ``schema.types[type_name]``.

    ``data`` may be ``bytes`` or ``str`` (decoded as JSON), or a ``dict``/list
    already in memory.
    """
    return _load_via(schema, type_name, data, options, is_input=False)


def load_input(
    schema: SchemaInput,
    input_name: str,
    data: Any,
    options: RuntimeOptions | None = None,
) -> LoadResult:
    return _load_via(schema, input_name, data, options, is_input=True)


def load_type_strict(
    schema: SchemaInput,
    type_name: str,
    data: Any,
    options: RuntimeOptions | None = None,
) -> LoadResult:
    opts = options if options is not None else RuntimeOptions()
    opts.strict = True
    return load_type(schema, type_name, data, opts)


def load_input_strict(
    schema: SchemaInput,
    input_name: str,
    data: Any,
    options: RuntimeOptions | None = None,
) -> LoadResult:
    opts = options if options is not None else RuntimeOptions()
    opts.strict = True
    return load_input(schema, input_name, data, opts)


def _load_yaml_via(
    schema: SchemaInput,
    name: str,
    payload: str | bytes,
    options: RuntimeOptions | None,
    is_input: bool,
) -> LoadResult:
    from .yaml import load_yaml_object

    value, errs = load_yaml_object(payload)
    if has_errors(errs):
        return LoadResult(data={}, errors=errs)
    return _load_via(schema, name, value or {}, options, is_input=is_input)


def load_type_yaml(
    schema: SchemaInput,
    type_name: str,
    payload: str | bytes,
    options: RuntimeOptions | None = None,
) -> LoadResult:
    return _load_yaml_via(schema, type_name, payload, options, is_input=False)


def load_input_yaml(
    schema: SchemaInput,
    input_name: str,
    payload: str | bytes,
    options: RuntimeOptions | None = None,
) -> LoadResult:
    return _load_yaml_via(schema, input_name, payload, options, is_input=True)


def parse_type(
    schema: SchemaInput,
    type_name: str,
    data: Any,
    options: RuntimeOptions | None = None,
) -> ParseResult:
    if isinstance(data, (bytes, str)):
        return _parse_type_json(schema, type_name, data, _parse_options_from(options))
    return _parse_type(schema, type_name, data, _parse_options_from(options))


def parse_input(
    schema: SchemaInput,
    input_name: str,
    data: Any,
    options: RuntimeOptions | None = None,
) -> ParseResult:
    if isinstance(data, (bytes, str)):
        return _parse_input_json(schema, input_name, data, _parse_options_from(options))
    return _parse_input(schema, input_name, data, _parse_options_from(options))


def validate_type(
    schema: SchemaInput,
    type_name: str,
    data: Any,
    options: RuntimeOptions | None = None,
) -> ValidationErrors:
    return _validate_type_errors(schema, type_name, data, _validation_options_from(options))


def validate_input(
    schema: SchemaInput,
    input_name: str,
    data: Any,
    options: RuntimeOptions | None = None,
) -> ValidationErrors:
    return _validate_input_errors(schema, input_name, data, _validation_options_from(options))


def marshal_type(
    schema: SchemaInput,
    type_name: str,
    data: dict[str, Any] | None,
    options: RuntimeOptions | None = None,
) -> MarshalResult:
    return _marshal_type(schema, type_name, data, _serialize_options_from(options))


def marshal_input(
    schema: SchemaInput,
    input_name: str,
    data: dict[str, Any] | None,
    options: RuntimeOptions | None = None,
) -> MarshalResult:
    return _marshal_input(schema, input_name, data, _serialize_options_from(options))


def type_to_map(
    schema: SchemaInput,
    type_name: str,
    data: dict[str, Any] | None,
    options: RuntimeOptions | None = None,
) -> SerializeResult:
    return _type_to_map(schema, type_name, data, _serialize_options_from(options))


def input_to_map(
    schema: SchemaInput,
    input_name: str,
    data: dict[str, Any] | None,
    options: RuntimeOptions | None = None,
) -> SerializeResult:
    return _input_to_map(schema, input_name, data, _serialize_options_from(options))


def mask_type(
    schema: SchemaInput, type_name: str, data: dict[str, Any] | None
) -> dict[str, Any] | None:
    return _mask_type(schema, type_name, data)


def mask_input(
    schema: SchemaInput, input_name: str, data: dict[str, Any] | None
) -> dict[str, Any] | None:
    return _mask_input(schema, input_name, data)


def merge_type(
    schema: SchemaInput,
    type_name: str,
    new_data: dict[str, Any] | None,
    existing_data: dict[str, Any] | None,
) -> dict[str, Any] | None:
    return _merge_type(schema, type_name, new_data, existing_data)


def merge_input(
    schema: SchemaInput,
    input_name: str,
    new_data: dict[str, Any] | None,
    existing_data: dict[str, Any] | None,
) -> dict[str, Any] | None:
    return _merge_input(schema, input_name, new_data, existing_data)


def missing_validators(
    schema: SchemaInput, registry: ScalarValidatorRegistry | None = None
) -> list[str]:
    return _missing_validators(resolve_schema(schema), registry)


__all__ = [
    "LoadResult",
    "MarshalResult",
    "ParseOptions",
    "ParseResult",
    "Runtime",
    "RuntimeOptions",
    "ScalarNormalizeRegistry",
    "ScalarParseRegistry",
    "ScalarValidatorRegistry",
    "SerializeOptions",
    "SerializeResult",
    "ValidationOptions",
    "create_default_scalar_normalize_registry",
    "create_default_scalar_parse_registry",
    "create_default_scalar_validator_registry",
    "has_errors",
    "input_to_map",
    "load_input",
    "load_input_strict",
    "load_input_yaml",
    "load_type",
    "load_type_strict",
    "load_type_yaml",
    "marshal_input",
    "marshal_type",
    "mask_input",
    "mask_type",
    "merge_input",
    "merge_type",
    "merge_validation_errors",
    "missing_validators",
    "new_validation_errors",
    "parse_input",
    "parse_type",
    "type_to_map",
    "validate_input",
    "validate_type",
]
