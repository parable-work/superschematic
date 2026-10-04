//! Validation helpers the generated Rust types crates call.
//!
//! A generated `validators.rs` checks a JSON value against its schema type
//! the way the generated Go, TypeScript and Python validators do (D14): one
//! error per failing value, named by the rule it breaks, at a path such as
//! `field`, `field[i]` or `field.key`. This crate holds what every such
//! module shares: the error map, the JSON type checks with their messages,
//! code-point lengths, path spelling and compiled patterns. The rules
//! themselves are generated. Unlike the Go, TypeScript and Python schema
//! runtimes it reads no IR.

mod errors;
mod json;
mod path;
mod pattern;
mod text;

pub use errors::{Entry, ValidationError, ValidationErrors, REQUIRED_MESSAGE};
pub use json::{
    expect_boolean, expect_integer, expect_list, expect_number, expect_object, expect_string,
    finite_number, integer_value, is_absent,
};
pub use path::{bracket_key_path, index_path, key_path};
pub use pattern::{translate_pattern, Pattern};
pub use text::code_points;
