// Command providerschemas writes the pinned provider schema of the core's
// generic connector (docs/stack-model.md, sections 6.2 and 6.4, D63) into
// internal/stack/generic/schemas, one file per type its pulumi-tls.json
// lists, from the pulumi-tls release it pins. It is package pintool over
// that pin; that package says how.
//
// Run from the repository's root: go run ./internal/tools/providerschemas
// With -check the command exits 1 when a committed file differs from what
// it would write, which is how CI keeps the files in step with the pin.
// With -version it moves the pin to another release and records the new
// digests; generic.ProviderVersion must then move with it.
package main

import (
	"github.com/parable-work/superschematic/stack/providerschema/pintool"

	"github.com/parable-work/superschematic/internal/stack/generic/schemas"
)

func main() { pintool.MainIn(schemas.Dir, schemas.PinFile) }
