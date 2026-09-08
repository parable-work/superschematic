// Package parse interprets an IR Schema to coerce, normalize, default, and
// scalar-parse a runtime data map at runtime. It is the runtime mirror of the
// compile-time-generated FromMap / FromMapStrict / FromJSON / FromYAML
// helpers, operating on map[string]any instead of typed structs.
//
// # Operation order
//
// For each field of the target type or input, parse performs:
//
//  1. Field lookup by JSONTag (falls back to Name).
//  2. If the field is absent and the IR FieldDef has a Default, parse the
//     default string into the field's primitive (Int, Float, Bool, String /
//     custom scalar / enum) and insert it. Defaults on object-typed fields
//     and arrays are not applied in this phase.
//  3. Lenient coercion: JSON-decoded values are coerced into the expected
//     primitive (integer-valued float64 -> int64 for Int scalars, string -> bool for
//     "true"/"false", json.Number -> int64 / float64, etc.). Strict mode
//     disables coercion and emits {Validator: "type"} for any mismatch.
//  4. If ScalarDef.HasCustomNormalize and the NormalizeRegistry has an
//     entry, run normalize on the value and replace it in the result.
//  5. If ScalarDef.HasCustomParse and the ParseRegistry has an entry, run
//     parse. On error emit {Validator: "parse"} at the field path; on
//     success replace the value with the parse-canonicalized value while
//     preserving integer scalar values as int64.
//  6. For nested type / input fields, recurse with the same Parser instance.
//  7. For arrays, apply per-element recursion using index-keyed error paths
//     (foo[0], foo[1], ...).
//
// # Contract with validate
//
// parse does NOT run schema validation constraints (MinLength, Pattern,
// validateMin / validateMax, HasCustomValidate). Those stay in the validate
// package. The Phase C.5 runtime facade is the one place that composes parse
// and validate into a single LoadType call. Callers that need both today can
// invoke them in sequence:
//
//	m, errs := parse.New(schema).ParseTypeJSON("MyType", data)
//	if errs.HasErrors() {
//	    return errs
//	}
//	return validate.New(schema).ValidateType("MyType", m)
//
// # Error shape
//
// Errors use the same scalar-lib ValidationErrors map that validate emits,
// so downstream error handling does not need to branch on which phase
// produced them.
package parse
