// Command superschematic is the installed binary: a distribution of the
// core and the official extensions (docs/stack-model.md, section 13). It
// reads .schema.{ts,json,yaml} service directories and generates code
// artifacts, as the core does, and it links every official target and DNS
// platform and the provisioner that applies them, so an engineer installs one
// binary and gets every official target.
//
// It is a Go module of its own, so the root module never depends on an
// extension and the Pulumi SDK stays out of it. The core with no extension
// linked is internal/cmd/superschematic-core in the root module; the tests
// that prove the core works alone run that one.
package main

import (
	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/cmd/superschematic/official"
)

func main() {
	if err := cli.New(cli.Config{Name: "superschematic"}, extensions()...).Execute(); err != nil {
		cli.Exit(err)
	}
}

// extensions lists the official extensions the binary links
// (official.Extensions).
func extensions() []registry.Extension {
	return official.Extensions()
}
