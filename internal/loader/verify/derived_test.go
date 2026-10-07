package verify

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestEnvVarsFieldsThatCollideWithDerivedFields: an @envVars field named
// after a derived field, or after one of its variables, is refused; one
// that only shares a prefix without the underscore is not. The naming
// file's templates name the derived fields.
func TestEnvVarsFieldsThatCollideWithDerivedFields(t *testing.T) {
	schema := ir.NewSchema("shop-orders", ir.SchemaKindAPI)
	schema.AuthDB = "shop-db"
	schema.Calls = []ir.ServiceRef{{Name: "shop-api", Kind: ir.SchemaKindAPI}}
	schema.Types["OrdersConfig"] = &ir.TypeDef{Name: "OrdersConfig", EnvVars: true, Fields: []*ir.FieldDef{
		{Name: "SHOP_DB_DATABASE"},
		{Name: "SHOP_API_SERVICE_URL"},
		{Name: "SHOP_DB_DATABASES"},
		{Name: "LOG_LEVEL"},
	}}
	r := &Result{}
	checkDerivedFields(schema, Input{}, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"@envVars field SHOP_DB_DATABASE of OrdersConfig collides with SHOP_DB_DATABASE, the config field the authDb shop-db derives; a stack's platform sets it, so rename the setting",
		"@envVars field SHOP_API_SERVICE_URL of OrdersConfig collides with SHOP_API_SERVICE, the config field the calls shop-api derives; a stack's platform sets it, so rename the setting",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors = %q, want %q", got, want)
	}

	names := naming.Default()
	names.DerivedFields.Database = "{SERVICE}_DSN"
	r = &Result{}
	checkDerivedFields(schema, Input{Naming: names}, r)
	if len(r.Errors) != 1 || !strings.HasPrefix(r.Errors[0].Msg, "@envVars field SHOP_API_SERVICE_URL of") {
		t.Errorf("with database = {SERVICE}_DSN: %+v", r.Errors)
	}

	stock := ir.NewSchema("stock-api", ir.SchemaKindAPI)
	stock.OperationSets = []*ir.OperationSet{{Name: "Stock", Operations: []*ir.FieldDef{
		{Name: "reindex", ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}},
	}}}
	stock.Types["StockConfig"] = &ir.TypeDef{Name: "StockConfig", EnvVars: true, Fields: []*ir.FieldDef{
		{Name: "STOCK_API_CALLERS_LIMIT"}, {Name: "STOCK_API_CALLER"},
	}}
	r = &Result{}
	checkDerivedFields(stock, Input{}, r)
	if len(r.Errors) != 1 || r.Errors[0].Msg != "@envVars field STOCK_API_CALLERS_LIMIT of StockConfig collides with STOCK_API_CALLERS, the callers field the http edges to stock-api derive; a stack's platform sets it, so rename the setting" {
		t.Errorf("an API with a service clause: %+v", r.Errors)
	}
	stock.OperationSets = nil
	r = &Result{}
	checkDerivedFields(stock, Input{}, r)
	if len(r.Errors) != 0 {
		t.Errorf("an API without a service clause has no callers field: %+v", r.Errors)
	}

	general := ir.NewSchema("shop-config", ir.SchemaKindGeneral)
	general.Dependencies = []ir.ServiceRef{{Name: "shop-db", Kind: ir.SchemaKindDB}}
	general.Types["ShopConfig"] = &ir.TypeDef{Name: "ShopConfig", EnvVars: true, Fields: []*ir.FieldDef{{Name: "SHOP_DB_DATABASE"}}}
	r = &Result{}
	checkDerivedFields(general, Input{}, r)
	if len(r.Errors) != 0 {
		t.Errorf("a General schema derives no fields: %+v", r.Errors)
	}
}
