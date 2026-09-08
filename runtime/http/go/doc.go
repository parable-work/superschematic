// Package httpruntime is the shared HTTP runtime the generated Go API packages import.
//
// Concrete shared code will be added in focused subpackages such as response,
// apperror, requestctx, middleware, and routing so generated API packages can
// stop emitting identical helpers into every schema-specific module.
package httpruntime
