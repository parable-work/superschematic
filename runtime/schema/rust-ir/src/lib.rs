//! The behavior engine's run-time schema reader (D58): the IR reader and
//! the validation and parse of a value against a type it declares, ported
//! from the TypeScript runtime (`@superschematic/schema-runtime`'s `ir/`,
//! `validation/` and `parse/`) so a schema defined while a program runs is
//! checked as the TypeScript runtime checks it, with the same paths, codes
//! and messages. Every scalar's own checks are superscalar's core's (D3),
//! which this crate links by path, so it is not published; the helpers it
//! shares with the generated Rust validators come from
//! `superschematic-schema-runtime`.
//!
//! ```
//! use serde_json::json;
//! use superschematic_schema_ir::{parse_schema_ir, ParseOptions, Runtime};
//!
//! let schema = parse_schema_ir(&json!({
//!     "types": {"Order": {"name": "Order", "role": "DBTable", "fields": [
//!         {"name": "email", "typeRef": {"name": "Contact.Email"}, "required": true}
//!     ]}}
//! }))
//! .unwrap();
//! let runtime = Runtime::new(schema);
//! let errors = runtime.validate_type("Order", &json!({}));
//! assert_eq!(errors.errors_at("email").unwrap()[0].validator, "required");
//! let parsed = runtime.parse_type("Order", &json!({"email": " A@B.CO "}), ParseOptions { strict: true });
//! assert_eq!(parsed.data["email"], "a@b.co");
//! ```

mod errors;
mod jsjson;
mod jsnum;
mod parse;
mod reader;
mod scalars;
mod schema;
mod validate;

use serde_json::Value;

pub use errors::{FieldEntry, FieldErrors};
pub use parse::{parse_input, parse_type, ParseOptions, ParseResult};
pub use reader::{
    builtin_scalar_value_classes, builtin_scalars, parse_schema_ir, parse_schema_ir_json, IrError,
};
pub use schema::{
    is_any_json_scalar, structured_json_type, ArgumentDef, EnumDef, EnumValueDef, FieldDef,
    FileUploadConfig, ImageConstraints, Import, IndexDef, JsonShape, MiddlewareConfig, RelationDef,
    ScalarDef, Schema, TypeDef, TypeKind, TypeRef, UnionDef,
};

use validate::Patterns;

/// The errors of data as a value of the named object type: empty when it is
/// valid, or when the schema has no such type. A value that is not an
/// object is read as an empty one. Each call compiles the patterns it
/// meets; a [`Runtime`] compiles each once.
pub fn validate_schema_type(schema: &Schema, type_name: &str, data: &Value) -> FieldErrors {
    validate::validate_schema_type(schema, &Patterns::default(), type_name, data)
}

/// The errors of data as a value of the named input type, as
/// [`validate_schema_type`] reads an object type.
pub fn validate_schema_input(schema: &Schema, input_name: &str, data: &Value) -> FieldErrors {
    validate::validate_schema_input(schema, &Patterns::default(), input_name, data)
}

/// One schema with the patterns its validation compiles, built once and
/// called many times: the TypeScript runtime's `Runtime`, for parse and
/// validation.
pub struct Runtime {
    schema: Schema,
    patterns: Patterns,
}

impl Runtime {
    pub fn new(schema: Schema) -> Self {
        Self {
            schema,
            patterns: Patterns::default(),
        }
    }

    pub fn schema(&self) -> &Schema {
        &self.schema
    }

    /// The errors of data as a value of the named object type
    /// ([`validate_schema_type`]).
    pub fn validate_type(&self, type_name: &str, data: &Value) -> FieldErrors {
        validate::validate_schema_type(&self.schema, &self.patterns, type_name, data)
    }

    /// The errors of data as a value of the named input type
    /// ([`validate_schema_input`]).
    pub fn validate_input(&self, input_name: &str, data: &Value) -> FieldErrors {
        validate::validate_schema_input(&self.schema, &self.patterns, input_name, data)
    }

    /// Parses data as a value of the named object type ([`parse_type`]).
    pub fn parse_type(&self, type_name: &str, data: &Value, options: ParseOptions) -> ParseResult {
        parse_type(&self.schema, type_name, data, options)
    }

    /// Parses data as a value of the named input type ([`parse_input`]).
    pub fn parse_input(
        &self,
        input_name: &str,
        data: &Value,
        options: ParseOptions,
    ) -> ParseResult {
        parse_input(&self.schema, input_name, data, options)
    }
}

impl std::fmt::Debug for Runtime {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Runtime")
            .field("schema", &self.schema)
            .finish_non_exhaustive()
    }
}

#[cfg(test)]
mod tests {
    use super::Runtime;

    #[test]
    fn a_runtime_is_shared_across_threads() {
        fn shared<T: Send + Sync>() {}
        shared::<Runtime>();
    }
}
