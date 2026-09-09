package parse

import scalarlib "github.com/parable-work/superscalar/go"

// ValidationError is a single validation error with a validator name and
// message. This is a type alias for [scalarlib.ValidationError] to ensure
// error types are consistent across runtime parse, runtime validate, and
// generated code.
type ValidationError = scalarlib.ValidationError

// ValidationErrors is a map of field names to their validation errors.
// Values are either []ValidationError (leaf errors) or ValidationErrors
// (nested errors). This is a type alias for [scalarlib.ValidationErrors].
type ValidationErrors = scalarlib.ValidationErrors

// NewValidationErrors creates a new empty ValidationErrors instance.
var NewValidationErrors = scalarlib.NewValidationErrors
