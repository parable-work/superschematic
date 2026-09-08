package parse

import (
	"sort"

	scalarlib "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/internal/scalarcore"
)

// ScalarParseFunc parses a scalar's runtime string form. On success it
// returns the canonicalized string (e.g. an RFC3339 DateTime); on failure
// it returns the input untouched and a non-empty ValidationError slice
// keyed with {Validator: "parse"}.
type ScalarParseFunc func(input string) (string, []ValidationError)

// ParseRegistry maps canonical scalar names to their parse functions. The
// runtime parser consults this registry when ScalarDef.HasCustomParse is true.
type ParseRegistry struct {
	fns map[string]ScalarParseFunc
}

// NewParseRegistry creates an empty ParseRegistry.
func NewParseRegistry() *ParseRegistry {
	return &ParseRegistry{fns: make(map[string]ScalarParseFunc)}
}

// Register adds or replaces a parse function for the named scalar.
func (r *ParseRegistry) Register(name string, fn ScalarParseFunc) {
	r.fns[name] = fn
}

// Unregister removes the parse function for the named scalar. Returns true
// if an entry existed.
func (r *ParseRegistry) Unregister(name string) bool {
	_, ok := r.fns[name]
	if ok {
		delete(r.fns, name)
	}
	return ok
}

// Get returns the parse function for the named scalar and whether it was
// registered.
func (r *ParseRegistry) Get(name string) (ScalarParseFunc, bool) {
	fn, ok := r.fns[name]
	return fn, ok
}

// Has reports whether a parse function is registered for the named scalar.
func (r *ParseRegistry) Has(name string) bool {
	_, ok := r.fns[name]
	return ok
}

// Len returns the number of registered parse functions.
func (r *ParseRegistry) Len() int {
	return len(r.fns)
}

// Names returns the sorted list of registered scalar names.
func (r *ParseRegistry) Names() []string {
	names := make([]string, 0, len(r.fns))
	for n := range r.fns {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// MissingParsers reports canonical names of scalars in schema where
// HasCustomParse is true but no entry is registered. Mirrors
// validate.Registry.MissingValidators.
func (r *ParseRegistry) MissingParsers(schema *ir.Schema) []string {
	var missing []string
	for _, s := range schema.Scalars {
		if s.HasCustomParse && !r.Has(s.Name) {
			missing = append(missing, s.Name)
		}
	}
	sort.Strings(missing)
	return missing
}

// NewDispatchParseRegistry returns a ParseRegistry that routes every name in
// names through parse, the name-keyed entry point of a scalar core
// (scalar-lib's Parse). On success the core's canonical form is returned; a
// non-nil error becomes a single ValidationError tagged "parse" and the
// input is returned untouched.
func NewDispatchParseRegistry(names []string, parse func(canonical, value string) (string, error)) *ParseRegistry {
	r := NewParseRegistry()
	for _, name := range names {
		canonical := name
		r.Register(canonical, func(input string) (string, []ValidationError) {
			out, err := parse(canonical, input)
			if err != nil {
				return input, []ValidationError{{Validator: "parse", Message: err.Error()}}
			}
			return out, nil
		})
	}
	return r
}

// DefaultParseRegistry returns the parse registry of the scalar core this
// module links: every canonical name whose metadata marks a custom parse
// step, dispatched through [scalarlib.Parse]. The Parser only consults the
// registry for HasCustomParse scalars, so those are the only names it holds.
func DefaultParseRegistry() *ParseRegistry {
	return NewDispatchParseRegistry(scalarcore.ParseNames(), scalarlib.Parse)
}
