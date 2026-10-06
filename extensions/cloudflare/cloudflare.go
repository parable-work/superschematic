// Package cloudflare is the Cloudflare extension of the stack model
// (docs/stack-model.md, sections 6.8 and 6.9). In v1 it registers one DNS
// platform, `cloudflare`, which writes an environment's domain records
// into a Cloudflare zone, beside whatever target runs its compute: the gcp
// target's servers with their records on Cloudflare is the first mix of
// providers. Workers and D1 platforms come later, in this module.
//
// It registers through the public registry package alone, as any
// extension does (D10), in a Go module of its own (D1). The DNS platform
// is a pure function to resource graph nodes typed by the Pulumi
// `cloudflare` provider, whose schemas the extension pins and registers
// with the platform (package schemas), so resolution checks every node
// offline. The provisioner that applies them is `pulumi`, another
// extension, given ProviderVersion for the `cloudflare` package.
package cloudflare

import (
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/extensions/cloudflare/schemas"
)

// The names the extension registers.
const (
	// Name is the extension's name.
	Name = "cloudflare"

	// DNS writes an environment's records into a Cloudflare zone.
	DNS = "cloudflare"
)

// Package is the Pulumi package the extension's resource types belong to,
// the key of ProviderVersion in the pulumi provisioner's ProviderVersions.
const Package = "cloudflare"

// ProviderVersion is the pulumi-cloudflare release the resource types are
// pinned at (schemas/pulumi-cloudflare.json). The provisioner installs the
// provider at this version, so what resolution validated is what applies.
const ProviderVersion = "6.22.0"

// Extension is the Cloudflare extension.
type Extension struct{}

// Name is the extension's name.
func (Extension) Name() string { return Name }

// Register adds the Cloudflare DNS platform with the pinned schema of the
// record type it emits and the API token its provider reads.
func (Extension) Register(r *registry.Registry) error {
	types, err := schemas.ResourceTypes(ProviderVersion)
	if err != nil {
		return fmt.Errorf("cloudflare: %w", err)
	}
	return r.RegisterDNSPlatform(registry.DNSPlatformSpec{
		Name:          DNS,
		Extension:     Name,
		Values:        json.RawMessage(dnsValues),
		Lower:         lowerRecords,
		ResourceTypes: types,
		Credentials:   credentials,
	})
}
