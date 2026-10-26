package sqlutil

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestQueueSpecOfReadsTheKey: a queue's spec takes its key's column, its
// onDuplicate and serialize from its declaration (D80), and a queue with
// no key dedupes and serializes nothing.
func TestQueueSpecOfReadsTheKey(t *testing.T) {
	td := &ir.TypeDef{Name: "OrderChanged", Queue: &ir.QueueDef{Key: "orderId", OnDuplicate: "replace", Serialize: true}}
	spec := QueueSpecOf(td, "order_changed_queue", []string{"order_id", "note"}, true)
	if spec.Key != "order_id" || !spec.Dedupes() || !spec.Serializes() {
		t.Errorf("QueueSpecOf = %+v", spec)
	}
	plain := QueueSpecOf(&ir.TypeDef{Name: "Ping", Queue: &ir.QueueDef{}}, "ping_queue", nil, false)
	if plain.Key != "" || plain.Dedupes() || plain.Serializes() {
		t.Errorf("QueueSpecOf of a queue with no key = %+v", plain)
	}
	keyword := &ir.TypeDef{Name: "Shipped", Queue: &ir.QueueDef{Key: "order"}, Fields: []*ir.FieldDef{{Name: "order", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
	if got := QueueKeyColumn(keyword); got != `"order"` {
		t.Errorf("QueueKeyColumn quotes a keyword as %s", got)
	}
}

// TestQueueStatementsWithoutAKeyAreUnchanged: a queue with no key, or one
// that appends, writes no conflict and the claims every queue wrote before
// a key existed.
func TestQueueStatementsWithoutAKeyAreUnchanged(t *testing.T) {
	plain := QueueSpec{Table: "ping_queue", Columns: []string{"sent_at"}, KeepDone: true}
	appends := plain
	appends.Key, appends.OnDuplicate = "sent_at", ir.QueueOnDuplicateAppend
	for _, dialect := range []func(QueueSpec) QueueStatements{PostgresQueueStatements, SQLiteQueueStatements} {
		a, b := dialect(plain), dialect(appends)
		if a != b || a.Conflict != "" {
			t.Errorf("an appending queue's statements differ from a plain one's:\n%+v\n%+v", a, b)
		}
		for _, s := range a.All() {
			if s.Name == "conflict" {
				t.Errorf("All lists a conflict for a queue without one")
			}
		}
	}
}

// TestQueueConflictReplacesOrSkips: replace overwrites the ready message
// of the key, every column but the key with the new message's, due at once
// with no attempts and no last error; skip keeps it (D80). Both name the
// partial index of the ready messages by key.
func TestQueueConflictReplacesOrSkips(t *testing.T) {
	spec := QueueSpec{Table: "order_changed_queue", Columns: []string{"order_id", "line_id", "priority"}, KeepDone: true, Key: "order_id", OnDuplicate: ir.QueueOnDuplicateReplace}
	want := "ON CONFLICT (order_id) WHERE state = 'ready' DO UPDATE SET line_id = excluded.line_id, priority = excluded.priority, due_at = excluded.due_at, attempts = 0, last_error = NULL, updated_at = now()"
	if got := PostgresQueueStatements(spec).Conflict; got != want {
		t.Errorf("Postgres replace:\n%s\nwant\n%s", got, want)
	}
	if got := SQLiteQueueStatements(spec).Conflict; got != strings.Replace(want, "now()", sqliteNow, 1) {
		t.Errorf("SQLite replace:\n%s", got)
	}
	spec.OnDuplicate = ir.QueueOnDuplicateSkip
	if got := PostgresQueueStatements(spec).Conflict; got != "ON CONFLICT (order_id) WHERE state = 'ready' DO NOTHING" {
		t.Errorf("skip: %s", got)
	}
	names := []string{}
	for _, s := range PostgresQueueStatements(spec).All() {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "insert,conflict,expire,claim,complete,fail,extend,release,prune" {
		t.Errorf("All = %v", names)
	}
}

// TestQueueFailAndReleaseGiveWay: on a dedupe queue, a failed or released
// message whose key a ready message holds is done, and its due time stays;
// the other states are as on any queue (D80).
func TestQueueFailAndReleaseGiveWay(t *testing.T) {
	spec := QueueSpec{Table: "q", Columns: []string{"k"}, KeepDone: true, Key: "k", OnDuplicate: ir.QueueOnDuplicateSkip}
	pending := "EXISTS (SELECT 1 FROM q AS p WHERE p.k = q.k AND p.state = 'ready')"
	for name, s := range map[string]QueueStatements{"postgres": PostgresQueueStatements(spec), "sqlite": SQLiteQueueStatements(spec)} {
		for _, want := range []string{
			"WHEN " + pending + " THEN 'done' ELSE 'ready' END",
			"OR " + pending + " THEN due_at ELSE",
		} {
			if !strings.Contains(s.Fail, want) {
				t.Errorf("%s fail lacks %q:\n%s", name, want, s.Fail)
			}
		}
		if !strings.Contains(s.Release, "state = CASE WHEN "+pending+" THEN 'done' ELSE 'ready' END") {
			t.Errorf("%s release:\n%s", name, s.Release)
		}
		if strings.Contains(s.Claim, "NOT EXISTS") {
			t.Errorf("%s claim of a queue that does not serialize reads the key:\n%s", name, s.Claim)
		}
	}
}

// TestQueueClaimTakesTheHeadOfEachKey: on a queue that serializes its keys,
// a claim takes no message whose key another claim holds, and of a key's
// ready messages only the first due by due time and ID, on Postgres with
// FOR UPDATE OF its row alone; SQLite's due read is the claim's condition
// (D80).
func TestQueueClaimTakesTheHeadOfEachKey(t *testing.T) {
	spec := QueueSpec{Table: "sync_requested_queue", Columns: []string{"account_id"}, KeepDone: true, Key: "account_id", Serialize: true}
	head := lines(
		"    AND NOT EXISTS (SELECT 1 FROM sync_requested_queue AS o WHERE o.account_id = m.account_id AND o.id <> m.id",
		"      AND (o.state = 'claimed' OR (m.state = 'ready' AND o.state = 'ready' AND (o.due_at, o.id) < (m.due_at, m.id))))",
	)
	pg := PostgresQueueStatements(spec)
	for _, want := range []string{
		"  SELECT m.id FROM sync_requested_queue AS m\n  WHERE ((m.state = 'ready' AND m.due_at <= now()) OR (m.state = 'claimed' AND m.claim_expires_at <= now()))\n" + head,
		"  ORDER BY m.due_at, m.id\n  LIMIT $1\n  FOR UPDATE OF m SKIP LOCKED",
	} {
		if !strings.Contains(pg.Claim, want) {
			t.Errorf("Postgres claim lacks\n%s\nin\n%s", want, pg.Claim)
		}
	}
	if pg.Conflict != "" || strings.Contains(pg.Fail, "EXISTS") {
		t.Errorf("a queue that serializes and appends dedupes: %+v", pg)
	}
	sqlite := SQLiteQueueStatements(spec)
	if !strings.Contains(sqlite.Claim, head) || !strings.Contains(sqlite.Due, head) {
		t.Errorf("SQLite claim and due lack the head of the key:\n%s\n%s", sqlite.Claim, sqlite.Due)
	}
}

// TestQueueOperatorRetryLeavesAKeyOneReadyMessage: on a dedupe queue the
// operator's retry takes one dead message per key, the last to die, and
// none whose key a ready message holds, which States says (D80).
func TestQueueOperatorRetryLeavesAKeyOneReadyMessage(t *testing.T) {
	s := PostgresQueueOperatorStatements("q", "k")
	for name, sql := range map[string]string{"retry": s.Retry, "retryAll": s.RetryAll} {
		for _, want := range []string{
			"AND NOT EXISTS (SELECT 1 FROM q AS p WHERE p.k = q.k AND p.state = 'ready')",
			"AND id IN (SELECT DISTINCT ON (k) id FROM q WHERE state = 'dead'",
			"ORDER BY k, updated_at DESC, id)",
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, sql)
			}
		}
	}
	if !strings.Contains(s.States, "THEN 'dead, and a ready message holds its key' ELSE q.state END") {
		t.Errorf("states:\n%s", s.States)
	}
	plain := PostgresQueueOperatorStatements("q", "")
	if strings.Contains(plain.Retry, "DISTINCT") || strings.Contains(plain.States, "CASE") {
		t.Errorf("a queue without a key retries by key:\n%+v", plain)
	}
}
