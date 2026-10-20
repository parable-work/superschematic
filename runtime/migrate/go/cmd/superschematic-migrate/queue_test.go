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

// repoRoot is the checkout's root, from this package's directory.
var repoRoot = filepath.Join("..", "..", "..", "..", "..")

// queueDocument writes the golden queue document the compiler writes for
// fixture-queue-db's OrderPlaced (internal/queueops/testdata/golden) where
// the command reads it, and returns its path.
func queueDocument(t *testing.T, name string) string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join(repoRoot, "internal", "queueops", "testdata", "golden", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The messages the golden documents name.
const (
	deadID  = "00000000-0000-4000-8000-000000000001"
	dead2ID = "00000000-0000-4000-8000-000000000002"
	readyID = "00000000-0000-4000-8000-000000000003"
	noID    = "00000000-0000-4000-8000-0000000000ff"
)

// TestQueueOnPostgres runs each golden queue document on fixture-queue-db's
// tables, as sqlgen writes them, on a database of its own: two dead
// messages and a ready one. It lists the dead, the oldest death first up to
// the limit, with the total, each message's attempts, last error, time of
// death and columns; refuses a retry that names a ready message and one the
// queue does not hold, and retries nothing; retries one, which is ready,
// due now, with no attempts and no claim, its last error kept; drops the
// other, then refuses to drop it again; and retries and drops every dead
// message, leaving the ready one as it was.
func TestQueueOnPostgres(t *testing.T) {
	dbURL := testdb.NewPostgres(t)
	createSQL, err := os.ReadFile(filepath.Join(repoRoot, "internal", "generator", "sqlgen", "testdata", "golden", "fixture-queue-db", "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	testdb.Exec(t, dbURL, string(createSQL), `INSERT INTO order_placed_queue
  (id, order_id, priority, quantity, note, ship_to, tags, state, attempts, last_error, due_at, updated_at)
VALUES
  ('`+deadID+`', gen_random_uuid(), 'high', 2, 'gift', '{"line1":"1 Main St","city":"Leeds"}', '{a,b}', 'dead', 4, 'the card was refused', now() - interval '3 hours', now() - interval '2 hours'),
  ('`+dead2ID+`', gen_random_uuid(), 'low', 1, NULL, '{"line1":"2 Side St","city":"York"}', '{}', 'dead', 1, NULL, now() - interval '2 hours', now() - interval '1 hour'),
  ('`+readyID+`', gen_random_uuid(), 'low', 1, NULL, '{"line1":"3 High St","city":"Hull"}', '{}', 'ready', 0, NULL, now() + interval '1 hour', now())`)
	row := func(id string) string {
		t.Helper()
		got := testdb.Strings(t, dbURL, `SELECT state || ' attempts=' || attempts || ' due=' || CASE WHEN due_at <= now() THEN 'now' ELSE 'later' END
  || ' claim=' || (claim_token IS NOT NULL OR claim_expires_at IS NOT NULL) || ' error=' || COALESCE(last_error, '')
  || ' changed=' || (updated_at > now() - interval '1 minute')
  FROM order_placed_queue WHERE id = $1::uuid`, id)
		if len(got) == 0 {
			return "gone"
		}
		return got[0]
	}
	queue := func(name string, code int, out ...string) invocation {
		t.Helper()
		return invoke(t, nil, "queue", "--queue", queueDocument(t, name), "--database-url", dbURL).expect(t, code, out...)
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
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range message {
			if !yield(k) {
				return
			}
		}
	})
	if want := []string{"note", "order_id", "placed_at", "priority", "quantity", "ship_to", "tags"}; !slices.Equal(keys, want) {
		t.Errorf("the message's columns are %q, want %q", keys, want)
	}
	if message["note"] != "gift" || message["quantity"] != float64(2) || message["priority"] != "high" {
		t.Errorf("the message is %s", m.Message)
	}

	// A retry that names a message that is not dead changes none.
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
		`INSERT INTO order_placed_queue (id, order_id, priority, quantity, ship_to, state, attempts) VALUES ('`+dead2ID+`', gen_random_uuid(), 'low', 1, '{}', 'dead', 2)`)
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
}

// TestQueueRefuses refuses a queue document on a database that is not
// Postgres, a command without one, and a document it cannot read.
func TestQueueRefuses(t *testing.T) {
	doc := queueDocument(t, "dead")
	invoke(t, nil, "queue", "--queue", doc, "--database-url", testdb.NewSQLite(t)).
		expect(t, exitFailed, "queue OrderPlaced of fixture-queue-db: a queue's messages are claimed on Postgres alone, and the database URL selects sqlite")
	invoke(t, nil, "queue", "--database-url", "postgres://localhost/x").expect(t, exitUsage, "queue needs --queue")
	invoke(t, nil, "queue", "--queue", doc).expect(t, exitUsage, "no database: pass --database-url or set DATABASE_URL")
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version": 2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	invoke(t, nil, "queue", "--queue", bad, "--database-url", "postgres://localhost/x").expect(t, exitFailed, "queue document version 2; this runner reads version 1")
}
