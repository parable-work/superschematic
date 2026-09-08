package runtime

import (
	"github.com/parable-work/superschematic/runtime/schema/go/parse"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// Registry bundles the scalar registries a Runtime consults: parse,
// normalize and validate functions keyed by canonical scalar name. It is
// the injection point for the set of scalar names a runtime knows; the
// names themselves are registered by whoever builds the Registry.
type Registry struct {
	Parse     *parse.ParseRegistry
	Normalize *parse.NormalizeRegistry
	Validate  *validate.Registry
}

// NewRegistry returns a Registry with three empty registries.
func NewRegistry() *Registry {
	return &Registry{
		Parse:     parse.NewParseRegistry(),
		Normalize: parse.NewNormalizeRegistry(),
		Validate:  validate.NewRegistry(),
	}
}

// Default returns the registry a Runtime uses when none is injected:
// [parse.DefaultParseRegistry], [parse.DefaultNormalizeRegistry] and
// [validate.DefaultRegistry], the scalar core this module links, dispatched
// by canonical name. An extension assembled over the core (the Parable set
// lives in utils/parable-schematic/ext/scalars) supplies its own bundle via
// [WithRegistry].
func Default() *Registry {
	return &Registry{
		Parse:     parse.DefaultParseRegistry(),
		Normalize: parse.DefaultNormalizeRegistry(),
		Validate:  validate.DefaultRegistry(),
	}
}

// WithRegistry injects all three scalar registries at once. The
// per-registry options ([WithParseRegistry], [WithNormalizeRegistry],
// [WithValidateRegistry]) still apply in option order, so one part of an
// injected bundle can be replaced.
func WithRegistry(reg *Registry) Option {
	return func(rt *Runtime) {
		rt.parseReg = reg.Parse
		rt.normReg = reg.Normalize
		rt.validReg = reg.Validate
	}
}
