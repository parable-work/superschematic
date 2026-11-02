package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// timersORMCases are the cases the generated module's test runs over
// fixture-timers-db, by name: the repository's share of the cases every
// language's Timers end-to-end test holds (D103), and RunDeadline's and
// RunChange's, which a behavior worker's handlers call.
var timersORMCases = []string{
	"a create arms the deadline: its sub-table row and its queue message with its due time",
	"a status that leaves the state cancels the deadline by until",
	"a field start arms its deadline from the field, and a write that moves the field moves it",
	"a table with two Workflows reads the one its config names",
	"RunDeadline with a claim fires a due deadline, moves the status and completes its message",
	"RunDeadline without a claim runs the deadline armed at the time it names, and nothing for another",
	"RunDeadline on a claim another command moved runs nothing",
	"RunDeadline of a row a raw delete removed completes its message as gone",
	"RunChange of a raw write of a field start arms its deadline again and completes its message",
	"RunChange of a raw delete clears the row's deadline messages",
	"a change message that failed holds its row's later messages until it runs",
	"deleting the row removes its deadlines",
}

// TestTimersTablesOnPostgres holds the generated Go ORM of a database whose
// tables implement Timers to the rules module's adapter on Postgres (D103),
// as TestWorkflowTablesOnPostgres holds a database whose fields are
// Workflows. It loads fixture-timers-db with the core's registry,
// generates its Go types, its ORM and its DDL into a temporary tree,
// checks that the ORM has no repository of a sub-table and no upsert of a
// table whose behaviors keep one, and that TicketCreate holds no field of
// Timers, and builds, vets and tests the ORM module with timersORMTest,
// whose every case (timersORMCases) must pass on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The module links the core's
// static archive, which scripts/rules-archive.sh stages; the test skips
// without it, and in -short mode.
func TestTimersTablesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Timers ORM's end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated Timers ORM against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateBehaviorORMModule(t, paths, timersFixture)
	for _, file := range []string{"repository_ticket_timers_deadlines.go", "repository_invoice_timers_deadlines.go"} {
		if _, err := os.Stat(filepath.Join(ormDir, file)); err == nil {
			t.Errorf("the generated ORM holds %s: a behavior's sub-table has no repository", file)
		}
	}
	repository, err := os.ReadFile(filepath.Join(ormDir, "repository_ticket.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"UpsertOne(", "UpsertOneResult(", "UpsertMany("} {
		if strings.Contains(string(repository), ") "+method) {
			t.Errorf("TicketRepository has %s: a table whose behaviors keep sub-tables or arm deadlines has no upsert", strings.TrimSuffix(method, "("))
		}
	}
	if members := structMembers(t, ormDir, "TicketCreate"); len(members) == 0 || slices.Contains(members, "Timers") || slices.Contains(members, "Status") || slices.Contains(members, "Version") {
		t.Errorf("TicketCreate's members are %v: it holds no field a behavior owns", members)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "timers_test.go"), []byte(timersORMTest), 0o644); err != nil {
		t.Fatalf("write the Timers test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range timersORMCases {
		if want := "--- PASS: TestTimersTablesOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// timersORMTest is the generated module's test: Ticket's and Invoice's
// deadlines through the generated repository, in a schema of its own,
// read back past it with SQL.
const timersORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-timers-db"
	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// subRow is a row of a Timers sub-table as the database holds it, its
// times in epoch milliseconds, 0 for NULL.
type subRow struct {
	baseAt, dueAt, firedAt int64
	armedFrom, outcome     string
	fired                  bool
}

// message is a live message of a deadline queue, its due time in epoch
// milliseconds.
type message struct {
	at, dueAt int64
}

func millis(at *types.TemporalDateTime) int64 {
	if at == nil {
		return 0
	}
	return time.Time(*at).UnixMilli()
}

func TestTimersTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Timers tables against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_timers_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	// The behavior worker's caller, and a clerk who may reset and snooze.
	worker := WithBehaviorCaller(ctx, rules.Caller{Subject: "fixture-timers-api/TicketTimers"})
	clerk := WithBehaviorCaller(ctx, rules.Caller{Subject: "kim", Permissions: []string{"tickets.timers"}})

	// subRows are a row's rows of a Timers sub-table, by name.
	subRows := func(t *testing.T, table, row string, id types.IdentityUUID) map[string]subRow {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT name, base_at, armed_from, due_at, fired, fired_at, outcome FROM "+table+" WHERE "+row+"_id = $1", id.ToUUID().String())
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		defer rows.Close()
		out := map[string]subRow{}
		for rows.Next() {
			var name string
			var baseAt, dueAt, firedAt *time.Time
			var armedFrom, outcome *string
			var fired *bool
			if err := rows.Scan(&name, &baseAt, &armedFrom, &dueAt, &fired, &firedAt, &outcome); err != nil {
				t.Fatalf("read %s: %v", table, err)
			}
			var r subRow
			if baseAt != nil {
				r.baseAt = baseAt.UnixMilli()
			}
			if dueAt != nil {
				r.dueAt = dueAt.UnixMilli()
			}
			if firedAt != nil {
				r.firedAt = firedAt.UnixMilli()
			}
			if armedFrom != nil {
				r.armedFrom = *armedFrom
			}
			if outcome != nil {
				r.outcome = *outcome
			}
			r.fired = fired != nil && *fired
			out[name] = r
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// live are a row's live messages of a deadline queue, by name; all
	// counts its messages in any state.
	live := func(t *testing.T, queue string, id types.IdentityUUID) map[string]message {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT name, at, due_at FROM "+queue+" WHERE row_key = $1 AND state IN ('ready', 'claimed')", id.ToUUID().String())
		if err != nil {
			t.Fatalf("read %s: %v", queue, err)
		}
		defer rows.Close()
		out := map[string]message{}
		for rows.Next() {
			var name string
			var at int64
			var dueAt time.Time
			if err := rows.Scan(&name, &at, &dueAt); err != nil {
				t.Fatalf("read %s: %v", queue, err)
			}
			if _, twice := out[name]; twice {
				t.Fatalf("%s holds two live messages of %s", queue, name)
			}
			out[name] = message{at: at, dueAt: dueAt.UnixMilli()}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	all := func(t *testing.T, queue string, id types.IdentityUUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+queue+" WHERE row_key = $1", id.ToUUID().String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	create := func(t *testing.T, input *TicketCreate) types.IdentityUUID {
		t.Helper()
		row, err := db.Ticket.CreateOne(ctx, input)
		if err != nil {
			t.Fatalf("create a ticket: %v", err)
		}
		return *row.Id
	}
	status := func(t *testing.T, id types.IdentityUUID) string {
		t.Helper()
		row, err := db.Ticket.GetOne(ctx, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		return row.Status
	}
	// claim makes the row's live message of name due and claims it, as a
	// worker would, giving back any other message the claim took.
	claim := func(t *testing.T, id types.IdentityUUID, name string) TicketDeadlineClaim {
		t.Helper()
		if _, err := pool.Exec(ctx, "UPDATE ticket_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = $2 AND state = 'ready'", id.ToUUID().String(), name); err != nil {
			t.Fatal(err)
		}
		claims, err := db.TicketDeadlineQueue().Claim(ctx, 10, time.Minute)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		var found *TicketDeadlineClaim
		for i := range claims {
			if claims[i].Message.RowKey == id && claims[i].Message.Name == name {
				found = &claims[i]
				continue
			}
			if err := db.TicketDeadlineQueue().Release(ctx, claims[i].ID, claims[i].Token); err != nil {
				t.Fatal(err)
			}
		}
		if found == nil {
			t.Fatalf("no message of %s was claimed", name)
		}
		return *found
	}

	t.Run("a create arms the deadline: its sub-table row and its queue message with its due time", func(t *testing.T) {
		before := time.Now().Add(-time.Second).UnixMilli()
		row, err := db.Ticket.CreateOne(ctx, &TicketCreate{Title: "Printer"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		id := *row.Id
		subs := subRows(t, "ticket_timers_deadlines", "ticket", id)
		respond, ok := subs["respond"]
		if !ok || len(subs) != 1 {
			t.Fatalf("the sub-table rows are %+v, want respond alone", subs)
		}
		if respond.baseAt != millis(row.StatusEnteredAt) || respond.baseAt < before || respond.dueAt != respond.baseAt+1500 || respond.fired || respond.armedFrom != fmt.Sprint(respond.baseAt) {
			t.Fatalf("respond = %+v, want due 1500 ms after the create at %d, not fired", respond, millis(row.StatusEnteredAt))
		}
		messages := live(t, "ticket_deadline_queue", id)
		if len(messages) != 1 || messages["respond"] != (message{at: respond.dueAt, dueAt: respond.dueAt}) {
			t.Fatalf("the live messages are %+v, want respond's alone, at and due at %d", messages, respond.dueAt)
		}
	})

	t.Run("a status that leaves the state cancels the deadline by until", func(t *testing.T) {
		id := create(t, &TicketCreate{Title: "Router"})
		if _, err := db.Ticket.Status().Transition(ctx, id, TicketStatusTransitionParams{To: TicketStatusStateWaiting}); err != nil {
			t.Fatalf("transition: %v", err)
		}
		row, err := db.Ticket.GetOne(ctx, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		subs := subRows(t, "ticket_timers_deadlines", "ticket", id)
		if respond := subs["respond"]; respond.dueAt != 0 || respond.fired {
			t.Fatalf("respond = %+v, want cancelled: no due time, not fired", respond)
		}
		idle := subs["idle"]
		if idle.dueAt != millis(row.StatusEnteredAt)+3600000 || idle.armedFrom != fmt.Sprint(millis(row.StatusEnteredAt)) {
			t.Fatalf("idle = %+v, want due an hour after waiting began at %d", idle, millis(row.StatusEnteredAt))
		}
		messages := live(t, "ticket_deadline_queue", id)
		if len(messages) != 1 || messages["idle"].at != idle.dueAt {
			t.Fatalf("the live messages are %+v, want idle's alone, at %d", messages, idle.dueAt)
		}
	})

	t.Run("a field start arms its deadline from the field, and a write that moves the field moves it", func(t *testing.T) {
		visit := time.Now().Add(48 * time.Hour).Truncate(time.Millisecond).UTC()
		at := types.TemporalDateTime(visit)
		id := create(t, &TicketCreate{Title: "Visit", VisitAt: &at})
		remind := subRows(t, "ticket_timers_deadlines", "ticket", id)["remind"]
		if remind.dueAt != visit.UnixMilli()-86400000 || remind.baseAt != visit.UnixMilli() {
			t.Fatalf("remind = %+v, want due a day before the visit at %d", remind, visit.UnixMilli())
		}
		if got := live(t, "ticket_deadline_queue", id)["remind"]; got.at != remind.dueAt {
			t.Fatalf("remind's message = %+v, want at %d", got, remind.dueAt)
		}
		moved := types.TemporalDateTime(visit.Add(24 * time.Hour))
		if _, err := db.Ticket.UpdateOne(ctx, id, &TicketUpdate{VisitAt: &moved}); err != nil {
			t.Fatalf("update: %v", err)
		}
		remind = subRows(t, "ticket_timers_deadlines", "ticket", id)["remind"]
		want := time.Time(moved).UnixMilli() - 86400000
		if remind.dueAt != want {
			t.Fatalf("remind = %+v, want due at %d, a day before the moved visit", remind, want)
		}
		if got := live(t, "ticket_deadline_queue", id)["remind"]; got.at != want {
			t.Fatalf("remind's message = %+v, want it moved to %d", got, want)
		}
	})

	t.Run("a table with two Workflows reads the one its config names", func(t *testing.T) {
		row, err := db.Invoice.CreateOne(ctx, &InvoiceCreate{Number: "I-1"})
		if err != nil {
			t.Fatalf("create the invoice: %v", err)
		}
		id := *row.Id
		overdue := subRows(t, "invoice_timers_deadlines", "invoice", id)["overdue"]
		if overdue.dueAt != overdue.baseAt+2592000000 || overdue.baseAt == 0 {
			t.Fatalf("overdue = %+v, want due thirty days after the create", overdue)
		}
		if _, err := db.Invoice.Status().Transition(ctx, id, InvoiceStatusTransitionParams{To: InvoiceStatusStateIssued}); err != nil {
			t.Fatalf("issue: %v", err)
		}
		if got := live(t, "invoice_deadline_queue", id); got["overdue"].at != overdue.dueAt {
			t.Fatalf("after its status moved, the live messages are %+v, want overdue's kept", got)
		}
		if _, err := db.Invoice.Payment().Transition(ctx, id, InvoicePaymentTransitionParams{To: InvoicePaymentStatePaid}); err != nil {
			t.Fatalf("pay: %v", err)
		}
		if got := subRows(t, "invoice_timers_deadlines", "invoice", id)["overdue"]; got.dueAt != 0 {
			t.Fatalf("overdue = %+v, want cancelled once payment left unpaid", got)
		}
		if got := live(t, "invoice_deadline_queue", id); len(got) != 0 {
			t.Fatalf("the live messages are %+v, want none", got)
		}
	})

	t.Run("RunDeadline with a claim fires a due deadline, moves the status and completes its message", func(t *testing.T) {
		id := create(t, &TicketCreate{Title: "Desk"})
		c := claim(t, id, "respond")
		run, err := db.Ticket.RunDeadline(worker, c.Message, &rules.Claim{ID: c.ID, Token: c.Token})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		// A deadline command returns nothing; what it did is the row's.
		if !run.Ran || run.Result == nil || string(run.Result.Result) != "null" {
			t.Fatalf("run = %+v, want it run", run)
		}
		if got := status(t, id); got != "escalated" {
			t.Fatalf("status = %s, want escalated", got)
		}
		respond := subRows(t, "ticket_timers_deadlines", "ticket", id)["respond"]
		if !respond.fired || respond.outcome != "moved" || respond.firedAt == 0 {
			t.Fatalf("respond = %+v, want fired, moved", respond)
		}
		if got := all(t, "ticket_deadline_queue", id); got != 0 {
			t.Fatalf("the row has %d messages, want none: the run completed its claim", got)
		}
	})

	t.Run("RunDeadline without a claim runs the deadline armed at the time it names, and nothing for another", func(t *testing.T) {
		id := create(t, &TicketCreate{Title: "Lamp"})
		c := claim(t, id, "respond")
		if err := db.TicketDeadlineQueue().Release(ctx, c.ID, c.Token); err != nil {
			t.Fatal(err)
		}
		other := c.Message
		other.At++
		run, err := db.Ticket.RunDeadline(worker, other, nil)
		if refusal, ok := rules.AsRefusal(err); !ok || refusal.Code != rules.CodeNotFound || run != nil {
			t.Fatalf("a run at another time = %+v, %v, want the not_found refusal of an arming not there", run, err)
		}
		if got := status(t, id); got != "open" {
			t.Fatalf("status = %s, want open", got)
		}
		run, err = db.Ticket.RunDeadline(worker, c.Message, nil)
		if err != nil || !run.Ran {
			t.Fatalf("a run at its time = %+v, %v, want it run", run, err)
		}
		if got := status(t, id); got != "escalated" {
			t.Fatalf("status = %s, want escalated", got)
		}
		if got := all(t, "ticket_deadline_queue", id); got != 0 {
			t.Fatalf("the row has %d messages, want none", got)
		}
	})

	t.Run("RunDeadline on a claim another command moved runs nothing", func(t *testing.T) {
		id := create(t, &TicketCreate{Title: "Chair"})
		if _, err := db.Ticket.Status().Transition(ctx, id, TicketStatusTransitionParams{To: TicketStatusStateWaiting}); err != nil {
			t.Fatalf("transition: %v", err)
		}
		c := claim(t, id, "idle")
		snoozed := time.Now().Add(2 * time.Hour).UnixMilli()
		result, err := db.Ticket.Timers().SnoozeTimer(clerk, id, TicketTimersSnoozeTimerParams{Deadline: TicketTimersDeadlineIdle, DueAt: snoozed})
		if err != nil || result.DueAt != snoozed {
			t.Fatalf("snooze = %+v, %v, want due at %d", result, err, snoozed)
		}
		run, err := db.Ticket.RunDeadline(worker, c.Message, &rules.Claim{ID: c.ID, Token: c.Token})
		if err != nil || run.Ran || run.Why != rules.WhyClaimLost {
			t.Fatalf("run on the old claim = %+v, %v, want nothing run, its claim lost", run, err)
		}
		if got := status(t, id); got != "waiting" {
			t.Fatalf("status = %s, want waiting: the old arming fired nothing", got)
		}
		if idle := subRows(t, "ticket_timers_deadlines", "ticket", id)["idle"]; idle.dueAt != snoozed || idle.fired {
			t.Fatalf("idle = %+v, want due at the snooze, %d", idle, snoozed)
		}
		if got := live(t, "ticket_deadline_queue", id); len(got) != 1 || got["idle"].at != snoozed {
			t.Fatalf("the live messages are %+v, want idle's alone, at the snooze", got)
		}
	})

	t.Run("RunDeadline of a row a raw delete removed completes its message as gone", func(t *testing.T) {
		id := create(t, &TicketCreate{Title: "Shelf"})
		c := claim(t, id, "respond")
		if _, err := pool.Exec(ctx, "DELETE FROM ticket WHERE id = $1", id.ToUUID().String()); err != nil {
			t.Fatal(err)
		}
		run, err := db.Ticket.RunDeadline(worker, c.Message, &rules.Claim{ID: c.ID, Token: c.Token})
		if err != nil || run.Ran || run.Why != rules.WhyGone || run.Refusal == nil || run.Refusal.Code != rules.CodeNotFound {
			t.Fatalf("run = %+v, %v, want nothing run, the row gone", run, err)
		}
		if got := all(t, "ticket_deadline_queue", id); got != 0 {
			t.Fatalf("the row has %d messages, want its claimed one completed", got)
		}
	})

	// runChanges runs the row's messages of Ticket's change queue, as its
	// worker's handler does, each with its claim, in the order the queue
	// gives them, the head of the row's line first; it gives back every
	// other row's message the claim took. A run that runs nothing fails
	// the test, and so does a run of the claim it spent.
	runChanges := func(t *testing.T, id types.IdentityUUID) {
		t.Helper()
		for round := 0; round < 20 && all(t, "ticket_change_queue", id) > 0; round++ {
			claims, err := db.TicketChangeQueue().Claim(ctx, 10, time.Minute)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			for _, c := range claims {
				if c.Message.RowKey != id {
					if err := db.TicketChangeQueue().Release(ctx, c.ID, c.Token); err != nil {
						t.Fatal(err)
					}
					continue
				}
				run, err := db.Ticket.RunChange(worker, c.Message, &rules.Claim{ID: c.ID, Token: c.Token})
				if err != nil || !run.Ran {
					t.Fatalf("RunChange of %s's %s = %+v, %v, want run", id, c.Message.Write, run, err)
				}
				again, err := db.Ticket.RunChange(worker, c.Message, &rules.Claim{ID: c.ID, Token: c.Token})
				if err != nil || again.Ran || again.Why != rules.WhyClaimLost {
					t.Fatalf("RunChange of a spent claim = %+v, %v, want nothing run, the claim lost", again, err)
				}
			}
		}
		if n := all(t, "ticket_change_queue", id); n != 0 {
			t.Fatalf("the row keeps %d change messages, want them run", n)
		}
	}

	t.Run("RunChange of a raw write of a field start arms its deadline again and completes its message", func(t *testing.T) {
		visit := time.Now().Add(48 * time.Hour).Truncate(time.Millisecond)
		at := types.TemporalDateTime(visit)
		id := create(t, &TicketCreate{Title: "Visit", VisitAt: &at})
		before := subRows(t, "ticket_timers_deadlines", "ticket", id)["remind"]
		if before.dueAt != visit.UnixMilli()-86400000 {
			t.Fatalf("remind = %+v, want due a day before the visit", before)
		}
		// The create's own write, heard, arms nothing again.
		runChanges(t, id)
		if after := subRows(t, "ticket_timers_deadlines", "ticket", id)["remind"]; after != before {
			t.Fatalf("remind = %+v after the create was heard, want it as the create left it, %+v", after, before)
		}
		later := visit.Add(24 * time.Hour)
		if _, err := pool.Exec(ctx, "UPDATE ticket SET visit_at = $2 WHERE id = $1", id.ToUUID().String(), later); err != nil {
			t.Fatal(err)
		}
		if n := all(t, "ticket_change_queue", id); n != 1 {
			t.Fatalf("the raw write left %d change messages, want its one", n)
		}
		runChanges(t, id)
		want := later.UnixMilli() - 86400000
		if remind := subRows(t, "ticket_timers_deadlines", "ticket", id)["remind"]; remind.dueAt != want || remind.fired {
			t.Fatalf("remind = %+v, want due a day before the new visit, %d", remind, want)
		}
		if messages := live(t, "ticket_deadline_queue", id); messages["remind"] != (message{at: want, dueAt: want}) {
			t.Fatalf("the live messages are %+v, want remind's at and due at %d", messages, want)
		}
	})

	t.Run("RunChange of a raw delete clears the row's deadline messages", func(t *testing.T) {
		visit := types.TemporalDateTime(time.Now().Add(96 * time.Hour).Truncate(time.Millisecond))
		id := create(t, &TicketCreate{Title: "Gone", VisitAt: &visit})
		if got := all(t, "ticket_deadline_queue", id); got != 2 {
			t.Fatalf("the row has %d deadline messages, want respond's and remind's", got)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM ticket WHERE id = $1", id.ToUUID().String()); err != nil {
			t.Fatal(err)
		}
		runChanges(t, id)
		if got := all(t, "ticket_deadline_queue", id); got != 0 {
			t.Fatalf("the deleted row keeps %d deadline messages, want none", got)
		}
	})

	t.Run("a change message that failed holds its row's later messages until it runs", func(t *testing.T) {
		id := create(t, &TicketCreate{Title: "Order"})
		runChanges(t, id)
		for _, title := range []string{"First", "Second"} {
			if _, err := pool.Exec(ctx, "UPDATE ticket SET title = $2 WHERE id = $1", id.ToUUID().String(), title); err != nil {
				t.Fatal(err)
			}
		}
		// mine claims the row's messages the queue gives, and gives back
		// every other row's.
		mine := func(t *testing.T) []TicketChangeClaim {
			t.Helper()
			claims, err := db.TicketChangeQueue().Claim(ctx, 10, time.Minute)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			var out []TicketChangeClaim
			for _, c := range claims {
				if c.Message.RowKey != id {
					if err := db.TicketChangeQueue().Release(ctx, c.ID, c.Token); err != nil {
						t.Fatal(err)
					}
					continue
				}
				out = append(out, c)
			}
			return out
		}
		var first string
		if err := pool.QueryRow(ctx, "SELECT id::text FROM ticket_change_queue WHERE row_key = $1 ORDER BY created_at LIMIT 1", id.ToUUID().String()).Scan(&first); err != nil {
			t.Fatal(err)
		}
		head := mine(t)
		if len(head) != 1 || head[0].ID != first {
			t.Fatalf("the claim took %d of the row's messages, want its first written, %s, alone", len(head), first)
		}
		if dead, err := db.TicketChangeQueue().Fail(ctx, head[0].ID, head[0].Token, "a failure"); err != nil || dead {
			t.Fatalf("fail = %v, %v, want it ready again after its backoff", dead, err)
		}
		// The failed message waits its backoff, and the row's later one,
		// due, waits behind it.
		if claimed := mine(t); len(claimed) != 0 {
			t.Fatalf("the claim took %d of the row's messages while its first waits its backoff, want none", len(claimed))
		}
		if _, err := pool.Exec(ctx, "UPDATE ticket_change_queue SET due_at = now() WHERE id = $1::uuid", first); err != nil {
			t.Fatal(err)
		}
		again := mine(t)
		if len(again) != 1 || again[0].ID != first {
			t.Fatalf("the claim took %d of the row's messages once the first fell due, want the first alone", len(again))
		}
		if err := db.TicketChangeQueue().Release(ctx, again[0].ID, again[0].Token); err != nil {
			t.Fatal(err)
		}
		runChanges(t, id)
	})

	t.Run("deleting the row removes its deadlines", func(t *testing.T) {
		visit := types.TemporalDateTime(time.Now().Add(72 * time.Hour).UTC())
		id := create(t, &TicketCreate{Title: "Bin", VisitAt: &visit})
		if got := len(subRows(t, "ticket_timers_deadlines", "ticket", id)); got != 2 {
			t.Fatalf("the row has %d sub-table rows, want respond and remind", got)
		}
		if err := db.Ticket.DeleteOne(ctx, id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if got := subRows(t, "ticket_timers_deadlines", "ticket", id); len(got) != 0 {
			t.Fatalf("the sub-table rows are %+v, want none", got)
		}
		if got := all(t, "ticket_deadline_queue", id); got != 0 {
			t.Fatalf("the row has %d messages, want none", got)
		}
	})
}
`
