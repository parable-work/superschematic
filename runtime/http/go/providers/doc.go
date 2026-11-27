// Package providers is the runtime extension points in Go (D159, D184):
// five interfaces a running app calls, each implemented by a provider
// package. A credential provider (CredentialProvider), a channel adapter
// (ChannelAdapter), a sandbox provider (SandboxProvider), a model provider
// (ModelProvider) and a file processor (FileProcessor).
//
// A Go provider is a Go module in the provider's directory, the one the
// API's config lists (providers): its go.mod names the module, and the
// package at the module's root exports
//
//	func Definition() providers.Definition
//
// which Define checks. The directory declares the provider once, in its
// package.json (D159), its Cargo.toml (D185) or, for a provider of Go
// alone, its superschematic.json ({"provider": {...}}), which the build
// reads and checks against the interface's contract (ir.RuntimeProvider);
// CheckManifest holds a definition to that declaration in the package's
// own test. A generated Go server imports the module of each provider its
// API lists and reads the providers its environment configures with From,
// over the slots the API's @providers names, into its Deps.
//
// Go has an ambient caller, the request's context (D180), so a provider
// runs within its own principal as the TypeScript runtime runs it: every
// call core makes into a provider From answers is made with a context
// that carries the provider's principal (kind extension, subject
// extension:<name>) and its role, through Options.Bind, which a generated
// server sets to bind the ORM's principal too. What a provider writes
// through an ORM opened with no user is then recorded as its own.
//
// The contracts are runtime/http/testdata/runtime_contracts.json, which
// ir.RuntimeContracts writes and the TypeScript and Rust runtimes read; a
// test of this package reads it too, and holds each Go interface's
// methods to its contract's operations.
package providers
