"""Schema-aware parse package."""

from .parser import (
    ParseOptions,
    ParseResult,
    parse_input,
    parse_input_json,
    parse_input_map,
    parse_type,
    parse_type_json,
    parse_type_map,
)
from .registry import (
    ScalarNormalizeFunc,
    ScalarNormalizeRegistry,
    ScalarParseFunc,
    ScalarParseRegistry,
    create_default_scalar_normalize_registry,
    create_default_scalar_parse_registry,
)

__all__ = [
    "ParseOptions",
    "ParseResult",
    "ScalarNormalizeFunc",
    "ScalarNormalizeRegistry",
    "ScalarParseFunc",
    "ScalarParseRegistry",
    "create_default_scalar_normalize_registry",
    "create_default_scalar_parse_registry",
    "parse_input",
    "parse_input_json",
    "parse_input_map",
    "parse_type",
    "parse_type_json",
    "parse_type_map",
]
