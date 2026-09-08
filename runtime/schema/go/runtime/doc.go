// Package runtime is the top-level facade for schema-runtime. It composes
// the granular parse, validate, serialize, mask, and merge packages into a
// single object-level API that mirrors what the compile-time-generated type
// library offers.
//
// # Recommended entry point
//
// For most callers, the package-level functions are the simplest path:
//
//	data, errs := runtime.LoadType(schema, "MyType", jsonBytes)
//	errs := runtime.ValidateType(schema, "MyType", data)
//	b, errs := runtime.MarshalType(schema, "MyType", data)
//
// Each call applies sensible defaults: the scalar registries come from
// [Default] (the scalar-lib parse, normalize and validate functions),
// serialize runs with no secret masking, etc. Inject a different scalar
// set with [WithRegistry].
//
// # Reusable Runtime
//
// Services that issue many calls against the same schema should construct
// a [Runtime] once and reuse it; this caches the per-package state (regex
// compilation, registry lookups) instead of repeating it per call.
//
//	rt := runtime.New(schema, runtime.WithMaskSecrets(true))
//	data, errs := rt.LoadType("MyType", jsonBytes)
//	b, errs   := rt.MarshalType("MyType", data)
//
// # LoadType semantics
//
// LoadType is the bread-and-butter call. It runs parse first (coerce,
// normalize, defaults, scalar parse) and then validate (required checks,
// scalar constraints, custom validators). Errors from either phase are
// merged into a single ValidationErrors response so the caller never has
// to branch on which phase produced them. This mirrors the generated
// FromJSON + Validate() pair exactly.
//
// # Granular packages remain available
//
// Power users can import [parse], [validate], [serialize], [mask], or
// [merge] directly when they need fine control over a single phase. The
// facade does not duplicate logic; it composes the same code paths.
package runtime
