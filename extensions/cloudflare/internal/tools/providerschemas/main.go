// Command providerschemas writes the Cloudflare extension's pinned
// provider schemas (docs/stack-model.md, section 6.4) into schemas/, one
// file per type that schemas/pulumi-cloudflare.json lists, from the
// pulumi-cloudflare release it pins. It is package pintool over the
// cloudflare pin; that package says how.
//
// Run from extensions/cloudflare: go run ./internal/tools/providerschemas
// With -check the command exits 1 when a committed file differs from what
// it would write, which is how CI keeps the files in step with the pin.
// With -version it moves the pin to another release and records the new
// digests.
package main

import (
	"github.com/parable-work/superschematic/stack/providerschema/pintool"

	"github.com/parable-work/superschematic/extensions/cloudflare/schemas"
)

func main() { pintool.Main(schemas.PinFile) }
