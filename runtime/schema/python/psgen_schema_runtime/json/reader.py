"""JSON Schema draft-07 -> Schema parser.

Reads the JSON Schema + ``x-*`` extension dialect that ``psgen`` emits to
``platform-schemas/dist/**``.
"""

from __future__ import annotations

import json
import re
from typing import Any

from ..validation.types import (
    ArgumentDef,
    EnumDef,
    EnumValueDef,
    FieldDef,
    FileUploadConfig,
    ImageConstraints,
    Import,
    IndexDef,
    MiddlewareConfig,
    OperationSet,
    RelationDef,
    ScalarDef,
    Schema,
    TypeDef,
    TypeRef,
    UnionDef,
)


_SCALAR_CONSTRAINT_KEYS = frozenset(
    {
        "minLength",
        "maxLength",
        "pattern",
        "format",
        "minimum",
        "maximum",
        "x-example",
        "x-reservedWords",
        "x-caseInsensitive",
        "x-reservedWordsCaseInsensitive",
        "x-reservedWordsMatchPartial",
        "x-typeMapping",
        "x-fileUpload",
        "x-imageConstraints",
        "x-hasCustomNormalize",
        "x-hasCustomValidate",
        "x-hasCustomParse",
        "x-scalar",
    }
)


class SchemaParseError(ValueError):
    """Raised when the JSON Schema input cannot be parsed into a Schema."""


def _is_object(value: Any) -> bool:
    return isinstance(value, dict)


def _as_object(value: Any, path: str) -> dict[str, Any]:
    if not _is_object(value):
        raise SchemaParseError(f"runtime schema parse error: expected object at {path}")
    return value


def _as_string(value: Any) -> str:
    return value if isinstance(value, str) else ""


def _as_bool(value: Any) -> bool:
    return value if isinstance(value, bool) else False


def _as_truthy(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if _is_object(value):
        return True
    return value is not None


def _as_number_or_none(value: Any) -> float | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    return None


def _as_int_or_none(value: Any) -> int | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return int(value)
    return None


def _as_positive_int(value: Any) -> int:
    if isinstance(value, bool):
        return 0
    if isinstance(value, (int, float)):
        as_int = int(value)
        return as_int if as_int > 0 else 0
    return 0


def _as_string_array(value: Any, path: str) -> list[str]:
    if not isinstance(value, list):
        return []
    out: list[str] = []
    for item in value:
        if not isinstance(item, str):
            raise SchemaParseError(
                f"runtime schema parse error: expected string array at {path}"
            )
        out.append(item)
    return out


def _format_default_value(value: Any) -> str:
    if isinstance(value, str):
        return value
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return str(value)
    return str(value)


def _map_json_type_to_primitive(type_name: str) -> str:
    return {
        "string": "String",
        "integer": "Int",
        "number": "Float",
        "boolean": "Boolean",
        "object": "JSON",
    }.get(type_name, "String")


def _resolve_ref_name(ref: str) -> str:
    parts = ref.split("/")
    try:
        scalars_idx = parts.index("scalars")
    except ValueError:
        scalars_idx = -1
    if scalars_idx != -1 and scalars_idx < len(parts) - 1:
        return "_".join(parts[scalars_idx + 1 :]).replace(".", "_")
    return parts[-1] if parts else ""


def _parse_required_set(definition: dict[str, Any]) -> set[str]:
    return set(_as_string_array(definition.get("required"), "definition.required"))


def _parse_indexes(definition: dict[str, Any]) -> list[IndexDef]:
    raw = definition.get("x-indexes")
    if not isinstance(raw, list):
        return []
    indexes: list[IndexDef] = []
    for entry in raw:
        if not _is_object(entry):
            continue
        indexes.append(
            IndexDef(
                keys=_as_string_array(entry.get("keys"), "definition.x-indexes[].keys"),
                unique=_as_bool(entry.get("unique")),
            )
        )
    return indexes


def _parse_middleware_config(raw: dict[str, Any]) -> MiddlewareConfig | None:
    rate_limit_raw = raw.get("x-rateLimit")
    body_limit_raw = raw.get("x-bodyLimit")
    timeout_raw = raw.get("x-timeout")
    encrypted = _as_bool(raw.get("x-encrypted"))
    if (
        not _is_object(rate_limit_raw)
        and not _is_object(body_limit_raw)
        and not _is_object(timeout_raw)
        and not encrypted
    ):
        return None
    rate_limit = (
        _as_number_or_none(rate_limit_raw.get("requestsPerMinute"))
        if _is_object(rate_limit_raw)
        else None
    )
    body_limit = (
        _as_number_or_none(body_limit_raw.get("megabytes"))
        if _is_object(body_limit_raw)
        else None
    )
    timeout = (
        _as_number_or_none(timeout_raw.get("seconds"))
        if _is_object(timeout_raw)
        else None
    )
    return MiddlewareConfig(
        rate_limit=None if rate_limit is None else int(rate_limit),
        body_limit=None if body_limit is None else int(body_limit),
        timeout=None if timeout is None else int(timeout),
        encrypted=encrypted,
    )


def _parse_relation_def(raw: Any) -> RelationDef | None:
    if not _is_object(raw):
        return None
    return RelationDef(type=_as_string(raw.get("type")), field=_as_string(raw.get("field")))


def _parse_type_ref(raw: dict[str, Any], path: str) -> TypeRef:
    ref = raw.get("$ref")
    if isinstance(ref, str) and ref:
        resolved = _resolve_ref_name(ref)
        if not resolved:
            raise SchemaParseError(
                f"runtime schema parse error: invalid $ref at {path}"
            )
        return TypeRef(name=resolved, is_array=False, elem_non_null=True)

    type_name = _as_string(raw.get("type"))
    if type_name == "array":
        items_raw = raw.get("items")
        if items_raw is None:
            raise SchemaParseError(
                f"runtime schema parse error: array without items at {path}"
            )
        items = _as_object(items_raw, f"{path}.items")
        inner = _parse_type_ref(items, f"{path}.items")
        if inner.is_array:
            raise SchemaParseError(
                f"runtime schema parse error: nested arrays are not supported at {path}"
            )
        return TypeRef(name=inner.name, is_array=True, elem_non_null=True)

    if type_name:
        return TypeRef(
            name=_map_json_type_to_primitive(type_name), is_array=False, elem_non_null=True
        )
    return TypeRef(name="JSON", is_array=False, elem_non_null=True)


def _parse_argument_def(name: str, raw_arg: Any, path: str) -> ArgumentDef:
    arg = _as_object(raw_arg, path)
    default_raw = arg.get("default")
    default_value = (
        _format_default_value(default_raw)
        if ("default" in arg and default_raw is not None)
        else None
    )
    return ArgumentDef(
        name=name,
        description=_as_string(arg.get("description")),
        type_ref=_parse_type_ref(arg, path),
        required=_as_bool(arg.get("x-required")),
        default_value=default_value,
        is_query=_as_string(arg.get("x-paramType")) == "query",
    )


def _parse_arguments(raw_field: dict[str, Any], path: str) -> list[ArgumentDef]:
    arguments_raw = raw_field.get("x-arguments")
    if not _is_object(arguments_raw):
        return []
    return [
        _parse_argument_def(arg_name, raw_arg, f"{path}.x-arguments.{arg_name}")
        for arg_name, raw_arg in arguments_raw.items()
    ]


def _parse_field_def(
    field_name: str, raw_field: Any, required_set: set[str], path: str
) -> FieldDef:
    field_obj = _as_object(raw_field, path)
    auto_generated = _as_bool(field_obj.get("x-autoGenerated"))
    required = field_name in required_set and not auto_generated
    json_tag = _as_string(field_obj.get("x-jsonTag"))
    default_raw = field_obj.get("default")
    default_value = (
        _format_default_value(default_raw)
        if ("default" in field_obj and default_raw is not None)
        else None
    )
    middleware = _parse_middleware_config(field_obj)
    return FieldDef(
        name=field_name,
        description=_as_string(field_obj.get("description")),
        title=_as_string(field_obj.get("title")),
        placeholder=_as_string(field_obj.get("x-placeholder")),
        json_key=json_tag or field_name,
        required=required,
        auto_generated=auto_generated,
        default_value=default_value,
        validate_min=_as_number_or_none(field_obj.get("x-validateMin")),
        validate_max=_as_number_or_none(field_obj.get("x-validateMax")),
        validate_min_length=_as_int_or_none(field_obj.get("x-validateMinLength")),
        validate_max_length=_as_int_or_none(field_obj.get("x-validateMaxLength")),
        validate_list_min=_as_int_or_none(field_obj.get("x-validateListMin")),
        validate_list_max=_as_int_or_none(field_obj.get("x-validateListMax")),
        validate_pattern=_as_string(field_obj.get("x-validatePattern")),
        arguments=_parse_arguments(field_obj, path),
        key=_as_bool(field_obj.get("x-key")),
        unique=_as_bool(field_obj.get("x-unique")),
        search_field=_as_bool(field_obj.get("x-searchField")),
        relation=_parse_relation_def(field_obj.get("x-relation")),
        has_many=_as_truthy(field_obj.get("x-hasMany")),
        many_to_many=_as_truthy(field_obj.get("x-manyToMany")),
        json_field=_as_bool(field_obj.get("x-jsonField")),
        secret=_as_bool(field_obj.get("x-secret")),
        ui_hidden=_as_bool(field_obj.get("x-uiHidden")),
        internal_metadata=_as_bool(field_obj.get("x-internal-metadata")),
        auth=_as_bool(field_obj.get("x-auth")),
        encrypted=(middleware.encrypted if middleware else _as_bool(field_obj.get("x-encrypted"))),
        require_ownership=_as_bool(field_obj.get("x-requireOwnership")),
        permissions=_as_string_array(field_obj.get("x-permissions"), f"{path}.x-permissions"),
        rest_method=_as_string(field_obj.get("x-restMethod")),
        param_type=_as_string(field_obj.get("x-paramType")),
        middleware=middleware,
        type_ref=_parse_type_ref(field_obj, path),
    )


def _parse_type_definition(
    name: str, definition: dict[str, Any], kind: str, path: str
) -> TypeDef:
    if kind not in ("type", "input"):
        raise SchemaParseError(
            f'runtime schema parse error: unsupported kind "{kind}" for {name}'
        )
    properties_raw = definition.get("properties")
    if not _is_object(properties_raw):
        raise SchemaParseError(
            f"runtime schema parse error: {name} must define object properties"
        )

    required_set = _parse_required_set(definition)
    fields = [
        _parse_field_def(
            field_name, raw_field, required_set, f"{path}.properties.{field_name}"
        )
        for field_name, raw_field in properties_raw.items()
    ]
    return TypeDef(
        name=name,
        description=_as_string(definition.get("description")),
        kind="input" if kind == "input" else "object",
        fields=fields,
        indexes=_parse_indexes(definition),
        env_vars=_as_bool(definition.get("x-envVars")),
    )


def _parse_enum_definition(name: str, definition: dict[str, Any], path: str) -> EnumDef:
    enum_values = _as_string_array(definition.get("enum"), f"{path}.enum")
    enum_metadata_raw = definition.get("x-enumValues")
    enum_metadata = enum_metadata_raw if _is_object(enum_metadata_raw) else {}

    values: list[EnumValueDef] = []
    for value_name in enum_values:
        metadata = enum_metadata.get(value_name)
        metadata_obj = metadata if _is_object(metadata) else {}
        values.append(
            EnumValueDef(
                name=value_name,
                description=_as_string(metadata_obj.get("description")),
                serialized_as=_as_string(metadata_obj.get("serializedAs")),
            )
        )

    return EnumDef(
        name=name,
        owner=_as_string(definition.get("x-owner")),
        description=_as_string(definition.get("description")),
        values=values,
    )


def _parse_file_upload_config(definition: dict[str, Any]) -> FileUploadConfig | None:
    raw = definition.get("x-fileUpload")
    if not _is_object(raw):
        return None
    return FileUploadConfig(
        max_size=_as_positive_int(raw.get("maxSize")),
        allowed_types=_as_string_array(
            raw.get("allowedTypes"), "scalar.x-fileUpload.allowedTypes"
        ),
        category=_as_string(raw.get("category")),
    )


def _parse_image_constraints(definition: dict[str, Any]) -> ImageConstraints | None:
    raw = definition.get("x-imageConstraints")
    if not _is_object(raw):
        return None
    return ImageConstraints(
        max_width=_as_positive_int(raw.get("maxWidth")),
        max_height=_as_positive_int(raw.get("maxHeight")),
        min_aspect_ratio=_as_number_or_none(raw.get("minAspectRatio")),
        max_aspect_ratio=_as_number_or_none(raw.get("maxAspectRatio")),
        require_transparency=_as_bool(raw.get("requireTransparency")),
    )


def _parse_scalar_definition(name: str, definition: dict[str, Any]) -> ScalarDef:
    type_mappings_raw = definition.get("x-typeMapping")
    type_mappings: dict[str, str] = {}
    if _is_object(type_mappings_raw):
        for lang, mapped_type in type_mappings_raw.items():
            if isinstance(mapped_type, str) and mapped_type:
                type_mappings[lang] = mapped_type

    pattern = _as_string(definition.get("pattern"))
    if pattern:
        try:
            re.compile(pattern)
        except re.error as exc:
            raise SchemaParseError(
                f"runtime schema parse error: invalid regex pattern for scalar {name}: {exc}"
            )

    example = _as_string(definition.get("x-example"))
    if not example:
        examples = definition.get("examples")
        if isinstance(examples, list) and examples:
            example = _format_default_value(examples[0])

    return ScalarDef(
        name=name,
        description=_as_string(definition.get("description")),
        primitive=_map_json_type_to_primitive(_as_string(definition.get("type"))),
        min_length=_as_positive_int(definition.get("minLength")),
        max_length=_as_positive_int(definition.get("maxLength")),
        pattern=pattern,
        format=_as_string(definition.get("format")),
        reserved_words=_as_string_array(
            definition.get("x-reservedWords"), f"scalar {name}.x-reservedWords"
        ),
        case_insensitive=_as_bool(definition.get("x-caseInsensitive")),
        reserved_words_case_insensitive=_as_bool(
            definition.get("x-reservedWordsCaseInsensitive")
        ),
        reserved_words_match_partial=_as_bool(
            definition.get("x-reservedWordsMatchPartial")
        ),
        minimum=_as_number_or_none(definition.get("minimum")),
        maximum=_as_number_or_none(definition.get("maximum")),
        example=example,
        file_upload=_parse_file_upload_config(definition),
        image_constraints=_parse_image_constraints(definition),
        has_custom_normalize=_as_bool(definition.get("x-hasCustomNormalize")),
        has_custom_validate=_as_bool(definition.get("x-hasCustomValidate")),
        has_custom_parse=_as_bool(definition.get("x-hasCustomParse")),
        type_mappings=type_mappings,
    )


def _parse_imports(root: dict[str, Any]) -> list[Import]:
    raw = root.get("x-imports")
    if not isinstance(raw, list):
        return []
    imports: list[Import] = []
    for entry in raw:
        if not _is_object(entry):
            continue
        from_ = _as_string(entry.get("from"))
        if not from_:
            continue
        types_raw = entry.get("types")
        types: list[str] = []
        if isinstance(types_raw, str):
            types = [types_raw]
        elif isinstance(types_raw, list):
            types = [t for t in types_raw if isinstance(t, str)]
        imports.append(Import(from_=from_, types=types))
    return imports


def _parse_union_definitions(root: dict[str, Any]) -> dict[str, UnionDef]:
    raw = root.get("x-unions")
    if not _is_object(raw):
        return {}
    unions: dict[str, UnionDef] = {}
    for name, union_raw in raw.items():
        if not _is_object(union_raw):
            continue
        types: list[str] = []
        any_of_raw = union_raw.get("anyOf")
        if isinstance(any_of_raw, list):
            for member in any_of_raw:
                if not _is_object(member):
                    continue
                ref = _as_string(member.get("$ref"))
                if not ref:
                    continue
                resolved = _resolve_ref_name(ref)
                if resolved:
                    types.append(resolved)
        unions[name] = UnionDef(
            name=name,
            description=_as_string(union_raw.get("description")),
            types=types,
        )
    return unions


def _parse_operation_set(raw: Any, path: str) -> OperationSet | None:
    if not _is_object(raw):
        return None
    required_set = _parse_required_set(raw)
    operations: list[FieldDef] = []
    properties_raw = raw.get("properties")
    if _is_object(properties_raw):
        for op_name, op_raw in properties_raw.items():
            operations.append(
                _parse_field_def(
                    op_name, op_raw, required_set, f"{path}.properties.{op_name}"
                )
            )
    middleware = _parse_middleware_config(raw)
    return OperationSet(
        operations=operations,
        middleware=middleware,
        encrypted=(
            middleware.encrypted if middleware else _as_bool(raw.get("x-encrypted"))
        ),
    )


def _looks_like_scalar_definition(definition: dict[str, Any]) -> bool:
    if isinstance(definition.get("x-scalar"), str):
        return True
    if _is_object(definition.get("x-typeMapping")):
        return True
    for key in _SCALAR_CONSTRAINT_KEYS:
        if key in definition:
            return True
    type_name = _as_string(definition.get("type"))
    return type_name in ("string", "integer", "number", "boolean")


def parse_schema(schema: Any) -> Schema:
    """Convert a JSON-Schema-shaped dict into a :class:`Schema`."""
    root = _as_object(schema, "root")
    definitions_raw = root.get("definitions")
    definitions = definitions_raw if _is_object(definitions_raw) else {}

    parsed = Schema(
        name=_as_string(root.get("title")),
        kind=_as_string(root.get("x-kind")),
        description=_as_string(root.get("description")),
        root_type=_as_string(root.get("x-rootType")),
        imports=_parse_imports(root),
        unions=_parse_union_definitions(root),
        queries=_parse_operation_set(root.get("x-queries"), "x-queries"),
        mutations=_parse_operation_set(root.get("x-mutations"), "x-mutations"),
    )

    for name, raw_definition in definitions.items():
        definition = _as_object(raw_definition, f"definitions.{name}")
        kind = _as_string(definition.get("x-kind"))

        if kind == "type":
            parsed.types[name] = _parse_type_definition(
                name, definition, "type", f"definitions.{name}"
            )
            continue
        if kind == "input":
            parsed.inputs[name] = _parse_type_definition(
                name, definition, "input", f"definitions.{name}"
            )
            continue
        if kind == "enum":
            parsed.enums[name] = _parse_enum_definition(
                name, definition, f"definitions.{name}"
            )
            continue
        if kind:
            raise SchemaParseError(
                f'runtime schema parse error: unsupported x-kind "{kind}" for definition {name}'
            )
        if _looks_like_scalar_definition(definition):
            parsed.scalars[name] = _parse_scalar_definition(name, definition)

    return parsed


def parse_schema_json(schema_json: str | bytes) -> Schema:
    """Parse a JSON string/bytes into a :class:`Schema`."""
    if isinstance(schema_json, bytes):
        schema_json = schema_json.decode("utf-8")
    try:
        parsed = json.loads(schema_json)
    except json.JSONDecodeError as exc:
        raise SchemaParseError(
            f"runtime schema parse error: invalid JSON: {exc}"
        ) from exc
    return parse_schema(parsed)


__all__ = ["parse_schema", "parse_schema_json", "SchemaParseError"]
