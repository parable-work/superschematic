// Package validate provides a runtime validation engine that interprets IR schema definitions
// to validate data at runtime, producing the same error structure as code-generated validators.
//
// The validator mirrors the behavior of superschematic's generated Validate() and ValidateRequired()
// methods, but instead of generating code, it interprets [ir.Schema] rules dynamically.
// This enables validation in contexts where code generation is not available, such as
// dynamic configuration validation, plugin systems, and cross-language schema enforcement.
//
// # Usage
//
//	v := validate.New(schema)
//	errs := v.ValidateType("MyType", map[string]any{
//	    "name":  "test",
//	    "email": "invalid",
//	})
//	if errs.HasErrors() {
//	    // handle validation errors
//	}
//
// # Scalar Validator Registry
//
// The [Registry] bridges IR scalar definitions to superscalar validation functions.
// [DefaultRegistry] pre-populates entries for all string-based scalars in superscalar
// (Color, Email, PhoneNumber). For scalars with HasCustomValidate that are not covered
// by the default registry, register additional validators or use [Registry.MissingValidators]
// to detect gaps:
//
//	reg := validate.DefaultRegistry()
//	reg.Register("CustomScalar", func(value string) []validate.ValidationError {
//	    // custom validation logic
//	    return nil
//	})
//
//	// Detect unregistered custom scalars
//	if missing := reg.MissingValidators(schema); len(missing) > 0 {
//	    log.Warn("scalars with HasCustomValidate but no registry entry", "scalars", missing)
//	}
//
//	v := validate.New(schema, validate.WithRegistry(reg))
//
// Scalars without a registry entry still have their IR-defined constraints enforced
// (pattern, minLength, maxLength, minimum, maximum); only the custom validation
// function call is skipped.
package validate
