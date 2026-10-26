package tsgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAPIViewSourcedFromAProjection loads fixture-finances-api, whose
// TransactionView is @source(TransactionListRow), a projection of
// fixture-finances-db (D90): verification holds each of the view's fields
// to the projection's column of its name and type, as it holds a view of a
// table to the table's fields, and the API's types package and the
// database's, whose orm.ts reads the projection, type-check together.
func TestAPIViewSourcedFromAProjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-finances-db"))
	if err != nil {
		t.Fatal(err)
	}
	api, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-finances-api"))
	if err != nil {
		t.Fatal(err)
	}
	view := api.Types["TransactionView"]
	if view == nil || view.Source == nil || view.Source.Target != "fixture-finances-db.TransactionListRow" {
		t.Fatalf("TransactionView = %+v, want @source of the projection", view)
	}
	if source := db.Types["TransactionListRow"]; source == nil || source.Role != ir.RoleProjection {
		t.Fatalf("TransactionListRow = %+v, want the projection", source)
	}
	buildTSPackages(t, []tsPackageCase{
		{name: "fixture-finances-db", schema: db},
		{name: "fixture-finances-api", schema: api, deps: map[string]*ir.Schema{"fixture-finances-db": db}},
	})
}
