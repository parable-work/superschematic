package ptr

// To returns a pointer to v. Generated code and the runtime tests use it to
// build optional fields inline.
func To[T any](v T) *T { return &v }
