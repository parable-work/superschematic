package runtime

import scalarlib "github.com/parable-work/superscalar/go"

// ValidationError is a single validation error. Type-aliased so callers can
// import a single runtime package and never reach into validate / parse /
// serialize internals to handle errors.
type ValidationError = scalarlib.ValidationError

// ValidationErrors is the canonical error shape returned by every facade
// function. Values are either []ValidationError (leaf errors) or
// ValidationErrors (nested errors) -- same shape as the generated
// Validate() helpers emit.
type ValidationErrors = scalarlib.ValidationErrors

// NewValidationErrors creates a new empty ValidationErrors instance.
var NewValidationErrors = scalarlib.NewValidationErrors

// mergeErrors copies entries from src into dst. Keys that exist in both
// are merged: leaf slices concatenate, nested ValidationErrors recurse.
func mergeErrors(dst, src ValidationErrors) {
	if src == nil {
		return
	}
	for key, val := range src {
		existing, present := dst[key]
		if !present {
			dst[key] = val
			continue
		}
		switch e := existing.(type) {
		case []ValidationError:
			if v, ok := val.([]ValidationError); ok {
				dst[key] = append(e, v...)
				continue
			}
		case ValidationErrors:
			if v, ok := val.(ValidationErrors); ok {
				mergeErrors(e, v)
				continue
			}
		}
		dst[key] = val
	}
}
