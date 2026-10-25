package memorystoregen

import "fmt"

// GenerateTypeScript writes the store's TypeScript definitions into
// in.Dir: a package with a const per declaration whose `for` takes the
// parts in order and returns the runtime's Channel<T> or Key (D75).
//
// The TypeScript definitions are not built yet: this entry point returns
// ErrNotBuilt.
func GenerateTypeScript(in Input) error {
	return fmt.Errorf("the TypeScript definitions of MemoryStore service %s are %w (D75)", in.Store, ErrNotBuilt)
}
