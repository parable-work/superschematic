// Package schemas holds the pinned provider schema of the core's generic
// connector (docs/stack-model.md, sections 6.2 and 6.4, D60): the Pulumi
// `tls` provider's PrivateKey, the key pair the connector generates for
// each edge, taken from the pulumi-tls release pulumi-tls.json pins, in
// the format of package providerschema. The file records the type's input
// properties and its Terraform name.
//
// The root module's internal/tools/providerschemas writes the file, and
// its -check mode fails when it drifts from the pinned provider. The
// generic connector registers it as the JSON Schema of its type's
// properties, so resolution validates every key node offline in any
// environment, whatever its target.
package schemas

import (
	"embed"
	"encoding/json"

	"github.com/parable-work/superschematic/stack/providerschema"
)

//go:embed *.json
var files embed.FS

// PinFile is the file that pins the provider: its version, the upstream
// files the schema comes from with their digests, and the types to pin.
const PinFile = "pulumi-tls.json"

// Dir is the directory, from the root of the repository, that holds the
// pin and the pinned schema: where the root's providerschemas command
// writes them.
const Dir = "internal/stack/generic/schemas"

// ReadPin returns the embedded pin.
func ReadPin() (*providerschema.Pin, error) { return providerschema.ReadPin(files, PinFile) }

// ResourceTypes returns the JSON Schema of every pinned type, keyed by
// token. It refuses a pin at another version than version.
func ResourceTypes(version string) (map[string]json.RawMessage, error) {
	return providerschema.ResourceTypes(files, PinFile, version)
}
