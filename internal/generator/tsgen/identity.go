package tsgen

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/identitydesc"
	ir "github.com/parable-work/superschematic/ir"
)

// identityDescriptor is schema's identity descriptor (D50) as the object
// literal identity.ts exports, or "" when the schema has no User table.
func identityDescriptor(schema *ir.Schema) (string, error) {
	d, ok, err := identitydesc.Describe(schema)
	if err != nil || !ok {
		return "", err
	}
	text, err := d.JSON()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(text), "\n"), nil
}
