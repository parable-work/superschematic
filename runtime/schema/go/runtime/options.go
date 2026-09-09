package runtime

import (
	"github.com/parable-work/superschematic/runtime/schema/go/parse"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// Option configures a Runtime. Options bundle the per-package settings so
// callers configure registries, strict modes, and masking once instead of
// passing them through every call.
type Option func(*Runtime)

// WithParseRegistry overrides the scalar parse registry used by Load /
// Parse calls. Defaults to the Parse half of [Default].
func WithParseRegistry(r *parse.ParseRegistry) Option {
	return func(rt *Runtime) {
		rt.parseReg = r
	}
}

// WithNormalizeRegistry overrides the scalar normalize registry used by
// Load / Parse calls. Defaults to the Normalize half of [Default].
func WithNormalizeRegistry(r *parse.NormalizeRegistry) Option {
	return func(rt *Runtime) {
		rt.normReg = r
	}
}

// WithValidateRegistry overrides the scalar validate registry used by Load
// / Validate calls. Defaults to the Validate half of [Default].
func WithValidateRegistry(r *validate.Registry) Option {
	return func(rt *Runtime) {
		rt.validReg = r
	}
}

// WithStrictRegistry panics during Runtime construction if the schema has
// HasCustomValidate / HasCustomParse / HasCustomNormalize scalars without a
// registered handler. Off by default; consumers that want startup-time
// safety enable it during service init.
func WithStrictRegistry(strict bool) Option {
	return func(rt *Runtime) {
		rt.strictRegistry = strict
	}
}

// WithStrict makes the no-suffix Load / Parse / Marshal calls behave as
// their Strict variants (reject unknown fields, no coercion). Convenience
// for callers that pass a strict flag through configuration.
func WithStrict(strict bool) Option {
	return func(rt *Runtime) {
		rt.strict = strict
	}
}

// WithMaskSecrets zeroes @secret fields during Marshal / TypeToMap calls.
// Off by default to match generated MarshalJSON behaviour; service code
// that returns data to clients enables this to drop credentials.
func WithMaskSecrets(mask bool) Option {
	return func(rt *Runtime) {
		rt.maskSecrets = mask
	}
}
