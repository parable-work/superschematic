// Package official lists the official extensions the installed binary
// links (docs/stack-model.md, section 13): every official target and DNS
// platform, and the provisioner that applies them. cmd/superschematic is
// the core with them; a binary that links another extension beside them,
// such as extensions/topcoat's superschematic-topcoat, takes the same
// list from here, so the two never differ in what else they link.
package official

import (
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	"github.com/parable-work/superschematic/extensions/gcp"
	"github.com/parable-work/superschematic/extensions/pulumi"
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
		pulumi.Extension{ProviderVersions: map[string]string{
			"gcp":              gcp.ProviderVersion,
			cloudflare.Package: cloudflare.ProviderVersion,
			// The generic connector's key node (D63), which the core
			// emits for an http edge between two providers.
			stack.GenericKeyPackage: stack.GenericKeyProviderVersion,
		}},
	}
}
