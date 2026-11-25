package sqlutil

import (
	"slices"
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
	if !slices.Equal(spec.Keys, []string{"order_id"}) || !spec.Dedupes() || !spec.Serializes() {
		t.Errorf("QueueSpecOf = %+v", spec)
	}
	plain := QueueSpecOf(&ir.TypeDef{Name: "Ping", Queue: &ir.QueueDef{}}, "ping_queue", nil, false)
	if len(plain.Keys) > 0 || plain.Dedupes() || plain.Serializes() {
		t.Errorf("QueueSpecOf of a queue with no key = %+v", plain)
	}
	// A key over several fields is their columns, in the key's order,
	// each named as its field's column is.
	several := &ir.TypeDef{Name: "StockMoved", Queue: &ir.QueueDef{Keys: []string{"warehouse", "order"}, OnDuplicate: "replace"}, Fields: []*ir.FieldDef{
		{Name: "order", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "warehouse", TypeRef: ir.TypeRef{Name: "string"}, Required: true, SQLName: "depot"},
	}}
	if spec := QueueSpecOf(several, "stock_moved_queue", []string{`"order"`, "depot"}, true); !slices.Equal(spec.Keys, []string{"depot", `"order"`}) || !spec.Dedupes() || spec.Serializes() {
		t.Errorf("QueueSpecOf of a key over several fields = %+v", spec)
	}
	if got := QueueKeyColumnNames(several); !slices.Equal(got, []string{"depot", "order"}) {
		t.Errorf("QueueKeyColumnNames = %v", got)
	}
	// A table's change queue keeps its lines in the order written (D103);
	// its deadline queue, and a queue a schema declares, by due time.
	change := &ir.TypeDef{Name: "TaskChange", Queue: &ir.QueueDef{Key: "rowKey", OnDuplicate: ir.QueueOnDuplicateAppend, Serialize: true},
		BehaviorQueue: &ir.BehaviorQueue{Row: "Task", Kind: ir.BehaviorQueueChanges}}
	if spec := QueueSpecOf(change, "task_change_queue", nil, false); !spec.InOrder || !spec.Serializes() || spec.Dedupes() {
		t.Errorf("QueueSpecOf of a change queue = %+v", spec)
	}
	// So does its event queue (D104): one row's events reach the app in
	// the order the core wrote them.
	events := &ir.TypeDef{Name: "ProblemEvent", Queue: &ir.QueueDef{Key: "rowKey", Serialize: true},
		BehaviorQueue: &ir.BehaviorQueue{Row: "Problem", Kind: ir.BehaviorQueueEvents}}
	if spec := QueueSpecOf(events, "problem_event_queue", nil, false); !spec.InOrder || !spec.Serializes() || spec.Dedupes() {
		t.Errorf("QueueSpecOf of an event queue = %+v", spec)
	}
	deadlines := &ir.TypeDef{Name: "TicketDeadline", Queue: &ir.QueueDef{Key: "rowKey", OnDuplicate: ir.QueueOnDuplicateReplace},
		BehaviorQueue: &ir.BehaviorQueue{Row: "Ticket", Kind: ir.BehaviorQueueDeadlines}}
	if spec.InOrder || QueueSpecOf(deadlines, "ticket_deadline_queue", nil, false).InOrder {
		t.Errorf("a queue a schema declares, or a deadline queue, keeps its lines in order")
	}
	keyword := &ir.TypeDef{Name: "Shipped", Queue: &ir.QueueDef{Key: "order"}, Fields: []*ir.FieldDef{{Name: "order", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
	if got := QueueKeyColumns(keyword); !slices.Equal(got, []string{`"order"`}) {
		t.Errorf("QueueKeyColumns quotes a keyword as %s", got)
	}
}

// TestQueueStatementsOfAKeyOverSeveralFields (D80, amended): a queue keyed
// by several fields names every column of its key wherever one keyed by a
// single field names its one: the conflict's target, which a replace
// overwrites every other column under; the ready message that a failed or
// released one gives way to; and the head of a key's line, which no other
// claim of the same key holds. Each dialect writes them alike.
func TestQueueStatementsOfAKeyOverSeveralFields(t *testing.T) {
	spec := QueueSpec{Table: "stock_moved_queue", Columns: []string{"tenant_id", "sku", "count"}, KeepDone: true, Keys: []string{"tenant_id", "sku"}, OnDuplicate: ir.QueueOnDuplicateReplace, Serialize: true}
	for name, s := range map[string]QueueStatements{"postgres": PostgresQueueStatements(spec), "sqlite": SQLiteQueueStatements(spec)} {
		now := "now()"
		if name == "sqlite" {
			now = SQLiteNow
		}
		if want := "ON CONFLICT (tenant_id, sku) WHERE state = 'ready' DO UPDATE SET count = excluded.count, due_at = excluded.due_at, attempts = 0, last_error = NULL, updated_at = " + now; s.Conflict != want {
			t.Errorf("%s: conflict =\n%s\nwant\n%s", name, s.Conflict, want)
		}
		pending := "EXISTS (SELECT 1 FROM stock_moved_queue AS p WHERE p.tenant_id = stock_moved_queue.tenant_id AND p.sku = stock_moved_queue.sku AND p.state = 'ready')"
		for _, statement := range []string{s.Fail, s.Release} {
			if !strings.Contains(statement, pending) {
				t.Errorf("%s: a fail or a release gives way without comparing both columns of the key:\n%s", name, statement)
			}
		}
		head := "AND NOT EXISTS (SELECT 1 FROM stock_moved_queue AS o WHERE o.tenant_id = m.tenant_id AND o.sku = m.sku AND o.id <> m.id"
		if !strings.Contains(s.Claim, head) {
			t.Errorf("%s: the claim takes the head of a line without comparing both columns of the key:\n%s", name, s.Claim)
		}
		if name == "sqlite" && !strings.Contains(s.Due, head) {
			t.Errorf("sqlite: the due read does not compare both columns of the key:\n%s", s.Due)
		}
	}
	skip := spec
	skip.OnDuplicate, skip.Serialize = ir.QueueOnDuplicateSkip, false
	if got, want := PostgresQueueStatements(skip).Conflict, "ON CONFLICT (tenant_id, sku) WHERE state = 'ready' DO NOTHING"; got != want {
		t.Errorf("a skip's conflict = %s, want %s", got, want)
	}

	// An operator's retry leaves each key one ready message: the last to
	// die of each pair of columns, and none whose pair a ready one holds.
	ops := PostgresQueueOperatorStatements("stock_moved_queue", "tenant_id", "sku")
	for _, want := range []string{
		"AND NOT EXISTS (SELECT 1 FROM stock_moved_queue AS p WHERE p.tenant_id = stock_moved_queue.tenant_id AND p.sku = stock_moved_queue.sku AND p.state = 'ready')",
		"SELECT DISTINCT ON (tenant_id, sku) id FROM stock_moved_queue WHERE state = 'dead' ORDER BY tenant_id, sku, updated_at DESC, id",
	} {
		if !strings.Contains(ops.RetryAll, want) {
			t.Errorf("the operator's retry of a key over several fields lacks %s:\n%s", want, ops.RetryAll)
		}
	}
	if want := "EXISTS (SELECT 1 FROM stock_moved_queue AS p WHERE p.tenant_id = q.tenant_id AND p.sku = q.sku AND p.state = 'ready')"; !strings.Contains(ops.States, want) {
		t.Errorf("the operator's states of a key over several fields lack %s:\n%s", want, ops.States)
	}
}

// TestQueueStatementsWithoutAKeyAreUnchanged: a queue with no key, or one
// that appends, writes no conflict and the claims every queue wrote before
// a key existed.
func TestQueueStatementsWithoutAKeyAreUnchanged(t *testing.T) {
	plain := QueueSpec{Table: "ping_queue", Columns: []string{"sent_at"}, KeepDone: true}
	appends := plain
	appends.Keys, appends.OnDuplicate = []string{"sent_at"}, ir.QueueOnDuplicateAppend
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
	spec := QueueSpec{Table: "order_changed_queue", Columns: []string{"order_id", "line_id", "priority"}, KeepDone: true, Keys: []string{"order_id"}, OnDuplicate: ir.QueueOnDuplicateReplace}
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
	if strings.Join(names, ",") != "insert,dueIn,conflict,expire,claim,complete,fail,extend,release,prune" {
		t.Errorf("All = %v", names)
	}
}

// TestQueueFailAndReleaseGiveWay: on a dedupe queue, a failed or released
// message whose key a ready message holds is done, and its due time stays;
// the other states are as on any queue (D80).
func TestQueueFailAndReleaseGiveWay(t *testing.T) {
	spec := QueueSpec{Table: "q", Columns: []string{"k"}, KeepDone: true, Keys: []string{"k"}, OnDuplicate: ir.QueueOnDuplicateSkip}
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
	spec := QueueSpec{Table: "sync_requested_queue", Columns: []string{"account_id"}, KeepDone: true, Keys: []string{"account_id"}, Serialize: true}
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

// TestQueueClaimKeepsAChangeQueuesLinesInOrder: on a queue that keeps its
// lines in order, a table's change queue (D103), a key's head is its first
// written ready message, by created_at and ID, due or not, so one that
// waits a failure's backoff holds the key's later messages; a claim still
// takes it only when it is due (D80, D68).
func TestQueueClaimKeepsAChangeQueuesLinesInOrder(t *testing.T) {
	spec := QueueSpec{Table: "task_change_queue", Columns: []string{"row_key"}, Keys: []string{"row_key"}, Serialize: true, InOrder: true}
	head := lines(
		"    AND NOT EXISTS (SELECT 1 FROM task_change_queue AS o WHERE o.row_key = m.row_key AND o.id <> m.id",
		"      AND (o.state = 'claimed' OR (m.state = 'ready' AND o.state = 'ready' AND (o.created_at, o.id) < (m.created_at, m.id))))",
	)
	pg := PostgresQueueStatements(spec)
	if !strings.Contains(pg.Claim, "  WHERE ((m.state = 'ready' AND m.due_at <= now()) OR (m.state = 'claimed' AND m.claim_expires_at <= now()))\n"+head) {
		t.Errorf("Postgres claim of a change queue lacks its line's order:\n%s", pg.Claim)
	}
	sqlite := SQLiteQueueStatements(spec)
	if !strings.Contains(sqlite.Claim, head) || !strings.Contains(sqlite.Due, head) {
		t.Errorf("SQLite claim and due of a change queue lack its line's order:\n%s\n%s", sqlite.Claim, sqlite.Due)
	}
	if strings.Contains(pg.Fail, "created_at") {
		t.Errorf("a change queue's fail moves its message's place in line:\n%s", pg.Fail)
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

// TestQueueDueIn (D80, amended): every queue carries the due time of a
// message enqueued with a delay, its dialect's clock plus the seconds of
// the enqueue's first parameter, whatever its key and whatever clock its
// claims go by, and lists it beside its insert. A queue that replaces the
// ready message of a key takes the new message's due time with its
// columns, so a delayed enqueue moves when the ready one is due.
func TestQueueDueIn(t *testing.T) {
	plain := QueueSpec{Table: "ping_queue", Columns: []string{"sent_at"}, KeepDone: true}
	keyed := QueueSpec{Table: "q", Columns: []string{"k", "v"}, KeepDone: true, Keys: []string{"k"}, OnDuplicate: ir.QueueOnDuplicateReplace}
	for _, spec := range []QueueSpec{plain, keyed} {
		if got, want := PostgresQueueStatements(spec).DueIn, "now() + make_interval(secs => $1)"; got != want {
			t.Errorf("postgres dueIn = %s, want %s", got, want)
		}
		const sqlite = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+' || (?1) || ' seconds')"
		for name, s := range map[string]QueueStatements{"sqlite": SQLiteQueueStatements(spec), "sqlite at a given time": SQLiteQueueStatementsAt(spec)} {
			if s.DueIn != sqlite {
				t.Errorf("%s dueIn = %s, want %s", name, s.DueIn, sqlite)
			}
		}
		all := PostgresQueueStatements(spec).All()
		if all[0].Name != "insert" || all[1].Name != "dueIn" || all[1].SQL == "" {
			t.Errorf("All begins %v, want the insert and then dueIn", all[:2])
		}
	}
	for name, s := range map[string]QueueStatements{"postgres": PostgresQueueStatements(keyed), "sqlite": SQLiteQueueStatements(keyed)} {
		if !strings.Contains(s.Conflict, "due_at = excluded.due_at") {
			t.Errorf("%s: a replace does not take the new message's due time: %s", name, s.Conflict)
		}
	}
}
