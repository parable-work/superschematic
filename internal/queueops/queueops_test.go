package queueops

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite the golden documents")

// The messages the golden documents name, which the runner's tests insert
// into fixture-queue-db's queue: two dead ones and a ready one.
const (
	deadID  = "00000000-0000-4000-8000-000000000001"
	dead2ID = "00000000-0000-4000-8000-000000000002"
	readyID = "00000000-0000-4000-8000-000000000003"
)

// goldenRequests are the documents of testdata/golden, which the runner's
// tests run (runtime/migrate/go/cmd/superschematic-migrate): on
// fixture-queue-db's OrderPlaced, a listing, a retry and a drop of IDs and
// of every dead message, and a retry that names a message that is not
// dead; and on StockChecked, which keeps one ready message per SKU (D80),
// a retry of two dead messages of one SKU and of every dead message.
var goldenRequests = map[string]Request{
	"retry-keyed":     {Service: "fixture-queue-db", Queue: "StockChecked", Operation: Retry, IDs: []string{deadID, dead2ID}, Keys: []string{"sku"}},
	"retry-all-keyed": {Service: "fixture-queue-db", Queue: "StockChecked", Operation: Retry, All: true, Keys: []string{"sku"}},
	"dead":            {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Dead, Limit: 1},
	"retry":           {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Retry, IDs: []string{deadID}},
	"retry-all":       {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Retry, All: true},
	"retry-refused":   {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Retry, IDs: []string{deadID, readyID, "00000000-0000-4000-8000-0000000000ff"}},
	"drop":            {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Drop, IDs: []string{dead2ID}},
	"drop-all":        {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Drop, All: true},
}

// TestGoldenDocuments writes each golden request's document and compares
// it with testdata/golden, which -update rewrites, and the same request's
// document of SQLite's statements, with the message's columns of the
// fixture's queue, with testdata/golden/sqlite, which the runner's tests
// run on a SQLite file and on D1 (D68).
func TestGoldenDocuments(t *testing.T) {
	schema := queueFixture(t)
	opts := sqlgen.Options{SchemaName: "fixture-queue-db"}
	for name, req := range goldenRequests {
		t.Run(name, func(t *testing.T) {
			got, err := Document(req)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, filepath.Join("testdata", "golden", name+".json"), got)

			req.Dialect = SQLite
			if req.Message, err = MessageColumns(schema, opts, req.Queue); err != nil {
				t.Fatal(err)
			}
			got, err = Document(req)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, filepath.Join("testdata", "golden", "sqlite", name+".json"), got)
		})
	}
}

// queueFixture loads fixture-queue-db, whose queues the golden documents
// name.
func queueFixture(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("..", "loader", "tsreader", "testdata", "services", "fixture-queue-db"))
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// checkGolden compares got with the golden file at path, which -update
// rewrites.
func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/queueops -update writes it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from what this package writes; -update rewrites it:\n%s", path, got)
	}
}

// TestSQLiteCreateSQL writes fixture-queue-db's SQLite create.sql, the
// plan from an empty database to its SQLite model, into
// testdata/golden/sqlite, where the runner's tests, which do not import
// the compiler, read the tables the SQLite documents run on.
func TestSQLiteCreateSQL(t *testing.T) {
	model, err := sqlmigrate.BuildModel(queueFixture(t), sqlgen.Options{SchemaName: "fixture-queue-db"}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	script, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, filepath.Join("testdata", "golden", "sqlite", "create.sql"), []byte(script))
}

// TestSQLiteDocuments holds what the SQLite documents differ by from
// Postgres's (D68): they name their dialect, list a message by its
// columns, each JSON column nested and none of the columns every queue
// keeps, and take the IDs as a JSON array; a drop's states are a drop's.
// A Postgres document takes no message columns, and no document takes a
// dialect it has no statements of.
func TestSQLiteDocuments(t *testing.T) {
	schema := queueFixture(t)
	opts := sqlgen.Options{SchemaName: "fixture-queue-db"}
	message, err := MessageColumns(schema, opts, "OrderPlaced")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range message {
		names = append(names, c.Name+":"+c.Holds)
	}
	if got, want := strings.Join(names, " "), "order_id: placed_at: priority: quantity: note: ship_to:json tags:json"; got != want {
		t.Errorf("OrderPlaced's message columns are %s, want %s", got, want)
	}
	if _, err := MessageColumns(schema, opts, "Order"); err == nil || !strings.Contains(err.Error(), "declares no queue Order") {
		t.Errorf("Order, which is no queue: %v", err)
	}
	req := goldenRequests["drop"]
	req.Dialect, req.Message = SQLite, message
	doc, err := Document(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"dialect": "sqlite"`, `json_each(?1)`, `'tags', CASE WHEN json_valid(q.\"tags\") THEN json(q.\"tags\") ELSE q.\"tags\" END`, `json_array_length(?1)`} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("the SQLite drop's document holds no %s:\n%s", want, doc)
		}
	}
	if strings.Contains(string(doc), "'state'") || strings.Contains(string(doc), "'updated_at'") {
		t.Errorf("the SQLite listing holds a column every queue keeps:\n%s", doc)
	}
	req = goldenRequests["dead"]
	req.Message = message
	if _, err := Document(req); err == nil || !strings.Contains(err.Error(), "on Postgres reads the message's columns from its row") {
		t.Errorf("a Postgres document with message columns: %v", err)
	}
	req.Message, req.Dialect = nil, "mysql"
	if _, err := Document(req); err == nil || !strings.Contains(err.Error(), `dialect "mysql" is not postgres or sqlite`) {
		t.Errorf("a document of another dialect: %v", err)
	}
}

// TestBackfillDocument writes the document of a backfill of
// fixture-queue-db's OrderChanged (D88), which OrderLine's trigger
// enqueues, and compares it with testdata/golden/backfill.json, which the
// runner's tests run: one statement, the trigger's insert for every line
// that names a product, one per order under replace, which returns how
// many it enqueued. A queue no trigger enqueues has no statement, and a
// name that is no queue is refused.
func TestBackfillDocument(t *testing.T) {
	schema := queueFixture(t)
	opts := sqlgen.Options{SchemaName: "fixture-queue-db"}
	statements, err := BackfillStatements(schema, opts, "OrderChanged", Postgres)
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 1 || !strings.Contains(statements[0], "SELECT DISTINCT ON (src.order_id)") || !strings.Contains(statements[0], "WHERE src.product_id IS NOT NULL") ||
		!strings.HasSuffix(statements[0], "RETURNING 1\n)\nSELECT count(*) FROM enqueued") {
		t.Fatalf("the backfill of OrderChanged:\n%s", strings.Join(statements, "\n---\n"))
	}
	if none, err := BackfillStatements(schema, opts, "StockChecked", Postgres); err != nil || len(none) != 0 {
		t.Fatalf("StockChecked, which no trigger enqueues: %q, %v", none, err)
	}
	if _, err := BackfillStatements(schema, opts, "Order", Postgres); err == nil || !strings.Contains(err.Error(), "declares no queue Order") {
		t.Fatalf("Order, which is no queue: %v", err)
	}
	got, err := Document(Request{Service: "fixture-queue-db", Queue: "OrderChanged", Operation: Backfill, Backfills: statements, Keys: []string{"order_id"}})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, filepath.Join("testdata", "golden", "backfill.json"), got)

	// On SQLite, one row of each order, the first by the line's key, and
	// a row of 1 per message, which the runner adds up.
	lite, err := BackfillStatements(schema, opts, "OrderChanged", SQLite)
	if err != nil {
		t.Fatal(err)
	}
	if len(lite) != 1 || !strings.Contains(lite[0], `row_number() OVER (PARTITION BY src."order_id" ORDER BY src."id")`) ||
		!strings.Contains(lite[0], `WHERE src."product_id" IS NOT NULL`) || !strings.HasSuffix(lite[0], "\nRETURNING 1") || strings.Contains(lite[0], "WITH") {
		t.Fatalf("the SQLite backfill of OrderChanged:\n%s", strings.Join(lite, "\n---\n"))
	}
	got, err = Document(Request{Service: "fixture-queue-db", Queue: "OrderChanged", Operation: Backfill, Backfills: lite, Keys: []string{"order_id"}, Dialect: SQLite})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, filepath.Join("testdata", "golden", "sqlite", "backfill.json"), got)
}

// TestDocumentRefuses holds Document to what each operation takes.
func TestDocumentRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		req  Request
		want string
	}{
		"no queue":        {Request{Service: "shop-db", Operation: Dead, Limit: 1}, "names its DB service and its queue"},
		"no limit":        {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Dead}, "a limit of one or more"},
		"dead with ids":   {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Dead, Limit: 1, IDs: []string{deadID}}, "takes no IDs"},
		"retry nothing":   {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Retry}, "retry takes the IDs of dead messages, or all of them"},
		"drop both":       {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Drop, All: true, IDs: []string{deadID}}, "drop takes the IDs of dead messages, or all of them"},
		"upper-case id":   {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Drop, IDs: []string{strings.ToUpper("0000000a-0000-4000-8000-000000000001")}}, "not a message's ID in lower case"},
		"other operation": {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: "prune"}, `operation "prune" is not dead, retry, drop or backfill`},
		"nothing to fill": {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Backfill}, "no @enqueueOnChange of shop-db enqueues OrderPlaced"},
		"backfill all":    {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Backfill, All: true, Backfills: []string{"SELECT 1"}}, "a backfill takes no limit, no IDs and no all"},
		"retry backfills": {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Retry, All: true, Backfills: []string{"SELECT 1"}}, "retry takes no backfill statements"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Document(tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestNormalizeID takes a UUID in either case and refuses anything else.
func TestNormalizeID(t *testing.T) {
	if got, err := NormalizeID("3F2C8A10-5B1E-4C7A-9D2E-8F6B4A1C0E93"); err != nil || got != "3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e93" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, id := range []string{"", "42", "3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e9", "3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e93 ", "'; DROP TABLE x; --"} {
		if _, err := NormalizeID(id); err == nil {
			t.Errorf("NormalizeID(%q) took it", id)
		}
	}
}

// TestReadResult reads the runner's result of a request and refuses one
// of another operation or version.
func TestReadResult(t *testing.T) {
	req := goldenRequests["dead"]
	r, err := ReadResult(req, []byte(`{"version":1,"service":"fixture-queue-db","queue":"OrderPlaced","operation":"dead","total":2,"messages":[{"id":"`+deadID+`","attempts":6,"lastError":"boom","diedAt":"2026-10-19T12:00:00Z","message":{"note":null}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 || len(r.Messages) != 1 || r.Messages[0].ID != deadID || string(r.Messages[0].Message) != `{"note":null}` {
		t.Fatalf("read %+v", r)
	}
	if _, err := ReadResult(req, []byte(`{"version":1,"service":"fixture-queue-db","queue":"OrderPlaced","operation":"drop","ids":[]}`)); err == nil || !strings.Contains(err.Error(), "not of dead on fixture-queue-db.OrderPlaced") {
		t.Fatalf("got %v", err)
	}
	if _, err := ReadResult(req, []byte(`{"version":2}`)); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("got %v", err)
	}
}

// TestDocumentNamesTheQueuesTable: a queue whose @table names its table
// (D85) has its statements run on that table, quoted, and a queue without
// one on its class's name with _queue.
func TestDocumentNamesTheQueuesTable(t *testing.T) {
	for _, tc := range []struct {
		table, want string
	}{
		{"linePosted", `"linePosted"`},
		{"", "line_posted_queue"},
	} {
		doc, err := Document(Request{Service: "fixture-naming-db", Queue: "LinePosted", Operation: Dead, Limit: 1, Table: tc.table})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(doc), "FROM "+strings.ReplaceAll(tc.want, `"`, `\"`)+" ") {
			t.Errorf("table %q: the document names no %s:\n%s", tc.table, tc.want, doc)
		}
	}
}
