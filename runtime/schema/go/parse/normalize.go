package parse

import (
	"sort"

	scalarlib "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/internal/scalarcore"
)

// ScalarNormalizeFunc transforms a scalar's string form into its canonical
// representation. Pure transform: no validation, no errors. Matches the
// signature of scalar-lib's Normalize<Name>(string) string family.
type ScalarNormalizeFunc func(input string) string

// NormalizeRegistry maps canonical scalar names (e.g. "Contact.Email") to
// their normalize functions. The runtime parser consults this registry when
// ScalarDef.HasCustomNormalize is true.
type NormalizeRegistry struct {
	fns map[string]ScalarNormalizeFunc
}

// NewNormalizeRegistry creates an empty NormalizeRegistry.
func NewNormalizeRegistry() *NormalizeRegistry {
	return &NormalizeRegistry{fns: make(map[string]ScalarNormalizeFunc)}
}

// Register adds or replaces a normalize function for the named scalar.
func (r *NormalizeRegistry) Register(name string, fn ScalarNormalizeFunc) {
	r.fns[name] = fn
}

// Unregister removes the normalize function for the named scalar. Returns
// true if an entry existed.
func (r *NormalizeRegistry) Unregister(name string) bool {
	_, ok := r.fns[name]
	if ok {
		delete(r.fns, name)
	}
	return ok
}

// Get returns the normalize function for the named scalar and whether it
// was registered.
func (r *NormalizeRegistry) Get(name string) (ScalarNormalizeFunc, bool) {
	fn, ok := r.fns[name]
	return fn, ok
}

// Has reports whether a normalize function is registered for the named scalar.
func (r *NormalizeRegistry) Has(name string) bool {
	_, ok := r.fns[name]
	return ok
}

// Len returns the number of registered normalize functions.
func (r *NormalizeRegistry) Len() int {
	return len(r.fns)
}

// Names returns the sorted list of registered scalar names.
func (r *NormalizeRegistry) Names() []string {
	names := make([]string, 0, len(r.fns))
	for n := range r.fns {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// MissingNormalizers reports canonical names of scalars in schema where
// HasCustomNormalize is true but no entry is registered. Empty slice means
// full coverage. Mirrors validate.Registry.MissingValidators.
func (r *NormalizeRegistry) MissingNormalizers(schema *ir.Schema) []string {
	var missing []string
	for _, s := range schema.Scalars {
		if s.HasCustomNormalize && !r.Has(s.Name) {
			missing = append(missing, s.Name)
		}
	}
	sort.Strings(missing)
	return missing
}

// NewDispatchNormalizeRegistry returns a NormalizeRegistry that routes every
// name in names through normalize, the name-keyed entry point of a scalar
// core (scalar-lib's Normalize). Normalize is a pure transform with no error
// channel, so a core error leaves the input unchanged; validation reports it.
func NewDispatchNormalizeRegistry(names []string, normalize func(canonical, value string) (string, error)) *NormalizeRegistry {
	r := NewNormalizeRegistry()
	for _, name := range names {
		canonical := name
		r.Register(canonical, func(input string) string {
			out, err := normalize(canonical, input)
			if err != nil {
				return input
			}
			return out
		})
	}
	return r
}

// DefaultNormalizeRegistry returns the normalize registry of the scalar core
// this module links: every canonical name whose metadata marks a custom
// normalize step, dispatched through [scalarlib.Normalize]. The Parser only
// consults the registry for HasCustomNormalize scalars, so those are the
// only names it holds.
func DefaultNormalizeRegistry() *NormalizeRegistry {
	return NewDispatchNormalizeRegistry(scalarcore.NormalizeNames(), scalarlib.Normalize)
}
