//go:build !unix

package testpaths

// lockDir is a no-op where flock is not available: builds there are not
// serialized across test processes.
func lockDir(string) (func(), error) { return func() {}, nil }
