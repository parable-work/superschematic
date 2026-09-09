package serialize

import scalarlib "github.com/parable-work/superscalar/go"

// ValidationError is a single validation error with a validator name and
// message. Type-aliased to keep error types consistent across all
// schema-runtime packages.
type ValidationError = scalarlib.ValidationError

// ValidationErrors is a map of field names to their validation errors.
// Values are either []ValidationError (leaf errors) or ValidationErrors
// (nested errors).
type ValidationErrors = scalarlib.ValidationErrors

// NewValidationErrors creates a new empty ValidationErrors instance.
var NewValidationErrors = scalarlib.NewValidationErrors
