package memorystoregen

import "fmt"

// GenerateGo writes the store's Go definitions into in.Dir: a package
// named after the store (`shopcache`), with a value per declaration whose
// For takes the parts in order and returns the runtime's
// memorystore.Channel[T] or memorystore.Key (D75).
//
// The Go definitions are not built yet: this entry point returns
// ErrNotBuilt.
func GenerateGo(in Input) error {
	return fmt.Errorf("the Go definitions of MemoryStore service %s are %w (D75)", in.Store, ErrNotBuilt)
}
