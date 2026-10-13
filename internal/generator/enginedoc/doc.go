// Package enginedoc builds the engine document of a DB schema with engine
// types (D58): everything a server needs to put the schema's engine types
// into the engine, which keeps their instances. A type of a DB schema that
// composes a behavior is an engine type; sql and orm leave it out, and the
// engine document carries it as a schema-file document the engine defines
// and publishes. README.md in this directory is its contract.
//
// The enginedoc generator writes the document as engine/<schema>.json in
// the Go types module, and the types generator holds it as the constant
// EngineDocument in Go, engineDocument in TypeScript ("./engine") and
// ENGINE_DOCUMENT in Rust. All four are the same bytes, which Build and
// Document.JSON make once per build.
package enginedoc
