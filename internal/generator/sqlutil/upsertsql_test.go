package sqlutil_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
)

// syncTables models fixture-sync-db's tables (D89).
func syncTables(t *testing.T) map[string]*TableModel {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-sync-db"))
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

// TestUpsertStatementsOfASync holds what both ORMs upsert a bank sync's
// line items with (D89): the read of the rows a key holds, inside the
// key's partial index; the assignments and comparisons of an upsert only
// if changed, a location compared as its text; and how many rows one
// statement writes.
func TestUpsertStatementsOfASync(t *testing.T) {
	line := syncTables(t)["LineItem"]
	if line == nil {
		t.Fatal("fixture-sync-db has no LineItem")
	}
	columns := `"id", "plaid_transaction_id", "account_id", "reference", "name", "amount", "posted_at", "pending", "location", "details", "tags", "created_at", "updated_at", "updated_by", "deleted_at"`
	for _, tc := range []struct {
		key, conflict, rows string
	}{
		{"id", "(id)", "SELECT " + columns + " FROM line_item WHERE (id) IN (%s)"},
		{"plaidTransactionId", "(plaid_transaction_id) WHERE (plaid_transaction_id IS NOT NULL) AND deleted_at IS NULL",
			"SELECT " + columns + " FROM line_item WHERE (plaid_transaction_id) IN (%s) AND ((plaid_transaction_id IS NOT NULL) AND deleted_at IS NULL)"},
		{"account,reference", "(account_id, reference) WHERE deleted_at IS NULL",
			"SELECT " + columns + " FROM line_item WHERE (account_id, reference) IN (%s) AND (deleted_at IS NULL)"},
	} {
		key := line.UniqueKeyNamed(tc.key)
		if key == nil {
			t.Fatalf("LineItem has no key %s", tc.key)
		}
		if key.Conflict != tc.conflict || key.Rows != tc.rows || key.Expression {
			t.Errorf("LineItem's key %s = %+v, want the conflict %q and the read %q", tc.key, key, tc.conflict, tc.rows)
		}
		if err := CheckFormat(tc.key, key.Rows); err != nil {
			t.Error(err)
		}
	}
	s := TableStatementsOf(line)
	if want := "INSERT INTO line_item (%s) VALUES %s ON CONFLICT %s DO UPDATE SET %s%s RETURNING " + columns + ", (xmax = 0) AS _inserted"; s.Upsert != want {
		t.Errorf("LineItem's upsert = %q, want %q", s.Upsert, want)
	}
	name := ColumnStatementsOf(line.Column("name"))
	if name.SetExcluded != `"name" = EXCLUDED."name"` || name.Compared != `line_item."name"` || name.CompareParam != "$%d" || name.CompareExcluded != `EXCLUDED."name"` {
		t.Errorf("LineItem.name's upsert parts = %+v", name)
	}
	location := ColumnStatementsOf(line.Column("location"))
	if location.Compared != `CAST(line_item.location AS POINT)::text` || location.CompareParam != "CAST($%d AS POINT)::text" || location.CompareExcluded != `CAST(EXCLUDED.location AS POINT)::text` {
		t.Errorf("LineItem.location's upsert parts = %+v, want a point compared as its text", location)
	}
	var inserted []string
	for _, c := range line.InsertColumns() {
		inserted = append(inserted, c.Name)
	}
	if want := "id plaid_transaction_id reference name amount posted_at pending location details tags updated_by deleted_at account_id"; strings.Join(inserted, " ") != want {
		t.Errorf("LineItem's insert columns = %s, want %s", strings.Join(inserted, " "), want)
	}
	if got, want := line.BatchRows(), (65535-2)/13; got != want {
		t.Errorf("LineItem's batch = %d rows, want %d", got, want)
	}
	// A time and a boolean filter by a set too.
	if !slices.Contains(FilterOps(line.Column("posted_at").Filter), FilterIn) || !slices.Contains(FilterOps(line.Column("pending").Filter), FilterIn) {
		t.Errorf("LineItem's posted_at and pending filter by %v and %v", FilterOps(line.Column("posted_at").Filter), FilterOps(line.Column("pending").Filter))
	}
	for _, c := range PostgresClauses().AllClauses() {
		if c.SQL == "" {
			t.Errorf("the clause %s is empty", c.Name)
		}
	}
}

// TestExpressionKeysCannotBeReadBack holds that a unique @index over an
// expression has no read of its rows (D89): an upsert cannot match a row to
// its input by the expression's columns.
func TestExpressionKeysCannotBeReadBack(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-ddl-db"))
	if err != nil {
		t.Fatal(err)
	}
	tables, err := TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if table.Type != "Category" {
			continue
		}
		key := table.UniqueKeyNamed("label")
		if key == nil || !key.Expression || key.Rows != "" || key.Conflict != "((lower(label)))" {
			t.Errorf("Category's key by lower(label) = %+v", key)
		}
		return
	}
	t.Fatal("fixture-ddl-db has no Category")
}

// TestNearestSettingsAndTransactions holds the settings nearest's options
// set and the statements a transaction of options and a nested one run
// (D89).
func TestNearestSettingsAndTransactions(t *testing.T) {
	var options, settings []string
	for _, s := range NearestSettingsOf() {
		options = append(options, s.Option)
		settings = append(settings, s.Setting)
		if err := CheckFormat(s.Setting, s.SQL); err != nil {
			t.Error(err)
		}
	}
	if want := "efSearch iterativeScan maxScanTuples probes iterativeScan maxProbes"; strings.Join(options, " ") != want {
		t.Errorf("nearest's options = %s, want %s", strings.Join(options, " "), want)
	}
	if want := "hnsw.ef_search hnsw.iterative_scan hnsw.max_scan_tuples ivfflat.probes ivfflat.iterative_scan ivfflat.max_probes"; strings.Join(settings, " ") != want {
		t.Errorf("nearest's settings = %s, want %s", strings.Join(settings, " "), want)
	}
	if s := NearestSettingsOf()[4]; slices.Contains(s.Values, IterativeScanStrict) {
		t.Errorf("ivfflat's iterative scan takes %v; it has no strict order", s.Values)
	}
	if got := NearestSettingsOf()[0].SQL; got != "set_config('hnsw.ef_search', $%d, true)" {
		t.Errorf("ef_search is set by %q", got)
	}
	for _, tc := range []struct {
		level    string
		readOnly bool
		want     string
	}{
		{"", false, "BEGIN"},
		{Serializable, false, "BEGIN ISOLATION LEVEL SERIALIZABLE"},
		{RepeatableRead, true, "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"},
		{"", true, "BEGIN READ ONLY"},
	} {
		got, err := BeginTransaction(tc.level, tc.readOnly)
		if err != nil || got != tc.want {
			t.Errorf("BeginTransaction(%q, %t) = %q, %v, want %q", tc.level, tc.readOnly, got, err, tc.want)
		}
	}
	if _, err := BeginTransaction("snapshot", false); err == nil {
		t.Error("BeginTransaction takes snapshot, which is no isolation level")
	}
	if SavepointName(2) != "sp_2" || Savepoint != "SAVEPOINT %s" {
		t.Errorf("a nested transaction's savepoint is %q, begun by %q", SavepointName(2), Savepoint)
	}
}
