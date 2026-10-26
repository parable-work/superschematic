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
		"memo": FilterString, "tags": FilterNone, "metadata": FilterJSON, "reviewed_at": FilterDateTime, "created_at": FilterDateTime,
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
	// A Generic.JSON column's: JSONB equality and IS NULL, no ILIKE (D79).
	if got := FilterOps(FilterJSON); !slices.Equal(got, []string{"eq", "in", "isNull"}) {
		t.Errorf("a JSON column's ops = %v", got)
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

// TestTablesOfModelsVectorsDecimalsAndSealedNames holds fixture-vectors-db's
// model (D84, D85): a decimal filters as a number, an Encrypted<T> field
// and a vector have no filter, and Chunk's table, vector column and
// soft-delete column are the names @table and @column give them, quoted,
// through to the nearest statement and its live condition.
func TestTablesOfModelsVectorsDecimalsAndSealedNames(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-vectors-db"))
	if err != nil {
		t.Fatal(err)
	}
	models, err := TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string]*TableModel{}
	for i := range models {
		tables[models[i].Type] = &models[i]
	}
	line, chunk := tables["LedgerLine"], tables["Chunk"]
	if line == nil || chunk == nil {
		t.Fatalf("tables = %v, want LedgerLine and Chunk", tables)
	}
	for _, tc := range []struct {
		table  *TableModel
		column string
		want   FilterKind
	}{
		{line, "rate", FilterDecimal}, {line, "confidence", FilterDecimal}, {line, "quantity", FilterDecimal}, {line, "amount", FilterInt},
		{chunk, "note", FilterNone}, {chunk, "chunkVector", FilterNone}, {chunk, "chunkText", FilterString},
	} {
		c := tc.table.Column(tc.column)
		if c == nil {
			t.Fatalf("%s has no column %s", tc.table.Type, tc.column)
		}
		if c.Filter != tc.want {
			t.Errorf("%s.%s filters as %q, want %q", tc.table.Type, tc.column, c.Filter, tc.want)
		}
	}
	if note := chunk.Column("note"); !note.Encrypted || note.Type != "TEXT" {
		t.Errorf("Chunk.note = %+v, want an encrypted TEXT column", note)
	}
	if ops := FilterOps(FilterDecimal); !slices.Equal(ops, []string{"eq", "gt", "gte", "lt", "lte", "between", "in", "isNull"}) {
		t.Errorf("a decimal's filter = %v", ops)
	}
	vectors := chunk.VectorColumns()
	if len(vectors) != 1 || vectors[0].Qualified != `"NoteChunk"."chunkVector"` || vectors[0].Type != "vector(3)" {
		t.Fatalf("Chunk's vector columns = %+v", vectors)
	}
	s := TableStatementsOf(chunk)
	if s.Nearest != `SELECT %s, %s AS _distance FROM "NoteChunk"%s ORDER BY %s LIMIT %d` || s.LiveCondition != `"NoteChunk"."removedAt" IS NULL` {
		t.Errorf("Chunk's nearest = %q, live condition %q", s.Nearest, s.LiveCondition)
	}
}
