"""IR dataclasses for the superschematic schema runtime.

Python uses snake_case attributes; the JSON Schema source uses camelCase
``x-*`` extension keys (the JSON reader translates between the two).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Literal


TypeKind = Literal["object", "input"]
DefinitionKind = Literal["type", "input", "enum"]


@dataclass
class TypeRef:
    name: str
    is_array: bool = False
    elem_non_null: bool = True
    # A list of lists (T[][]): each element of the outer list is a list of
    # ``name``. Requires ``is_array``. Inner lists are never null. Last, so
    # positional construction keeps its meaning.
    is_array_of_arrays: bool = False


@dataclass
class FileUploadConfig:
    max_size: int = 0
    allowed_types: list[str] = field(default_factory=list)
    category: str = ""


@dataclass
class ImageConstraints:
    max_width: int = 0
    max_height: int = 0
    min_aspect_ratio: float | None = None
    max_aspect_ratio: float | None = None
    require_transparency: bool = False


@dataclass
class ScalarDef:
    name: str
    description: str = ""
    primitive: str = "String"
    min_length: int = 0
    max_length: int = 0
    pattern: str = ""
    format: str = ""
    reserved_words: list[str] = field(default_factory=list)
    case_insensitive: bool = False
    reserved_words_case_insensitive: bool = False
    reserved_words_match_partial: bool = False
    minimum: float | None = None
    maximum: float | None = None
    example: str = ""
    file_upload: FileUploadConfig | None = None
    image_constraints: ImageConstraints | None = None
    has_custom_normalize: bool = False
    has_custom_validate: bool = False
    has_custom_parse: bool = False
    type_mappings: dict[str, str] = field(default_factory=dict)


@dataclass
class MiddlewareConfig:
    rate_limit: int | None = None
    body_limit: int | None = None
    timeout: int | None = None
    encrypted: bool = False


@dataclass
class RelationDef:
    type: str = ""
    field: str = ""


@dataclass
class ArgumentDef:
    name: str
    description: str = ""
    type_ref: TypeRef = field(default_factory=lambda: TypeRef(name=""))
    required: bool = False
    default_value: str | None = None
    is_query: bool = False


@dataclass
class FieldDef:
    name: str
    description: str = ""
    # Human-facing label (JSON Schema `title` key); empty = derive from name.
    title: str = ""
    # Longer-form Markdown explaining what the field is for (`x-purpose`).
    purpose: str = ""
    # Name of the glyph a UI shows for the field (`x-icon`).
    icon: str = ""
    # Input placeholder hint (`x-placeholder` in the legacy form).
    placeholder: str = ""
    json_key: str = ""
    required: bool = False
    auto_generated: bool = False
    default_value: str | None = None
    validate_min: float | None = None
    validate_max: float | None = None
    validate_min_length: int | None = None
    validate_max_length: int | None = None
    validate_list_min: int | None = None
    validate_list_max: int | None = None
    validate_pattern: str = ""
    arguments: list[ArgumentDef] = field(default_factory=list)
    key: bool = False
    unique: bool = False
    search_field: bool = False
    relation: RelationDef | None = None
    has_many: bool = False
    many_to_many: bool = False
    json_field: bool = False
    secret: bool = False
    ui_hidden: bool = False
    internal_metadata: bool = False
    auth: bool = False
    encrypted: bool = False
    require_ownership: bool = False
    permissions: list[str] = field(default_factory=list)
    rest_method: str = ""
    param_type: str = ""
    middleware: MiddlewareConfig | None = None
    type_ref: TypeRef = field(default_factory=lambda: TypeRef(name=""))


@dataclass
class IndexDef:
    keys: list[str] = field(default_factory=list)
    unique: bool = False


@dataclass
class TypeDef:
    name: str
    description: str = ""
    kind: TypeKind = "object"
    fields: list[FieldDef] = field(default_factory=list)
    indexes: list[IndexDef] = field(default_factory=list)
    env_vars: bool = False


@dataclass
class EnumValueDef:
    name: str
    description: str = ""
    serialized_as: str = ""


@dataclass
class EnumDef:
    name: str
    owner: str = ""
    description: str = ""
    values: list[EnumValueDef] = field(default_factory=list)


@dataclass
class UnionDef:
    name: str
    description: str = ""
    types: list[str] = field(default_factory=list)


@dataclass
class Import:
    from_: str = ""
    types: list[str] = field(default_factory=list)


@dataclass
class OperationSet:
    operations: list[FieldDef] = field(default_factory=list)
    middleware: MiddlewareConfig | None = None
    encrypted: bool = False


@dataclass
class Schema:
    name: str = ""
    kind: str = ""
    description: str = ""
    root_type: str = ""
    imports: list[Import] = field(default_factory=list)
    scalars: dict[str, ScalarDef] = field(default_factory=dict)
    enums: dict[str, EnumDef] = field(default_factory=dict)
    types: dict[str, TypeDef] = field(default_factory=dict)
    inputs: dict[str, TypeDef] = field(default_factory=dict)
    unions: dict[str, UnionDef] = field(default_factory=dict)
    queries: OperationSet | None = None
    mutations: OperationSet | None = None


SchemaInput = Schema | dict[str, Any] | str | bytes


__all__ = [
    "ArgumentDef",
    "DefinitionKind",
    "EnumDef",
    "EnumValueDef",
    "FieldDef",
    "FileUploadConfig",
    "ImageConstraints",
    "IndexDef",
    "Import",
    "MiddlewareConfig",
    "OperationSet",
    "RelationDef",
    "ScalarDef",
    "Schema",
    "SchemaInput",
    "TypeDef",
    "TypeKind",
    "TypeRef",
    "UnionDef",
]
