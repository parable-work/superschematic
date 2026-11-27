package generator

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestDeriverLanguageRefusals: a Go API's deriver of a table whose rows
// keep to an audience is refused (D180), and one of a plain table builds
// (D146).
func TestDeriverLanguageRefusals(t *testing.T) {
	api := &ir.Schema{Name: "shop-cart"}
	tables := []apigen.DeriverInfo{{Name: "FillInvoices", Schemas: []apigen.DerivedSchema{{Name: "Invoice"}}}}
	if err := refuseGoHeldDerivers(api, tables); err != nil {
		t.Errorf("a Go API's deriver of a plain table: %v", err)
	}
	held := []apigen.DeriverInfo{{Name: "FillArticles", Schemas: []apigen.DerivedSchema{
		{Name: "Invoice", Type: &ir.TypeDef{Name: "Invoice"}},
		{Name: "Article", Type: &ir.TypeDef{Name: "Article", Audience: &ir.AudienceDef{Owner: "owner"}}},
	}}}
	err := refuseGoHeldDerivers(api, held)
	if err == nil || !strings.Contains(err.Error(), "shop-cart declares deriver FillArticles, which fills the derived fields of Article, and Article keeps its rows to an audience (@audience); a Go API's deriver does not fill such a table yet, since the Go behaviors' core reads its effective audience as one UUID and fails every pull (D146, D180)") {
		t.Errorf("a Go API's deriver of a held table = %v, want it refused", err)
	}
}
