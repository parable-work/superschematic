// Package official lists the official extensions the installed binary
// links (docs/stack-model.md, section 13): every official target and DNS
// platform, the provisioner that applies them, the embedded target,
// which builds an environment as one binary and applies nothing (D116),
// and the resolvers of a task's common inputs, a Go module, a Rust crate,
// a JS package and an image (D203). cmd/superschematic is
// the core with them; a binary that links another extension beside them,
// such as extensions/topcoat's superschematic-topcoat, takes the same
// list from here, so the two never differ in what else they link.
package official

import (
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	"github.com/parable-work/superschematic/extensions/embedded"
	"github.com/parable-work/superschematic/extensions/gcp"
	"github.com/parable-work/superschematic/extensions/pulumi"
	"github.com/parable-work/superschematic/extensions/resolvers"
)

// Extensions lists the official extensions, in the order a binary passes
// them to cli.New.
//
// extensions/topcoat is not one of them: it writes a crate for a Rust web
// framework that is still before 1.0 (D44), and its own binary,
// extensions/topcoat/cmd/superschematic-topcoat, links it beside these.
func Extensions() []registry.Extension {
	return []registry.Extension{
		gcp.Extension{},
		cloudflare.Extension{},
		embedded.Extension{},
		resolvers.Extension{},
		pulumi.Extension{ProviderVersions: map[string]string{
			"gcp":              gcp.ProviderVersion,
			cloudflare.Package: cloudflare.ProviderVersion,
			// The generic connector's key node (D63), which the core
			// emits for an http edge between two providers.
			stack.GenericKeyPackage: stack.GenericKeyProviderVersion,
		}},
	}
}
