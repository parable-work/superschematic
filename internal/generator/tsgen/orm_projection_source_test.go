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

// TestASettingBoundProjectionHasARepository type-checks fixture-projection's
// package (D90): AppPreference's view binds session settings and collapses
// by slotKey, its key, and its repository reads an enum, a JSON value, a
// list and nullable columns as a table's does.
func TestASettingBoundProjectionHasARepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-projection"))
	if err != nil {
		t.Fatal(err)
	}
	check := `import type { AppPreference, AppPreferenceSortField, FixtureProjectionOrm } from './orm';

declare const orm: FixtureProjectionOrm;

export async function preferences(): Promise<AppPreference[]> {
  return orm.transaction(async tx => {
    await tx.exec("SELECT set_config('app.account_id', $1, true)", ['0f8b6c4e-1b2a-4c3d-9e8f-123456789abc']);
    const sort: AppPreferenceSortField = 'slotKey';
    const page = await tx.appPreference.findPage({ scope: { in: ['user' as never] }, branchName: { isNull: true } }, { sort: { field: sort } });
    return page.rows;
  });
}
`
	buildTSPackages(t, []tsPackageCase{{name: "fixture-projection", schema: schema, extra: map[string]string{"projection.check.ts": check}}})
}
