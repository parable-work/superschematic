// Command superschematic is the installed binary: a distribution of the
// core and the official extensions (docs/stack-model.md, section 13). It
// reads .schema.{ts,json,yaml} service directories and generates code
// artifacts, as the core does, and it links every official target and the
// provisioner that applies them, so an engineer installs one binary and gets
// every official target.
//
// It is a Go module of its own, so the root module never depends on an
// extension and the Pulumi SDK stays out of it. The core with no extension
// linked is internal/cmd/superschematic-core in the root module; the tests
// that prove the core works alone run that one.
package main

import (
	"fmt"
	"os"

	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	"github.com/parable-work/superschematic/extensions/gcp"
	"github.com/parable-work/superschematic/extensions/pulumi"
)

func main() {
	if err := cli.New(cli.Config{Name: "superschematic"}, extensions()...).Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// extensions lists the official extensions the binary links.
//
// extensions/topcoat is not one of them: it writes a crate for a Rust web
// framework that is still before 1.0 (D44), and its own binary,
// extensions/topcoat/cmd/superschematic-topcoat, links it.
func extensions() []registry.Extension {
	return []registry.Extension{
		gcp.Extension{},
		cloudflare.Extension{},
		pulumi.Extension{ProviderVersions: map[string]string{
			"gcp":              gcp.ProviderVersion,
			cloudflare.Package: cloudflare.ProviderVersion,
		}},
	}
}
