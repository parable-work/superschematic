package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestAppsRefusedInIR: an API schema authored as IR is held to what @app
// takes (D78): extensions each named once and without a dot, a name no
// other app, job, worker, deriver, type or operation set takes, and a
// deployable name no other app's takes; and only an API declares an app.
func TestAppsRefusedInIR(t *testing.T) {
	schema := ir.NewSchema("stock", ir.SchemaKindAPI)
	schema.Types["StockView"] = &ir.TypeDef{Name: "StockView", Role: ir.RoleAPIView}
	schema.OperationSets = []*ir.OperationSet{{Name: "StockQueries"}}
	schema.Jobs = []*ir.Job{{Name: "CountStock"}}
	schema.Workers = []*ir.Worker{{Name: "Restock", Queue: "Low"}}
	schema.Derivers = []*ir.Deriver{{Name: "Describe"}}
	schema.Apps = []*ir.App{
		{Name: "Backoffice", Extensions: []string{"topcoat"}},
		{Name: "Backoffice"},
		{Name: "Twice", Extensions: []string{"topcoat", "topcoat"}},
		{Name: "Dotted", Extensions: []string{"topcoat.pages"}},
		{Name: "StockView"},
		{Name: "StockQueries"},
		{Name: "CountStock"},
		{Name: "Restock"},
		{Name: "Describe"},
		{Name: "backoffice"},
	}
	r := &Result{}
	checkApps(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"app Backoffice is declared twice",
		"app Twice: names extension topcoat twice",
		`app Dotted: names extension "topcoat.pages", and an extension's name has no dot`,
		"app StockView takes the name of a type of the schema; an app's class is no type, so give one of them another name",
		"app StockQueries takes the name of an operation set of the schema; give one of them another name",
		"app CountStock takes the name of a job of the schema, and both would be the deployable stock-count-stock; give one of them another name",
		"app Restock takes the name of a worker of the schema, and both would be the deployable stock-restock; give one of them another name",
		"app Describe takes the name of a deriver of the schema, and both would be the deployable stock-describe; give one of them another name",
		"apps Backoffice and backoffice would both be the deployable stock-backoffice; give one of them another name",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}

	db := ir.NewSchema("stock-db", ir.SchemaKindDB)
	db.Apps = []*ir.App{{Name: "Backoffice"}}
	r = &Result{}
	checkApps(db, r)
	if len(r.Errors) != 1 || r.Errors[0].Msg != "schema stock-db is a DB service and declares apps; only an API service declares one" {
		t.Errorf("errors = %v, want the DB schema's app refused", r.Errors)
	}
}
