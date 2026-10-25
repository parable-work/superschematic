package sqlutil_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
)

const fixturesDir = "../../loader/tsreader/testdata/services"

// ledgerTables models fixture-ledger-db's tables.
func ledgerTables(t *testing.T) map[string]*TableModel {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-ledger-db"))
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

// TestTablesOfModelsALedger holds the model both ORMs read (D79): the
// columns in the DDL's order, a relation's foreign key named for its field,
// each column's filter kind, the sort fields, the unique keys with a
// partial index's predicate, and the relations that point at a table.
func TestTablesOfModelsALedger(t *testing.T) {
	tables := ledgerTables(t)
	if len(tables) != 2 || tables["Account"] == nil || tables["Entry"] == nil {
		t.Fatalf("tables = %v, want Account and Entry, the queue not among them", tables)
	}
	entry := tables["Entry"]
	var columns []string
	for _, c := range entry.Columns {
		columns = append(columns, c.Name)
	}
	if want := "id account_id transfer_id amount_cents booked_on kind memo external_id tags metadata reviewed_at created_at created_by updated_at updated_by deleted_at deleted_by"; strings.Join(columns, " ") != want {
		t.Errorf("Entry's columns = %s, want %s", strings.Join(columns, " "), want)
	}
	if !entry.SoftDelete || !entry.DeletedBy || !entry.Search || entry.Key != "id" {
		t.Errorf("Entry = %+v, want soft-deletable, searched, keyed by id", entry)
	}
	kinds := map[string]FilterKind{}
	for _, c := range entry.Columns {
		kinds[c.Name] = c.Filter
	}
	for column, want := range map[string]FilterKind{
		"id": FilterUUID, "account_id": FilterUUID, "amount_cents": FilterInt, "booked_on": FilterDate, "kind": FilterString,
		"memo": FilterString, "tags": FilterNone, "reviewed_at": FilterDateTime, "created_at": FilterDateTime,
		"created_by": FilterNone, "deleted_at": FilterNone,
	} {
		if kinds[column] != want {
			t.Errorf("Entry.%s filters as %q, want %q", column, kinds[column], want)
		}
	}
	if c := entry.Column("kind"); c.EnumDefault != "debit" || !c.Class.Enum {
		t.Errorf("Entry.kind = %+v, want the enum and its default debit", c)
	}
	var sorts []string
	for _, c := range entry.SortColumns() {
		sorts = append(sorts, c.Field)
	}
	if want := "id amountCents bookedOn createdAt updatedAt"; strings.Join(sorts, " ") != want {
		t.Errorf("Entry sorts by %s, want %s", strings.Join(sorts, " "), want)
	}
	if key := entry.UniqueKeyNamed("account,externalId"); key == nil || key.Conflict != "(account_id, external_id) WHERE deleted_at IS NULL" || key.Keep != "account_id = EXCLUDED.account_id" {
		t.Errorf("Entry's key by account and externalId = %+v", key)
	}
	account := tables["Account"]
	if key := account.UniqueKeyNamed("code"); key == nil || key.Conflict != "(code)" {
		t.Errorf("Account's key by code = %+v, want a full one: Account has no deletedAt", key)
	}
	var refs []string
	for _, r := range account.ReferencedBy {
		refs = append(refs, r.Filter)
	}
	if !slices.Equal(refs, []string{"ReferencedByEntryAccount", "ReferencedByEntryTransfer"}) || !account.ReferencedBy[0].SourceSoftDelete {
		t.Errorf("Account is referenced by %v", account.ReferencedBy)
	}
}

// TestTableStatementsOfALedger holds the statements both ORMs run on
// Postgres, as format strings of %s and %d alone.
func TestTableStatementsOfALedger(t *testing.T) {
	tables := ledgerTables(t)
	entry := TableStatementsOf(tables["Entry"])
	if err := entry.CheckFormats(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"getOneFields":   "SELECT %s FROM entry WHERE id = $1",
		"live":           " AND deleted_at IS NULL",
		"liveCondition":  "entry.deleted_at IS NULL",
		"findMany":       "SELECT %s FROM entry%s%s%s",
		"pageAfter":      "(%s, entry.id) %s ((SELECT %s FROM entry AS page_key WHERE page_key.id = %s), %s)",
		"insertMany":     "INSERT INTO entry (%s) VALUES %s RETURNING %s",
		"softDelete":     "UPDATE entry SET deleted_at = $1, deleted_by = $2 WHERE id = $3 AND deleted_at IS NULL",
		"softDeleteMany": "UPDATE entry SET deleted_at = $1, deleted_by = $2%s",
		"delete":         "DELETE FROM entry WHERE id = $1",
		"updatedBy":      "updated_by = $%d",
	} {
		var got string
		for _, st := range entry.AllStatements() {
			if st.Name == name {
				got = st.SQL
			}
		}
		if got != want {
			t.Errorf("Entry's %s = %q, want %q", name, got, want)
		}
	}
	if !strings.HasPrefix(entry.Upsert, "INSERT INTO entry (%s) VALUES (%s) ON CONFLICT %s DO UPDATE SET %s WHERE entry.deleted_at IS NULL RETURNING \"id\", \"account_id\"") {
		t.Errorf("Entry's upsert = %q", entry.Upsert)
	}
	account := TableStatementsOf(tables["Account"])
	if account.Live != "" || account.SoftDelete != "" || !strings.Contains(account.Upsert, "DO UPDATE SET %s RETURNING") {
		t.Errorf("Account, which has no deletedAt, has %+v", account)
	}
	c := ColumnStatementsOf(tables["Account"].Column("name"))
	if c.Qualified != `account."name"` || c.Set != `"name" = $%d` || c.Filter.In != `account."name" IN (%s)` || c.PageKey != `page_key."name"` {
		t.Errorf("Account.name's parts = %+v", c)
	}
	if got := SearchConditionOf(tables["Entry"]); got != `entry.search_text ILIKE $%d ESCAPE '\'` {
		t.Errorf("Entry's search = %q", got)
	}
	probe := ReferenceProbeOf(tables["Account"], tables["Account"].ReferencedBy[0])
	if probe.Probe != "SELECT 1 FROM entry WHERE entry.account_id = account.id" || probe.Live != " AND entry.deleted_at IS NULL" {
		t.Errorf("Account's probe = %+v", probe)
	}
}

// TestFilterOpsAndClauses holds the operations of each kind of filter and
// that every clause is a format both runtimes fill.
func TestFilterOpsAndClauses(t *testing.T) {
	if got := FilterOps(FilterInt); !slices.Equal(got, []string{"eq", "gt", "gte", "lt", "lte", "between", "in", "isNull"}) {
		t.Errorf("an integer's ops = %v", got)
	}
	if got := FilterOps(FilterNone); got != nil {
		t.Errorf("no filter's ops = %v", got)
	}
	for _, c := range PostgresClauses().AllClauses() {
		if err := CheckFormat(c.Name, c.SQL); err != nil {
			t.Error(err)
		}
	}
	if err := CheckFormat("bad", "LIKE '%x'"); err == nil {
		t.Error("a bare % is a verb the runtimes' fill refuses")
	}
}
