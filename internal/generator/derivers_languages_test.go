package generator

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestDeriverLanguageRefusals: a Go API's deriver of tables builds and one
// of engine types is refused (D146), and a Rust API's derivers fill one
// kind, engine types or tables, since deps.rs types the items of one loop.
func TestDeriverLanguageRefusals(t *testing.T) {
	api := &ir.Schema{Name: "shop-cart"}
	ticket := apigen.DerivedSchema{Name: "Ticket"}
	engine := &apigen.EngineInfo{Database: "shop-db", Derivers: []apigen.DeriverInfo{{Name: "TriageTickets", Schemas: []apigen.DerivedSchema{ticket}}}}
	tables := []apigen.DeriverInfo{{Name: "FillInvoices", Tables: true, Schemas: []apigen.DerivedSchema{{Name: "Invoice"}}}}

	if err := refuseGoEngineDerivers(api, nil); err != nil {
		t.Errorf("a Go API with no deriver of engine types: %v", err)
	}
	if err := refuseGoEngineDerivers(api, &apigen.EngineInfo{Database: "shop-db"}); err != nil {
		t.Errorf("a Go API over an engine database with no deriver: %v", err)
	}
	err := refuseGoEngineDerivers(api, engine)
	if err == nil || !strings.Contains(err.Error(), "shop-cart declares deriver TriageTickets, which fills the derived fields of Ticket, engine types of its database, and it is a Go API; a deriver of engine types runs on a TypeScript or Rust API, and a Go API's deriver fills tables alone (D146)") {
		t.Errorf("a Go API's deriver of engine types = %v, want it refused", err)
	}

	if err := refuseRustMixedDerivers(api, nil, tables); err != nil {
		t.Errorf("a Rust API's derivers of tables: %v", err)
	}
	if err := refuseRustMixedDerivers(api, engine, nil); err != nil {
		t.Errorf("a Rust API's derivers of engine types: %v", err)
	}
	err = refuseRustMixedDerivers(api, engine, tables)
	if err == nil || !strings.Contains(err.Error(), "shop-cart declares deriver TriageTickets, which fills engine types, and deriver FillInvoices, which fills tables, and it is a Rust API, whose derivers fill one kind or the other") {
		t.Errorf("a Rust API's derivers of both kinds = %v, want them refused", err)
	}
}
