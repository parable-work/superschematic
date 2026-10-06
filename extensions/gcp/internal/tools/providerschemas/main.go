// Command providerschemas writes the gcp target's pinned provider schemas
// (docs/stack-model.md, section 6.4) into schemas/, one file per type that
// schemas/pulumi-gcp.json lists, from the pulumi-gcp release it pins. It
// is package pintool over the gcp pin; that package says how.
//
// Run from extensions/gcp: go run ./internal/tools/providerschemas
// With -check the command exits 1 when a committed file differs from what
// it would write, which is how CI keeps the files in step with the pin.
// With -version it moves the pin to another release and records the new
// digests.
package main

import (
	"github.com/parable-work/superschematic/stack/providerschema/pintool"

	"github.com/parable-work/superschematic/extensions/gcp/schemas"
)

func main() { pintool.Main(schemas.PinFile) }
