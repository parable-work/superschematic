package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// The queue documents of SQLite's statements (D68), which the compiler
// writes for fixture-queue-db's queues, checked in as goldens beside the
// SQLite create.sql they run on (internal/queueops/testdata/golden/sqlite).

// sqliteQueueBackends are the databases SQLite's documents run on: a
// SQLite file, and D1, through the fake D1 server, as the D1 driver
// reaches the real one from the deploying machine.
var sqliteQueueBackends = []testdb.Backend{testdb.SQLite, testdb.D1}

// sqliteGolden is the path of a SQLite golden document or create.sql.
func sqliteGolden(name string) string {
	return filepath.Join(repoRoot, "internal", "queueops", "testdata", "golden", "sqlite", name)
}

// sqliteQueueDocument writes the golden SQLite queue document name where
// the command reads it, and returns its path.
func sqliteQueueDocument(t *testing.T, name string) string {
	t.Helper()
	doc, err := os.ReadFile(sqliteGolden(name + ".json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// sqliteQueueDB creates fixture-queue-db's tables, as the SQLite model
// writes them, on a new database of backend, then runs statements, and
// returns its URL.
func sqliteQueueDB(t *testing.T, backend testdb.Backend, statements ...string) string {
	t.Helper()
	dbURL := testdb.New(t, backend)
	createSQL, err := os.ReadFile(sqliteGolden("create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	testdb.Exec(t, dbURL, append([]string{string(createSQL)}, statements...)...)
	return dbURL
}

// sqliteAt is a time of SQLite's clock, as a queue's columns hold it,
// moved by modifier ('-2 hours').
func sqliteAt(modifier string) string {
	return "strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '" + modifier + "')"
}

// TestQueueOnSQLiteAndD1 runs the golden SQLite documents on
// fixture-queue-db's tables, on a SQLite file and on D1, as
// TestQueueOnPostgres runs Postgres's: two dead messages and a ready one.
// It lists the dead, the oldest death first up to the limit, with the
// total, each message's attempts, last error, time of death and columns,
// its JSON columns nested as Postgres's are; refuses a retry that names a
// ready message and one the queue does not hold, saying what Postgres
// says, and retries nothing; retries one, which is ready, due now, with no
// attempts and no claim, its last error kept; drops the other, then
// refuses to drop it again; and retries and drops every dead message,
// leaving the ready one as it was.
func TestQueueOnSQLiteAndD1(t *testing.T) {
	for _, backend := range sqliteQueueBackends {
		t.Run(string(backend), func(t *testing.T) {
			dbURL := sqliteQueueDB(t, backend, `INSERT INTO order_placed_queue
  (id, order_id, placed_at, priority, quantity, note, ship_to, tags, state, attempts, last_error, due_at, updated_at)
VALUES
  ('`+deadID+`', '00000000-0000-4000-8000-0000000000c1', `+sqliteAt("+0 seconds")+`, 'high', 2, 'gift', '{"line1":"1 Main St","city":"Leeds"}', '["a","b"]', 'dead', 4, 'the card was refused', `+sqliteAt("-3 hours")+`, `+sqliteAt("-2 hours")+`),
  ('`+dead2ID+`', '00000000-0000-4000-8000-0000000000c2', `+sqliteAt("+0 seconds")+`, 'low', 1, NULL, '{"line1":"2 Side St","city":"York"}', '[]', 'dead', 1, NULL, `+sqliteAt("-2 hours")+`, `+sqliteAt("-1 hours")+`),
  ('`+readyID+`', '00000000-0000-4000-8000-0000000000c3', `+sqliteAt("+0 seconds")+`, 'low', 1, NULL, '{"line1":"3 High St","city":"Hull"}', '[]', 'ready', 0, NULL, `+sqliteAt("+1 hours")+`, `+sqliteAt("+0 seconds")+`)`)
			row := func(id string) string {
				t.Helper()
				got := testdb.Strings(t, dbURL, `SELECT state || ' attempts=' || attempts
  || ' due=' || CASE WHEN due_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now') THEN 'now' ELSE 'later' END
  || ' claim=' || CASE WHEN claim_token IS NOT NULL OR claim_expires_at IS NOT NULL THEN 'true' ELSE 'false' END
  || ' error=' || COALESCE(last_error, '')
  || ' changed=' || CASE WHEN updated_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 minutes') THEN 'true' ELSE 'false' END
  FROM order_placed_queue WHERE id = ?`, id)
				if len(got) == 0 {
					return "gone"
				}
				return got[0]
			}
			queue := func(name string, code int, out ...string) invocation {
				t.Helper()
				return invoke(t, nil, "queue", "--queue", sqliteQueueDocument(t, name), "--database-url", dbURL).expect(t, code, out...)
			}
			result := func(i invocation) migrate.QueueResult {
				t.Helper()
				var r migrate.QueueResult
				decoder := json.NewDecoder(strings.NewReader(i.stdout))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&r); err != nil {
					t.Fatalf("the result: %v\n%s", err, i.stdout)
				}
				return r
			}

			// The listing's limit is one: the older death, and the total.
			listed := result(queue("dead", exitOK))
			if listed.Operation != migrate.QueueDead || listed.Total != 2 || len(listed.Messages) != 1 {
				t.Fatalf("listed %+v", listed)
			}
			m := listed.Messages[0]
			if m.ID != deadID || m.Attempts != 4 || m.LastError != "the card was refused" || !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`).MatchString(m.DiedAt) {
				t.Fatalf("listed %+v", m)
			}
			var message map[string]any
			if err := json.Unmarshal(m.Message, &message); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(message))
			for k := range message {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if want := []string{"note", "order_id", "placed_at", "priority", "quantity", "ship_to", "tags"}; !slices.Equal(keys, want) {
				t.Errorf("the message's columns are %q, want %q", keys, want)
			}
			if message["note"] != "gift" || message["quantity"] != float64(2) || message["priority"] != "high" {
				t.Errorf("the message is %s", m.Message)
			}
			if shipTo, ok := message["ship_to"].(map[string]any); !ok || shipTo["city"] != "Leeds" {
				t.Errorf("the message's ship_to is %#v, want the object it holds", message["ship_to"])
			}
			if tags, ok := message["tags"].([]any); !ok || len(tags) != 2 || tags[0] != "a" {
				t.Errorf("the message's tags are %#v, want the list they hold", message["tags"])
			}

			// A retry that names a message that is not dead changes none,
			// and names only the messages that stopped it.
			queue("retry-refused", exitFailed,
				"superschematic-migrate: queue OrderPlaced of fixture-queue-db: 2 of the 3 message(s) named are not dead, so none was retried: "+
					readyID+" is ready; "+noID+" is not in the queue")
			if got := row(deadID); !strings.HasPrefix(got, "dead attempts=4") {
				t.Fatalf("a refused retry changed %s: %s", deadID, got)
			}

			retried := result(queue("retry", exitOK))
			if !slices.Equal(retried.IDs, []string{deadID}) || retried.Messages != nil {
				t.Fatalf("retried %+v", retried)
			}
			if got, want := row(deadID), "ready attempts=0 due=now claim=false error=the card was refused changed=true"; got != want {
				t.Errorf("the retried message is %q, want %q", got, want)
			}

			dropped := result(queue("drop", exitOK))
			if !slices.Equal(dropped.IDs, []string{dead2ID}) {
				t.Fatalf("dropped %+v", dropped)
			}
			if got := row(dead2ID); got != "gone" {
				t.Errorf("the dropped message is %s", got)
			}
			queue("drop", exitFailed, "1 of the 1 message(s) named are not dead, so none was dropped: "+dead2ID+" is not in the queue")
			if got := result(queue("dead", exitOK)); got.Total != 0 || len(got.Messages) != 0 || !strings.Contains(queue("dead", exitOK).stdout, `"messages": []`) {
				t.Fatalf("listed %+v", got)
			}

			// Every dead message, and the ready one stays as it was.
			testdb.Exec(t, dbURL, `UPDATE order_placed_queue SET state = 'dead', attempts = 4 WHERE id = '`+deadID+`'`,
				`INSERT INTO order_placed_queue (id, order_id, placed_at, priority, quantity, ship_to, state, attempts) VALUES ('`+dead2ID+`', '00000000-0000-4000-8000-0000000000c4', `+sqliteAt("+0 seconds")+`, 'low', 1, '{}', 'dead', 2)`)
			if got := result(queue("retry-all", exitOK)); !slices.Equal(got.IDs, []string{deadID, dead2ID}) {
				t.Fatalf("retried %+v", got)
			}
			for _, id := range []string{deadID, dead2ID} {
				if got := row(id); !strings.HasPrefix(got, "ready attempts=0 due=now claim=false") {
					t.Errorf("the retried message %s is %q", id, got)
				}
			}
			if got := result(queue("retry-all", exitOK)); len(got.IDs) != 0 || got.IDs == nil {
				t.Fatalf("retried %+v with no dead message", got)
			}
			testdb.Exec(t, dbURL, `UPDATE order_placed_queue SET state = 'dead' WHERE id IN ('`+deadID+`', '`+dead2ID+`')`)
			if got := result(queue("drop-all", exitOK)); !slices.Equal(got.IDs, []string{deadID, dead2ID}) {
				t.Fatalf("dropped %+v", got)
			}
			if got := row(readyID); !strings.HasPrefix(got, "ready attempts=0 due=later claim=false error= ") {
				t.Errorf("the ready message is %q", got)
			}
			if got := testdb.Strings(t, dbURL, "SELECT count(*) FROM order_placed_queue"); !slices.Equal(got, []string{"1"}) {
				t.Errorf("the queue holds %v messages, want the ready one", got)
			}
		})
	}
}

// TestQueueListsAnUnreadableRowOnSQLite: a dead message whose JSON column
// holds no JSON, which SQLite's text can hold and Postgres's JSONB cannot,
// is listed with that column's text, and the listing of the others does
// not fail over it.
func TestQueueListsAnUnreadableRowOnSQLite(t *testing.T) {
	dbURL := sqliteQueueDB(t, testdb.SQLite, `INSERT INTO order_placed_queue
  (id, order_id, placed_at, priority, quantity, ship_to, tags, state, attempts, updated_at)
VALUES ('`+deadID+`', '00000000-0000-4000-8000-0000000000c1', `+sqliteAt("+0 seconds")+`, 'low', 1, '{"line1":"1 Main St"', '["a"]', 'dead', 4, `+sqliteAt("-1 hours")+`)`)
	i := invoke(t, nil, "queue", "--queue", sqliteQueueDocument(t, "dead"), "--database-url", dbURL).expect(t, exitOK)
	var r migrate.QueueResult
	if err := json.Unmarshal([]byte(i.stdout), &r); err != nil {
		t.Fatalf("the result: %v\n%s", err, i.stdout)
	}
	if len(r.Messages) != 1 {
		t.Fatalf("listed %+v", r)
	}
	var message map[string]any
	if err := json.Unmarshal(r.Messages[0].Message, &message); err != nil {
		t.Fatal(err)
	}
	if message["ship_to"] != `{"line1":"1 Main St"` {
		t.Errorf("the unreadable ship_to is %#v, want its text", message["ship_to"])
	}
	if tags, ok := message["tags"].([]any); !ok || len(tags) != 1 {
		t.Errorf("the readable tags are %#v", message["tags"])
	}
}

// TestQueueRetryOfAKeyedQueueOnSQLiteAndD1 runs the SQLite retries of
// fixture-queue-db's StockChecked, which keeps one ready message per SKU
// (D80), as TestQueueRetryOfAKeyedQueueOnPostgres runs Postgres's: a retry
// that names both dead messages of one SKU changes none and says what
// Postgres says of the one that died first; a retry of every dead message
// retries the last death of each SKU, and none of a SKU a ready message
// holds.
func TestQueueRetryOfAKeyedQueueOnSQLiteAndD1(t *testing.T) {
	const (
		heldID    = "00000000-0000-4000-8000-000000000004"
		waitingID = "00000000-0000-4000-8000-000000000005"
		aloneID   = "00000000-0000-4000-8000-000000000006"
	)
	for _, backend := range sqliteQueueBackends {
		t.Run(string(backend), func(t *testing.T) {
			dbURL := sqliteQueueDB(t, backend, `INSERT INTO stock_checked_queue (id, sku, quantity, state, attempts, updated_at) VALUES
  ('`+deadID+`', 'sku-1', 1, 'dead', 4, `+sqliteAt("-2 hours")+`),
  ('`+dead2ID+`', 'sku-1', 2, 'dead', 4, `+sqliteAt("-1 hours")+`),
  ('`+heldID+`', 'sku-2', 3, 'dead', 4, `+sqliteAt("-1 hours")+`),
  ('`+waitingID+`', 'sku-2', 4, 'ready', 0, `+sqliteAt("+0 seconds")+`),
  ('`+aloneID+`', 'sku-3', 5, 'dead', 4, `+sqliteAt("-1 hours")+`)`)
			states := func() string {
				t.Helper()
				return strings.Join(testdb.Strings(t, dbURL, "SELECT sku || ':' || quantity || ':' || state FROM stock_checked_queue ORDER BY sku, quantity"), " ")
			}
			const before = "sku-1:1:dead sku-1:2:dead sku-2:3:dead sku-2:4:ready sku-3:5:dead"
			if got := states(); got != before {
				t.Fatalf("the queue holds %s", got)
			}

			invoke(t, nil, "queue", "--queue", sqliteQueueDocument(t, "retry-keyed"), "--database-url", dbURL).expect(t, exitFailed,
				"superschematic-migrate: queue StockChecked of fixture-queue-db: 1 of the 2 message(s) named are not dead, so none was retried: "+
					deadID+" is dead, and a ready message holds its key")
			if got := states(); got != before {
				t.Fatalf("a refused retry changed the queue: %s", got)
			}

			i := invoke(t, nil, "queue", "--queue", sqliteQueueDocument(t, "retry-all-keyed"), "--database-url", dbURL).expect(t, exitOK)
			var r migrate.QueueResult
			if err := json.Unmarshal([]byte(i.stdout), &r); err != nil {
				t.Fatalf("the result: %v\n%s", err, i.stdout)
			}
			if !slices.Equal(r.IDs, []string{dead2ID, aloneID}) {
				t.Errorf("retried %q, want the last death of sku-1 and sku-3's", r.IDs)
			}
			if got, want := states(), "sku-1:1:dead sku-1:2:ready sku-2:3:dead sku-2:4:ready sku-3:5:ready"; got != want {
				t.Errorf("the queue holds %s, want %s", got, want)
			}
		})
	}
}

// TestQueueBackfillOnSQLiteAndD1 runs the golden SQLite backfill of
// fixture-queue-db's OrderChanged (D88), as TestQueueBackfillOnPostgres
// runs Postgres's: of three orders, the first with two lines that name a
// product, the second with one, and the third with a line that names none,
// the backfill enqueues one ready message for each of the first two, keyed
// by order, says it enqueued two, as Postgres's does, and holds no message
// for the third. Run again, it makes each order's message fresh and adds
// none.
func TestQueueBackfillOnSQLiteAndD1(t *testing.T) {
	const (
		first   = "00000000-0000-4000-8000-0000000000a1"
		second  = "00000000-0000-4000-8000-0000000000a2"
		third   = "00000000-0000-4000-8000-0000000000a3"
		product = "00000000-0000-4000-8000-0000000000b1"
	)
	for _, backend := range sqliteQueueBackends {
		t.Run(string(backend), func(t *testing.T) {
			// The lines' trigger enqueues as they are written; the queue is
			// then emptied, as it is of rows written before the trigger
			// existed.
			dbURL := sqliteQueueDB(t, backend,
				`INSERT INTO product (id, "name") VALUES ('`+product+`', 'widget')`,
				`INSERT INTO "order" (id, placed_at) VALUES ('`+first+`', `+sqliteAt("+0 seconds")+`), ('`+second+`', `+sqliteAt("+0 seconds")+`), ('`+third+`', `+sqliteAt("+0 seconds")+`)`,
				`INSERT INTO order_line (id, order_id, product_id, quantity) VALUES
  ('00000000-0000-4000-8000-0000000000d1', '`+first+`', '`+product+`', 1),
  ('00000000-0000-4000-8000-0000000000d2', '`+first+`', '`+product+`', 2),
  ('00000000-0000-4000-8000-0000000000d3', '`+second+`', '`+product+`', 3),
  ('00000000-0000-4000-8000-0000000000d4', '`+third+`', NULL, 4)`,
				`DELETE FROM order_changed_queue`)
			queued := func() string {
				t.Helper()
				return strings.Join(testdb.Strings(t, dbURL, "SELECT order_id || ':' || state || ':' || priority || ':' || COALESCE(line_id, '') FROM order_changed_queue ORDER BY order_id"), " ")
			}

			i := invoke(t, nil, "queue", "--queue", sqliteQueueDocument(t, "backfill"), "--database-url", dbURL).expect(t, exitOK)
			var r migrate.QueueResult
			decoder := json.NewDecoder(strings.NewReader(i.stdout))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&r); err != nil {
				t.Fatalf("the result: %v\n%s", err, i.stdout)
			}
			if r.Operation != migrate.QueueBackfill || r.Total != 2 || strings.Contains(i.stdout, `"ids"`) {
				t.Fatalf("backfilled %+v:\n%s", r, i.stdout)
			}
			// The first of each order's lines by the line's key, as
			// Postgres's DISTINCT ON takes it.
			want := first + ":ready:low:00000000-0000-4000-8000-0000000000d1 " + second + ":ready:low:00000000-0000-4000-8000-0000000000d3"
			if got := queued(); got != want {
				t.Fatalf("the queue holds %s, want %s", got, want)
			}

			i = invoke(t, nil, "queue", "--queue", sqliteQueueDocument(t, "backfill"), "--database-url", dbURL).expect(t, exitOK)
			if !strings.Contains(i.stdout, `"total": 2`) {
				t.Fatalf("a second backfill answered:\n%s", i.stdout)
			}
			if got := queued(); got != want {
				t.Fatalf("a second backfill left %s, want %s", got, want)
			}
		})
	}
}

// TestQueueRefusesAnotherDialect refuses a document on a database of the
// other dialect, and SQLite's on a Cloud SQL instance, before it opens
// anything.
func TestQueueRefusesAnotherDialect(t *testing.T) {
	invoke(t, nil, "queue", "--queue", sqliteQueueDocument(t, "dead"), "--database-url", "postgres://localhost/x").
		expect(t, exitFailed, "queue OrderPlaced of fixture-queue-db: the document's statements are sqlite's, and the database URL selects postgres")
	invoke(t, nil, "queue", "--queue", sqliteQueueDocument(t, "dead"), "--cloud-sql", "acme:us-east1:db", "--cloud-sql-user", "m@acme.iam", "--database", "shop").
		expect(t, exitFailed, "queue OrderPlaced of fixture-queue-db: the document's statements are sqlite's, and a Cloud SQL instance runs Postgres")
}
