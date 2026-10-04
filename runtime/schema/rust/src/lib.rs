//! Validation helpers the generated Rust types crates call.
//!
//! A generated `validators.rs` checks a JSON value against its schema type
//! the way the generated Go, TypeScript and Python validators do (D14): one
//! error per failing value, named by the rule it breaks, at a path such as
//! `field`, `field[i]` or `field.key`. This crate holds what every such
//! module shares: the error map, the JSON type checks with their messages,
//! code-point lengths, path spelling, compiled patterns, a field's own rules,
//! the structural checks on nested objects and lists, and what a
//! `parse_<type>` needs. The type-specific checks themselves are generated.
//! Unlike the Go, TypeScript and Python schema runtimes it reads no IR.

mod errors;
mod json;
mod nested;
mod parse;
mod path;
mod pattern;
mod rules;
mod text;

pub use errors::{Entry, ScalarResult, ValidationError, ValidationErrors, REQUIRED_MESSAGE};
pub use json::{
    as_array, as_object, expect_boolean, expect_integer, expect_list, expect_number, expect_object,
    expect_string, finite_number, integer_value, is_absent,
};
pub use nested::{
    check_nested, check_rows, require_elements, require_grid_elements, require_object,
};
pub use parse::{check_unknown_fields, fill_default, ParseError, UnknownFields};
pub use path::{bracket_key_path, index_path, key_path};
pub use pattern::{translate_pattern, Pattern};
pub use rules::{check_list_rules, check_value_rules, Rule};
pub use text::code_points;
