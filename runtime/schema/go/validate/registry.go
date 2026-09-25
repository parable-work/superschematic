package validate

import (
	"sort"

	scalarlib "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/internal/scalarcore"
)

// ScalarValidateFunc validates a string value for a custom scalar type.
// It returns a slice of validation errors, or nil if the value is valid.
// This signature matches superscalar's Validate{Name}(input string) pattern.
type ScalarValidateFunc func(value string) []ValidationError

// Registry maps scalar type names to their custom validation functions.
// When the IR marks a scalar with HasCustomValidate, the validator looks up the
// scalar name in the registry to find and invoke the custom validation function.
type Registry struct {
	validators map[string]ScalarValidateFunc
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		validators: make(map[string]ScalarValidateFunc),
	}
}

// Register adds a custom validation function for the named scalar type.
// If a function is already registered for the name, it is replaced.
func (r *Registry) Register(name string, fn ScalarValidateFunc) {
	r.validators[name] = fn
}

// Unregister removes the validation function for the named scalar type.
// Returns true if the entry existed and was removed.
func (r *Registry) Unregister(name string) bool {
	_, ok := r.validators[name]
	if ok {
		delete(r.validators, name)
	}
	return ok
}

// Get returns the validation function for the named scalar type and whether it was found.
func (r *Registry) Get(name string) (ScalarValidateFunc, bool) {
	fn, ok := r.validators[name]
	return fn, ok
}

// Has reports whether a validation function is registered for the named scalar type.
func (r *Registry) Has(name string) bool {
	_, ok := r.validators[name]
	return ok
}

// Len returns the number of registered scalar validators.
func (r *Registry) Len() int {
	return len(r.validators)
}

// Names returns a sorted list of all registered scalar type names.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.validators))
	for name := range r.validators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MissingValidators inspects a schema and returns the names of scalars that
// have HasCustomValidate set to true but have no corresponding entry in the
// registry. The returned slice is sorted alphabetically. An empty slice means
// every custom-validate scalar is covered.
func (r *Registry) MissingValidators(schema *ir.Schema) []string {
	var missing []string
	for _, scalar := range schema.Scalars {
		if scalar.HasCustomValidate && !r.Has(scalar.Name) {
			missing = append(missing, scalar.Name)
		}
	}
	sort.Strings(missing)
	return missing
}

// NewDispatchRegistry returns a Registry that routes every name in names
// through validate, the name-keyed entry point of a scalar core (superscalar's
// Validate). A non-nil error becomes a single ValidationError tagged "pattern",
// the name every validator gives a malformed scalar value, carrying the core's
// message, so a failure other than a regex mismatch (a length or a parse
// failure) still says why. nil means the value is valid.
//
// This is how a scalar core, or an extension assembled over one, hands the
// runtime its whole scalar set without a hand-maintained name -> func mirror.
func NewDispatchRegistry(names []string, validate func(canonical, value string) error) *Registry {
	r := NewRegistry()
	for _, name := range names {
		canonical := name
		r.Register(canonical, func(value string) []ValidationError {
			if err := validate(canonical, value); err != nil {
				return []ValidationError{{Validator: "pattern", Message: err.Error()}}
			}
			return nil
		})
	}
	return r
}

// DefaultRegistry returns the registry of the scalar core this module links:
// every canonical name in superscalar's table, dispatched through
// [scalarlib.Validate]. The Validator consults it for every registered name
// regardless of HasCustomValidate, so it carries the core's custom checks
// (Embedding.Vector / Generic.StringMap serde, min_length rules) that the
// generic IR constraints cannot express.
//
// Names the registry lacks fall back to the IR constraints, so a schema's
// inline scalars keep working without an entry.
func DefaultRegistry() *Registry {
	return NewDispatchRegistry(scalarcore.Names(), scalarlib.Validate)
}
