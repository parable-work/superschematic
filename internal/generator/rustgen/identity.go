package rustgen

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/identitydesc"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// identityDescriptor is schema's identity descriptor (D50) as the Rust raw
// string literal src/identity.rs holds, or "" when the schema has no User
// table.
func identityDescriptor(schema *ir.Schema) (string, error) {
	d, ok, err := identitydesc.Describe(schema)
	if err != nil || !ok {
		return "", err
	}
	text, err := d.JSON()
	if err != nil {
		return "", err
	}
	return rustutil.RawString(strings.TrimSuffix(string(text), "\n")), nil
}
