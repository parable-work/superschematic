package apigen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestTheUUIDTypeIsTheLeastByName: of two scalars whose leaf is UUID, the
// UUID type is the least by name, on any walk of the scalars, and one
// whose leaf is UUID wins over one that is only UUID-like (D94).
func TestTheUUIDTypeIsTheLeastByName(t *testing.T) {
	schema := ir.NewSchema("shop-api", ir.SchemaKindAPI)
	for _, name := range []string{"Identity.UUID", "Ledger.UUID", "Identity.UserId"} {
		schema.Scalars[name] = &ir.ScalarDef{Name: name, LanguagePrimitive: ir.LanguageString}
	}
	for range 50 {
		if got := resolveUUIDTypeExpr(schema); got != "types.IdentityUUID" {
			t.Fatalf("resolveUUIDTypeExpr = %q, want types.IdentityUUID", got)
		}
	}
}
