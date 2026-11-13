package sqlutil_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// fencedTables models fixture-fenced-db's tables.
func fencedTables(t *testing.T) map[string]*TableModel {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-fenced-db"))
	if err != nil {
		t.Fatal(err)
	}
	tables, err := TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*TableModel{}
	for i := range tables {
		out[tables[i].Type] = &tables[i]
	}
	return out
}

// TestTablesOfModelsFences holds the fences every language's repositories
// advance (D118): each fence's column, the columns it covers in the order
// declared, a relation as its key's column, and the assignment that
// advances it, which reads the stored value by the table's name. A table
// with no fence has none.
func TestTablesOfModelsFences(t *testing.T) {
	tables := fencedTables(t)
	for name, want := range map[string][]FenceModel{
		"Courier": nil,
		"Parcel":  {{Field: "version", Column: "version", Bump: "version = parcel.version + 1"}},
		"Delivery": {
			{Field: "planVersion", Column: "plan_version", Covers: []string{"address", "courier_id"}, Bump: "plan_version = delivery.plan_version + 1"},
			{Field: "runVersion", Column: "run_version", Covers: []string{"attempts", "proof"}, Bump: "run_version = delivery.run_version + 1"},
		},
		// name is quoted wherever a statement names it (QuoteIdentifier).
		"Depot": {{Field: "revision", Column: "revision", Covers: []string{`"name"`, "deleted_at"}, Bump: "revision = depot.revision + 1"}},
	} {
		table := tables[name]
		if table == nil {
			t.Fatalf("fixture-fenced-db has no table %s", name)
		}
		if len(table.Fences) != len(want) {
			t.Fatalf("%s's fences = %+v, want %+v", name, table.Fences, want)
		}
		for i, fence := range table.Fences {
			if fence.Field != want[i].Field || fence.Column != want[i].Column || fence.Bump != want[i].Bump || !slices.Equal(fence.Covers, want[i].Covers) {
				t.Errorf("%s's fence %d = %+v, want %+v", name, i, fence, want[i])
			}
			if fence.CoversRow() != (len(want[i].Covers) == 0) {
				t.Errorf("%s's fence on %s covers the row: %t", name, fence.Field, fence.CoversRow())
			}
			if !Assigns(fence.Bump, fence.Column) {
				t.Errorf("%s's bump %q does not read as an assignment of %s", name, fence.Bump, fence.Column)
			}
		}
	}
}

// TestFenceBumpsOfAWrite holds the rule each language's repositories write
// (D118): a write advances each fence that covers a column it sets, once,
// and a fence with no covers on any column; a write of uncovered columns
// alone advances none; and a write that sets a fence's own column leaves
// that fence to its caller.
func TestFenceBumpsOfAWrite(t *testing.T) {
	tables := fencedTables(t)
	const plan, run = "plan_version = delivery.plan_version + 1", "run_version = delivery.run_version + 1"
	for _, tc := range []struct {
		table   string
		columns []string
		want    []string
	}{
		{"Delivery", []string{"address"}, []string{plan}},
		{"Delivery", []string{"courier_id", "updated_at"}, []string{plan}},
		{"Delivery", []string{"address", "courier_id"}, []string{plan}},
		{"Delivery", []string{"proof"}, []string{run}},
		{"Delivery", []string{"attempts", "address", "note"}, []string{plan, run}},
		{"Delivery", []string{"note", "updated_at"}, nil},
		{"Delivery", nil, nil},
		// The caller drives planVersion, and runVersion is still advanced.
		{"Delivery", []string{"address", "plan_version", "attempts"}, []string{run}},
		{"Delivery", []string{"plan_version", "run_version", "address", "proof"}, nil},
		// A fence on its own is no covered column of the other.
		{"Delivery", []string{"plan_version"}, nil},
		{"Parcel", []string{"note"}, []string{"version = parcel.version + 1"}},
		{"Parcel", []string{"weight_grams", "tracking_code"}, []string{"version = parcel.version + 1"}},
		{"Parcel", []string{"note", "version"}, nil},
		{"Parcel", nil, nil},
		{"Depot", []string{"capacity"}, nil},
		{"Depot", []string{`"name"`, "capacity"}, []string{"revision = depot.revision + 1"}},
		{"Depot", []string{"deleted_at"}, []string{"revision = depot.revision + 1"}},
		{"Courier", []string{"name"}, nil},
	} {
		if got := tables[tc.table].FenceBumps(tc.columns...); !slices.Equal(got, tc.want) {
			t.Errorf("%s: a write of %v advances %v, want %v", tc.table, tc.columns, got, tc.want)
		}
	}
}

// TestAssignmentsBeginWithTheirColumn holds what a repository reads the
// columns a write sets from (D118): every assignment sqlutil writes begins
// with its column and AssignmentInfix, on Postgres and on SQLite, so
// Assigns tells which column one sets, and a column whose name another's
// begins with is not taken for it.
func TestAssignmentsBeginWithTheirColumn(t *testing.T) {
	for dialect, c := range map[string]Clauses{"postgres": PostgresClauses(), "sqlite": SQLiteClauses()} {
		for name, clause := range map[string]string{"set": c.Set, "setNull": c.SetNull, "setExcluded": c.SetExcluded} {
			if !strings.HasPrefix(clause, "%s"+AssignmentInfix) {
				t.Errorf("%s's %s = %q, which does not begin with its column and %q", dialect, name, clause, AssignmentInfix)
			}
		}
	}
	tables := fencedTables(t)
	delivery := tables["Delivery"]
	for _, statements := range []TableStatements{TableStatementsOf(delivery), SQLiteTableStatementsOf(delivery)} {
		if !Assigns(statements.UpdatedAt, "updated_at") {
			t.Errorf("Delivery's updatedAt = %q, which does not read as an assignment of updated_at", statements.UpdatedAt)
		}
	}
	for _, key := range tables["Parcel"].UniqueKeys {
		if !Assigns(key.Keep, key.Columns[0]) {
			t.Errorf("Parcel's key %s keeps with %q, which does not read as an assignment of %s", key.Name, key.Keep, key.Columns[0])
		}
	}
	for _, tc := range []struct {
		assignment, column string
		want               bool
	}{
		{"address = $1", "address", true},
		{"address = NULL", "address", true},
		{"address = EXCLUDED.address", "address", true},
		{"address_line = $1", "address", false},
		{`"order" = $1`, `"order"`, true},
		{`"order" = $1`, "order", false},
		{"plan_version = delivery.plan_version + 1", "plan_version", true},
		{"note = $1", "plan_version", false},
	} {
		if got := Assigns(tc.assignment, tc.column); got != tc.want {
			t.Errorf("Assigns(%q, %q) = %t, want %t", tc.assignment, tc.column, got, tc.want)
		}
	}
}

// TestTableStatementsCarryTheFixedBumps holds the statements whose columns
// are fixed (D118), on Postgres and on SQLite: Depot's soft deletes stamp
// deleted_at, which its fence covers, so each carries the bump; the
// statements a repository fills carry none, since their assignments say
// what they advance; and a table with no fence is written as before.
func TestTableStatementsCarryTheFixedBumps(t *testing.T) {
	tables := fencedTables(t)
	depot := tables["Depot"]
	for dialect, tc := range map[string]struct {
		statements TableStatements
		param      string
	}{
		"postgres": {TableStatementsOf(depot), "$"},
		"sqlite":   {SQLiteTableStatementsOf(depot), "?"},
	} {
		s, p := tc.statements, tc.param
		if err := s.CheckFormats(); err != nil {
			t.Fatal(err)
		}
		for name, check := range map[string][2]string{
			"softDelete":          {s.SoftDelete, "UPDATE depot SET deleted_at = " + p + "1, revision = depot.revision + 1 WHERE id = " + p + "2 AND deleted_at IS NULL"},
			"softDeleteIfVersion": {s.SoftDeleteIfVersion, "UPDATE depot SET deleted_at = " + p + "1, revision = depot.revision + 1 WHERE id = " + p + "2 AND _version = " + p + "3 AND deleted_at IS NULL"},
			"softDeleteMany":      {s.SoftDeleteMany, "UPDATE depot SET deleted_at = " + p + "1, revision = depot.revision + 1%s"},
			"update":              {s.Update, "UPDATE depot SET %s WHERE id = " + p + "%d"},
			"updateMany":          {s.UpdateMany, "UPDATE depot SET %s%s"},
		} {
			if check[0] != check[1] {
				t.Errorf("Depot's %s on %s = %q, want %q", name, dialect, check[0], check[1])
			}
		}
		if !strings.Contains(s.Upsert, " DO UPDATE SET %s%s RETURNING ") {
			t.Errorf("Depot's upsert on %s = %q, want its assignments left to the repository", dialect, s.Upsert)
		}
	}
	// A table whose fence covers neither stamp is deleted as before, and
	// one whose fence covers the row advances it.
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-ledger-db"))
	if err != nil {
		t.Fatal(err)
	}
	entry := schema.Types["Entry"]
	plain := modelOf(t, schema, "Entry")
	entry.Fences = []*ir.Fence{{Field: "amountCents", Covers: []string{"memo"}}}
	if got, want := TableStatementsOf(modelOf(t, schema, "Entry")), TableStatementsOf(plain); got.SoftDelete != want.SoftDelete || got.SoftDeleteMany != want.SoftDeleteMany {
		t.Errorf("Entry's soft delete with a fence over memo = %q, want it as with none: %q", got.SoftDelete, want.SoftDelete)
	}
	entry.Fences = []*ir.Fence{{Field: "amountCents", Covers: []string{"deletedBy"}}}
	if got, want := TableStatementsOf(modelOf(t, schema, "Entry")).SoftDelete, "UPDATE entry SET deleted_at = $1, deleted_by = $2, amount_cents = entry.amount_cents + 1 WHERE id = $3 AND deleted_at IS NULL"; got != want {
		t.Errorf("Entry's soft delete with a fence over deletedBy = %q, want %q", got, want)
	}
	entry.Fences = []*ir.Fence{{Field: "amountCents"}}
	if got, want := SQLiteTableStatementsOf(modelOf(t, schema, "Entry")).SoftDeleteMany, "UPDATE entry SET deleted_at = ?1, deleted_by = ?2, amount_cents = entry.amount_cents + 1%s"; got != want {
		t.Errorf("Entry's soft delete of many with a fence over the row = %q, want %q", got, want)
	}
}

// TestResealAdvancesTheFenceOverAnEncryptedColumn holds Reseal (D85) on a
// table with a fence (D118): it sets every encrypted column, so it carries
// the bump of a fence that covers one, or the row, once, and of no fence
// that covers other columns alone.
func TestResealAdvancesTheFenceOverAnEncryptedColumn(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-vault-db"))
	if err != nil {
		t.Fatal(err)
	}
	item := schema.Types["PlaidItem"]
	item.Fields = append(item.Fields, &ir.FieldDef{Name: "rotation", TypeRef: ir.TypeRef{Name: "number"}, Required: true})
	const sets = `UPDATE plaid_item SET access_token = $1, webhook_secret = $2`
	const where = ` WHERE id = $3 AND access_token IS NOT DISTINCT FROM $4 AND webhook_secret IS NOT DISTINCT FROM $5`
	for _, tc := range []struct {
		name   string
		fences []*ir.Fence
		want   string
	}{
		{"no fence", nil, sets + where},
		{"a fence over both encrypted columns", []*ir.Fence{{Field: "rotation", Covers: []string{"accessToken", "webhookSecret"}}}, sets + ", rotation = plaid_item.rotation + 1" + where},
		{"a fence over one of them", []*ir.Fence{{Field: "rotation", Covers: []string{"webhookSecret", "institution"}}}, sets + ", rotation = plaid_item.rotation + 1" + where},
		{"a fence over the row", []*ir.Fence{{Field: "rotation"}}, sets + ", rotation = plaid_item.rotation + 1" + where},
		{"a fence over another column", []*ir.Fence{{Field: "rotation", Covers: []string{"institution"}}}, sets + where},
	} {
		item.Fences = tc.fences
		table := modelOf(t, schema, "PlaidItem")
		if got := TableStatementsOf(table).Reseal; got != tc.want {
			t.Errorf("%s: reseal = %q, want %q", tc.name, got, tc.want)
		}
		if got, want := SQLiteTableStatementsOf(table).Reseal, strings.ReplaceAll(tc.want, "$", "?"); got != want {
			t.Errorf("%s: reseal on SQLite = %q, want %q", tc.name, got, want)
		}
	}
}

// TestFencesOfRefusesAFieldWithNoColumn holds the model to the schema: a
// fence, or a covered field, that stores no column of the table fails the
// build, where a statement would name a column the table lacks.
func TestFencesOfRefusesAFieldWithNoColumn(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-ledger-db"))
	if err != nil {
		t.Fatal(err)
	}
	entry := schema.Types["Entry"]
	for _, tc := range []struct {
		fence *ir.Fence
		want  string
	}{
		{&ir.Fence{Field: "sequence"}, "@fenced on Entry.sequence: the field stores no column of table entry"},
		{&ir.Fence{Field: "amountCents", Covers: []string{"memo", "postedBy"}}, "@fenced on Entry.amountCents: covers lists postedBy, which stores no column of table entry"},
	} {
		entry.Fences = []*ir.Fence{tc.fence}
		if _, err := TablesOf(schema, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("TablesOf with %+v = %v, want an error holding %q", tc.fence, err, tc.want)
		}
	}
}

// modelOf models the table of the type named name in schema.
func modelOf(t *testing.T, schema *ir.Schema, name string) *TableModel {
	t.Helper()
	tables, err := TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tables {
		if tables[i].Type == name {
			return &tables[i]
		}
	}
	t.Fatalf("%s has no table %s", schema.Name, name)
	return nil
}
