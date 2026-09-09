// Package serialize walks a runtime data map against an IR Schema and
// produces a sanitized, schema-shaped output suitable for transport.
//
// The serializer mirrors the compile-time-generated ToMap / MarshalJSON
// helpers. Specifically it:
//
//   - Drops fields not declared in the type (or rejects them in strict mode).
//   - Looks up declared fields by JSONTag, falling back to Name.
//   - Recurses into nested types and arrays.
//   - Normalizes nil slices to empty slices so JSON renders `[]` instead of
//     `null` for list-typed fields (matching generated MarshalJSON).
//   - Optionally zeroes @secret fields via [WithMaskSecrets].
//
// Errors are returned in the same [scalarlib.ValidationErrors] shape that
// validate and parse use, so downstream code never has to branch on which
// phase produced them.
package serialize
