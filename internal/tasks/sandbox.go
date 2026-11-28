package tasks

import "errors"

// ErrSandboxUnsupported is Sandbox's answer on a platform it has no
// filter for.
var ErrSandboxUnsupported = errors.New("the plan's sandbox needs Linux on amd64 or arm64, with seccomp")

// Sandbox holds the process to the rules of an input resolver
// (registry.TaskResolverSpec), for the rest of its life: it starts no
// program, opens no socket, and opens, makes, renames or removes no file.
// What it holds open stays open, so a tree over git still reads blobs
// through the process it started, and the plan is still written to
// standard output.
//
// The rules are a resolver's own, and a resolver an extension registers
// is Go the binary links: a read of the working tree, a `cargo metadata`
// or a fetch would compile and run. With the sandbox on, each fails with
// "operation not permitted", so the plan fails and names it.
//
// It holds a resolver that slips to its rules. It is no defence against
// a binary built to break them: code the binary links runs before the
// sandbox too.
//
// The filter cannot be taken off, and it covers every thread, so a
// program calls Sandbox when a plan is all it has left to do: after it
// has read the task file and the commit's tree and started the process
// that reads blobs (Repository.Start).
func Sandbox() error {
	return sandbox()
}
