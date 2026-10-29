package ormgen

// queueKeyParitySubtests are the subtests of the generated
// TestQueueKeyParity, each of which must pass on Postgres and on SQLite.
var queueKeyParitySubtests = []string{
	"ReplaceKeepsOneReadyMessage",
	"SkipKeepsTheFirst",
	"FailedAndReleasedGiveWay",
	"SerializeHoldsOneClaimPerKey",
	"SerializeTakesTheHeadOfEachKey",
	"ConcurrentClaimsNeverShareAKey",
	"LostClaimRaceTakesNothing",
	"FailRacingAnEnqueueGivesWay",
	"TriggerEnqueuesOnChange",
}

// queueKeyParityPostgresOnly are the subtests that race two transactions,
// which SQLite's one writer never does, so they skip there.
var queueKeyParityPostgresOnly = map[string]bool{
	"LostClaimRaceTakesNothing":   true,
	"FailRacingAnEnqueueGivesWay": true,
}

// queueKeyParityORMTest is the generated module's TestQueueKeyParity
// (D80), beside TestQueueParity, whose helpers it uses, on the queues of
// fixture-queue-db that have a key: OrderChanged, which replaces and
// serializes, StockChecked, which skips, and SyncRequested, which appends
// and serializes, and on OrderLine's @enqueueOnChange of OrderChanged. Its
// subtests:
//   - ReplaceKeepsOneReadyMessage: Go, TypeScript and Rust enqueue five
//     messages of one order in turn, and one ready message holds the last
//     one's line with no attempts; another order's is a message of its own;
//   - SkipKeepsTheFirst: the three enqueue one SKU's count in turn, and the
//     first waits alone; once a worker claims it, a new ask waits beside it;
//   - FailedAndReleasedGiveWay: a claimed message that fails, or is
//     released, while a newer message of its order waits is done, its
//     cause kept, and the newer one runs in its place; with none waiting it
//     is ready again;
//   - SerializeHoldsOneClaimPerKey: no side claims an order's waiting
//     message while another holds a claim of the order; one whose claim
//     expired is claimed again before the one that waits;
//   - SerializeTakesTheHeadOfEachKey: of an account's requests, each kept,
//     a claim takes the first due, one at a time, and other accounts' beside
//     it;
//   - ConcurrentClaimsNeverShareAKey: eight Go workers claim and complete
//     sixty requests of four accounts at once, and no two ever hold claims
//     on one account, while every claim succeeds;
//   - LostClaimRaceTakesNothing, on Postgres: each side's claim whose
//     snapshot missed another worker's claim of the key waits on the index
//     of claimed messages, and once that commits takes nothing, with no
//     error;
//   - FailRacingAnEnqueueGivesWay, on Postgres: each side's fail that meets
//     an enqueue of its key not yet committed waits on the index of ready
//     messages, runs once more when it commits, and gives way to it;
//   - TriggerEnqueuesOnChange: raw SQL that sets a line's product enqueues
//     exactly one message, a change to an unrelated column none, and
//     repeated changes, a delete among them, keep one ready message.
const queueKeyParityORMTest = `package orm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-queue-db"
)

// TestQueueKeyParity runs the keyed queues' cases, each in a database of
// its own, on the dialect QUEUE_PARITY_DIALECT names.
func TestQueueKeyParity(t *testing.T) {
	t.Run("ReplaceKeepsOneReadyMessage", testReplaceKeepsOneReadyMessage)
	t.Run("SkipKeepsTheFirst", testSkipKeepsTheFirst)
	t.Run("FailedAndReleasedGiveWay", testFailedAndReleasedGiveWay)
	t.Run("SerializeHoldsOneClaimPerKey", testSerializeHoldsOneClaimPerKey)
	t.Run("SerializeTakesTheHeadOfEachKey", testSerializeTakesTheHeadOfEachKey)
	t.Run("ConcurrentClaimsNeverShareAKey", testConcurrentClaimsNeverShareAKey)
	t.Run("LostClaimRaceTakesNothing", testLostClaimRaceTakesNothing)
	t.Run("FailRacingAnEnqueueGivesWay", testFailRacingAnEnqueueGivesWay)
	t.Run("TriggerEnqueuesOnChange", testTriggerEnqueuesOnChange)
}

// waits runs call in a goroutine and waits until a backend waits on a lock
// of the backend blocker, the other transaction's, which shows the call
// meets the key's index; then it runs release and returns call's error
// once call returns.
func (p *parity) waits(blocker int, call func() error, release func() error) error {
	t := p.t
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	for deadline := time.Now().Add(30 * time.Second); ; {
		var blocked int
		p.queryRow("SELECT count(*) FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))", []any{blocker}, &blocked)
		if blocked > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the call returned (%v) without waiting on the other transaction; it was to meet the key's index", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the call does not wait on the other transaction")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("the call still waits after the other transaction committed")
	}
	return nil
}

// testLostClaimRaceTakesNothing: for each side, account A's request m1
// waits; a long transaction adds m0, due before it. Worker one claims m1
// in a transaction it holds open, before m0 commits. Then m0 commits, and
// the side's claim, whose snapshot has m0 ready and m1 not yet claimed,
// takes m0 as the head of the line: its write waits on the claimed index
// for worker one's, and once that commits it takes nothing, and no error.
// The key still holds one claim.
func testLostClaimRaceTakesNothing(t *testing.T) {
	if onSQLite() {
		t.Skip("SQLite has one writer, so two claims never race")
	}
	for _, side := range []string{"Go", "TypeScript", "Rust"} {
		t.Run(side, func(t *testing.T) {
			p := newParity(t)
			p.on = "SyncRequested"
			ctx := p.ctx
			p.exec("INSERT INTO sync_requested_queue (account_id, reason, due_at) VALUES ($1::uuid, 'm1', now() - interval '1 minute')", orderA)
			late, err := p.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = late.Rollback(ctx) }()
			if _, err := late.Exec(ctx, "INSERT INTO sync_requested_queue (account_id, reason, due_at) VALUES ($1::uuid, 'm0', now() - interval '2 minutes')", orderA); err != nil {
				t.Fatal(err)
			}
			one, err := p.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = one.Rollback(ctx) }()
			var pid int
			if err := one.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			// Worker one's claim, the queue's own statement, in its transaction.
			var id, token, accountID, reason string
			var attempt int
			if err := one.QueryRow(ctx, syncRequestedQueue.statements.claim, 1, 60.0).Scan(&id, &token, &attempt, &accountID, &reason); err != nil || reason != "m1" {
				t.Fatalf("worker one's claim took %q (%v), want m1", reason, err)
			}
			if err := late.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			took := ""
			err = p.waits(pid, func() error {
				if side != "Go" {
					out, err := p.step(side, "claim", "5")
					took = out
					return err
				}
				claims, err := p.syncQueue().Claim(ctx, 5, time.Minute)
				took = fmt.Sprint(len(claims))
				return err
			}, func() error { return one.Commit(ctx) })
			if err != nil || (took != "0" && took != "[]") {
				t.Fatalf("%s's claim took %s, %v; want nothing and no error", side, took, err)
			}
			wantRows(t, "after the race", p.rows("sync_requested_queue", "account_id", orderA, "reason"), "claimed:m1", "ready:m0")
		})
	}
}

// testFailRacingAnEnqueueGivesWay: for each side, a worker holds order A's
// message; an enqueue of order A, in a transaction not yet committed, adds
// a ready message the side's fail does not see. The fail's write waits on
// the ready index for it; once it commits, the fail runs once more, sees
// the ready message, and gives way: done, with no error.
func testFailRacingAnEnqueueGivesWay(t *testing.T) {
	if onSQLite() {
		t.Skip("SQLite has one writer, so a fail never races an enqueue")
	}
	for _, side := range []string{"Go", "TypeScript", "Rust"} {
		t.Run(side, func(t *testing.T) {
			p := newParity(t)
			ctx := p.ctx
			p.on = "OrderChanged"
			p.keyedEnqueue("Go", changedJSON(orderA, 1, "low"))
			claimed := p.keyedClaims("Go", 1)
			if len(claimed) != 1 {
				t.Fatalf("Go claimed %+v, want order A's message", claimed)
			}
			// The enqueue's transaction stays open until release closes.
			inserted, committed, release := make(chan int, 1), make(chan error, 1), make(chan struct{})
			go func() {
				committed <- p.db.Transaction(ctx, func(tx TxInterface) error {
					rows, err := tx.Query(ctx, "SELECT pg_backend_pid()")
					if err != nil {
						return err
					}
					pid := 0
					for rows.Next() {
						if err := rows.Scan(&pid); err != nil {
							return err
						}
					}
					rows.Close()
					err = EnqueueOrderChanged(ctx, tx, &types.OrderChanged{OrderId: mustUUID(orderA), LineId: ptr(mustUUID(lineID(2))), Priority: types.Priority_Low})
					if err != nil {
						pid = 0
					}
					inserted <- pid
					if err != nil {
						return err
					}
					<-release
					return nil
				})
			}()
			pid := <-inserted
			if pid == 0 {
				t.Fatal(<-committed)
			}
			out := ""
			err := p.waits(pid, func() error {
				if side != "Go" {
					var err error
					out, err = p.step(side, "fail", claimed[0].ID, claimed[0].Token, "it broke")
					return err
				}
				dead, err := p.changedQueue().Fail(ctx, claimed[0].ID, claimed[0].Token, "it broke")
				out = fmt.Sprint(dead)
				return err
			}, func() error {
				close(release)
				return <-committed
			})
			if err != nil || out != "false" {
				t.Fatalf("%s's fail = %s, %v; want it to give way, with no error", side, out, err)
			}
			wantRows(t, "after the race", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "done:"+lineID(1), "ready:"+lineID(2))
		})
	}
}

func ptr[T any](v T) *T { return &v }

const (
	orderA = "a0000000-0000-4000-8000-00000000000a"
	orderB = "b0000000-0000-4000-8000-00000000000b"
)

// lineID is the nth line's ID.
func lineID(n int) string { return fmt.Sprintf("10000000-0000-4000-8000-%012d", n) }

// keyedClaim is a claim of any side of a keyed queue, its message's
// members as text, a UUID hyphenated whichever side printed it.
type keyedClaim struct {
	ID, Token string
	Attempt   int
	Members   map[string]string
}

// inTx runs write in a transaction of its own: the Go ORM's on Postgres, a
// database/sql one on SQLite.
func (p *parity) inTx(write func(tx TxInterface, file *sql.Tx) error) {
	p.t.Helper()
	if p.file == nil {
		if err := p.db.Transaction(p.ctx, func(tx TxInterface) error { return write(tx, nil) }); err != nil {
			p.t.Fatal(err)
		}
		return
	}
	tx, err := p.file.BeginTx(p.ctx, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := write(nil, tx); err != nil {
		_ = tx.Rollback()
		p.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		p.t.Fatal(err)
	}
}

// keyedEnqueue has side enqueue the message message, the JSON of a message
// of the queue p.on names.
func (p *parity) keyedEnqueue(side, message string) {
	p.t.Helper()
	if side != "Go" {
		p.must(side, "enqueue", message)
		return
	}
	p.inTx(func(tx TxInterface, file *sql.Tx) error {
		switch p.on {
		case "OrderChanged":
			var m types.OrderChanged
			if err := json.Unmarshal([]byte(message), &m); err != nil {
				return err
			}
			if file != nil {
				return EnqueueOrderChangedSQLite(p.ctx, file, &m)
			}
			return EnqueueOrderChanged(p.ctx, tx, &m)
		case "StockChecked":
			var m types.StockChecked
			if err := json.Unmarshal([]byte(message), &m); err != nil {
				return err
			}
			if file != nil {
				return EnqueueStockCheckedSQLite(p.ctx, file, &m)
			}
			return EnqueueStockChecked(p.ctx, tx, &m)
		default:
			var m types.SyncRequested
			if err := json.Unmarshal([]byte(message), &m); err != nil {
				return err
			}
			if file != nil {
				return EnqueueSyncRequestedSQLite(p.ctx, file, &m)
			}
			return EnqueueSyncRequested(p.ctx, tx, &m)
		}
	})
}

// members reads a message's JSON as text members, a UUID hyphenated.
func members(t *testing.T, message any) map[string]string {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for name, value := range raw {
		if value == nil {
			continue
		}
		text := fmt.Sprint(value)
		if s, ok := value.(string); ok && (name == "orderId" || name == "lineId" || name == "accountId") {
			text = fmt.Sprint(mustUUID(s))
		}
		out[name] = text
	}
	return out
}

// keyedClaims has side claim up to n messages of the queue p.on names.
func (p *parity) keyedClaims(side string, n int) []keyedClaim {
	p.t.Helper()
	var out []keyedClaim
	if side != "Go" {
		for _, c := range p.printed(side, n) {
			out = append(out, keyedClaim{ID: c.ID, Token: c.Token, Attempt: c.Attempt, Members: members(p.t, c.Message)})
		}
		return out
	}
	add := func(id, token string, attempt int, message any) {
		out = append(out, keyedClaim{ID: id, Token: token, Attempt: attempt, Members: members(p.t, message)})
	}
	switch p.on {
	case "OrderChanged":
		claims, err := p.changedQueue().Claim(p.ctx, n, time.Minute)
		if err != nil {
			p.t.Fatalf("Go's claim on %s: %v", p.on, err)
		}
		for _, c := range claims {
			add(c.ID, c.Token, c.Attempt, c.Message)
		}
	case "StockChecked":
		var claims []StockCheckedClaim
		var err error
		if p.file != nil {
			claims, err = NewStockCheckedSQLiteQueue(p.file).Claim(p.ctx, n, time.Minute)
		} else {
			claims, err = p.db.StockCheckedQueue().Claim(p.ctx, n, time.Minute)
		}
		if err != nil {
			p.t.Fatalf("Go's claim on %s: %v", p.on, err)
		}
		for _, c := range claims {
			add(c.ID, c.Token, c.Attempt, c.Message)
		}
	default:
		claims, err := p.syncQueue().Claim(p.ctx, n, time.Minute)
		if err != nil {
			p.t.Fatalf("Go's claim on %s: %v", p.on, err)
		}
		for _, c := range claims {
			add(c.ID, c.Token, c.Attempt, c.Message)
		}
	}
	return out
}

// changedQueue is the Go ORM's queue OrderChanged on the parity's dialect.
type changedQueue interface {
	Claim(ctx context.Context, n int, lease time.Duration) ([]OrderChangedClaim, error)
	Complete(ctx context.Context, id, token string) error
	Fail(ctx context.Context, id, token, cause string) (bool, error)
	Release(ctx context.Context, id, token string) error
}

func (p *parity) changedQueue() changedQueue {
	if p.file != nil {
		return NewOrderChangedSQLiteQueue(p.file)
	}
	return p.db.OrderChangedQueue()
}

// syncQueue is the Go ORM's queue SyncRequested on the parity's dialect.
type syncQueue interface {
	Claim(ctx context.Context, n int, lease time.Duration) ([]SyncRequestedClaim, error)
	Complete(ctx context.Context, id, token string) error
	Fail(ctx context.Context, id, token, cause string) (bool, error)
}

func (p *parity) syncQueue() syncQueue {
	if p.file != nil {
		return NewSyncRequestedSQLiteQueue(p.file)
	}
	return p.db.SyncRequestedQueue()
}

// rows lists the messages of table whose key column holds key, each as its
// state and the text of column, by state, then in the order they were
// enqueued.
func (p *parity) rows(table, keyColumn, key, column string) []string {
	p.t.Helper()
	query := p.dialectSQL(
		fmt.Sprintf("SELECT state || ':' || COALESCE(%s::text, '') FROM %s WHERE %s::text = $1 ORDER BY state, created_at, id", column, table, keyColumn),
		fmt.Sprintf("SELECT state || ':' || COALESCE(%s, '') FROM %s WHERE %s = ?1 ORDER BY state, created_at, id", column, table, keyColumn),
	)
	var out []string
	if p.file != nil {
		rows, err := p.file.QueryContext(p.ctx, query, key)
		if err != nil {
			p.t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				p.t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	rows, err := p.pool.Query(p.ctx, query, key)
	if err != nil {
		p.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			p.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// wantRows fails the test unless rows lists want.
func wantRows(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: the messages are %v, want %v", what, got, want)
	}
}

// keyedChange has side complete, fail or release the claim c.
func (p *parity) keyedChange(side, step string, c keyedClaim) string {
	p.t.Helper()
	if side != "Go" {
		args := []string{c.ID, c.Token}
		if step == "fail" {
			args = append(args, "it broke")
		}
		return p.must(side, step, args...)
	}
	q := p.changedQueue()
	var err error
	out := ""
	switch step {
	case "complete":
		err = q.Complete(p.ctx, c.ID, c.Token)
	case "release":
		err = q.Release(p.ctx, c.ID, c.Token)
	case "fail":
		var dead bool
		dead, err = q.Fail(p.ctx, c.ID, c.Token, "it broke")
		out = fmt.Sprint(dead)
	}
	if err != nil {
		p.t.Fatalf("Go's %s of %s: %v", step, c.ID, err)
	}
	return out
}

func changedJSON(order string, line int, priority string) string {
	m := map[string]any{"orderId": order, "priority": priority}
	if line > 0 {
		m["lineId"] = lineID(line)
	}
	data, _ := json.Marshal(m)
	return string(data)
}

// testReplaceKeepsOneReadyMessage: five enqueues of one order, by every
// side in turn, leave one ready message, the last one's.
func testReplaceKeepsOneReadyMessage(t *testing.T) {
	p := newParity(t)
	p.on = "OrderChanged"
	for i, side := range []string{"Go", "TypeScript", "Rust", "Go", "TypeScript"} {
		p.keyedEnqueue(side, changedJSON(orderA, i+1, "high"))
	}
	p.keyedEnqueue("Rust", changedJSON(orderB, 0, "low"))
	wantRows(t, "five enqueues of order A", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "ready:"+lineID(5))
	wantRows(t, "one of order B", p.rows("order_changed_queue", "order_id", orderB, "line_id"), "ready:")
	var attempts int
	var lastError string
	p.queryRow(p.dialectSQL(
		"SELECT attempts, COALESCE(last_error, '') FROM order_changed_queue WHERE order_id = $1::uuid",
		"SELECT attempts, COALESCE(last_error, '') FROM order_changed_queue WHERE order_id = ?1",
	), []any{orderA}, &attempts, &lastError)
	if attempts != 0 || lastError != "" {
		t.Fatalf("the replaced message has %d attempts and last error %q, want none", attempts, lastError)
	}
	claims := p.keyedClaims("Rust", 10)
	if len(claims) != 2 {
		t.Fatalf("Rust claimed %+v, want both orders' messages", claims)
	}
	for _, c := range claims {
		if c.Members["orderId"] == fmt.Sprint(mustUUID(orderA)) && (c.Members["lineId"] != fmt.Sprint(mustUUID(lineID(5))) || c.Members["priority"] != "high" || c.Attempt != 1) {
			t.Fatalf("order A's claim is %+v, want the fifth line's first attempt", c)
		}
	}
}

// testSkipKeepsTheFirst: the three sides ask for one SKU's count in turn;
// the first ask waits alone, and once claimed a new one waits beside it.
func testSkipKeepsTheFirst(t *testing.T) {
	p := newParity(t)
	p.on = "StockChecked"
	for i, side := range []string{"Go", "TypeScript", "Rust"} {
		p.keyedEnqueue(side, fmt.Sprintf("{\"sku\":\"SKU-1\",\"quantity\":%d}", i+1))
	}
	wantRows(t, "three asks", p.rows("stock_checked_queue", "sku", "SKU-1", "quantity"), "ready:1")
	claims := p.keyedClaims("TypeScript", 5)
	if len(claims) != 1 || claims[0].Members["quantity"] != "1" {
		t.Fatalf("TypeScript claimed %+v, want the first ask", claims)
	}
	p.keyedEnqueue("Rust", "{\"sku\":\"SKU-1\",\"quantity\":4}")
	p.keyedEnqueue("Go", "{\"sku\":\"SKU-1\",\"quantity\":5}")
	wantRows(t, "asks while one is claimed", p.rows("stock_checked_queue", "sku", "SKU-1", "quantity"), "claimed:1", "ready:4")
}

// testFailedAndReleasedGiveWay: a claimed message that fails or is released
// while a newer one of its order waits is done, its cause kept, and the
// newer one runs; with none waiting, a failure makes it ready again.
func testFailedAndReleasedGiveWay(t *testing.T) {
	for _, pair := range [][2]string{{"TypeScript", "Go"}, {"Rust", "TypeScript"}, {"Go", "Rust"}} {
		claimer, changer := pair[0], pair[1]
		t.Run(claimer+"ClaimsAnd"+changer+"Changes", func(t *testing.T) {
			p := newParity(t)
			p.on = "OrderChanged"
			p.keyedEnqueue("Go", changedJSON(orderA, 1, "low"))
			first := p.keyedClaims(claimer, 1)
			p.keyedEnqueue("Rust", changedJSON(orderA, 2, "low"))
			wantRows(t, "a change while the first is claimed", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "claimed:"+lineID(1), "ready:"+lineID(2))
			if dead := p.keyedChange(changer, "fail", first[0]); dead != "false" && dead != "" {
				t.Fatalf("%s's fail of a message that gives way says it is dead: %s", changer, dead)
			}
			wantRows(t, "the first failed", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "done:"+lineID(1), "ready:"+lineID(2))
			_, _, lastError := p.changedState(first[0].ID)
			if lastError != "it broke" {
				t.Fatalf("the message that gave way keeps %q, want its cause", lastError)
			}
			second := p.keyedClaims(changer, 1)
			if len(second) != 1 || second[0].Members["lineId"] != fmt.Sprint(mustUUID(lineID(2))) {
				t.Fatalf("%s claimed %+v, want the second line's message", changer, second)
			}
			p.keyedEnqueue("TypeScript", changedJSON(orderA, 3, "low"))
			p.keyedChange(claimer, "release", second[0])
			wantRows(t, "the second released", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "done:"+lineID(1), "done:"+lineID(2), "ready:"+lineID(3))
			third := p.keyedClaims(claimer, 1)
			p.keyedChange(changer, "fail", third[0])
			wantRows(t, "the third failed alone", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "done:"+lineID(1), "done:"+lineID(2), "ready:"+lineID(3))
		})
	}
}

// changedState is an OrderChanged message's state, attempts and last error.
func (p *parity) changedState(id string) (string, int, string) {
	p.t.Helper()
	var s, lastError string
	var attempts int
	p.queryRow(p.dialectSQL(
		"SELECT state, attempts, COALESCE(last_error, '') FROM order_changed_queue WHERE id = $1::uuid",
		"SELECT state, attempts, COALESCE(last_error, '') FROM order_changed_queue WHERE id = ?1",
	), []any{id}, &s, &attempts, &lastError)
	return s, attempts, lastError
}

// testSerializeHoldsOneClaimPerKey: no side claims an order's waiting
// message while another holds a claim of the order, and a claim that
// expired is claimed again before the message that waits.
func testSerializeHoldsOneClaimPerKey(t *testing.T) {
	p := newParity(t)
	p.on = "OrderChanged"
	p.keyedEnqueue("Go", changedJSON(orderA, 1, "low"))
	p.keyedEnqueue("TypeScript", changedJSON(orderB, 1, "low"))
	if claims := p.keyedClaims("TypeScript", 10); len(claims) != 2 {
		t.Fatalf("TypeScript claimed %+v, want both orders'", claims)
	}
	p.keyedEnqueue("Rust", changedJSON(orderA, 2, "low"))
	for _, side := range []string{"Go", "TypeScript", "Rust"} {
		if claims := p.keyedClaims(side, 10); len(claims) != 0 {
			t.Fatalf("%s claimed %+v while claims hold both orders", side, claims)
		}
	}
	p.exec(p.dialectSQL(
		"UPDATE order_changed_queue SET claim_expires_at = now() - interval '1 second' WHERE state = 'claimed' AND order_id = $1::uuid",
		"UPDATE order_changed_queue SET claim_expires_at = "+sqliteAgo(1)+" WHERE state = 'claimed' AND order_id = ?1",
	), orderA)
	again := p.keyedClaims("Rust", 10)
	if len(again) != 1 || again[0].Attempt != 2 || again[0].Members["lineId"] != fmt.Sprint(mustUUID(lineID(1))) {
		t.Fatalf("Rust claimed %+v, want the expired claim's message again, its second attempt", again)
	}
	if claims := p.keyedClaims("Go", 10); len(claims) != 0 {
		t.Fatalf("Go claimed %+v while Rust's claim holds order A", claims)
	}
	p.keyedChange("TypeScript", "complete", again[0])
	next := p.keyedClaims("Go", 10)
	if len(next) != 1 || next[0].Members["lineId"] != fmt.Sprint(mustUUID(lineID(2))) {
		t.Fatalf("Go claimed %+v, want order A's waiting message once its claim is done", next)
	}
}

// testSerializeTakesTheHeadOfEachKey: of one account's requests, which an
// appending queue keeps, each claim takes the first due, and another
// account's beside it.
func testSerializeTakesTheHeadOfEachKey(t *testing.T) {
	p := newParity(t)
	p.on = "SyncRequested"
	request := func(account, reason string) string {
		return fmt.Sprintf("{\"accountId\":%q,\"reason\":%q}", account, reason)
	}
	p.keyedEnqueue("Go", request(orderA, "r1"))
	p.keyedEnqueue("TypeScript", request(orderA, "r2"))
	p.keyedEnqueue("Rust", request(orderA, "r3"))
	p.keyedEnqueue("Go", request(orderB, "r4"))
	first := p.keyedClaims("Go", 10)
	reasons := map[string]bool{}
	for _, c := range first {
		reasons[c.Members["reason"]] = true
	}
	if len(first) != 2 || !reasons["r1"] || !reasons["r4"] {
		t.Fatalf("Go claimed %+v, want each account's first request", first)
	}
	if claims := p.keyedClaims("Rust", 10); len(claims) != 0 {
		t.Fatalf("Rust claimed %+v while both accounts are held", claims)
	}
	q := p.syncQueue()
	for _, c := range first {
		if c.Members["reason"] == "r1" {
			if err := q.Complete(p.ctx, c.ID, c.Token); err != nil {
				t.Fatal(err)
			}
		}
	}
	second := p.keyedClaims("TypeScript", 10)
	if len(second) != 1 || second[0].Members["reason"] != "r2" {
		t.Fatalf("TypeScript claimed %+v, want the second request", second)
	}
	// A failure waits its backoff, so the third request is the first due.
	if _, err := q.Fail(p.ctx, second[0].ID, second[0].Token, "try later"); err != nil {
		t.Fatal(err)
	}
	third := p.keyedClaims("Rust", 10)
	if len(third) != 1 || third[0].Members["reason"] != "r3" {
		t.Fatalf("Rust claimed %+v, want the third request while the second waits its backoff", third)
	}
}

// testConcurrentClaimsNeverShareAKey: eight Go workers claim and complete
// sixty requests of four accounts at once. No two ever hold claims of one
// account, every claim succeeds, and every request is done once.
func testConcurrentClaimsNeverShareAKey(t *testing.T) {
	p := newParity(t)
	p.on = "SyncRequested"
	accounts := []string{orderA, orderB, lineID(7), lineID(8)}
	for i := 0; i < 60; i++ {
		p.keyedEnqueue("Go", fmt.Sprintf("{\"accountId\":%q,\"reason\":\"r%d\"}", accounts[i%len(accounts)], i))
	}
	q := p.syncQueue()
	var mu sync.Mutex
	held := map[string]bool{}
	handled := 0
	var failure error
	deadline := time.Now().Add(60 * time.Second)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				mu.Lock()
				done := handled >= 60 || failure != nil
				mu.Unlock()
				if done {
					return
				}
				claims, err := q.Claim(p.ctx, 3, 30*time.Second)
				if err != nil {
					mu.Lock()
					failure = fmt.Errorf("a claim failed: %w", err)
					mu.Unlock()
					return
				}
				for _, c := range claims {
					key := fmt.Sprint(c.Message.AccountId)
					mu.Lock()
					if held[key] {
						failure = fmt.Errorf("two workers hold claims of account %s", key)
					}
					held[key] = true
					mu.Unlock()
					time.Sleep(2 * time.Millisecond)
					// The key is let go before the claim is done, so a worker
					// that claims it next finds it free.
					mu.Lock()
					delete(held, key)
					handled++
					mu.Unlock()
					if err := q.Complete(p.ctx, c.ID, c.Token); err != nil {
						mu.Lock()
						failure = fmt.Errorf("a complete failed: %w", err)
						mu.Unlock()
					}
				}
				if len(claims) == 0 {
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	if failure != nil {
		t.Fatal(failure)
	}
	var done int
	p.queryRow("SELECT count(*) FROM sync_requested_queue WHERE state = 'done'", nil, &done)
	if handled != 60 || done != 60 {
		t.Fatalf("the workers handled %d requests and %d are done, want all 60 once", handled, done)
	}
}

// testTriggerEnqueuesOnChange: OrderLine's @enqueueOnChange of OrderChanged
// under raw SQL. A line with no product enqueues nothing; setting its
// product enqueues exactly one message; a change to its note, which the
// declaration does not name, enqueues none; and repeated changes of its
// product and quantity, and its delete, keep one ready message.
func testTriggerEnqueuesOnChange(t *testing.T) {
	p := newParity(t)
	const productOne, productTwo = "20000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002"
	line := lineID(1)
	p.exec("INSERT INTO \"order\" (id, placed_at) VALUES ('" + orderA + "', '2026-10-14T12:00:00.000Z')")
	p.exec("INSERT INTO product (id, name) VALUES ('" + productOne + "', 'one'), ('" + productTwo + "', 'two')")
	count := func() int {
		var n int
		p.queryRow("SELECT count(*) FROM order_changed_queue", nil, &n)
		return n
	}
	p.exec("INSERT INTO order_line (id, order_id, product_id, quantity) VALUES ('" + line + "', '" + orderA + "', NULL, 1)")
	if n := count(); n != 0 {
		t.Fatalf("a line with no product enqueued %d messages, want none", n)
	}
	p.exec("UPDATE order_line SET product_id = '" + productOne + "' WHERE id = '" + line + "'")
	wantRows(t, "the line's product set", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "ready:"+line)
	var priority string
	p.queryRow("SELECT priority FROM order_changed_queue", nil, &priority)
	if priority != "low" {
		t.Fatalf("the trigger's message has priority %q, want the enum's default, low", priority)
	}
	p.on = "OrderChanged"
	claimed := p.keyedClaims("Go", 1)
	if len(claimed) != 1 || claimed[0].Members["lineId"] != fmt.Sprint(mustUUID(line)) {
		t.Fatalf("Go claimed %+v, want the trigger's message", claimed)
	}
	p.exec("UPDATE order_line SET note = 'by the door' WHERE id = '" + line + "'")
	wantRows(t, "the line's note changed", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "claimed:"+line)
	p.exec("UPDATE order_line SET quantity = 2 WHERE id = '" + line + "'")
	wantRows(t, "the line's quantity changed", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "claimed:"+line, "ready:"+line)
	p.exec("UPDATE order_line SET quantity = 3 WHERE id = '" + line + "'")
	p.exec("UPDATE order_line SET product_id = '" + productTwo + "' WHERE id = '" + line + "'")
	p.exec("UPDATE order_line SET quantity = 4, note = 'again' WHERE id = '" + line + "'")
	wantRows(t, "repeated changes", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "claimed:"+line, "ready:"+line)
	p.exec("DELETE FROM order_line WHERE id = '" + line + "'")
	wantRows(t, "the line deleted", p.rows("order_changed_queue", "order_id", orderA, "line_id"), "claimed:"+line, "ready:"+line)
	if n := count(); n != 2 {
		t.Fatalf("the queue holds %d messages, want the claimed one and the one that waits", n)
	}
}
`
