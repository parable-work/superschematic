package memorystoregen

import "fmt"

// GenerateRust writes the store's Rust definitions into in.Dir: a crate
// with a const per declaration whose `for_` takes the parts in order and
// returns the runtime's Channel<T> or Key (D75).
//
// The Rust definitions are not built yet: this entry point returns
// ErrNotBuilt.
func GenerateRust(in Input) error {
	return fmt.Errorf("the Rust definitions of MemoryStore service %s are %w (D75)", in.Store, ErrNotBuilt)
}
