package rules_test

import (
	"context"
	"fmt"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// A behavior's own events through the adapter (D104), with cores a test
// scripts over a real database of each dialect: Ticket as deadlines_test.go
// lays it out, with its event queue ticket_event_queue. An event is written
// in the command's unit, after the sub-table rows and before the deadline
// changes, with the command's time and its payload, and stays when its row
// is deleted; a refused or moved unit writes none; an event of a row whose
// table keeps no event queue is refused as internal; and a worker's claim
// of a message is spent by its id and token, a lost one spending nothing.

// eventTicketLayout is Ticket's layout with its event queue.
func eventTicketLayout() *rules.Layout {
	table := ticketTable(false)
	table.Events = &rules.Events{Queue: "TicketEvent", Table: "ticket_event_queue", RowKey: "row_key", Use: "use_id", Kind: "kind", At: "at", Payload: "payload"}
	return &rules.Layout{Types: map[string]*rules.Table{"Ticket": table}}
}

// createTicketEvents writes Ticket's tables and its event queue.
func createTicketEvents(t *testing.T, db *rulestest.DB) {
	t.Helper()
	createTickets(t, db)
	key := "TEXT"
	if db.Dialect == rules.DialectPostgres {
		key = "UUID"
	}
	for _, ddl := range db.EventQueue("ticket_event_queue", key) {
		db.Exec(t, ddl)
	}
}

// queuedEvents are Ticket's queued events as the adapter reads them back.
func queuedEvents(t *testing.T, db *rulestest.DB, layout *rules.Layout) []rules.QueuedEvent {
	t.Helper()
	var out []rules.QueuedEvent
	err := db.InTx(context.Background(), func(tx *rulestest.Tx) error {
		var err error
		out, err = rules.ReadQueuedEvents(context.Background(), rules.Host{Layout: layout, Store: tx.Store()}, "Ticket")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// emitted is an event of the Ticket of uuidKey, of the use Timers.
func emitted(kind, payload string) string {
	return fmt.Sprintf(`{"row":%s,"kind":%q,"useId":"Timers","payload":%s}`, ticketRef(), kind, payload)
}

func TestAnEventIsWrittenToItsRowsTablesEventQueueInTheCommandsUnitWithTheCommandsTimeAndItsPayloadAndStaysWhenTheRowIsDeleted(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTicketEvents(t, db)
		insertTicket(t, db, uuidColumn, "Leak")
		layout := eventTicketLayout()
		core := script(t, changed(`{"rows":[`+touched(1)+`],`+
			`"subRows":[{"row":`+ticketRef()+`,"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"insert","values":{"dueAt":9000}}}],`+
			`"events":[`+emitted("run", `{"name": "respond", "run": 1}`)+`],`+
			`"deadlines":[{"kind":"arm","row":`+ticketRef()+`,"useId":"Timers","name":"respond","at":9000,"payload":null}]}`))
		var rec *recorded
		err := inTx(t, db, rules.Host{Core: core, Layout: layout}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		insert := `INSERT INTO "ticket_event_queue" ("row_key", "use_id", "kind", "at", "payload", "due_at", "created_at") VALUES ($1, $2, $3, $4, $5::jsonb, clock_timestamp(), clock_timestamp())`
		if db.Dialect == rules.DialectSQLite {
			insert = `INSERT INTO "ticket_event_queue" ("row_key", "use_id", "kind", "at", "payload") VALUES (?1, ?2, ?3, ?4, ?5)`
		}
		if len(rec.applied) != 1 || len(rec.applied[0]) != 5 {
			t.Fatalf("applied %v, want one unit of the update, the sub-table row, the event and the arm's two", rec.applied)
		}
		unit := rec.applied[0]
		if got, want := statementText(unit[2]), statementText(rules.Statement{Text: insert, Values: []any{uuidColumn, "Timers", "run", int64(5000), `{"name":"respond","run":1}`}, Expect: 1}); got != want {
			t.Errorf("the event's insert, after the sub-table row and before the deadline's =\n  %s\nwant\n  %s", got, want)
		}
		if got := queuedEvents(t, db, layout); len(got) != 1 {
			t.Fatalf("queued events = %+v, want one", got)
		} else {
			wantJSON(t, "the queued event", got[0], `{"Key":"`+uuidKey+`","UseID":"Timers","Kind":"run","At":5000,"Payload":{"name":"respond","run":1}}`)
		}

		// A delete's events are written too, and the row's earlier event
		// stays: the app may still need to hear it.
		deleted := script(t, changed(`{"rows":[{"kind":"delete","type":"Ticket","key":"`+uuidKey+`","expectedVersion":2}],"events":[`+emitted("gone", `null`)+`]}`))
		err = inTx(t, db, rules.Host{Core: deleted, Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if ticketVersion(t, db, uuidColumn) != 0 {
			t.Fatal("the row is not deleted")
		}
		got := queuedEvents(t, db, layout)
		if len(got) != 2 || got[0].Kind != "run" || got[1].Kind != "gone" || string(got[1].Payload) != "null" || got[1].Key != uuidKey {
			t.Errorf("queued events after the delete = %+v, want run, then gone with a null payload", got)
		}
	})
}

func TestARefusedOrMovedCommandWritesNoEvent(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTicketEvents(t, db)
		insertTicket(t, db, uuidColumn, "Leak")
		layout := eventTicketLayout()
		refused := script(t, `{"done":{"refused":{"code":"vetoed","status":409,"class":"BehaviorVetoError","message":"no"}}}`)
		err := inTx(t, db, rules.Host{Core: refused, Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		if refusalOf(t, err).Code != rules.CodeVetoed {
			t.Fatalf("err = %v, want the core's refusal", err)
		}
		if got := queuedEvents(t, db, layout); len(got) != 0 {
			t.Fatalf("a refused command queued %+v", got)
		}

		// The first unit moves, so the command runs again: its one event is
		// written once, by the unit that applied.
		core := script(t, changed(`{"rows":[`+touched(1)+`],"events":[`+emitted("run", `{"run": 1}`)+`]}`))
		var result *rules.Result
		err = inTx(t, db, rules.Host{Core: core, Layout: layout}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, _ := record(store)
			wrapped.applies = []bool{false}
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			result, err = rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := queuedEvents(t, db, layout); result.Attempts != 2 || len(got) != 1 {
			t.Errorf("after a moved unit and %d attempts, queued %+v, want one event", result.Attempts, got)
		}

		// A unit every attempt finds moved is busy, and writes none.
		busy := script(t, changed(`{"rows":[`+touched(2)+`],"events":[`+emitted("run", `{"run": 2}`)+`]}`))
		err = inTx(t, db, rules.Host{Core: busy, Layout: layout}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, _ := record(store)
			wrapped.applies = make([]bool, rules.MaxAttempts)
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		if refusalOf(t, err).Code != rules.CodeBusy {
			t.Fatalf("err = %v, want busy", err)
		}
		if got := queuedEvents(t, db, layout); len(got) != 1 {
			t.Errorf("a busy command queued more: %+v", got)
		}
	})
}

func TestAChangeSetWithAnEventOnATableWithNoEventQueueIsRefusedAsInternalNamingTheTableAndTheUse(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTicketEvents(t, db)
		insertTicket(t, db, uuidColumn, "Leak")
		core := script(t, changed(`{"rows":[`+touched(1)+`],"events":[`+emitted("run", `{}`)+`]}`))
		err := inTx(t, db, rules.Host{Core: core, Layout: ticketLayout(false)}, nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error",
			Message: "rules: fireTimer of Ticket emits run of Timers on Ticket " + uuidKey + ", and Ticket's table ticket keeps no event queue in the behaviors' layout: a behavior whose declaration lists its events gives its table one (D104)"})
		if got := ticketVersion(t, db, uuidColumn); got != 1 {
			t.Errorf("the row's version = %d, want 1: nothing applied", got)
		}
		if got := len(db.Rows(t, `SELECT id FROM "ticket_event_queue"`)); got != 0 {
			t.Errorf("%d messages queued, want none", got)
		}
	})
}

func TestSpendEventSpendsAClaimedMessageAndALostClaimSpendsNothing(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTicketEvents(t, db)
		layout := eventTicketLayout()
		queue := func(token string) {
			t.Helper()
			db.Exec(t, `DELETE FROM "ticket_event_queue"`)
			db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_event_queue" (id, row_key, use_id, kind, at, state, claim_token) VALUES (%s, %s, 'Timers', 'run', 5000, 'claimed', %s)`,
				db.P(1), db.P(2), db.P(3)), messageID, uuidColumn, token)
		}
		spend := func(claim *rules.Claim, layout *rules.Layout) (*rules.EventRun, []rules.Statement, error) {
			t.Helper()
			var run *rules.EventRun
			var rec *recorded
			err := inTx(t, db, rules.Host{Layout: layout}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
				wrapped, r := record(store)
				rec = r
				return wrapped
			}, func(host rules.Host, _ *rulestest.Tx) error {
				var err error
				run, err = rules.SpendEvent(context.Background(), host, "Ticket", claim)
				return err
			})
			var applied []rules.Statement
			for _, unit := range rec.applied {
				applied = append(applied, unit...)
			}
			return run, applied, err
		}
		left := func() int { return len(db.Rows(t, `SELECT id FROM "ticket_event_queue"`)) }

		queue(claimToken)
		run, applied, err := spend(&rules.Claim{ID: messageID, Token: claimToken}, layout)
		if err != nil {
			t.Fatal(err)
		}
		text := `DELETE FROM "ticket_event_queue" WHERE "id" = $1::uuid AND "claim_token" = $2::uuid AND "state" = 'claimed'`
		if db.Dialect == rules.DialectSQLite {
			text = `DELETE FROM "ticket_event_queue" WHERE "id" = ?1 AND "claim_token" = ?2 AND "state" = 'claimed'`
		}
		wantStatements(t, "the spend", applied, []rules.Statement{{Text: text, Values: []any{messageID, claimToken}, Expect: 1}})
		if !run.Ran || run.Why != "" || left() != 0 {
			t.Errorf("a held claim's spend = %+v, %d messages left; want it run and the message gone", run, left())
		}

		queue(otherToken)
		run, _, err = spend(&rules.Claim{ID: messageID, Token: claimToken}, layout)
		if err != nil {
			t.Fatal(err)
		}
		if run.Ran || run.Why != rules.WhyClaimLost || left() != 1 {
			t.Errorf("a lost claim's spend = %+v, %d messages left; want claim lost and the message kept", run, left())
		}

		run, applied, err = spend(nil, layout)
		if err != nil || !run.Ran || len(applied) != 0 || left() != 1 {
			t.Errorf("a spend without a claim = %+v, %v, applied %v; want it run with nothing spent", run, err, applied)
		}

		_, _, err = spend(&rules.Claim{ID: messageID, Token: otherToken}, ticketLayout(false))
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: a claim of an event of Ticket was spent, and the behaviors' layout gives Ticket no event queue"})
	})
}

func TestReadQueuedEventsReadsOneRowsEventsInTheOrderWritten(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTicketEvents(t, db)
		insertTicket(t, db, uuidColumn, "Leak")
		layout := eventTicketLayout()
		run := func(version int, events ...string) {
			t.Helper()
			list := ""
			for i, event := range events {
				if i > 0 {
					list += ","
				}
				list += event
			}
			core := script(t, changed(`{"rows":[`+touched(version)+`],"events":[`+list+`]}`))
			err := inTx(t, db, rules.Host{Core: core, Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		want := []string{"first", "second", "third"}
		for i, kind := range want {
			run(i+1, emitted(kind, fmt.Sprintf(`{"at": %d}`, i)))
		}
		// One command's several events keep the order it wrote them: on
		// Postgres each statement's clock_timestamp() orders them, on SQLite,
		// whose millisecond created_at they share, the rowid.
		run(4, emitted("fourth", `{}`), emitted("fifth", `{}`), emitted("sixth", `{}`))
		want = append(want, "fourth", "fifth", "sixth")
		got := queuedEvents(t, db, layout)
		var kinds []string
		for _, event := range got {
			kinds = append(kinds, event.Kind)
		}
		if fmt.Sprint(kinds) != fmt.Sprint(want) {
			t.Errorf("queued kinds = %v, want %v", kinds, want)
		}
		if got[0].At != 5000 || string(got[0].Payload) != `{"at":0}` || got[0].Key != uuidKey || got[0].UseID != "Timers" {
			t.Errorf("the first queued event = %+v", got[0])
		}
		if none := queuedEvents(t, db, ticketLayout(false)); none == nil || len(none) != 0 {
			t.Errorf("a table with no event queue reads %v, want none", none)
		}
	})
}
