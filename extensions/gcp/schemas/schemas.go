// Package schemas holds the gcp target's pinned provider schemas
// (docs/stack-model.md, section 6.4): one file per Pulumi `gcp` resource
// type the target's platforms, connectors and DNS platform emit, taken from
// the pulumi-gcp provider at the version pulumi-gcp.json pins, in the
// format of package providerschema. Each file records the type's input
// properties, the object types they reference, and the type's Terraform
// name and property renames from the bridged provider's published mapping.
//
// internal/tools/providerschemas writes the files, and its -check mode
// fails when they drift from the pinned provider. The gcp target registers
// each as the JSON Schema of its type's properties, so resolution
// validates every node offline.
package schemas

import (
	"embed"
	"encoding/json"

	"github.com/parable-work/superschematic/stack/providerschema"
)

//go:embed *.json
var files embed.FS

// PinFile is the file that pins the provider: its version, the upstream
// files the schemas come from with their digests, and the types to pin.
const PinFile = "pulumi-gcp.json"

type (
	// Pin is the content of PinFile.
	Pin = providerschema.Pin

	// Schema is one pinned resource type.
	Schema = providerschema.Schema
)

// ReadPin returns the embedded pin.
func ReadPin() (*Pin, error) { return providerschema.ReadPin(files, PinFile) }

// FileName returns the name of the file that pins a type
// (`cloudrunv2.service.Service.json`).
func FileName(token string) string { return providerschema.FileName(token) }

// Load returns the embedded schema of a type.
func Load(token string) (*Schema, error) { return providerschema.Load(files, token) }

// ResourceTypes returns the JSON Schema of every pinned type, keyed by
// token. It refuses a pin at another version than version.
func ResourceTypes(version string) (map[string]json.RawMessage, error) {
	return providerschema.ResourceTypes(files, PinFile, version)
}
