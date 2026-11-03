package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// A table's change queue through the adapter (D103), with cores a test
// scripts over a real database of each dialect: RunChange converting a
// message as the trigger wrote it in each dialect into the core's change
// command; the rows that link to a row, read a page at a time in key
// order, a soft-deleted one left out; a gone row's unit deleting its
// deadlines, and its sub-table rows while no row of its key is left; and a
// change run for a worker's claim, which its unit spends after the row
// writes, a claim lost running nothing and a refusal leaving the message
// claimed for the worker to fail.

// taskTable is Task's layout: its title, its date-time visitAt, its flag
// urgent, its JSON data, the key of its project, and a Workflow on status;
// soft-deletable.
func taskTable() *rules.Table {
	return &rules.Table{
		Type:    "Task",
		Table:   "task",
		Key:     rules.Column{Field: "id", Column: "id", Type: "Identity.UUID"},
		Version: "_version",
		Live:    &rules.Live{Column: "deleted_at"},
		Own: []rules.Column{
			{Field: "title", Column: "title", Type: "string"},
			{Field: "visitAt", Column: "visit_at", Type: "Temporal.DateTime", Convert: rules.ConvertEpochMillis},
			{Field: "urgent", Column: "urgent", Type: "boolean"},
			{Field: "data", Column: "data", Type: "Generic.JSON"},
			{Field: "projectId", Column: "project_id", Type: "Identity.UUID"},
		},
		Uses: []rules.Use{{
			ID:       "status",
			Behavior: "Workflow",
			Member:   "status",
			Anchor:   "status",
			Fields: []rules.OwnedField{
				{Name: "status", Column: rules.Column{Field: "status", Column: "status", Type: "string"}, Value: true},
				{Name: "enteredAt", Column: rules.Column{Field: "statusEnteredAt", Column: "status_entered_at", Type: "Temporal.DateTime", Convert: rules.ConvertEpochMillis}},
			},
			Operations: []rules.Operation{{Name: "transition", Writes: true}},
		}},
		Spec: rules.TypeSpec{Name: "Task", Uses: []rules.UseSpec{{Behavior: "Workflow", Config: json.RawMessage(workflowConfig), Anchor: rules.Anchor{Field: "status"}}},
			Fields: []rules.FieldSpec{{Name: "projectId", Type: "Identity.UUID", Relation: &rules.RelationSpec{Name: "project", Type: "Project"}}}},
	}
}

func taskLayout() *rules.Layout {
	return &rules.Layout{Types: map[string]*rules.Table{"Task": taskTable()}}
}

// createTasks writes Task's table as sqlgen writes it, with @optimistic's
// trigger.
func createTasks(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	key := "TEXT"
	if db.Dialect == rules.DialectPostgres {
		key = "UUID"
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "task" (id %s PRIMARY KEY, title %s, visit_at %s, urgent %s, data %s, project_id %s, status %s, status_entered_at %s, deleted_at %s, _version %s)`,
		key, types.Text, types.Timestamp, types.Boolean, types.JSON, key, types.Text, types.Timestamp, types.Timestamp, types.Version))
	for _, ddl := range db.BumpVersion("task") {
		db.Exec(t, ddl)
	}
}

// wire is a UUID's hyphenated text as its base62 wire form.
func wire(t *testing.T, hyphenated string) string {
	t.Helper()
	n, ok := new(big.Int).SetString(strings.ReplaceAll(hyphenated, "-", ""), 16)
	if !ok {
		t.Fatalf("%q is no UUID", hyphenated)
	}
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	if n.Sign() == 0 {
		return "0"
	}
	var out []byte
	base, digit := big.NewInt(62), new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, base, digit)
		out = append([]byte{alphabet[digit.Int64()]}, out...)
	}
	return string(out)
}

// changeHost is a host on a core at the time 7000.
func changeHost(core rules.Core, layout *rules.Layout) rules.Host {
	return rules.Host{Core: core, Layout: layout, Now: func() int64 { return 7000 }, Request: func() string { return "c1" }}
}

func TestRunChangeConvertsTheTriggersImages(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		layout := taskLayout()
		visit := time.Date(2026, 1, 2, 3, 4, 5, 123_000_000, time.UTC).UnixMilli()
		entered := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC).UnixMilli()
		// Each image as the dialect's trigger writes it: on Postgres
		// jsonb_build_object's values (a timestamp with the session's
		// offset, a boolean, a jsonb's JSON, a UUID hyphenated); on SQLite
		// json_object's storage classes (the timestamp's TEXT, a boolean's
		// INTEGER, a JSON column's TEXT), the object itself a TEXT's.
		var before, after json.RawMessage
		if db.Dialect == rules.DialectPostgres {
			before = json.RawMessage(`{"title":"Plan","visit_at":"2026-01-02T05:04:05.123+02:00","urgent":false,"data":{"n":1,"s":"x"},"project_id":"` + uuidColumn + `","status":"todo","status_entered_at":"2026-01-02T03:00:00+00:00","deleted_at":null,"unknown":1}`)
			after = json.RawMessage(`{"title":"Plan","visit_at":null,"urgent":true,"data":"text","project_id":null,"status":"doing","status_entered_at":null}`)
		} else {
			before = json.RawMessage(`{"title":"Plan","visit_at":"2026-01-02T03:04:05.123Z","urgent":0,"data":"{\"n\":1,\"s\":\"x\"}","project_id":"` + uuidColumn + `","status":"todo","status_entered_at":"2026-01-02T03:00:00.000Z","deleted_at":null,"unknown":1}`)
			text, _ := json.Marshal(`{"title":"Plan","visit_at":null,"urgent":1,"data":"\"text\"","project_id":null,"status":"doing","status_entered_at":null}`)
			after = text
		}
		core := script(t, changed(""))
		var run *rules.ChangeRun
		err := inTx(t, db, changeHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			run, err = rules.RunChange(context.Background(), host, "Task", rules.Change{Key: absentColumn, Write: "update", At: 6500, Before: before, After: after}, rules.Caller{Subject: "fixture-api/TaskWorker"}, nil, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !run.Ran {
			t.Fatalf("run = %+v, want run", run)
		}
		wantJSON(t, "the change command", core.commands[0], fmt.Sprintf(`{"caller":{"subject":"fixture-api/TaskWorker"},"now":7000,"request":"c1","action":{"kind":"change","key":%q,"change":{
			"write":"update","at":6500,
			"before":{"own":{"title":"Plan","visitAt":%d,"urgent":false,"data":{"n":1,"s":"x"},"projectId":%q},"owned":{"status":{"status":"todo","enteredAt":%d}}},
			"after":{"own":{"title":"Plan","urgent":true,"data":"text"},"owned":{"status":{"status":"doing"}}}}}}`, absentKey, visit, uuidKey, entered))

		// An insert has no before and a delete no after: null or absent.
		core = script(t, changed(""))
		err = inTx(t, db, changeHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.RunChange(context.Background(), host, "Task", rules.Change{Key: absentKey, Write: "delete", At: 6600, Before: json.RawMessage(`{"title":"Plan"}`), After: json.RawMessage(`null`)}, rules.Caller{Subject: "w"}, nil, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "a delete's change", core.commands[0].Action, fmt.Sprintf(`{"kind":"change","key":%q,"change":{"write":"delete","at":6600,"before":{"own":{"title":"Plan"},"owned":{}}}}`, absentKey))

		// A write of another kind, or an image that is no object, is the
		// adapter's own failure.
		for _, c := range []struct {
			change rules.Change
			want   string
		}{
			{rules.Change{Key: absentColumn, Write: "truncate", At: 1}, fmt.Sprintf("rules: Task's change of %s is a write of truncate, which is no insert, update or delete", absentKey)},
			{rules.Change{Key: absentColumn, Write: "insert", At: 1, After: json.RawMessage(`[1]`)}, fmt.Sprintf("rules: Task's change of %s: its image [1] is no JSON object", absentKey)},
			{rules.Change{Key: "t-1", Write: "insert", At: 1}, "rules: Task's change of t-1: its key is no UUID, hyphenated or base62"},
		} {
			err := inTx(t, db, changeHost(script(t, changed("")), layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.RunChange(context.Background(), host, "Task", c.change, rules.Caller{Subject: "w"}, nil, nil)
				return err
			})
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: c.want})
		}
	})
}

// UUIDs in key order, hyphenated as their columns hold them.
const (
	task1    = "00000000-0000-4000-8000-000000000001"
	task2    = "00000000-0000-4000-8000-000000000002"
	task3    = "00000000-0000-4000-8000-000000000003"
	task4    = "00000000-0000-4000-8000-000000000004"
	task5    = "00000000-0000-4000-8000-000000000005"
	projectA = "00000000-0000-4000-8000-0000000000a1"
	projectB = "00000000-0000-4000-8000-0000000000b2"
)

func TestTheCoreReadsTheRowsThatLinkToARow(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTasks(t, db)
		insert := func(id, title, project string, deleted bool) {
			var deletedAt any
			if deleted {
				deletedAt = db.Timestamp(time.UnixMilli(5000))
			}
			db.Exec(t, fmt.Sprintf(`INSERT INTO "task" (id, title, project_id, status, deleted_at) VALUES (%s, %s, %s, 'todo', %s)`, db.P(1), db.P(2), db.P(3), db.P(4)), id, title, project, deletedAt)
		}
		insert(task5, "e", projectA, false)
		insert(task2, "b", projectA, false)
		insert(task4, "d", projectB, false)
		insert(task1, "a", projectA, false)
		insert(task3, "c", projectA, true)
		layout := taskLayout()
		linked := func(after string) string {
			request := map[string]any{"kind": "linked", "type": "Task", "field": "projectId", "key": wire(t, projectA), "limit": 2}
			if after != "" {
				request["after"] = after
			}
			return string(jsonOf(t, request))
		}
		read := func(requests ...string) (*scripted, error) {
			core := script(t, `{"read":[`+strings.Join(requests, ",")+`]}`, changed(""))
			err := inTx(t, db, changeHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Task", rules.Command{Caller: alice, Now: 7000, Action: rules.Action{Kind: rules.ActionChange, Key: wire(t, task1), Change: &rules.RowChange{Write: "update", At: 6000}}}, nil)
				return err
			})
			return core, err
		}
		row := func(id, title string) string {
			return fmt.Sprintf(`{"key":%q,"row":{"own":{"title":%q,"projectId":%q},"owned":{"status":{"status":"todo"}},"version":1}}`, wire(t, id), title, wire(t, projectA))
		}

		// The first page holds two, in key order, and more follow; the next
		// page, after the last key, holds the rest, a soft-deleted row left
		// out. A type the layout lacks has none.
		other := `{"kind":"linked","type":"Note","field":"taskId","key":"x","limit":500}`
		core, err := read(linked(""), linked(wire(t, task2)), other)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "the answers", core.answers[1], `{"reads":[
			{"request":`+linked("")+`,"answer":{"linked":{"rows":[`+row(task1, "a")+`,`+row(task2, "b")+`],"more":true}},"held":0},
			{"request":`+linked(wire(t, task2))+`,"answer":{"linked":{"rows":[`+row(task5, "e")+`],"more":false}},"held":0},
			{"request":`+other+`,"answer":{"linked":{"rows":[],"more":false}},"held":0}]}`)

		// A field the layout gives the table no own column of is the
		// adapter's own failure.
		_, err = read(`{"kind":"linked","type":"Task","field":"ownerId","key":"x","limit":500}`)
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: Task has no own field ownerId in the behaviors' layout"})

		// Under a lock, a store whose locks are fenced has no check of it
		// yet.
		core = script(t, `{"lock":["Task/x"]}`, `{"read":[`+linked("")+`]}`, changed(""))
		err = inTx(t, db, changeHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Task", rules.Command{Caller: alice, Now: 7000, Action: rules.Action{Kind: rules.ActionChange, Key: wire(t, task1), Change: &rules.RowChange{Write: "update", At: 6000}}}, nil)
			return err
		})
		if db.Dialect == rules.DialectSQLite {
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: a read of the rows that link to a row under a lock has no check on SQLite or D1 yet (D103)"})
		} else if err != nil {
			t.Errorf("a linked read under a lock on Postgres: %v", err)
		}
	})
}

// goneUnit is a change command's change set that finds the Ticket of key
// gone.
func goneUnit() string {
	return changed(fmt.Sprintf(`{"rows":[{"kind":"gone","type":"Ticket","key":%q}]}`, uuidKey))
}

// changeOf is a change command of the Ticket of key, as a worker's.
func changeOf(write string) rules.Command {
	return rules.Command{Caller: rules.Caller{Subject: "w"}, Now: 7000, Request: "c1", Action: rules.Action{Kind: rules.ActionChange, Key: uuidKey, Change: &rules.RowChange{Write: write, At: 6000}}}
}

func TestAGoneRowDeletesWhatNoRowOfItsKeyKeeps(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		for _, c := range []struct {
			name string
			// row is the ticket's state: "" none, "soft" soft-deleted, "live".
			row          string
			wantMessages int
			wantSubRows  int
		}{
			{name: "deleted", row: "", wantMessages: 0, wantSubRows: 0},
			{name: "soft-deleted", row: "soft", wantMessages: 0, wantSubRows: 1},
			{name: "came back", row: "live", wantMessages: 1, wantSubRows: 1},
		} {
			t.Run(c.name, func(t *testing.T) {
				createTickets(t, db)
				t.Cleanup(func() {
					for _, table := range []string{"ticket_deadline_queue", "ticket_timers_deadlines", "ticket"} {
						db.Exec(t, `DROP TABLE "`+table+`"`)
					}
				})
				if c.row != "" {
					insertTicket(t, db, uuidColumn, "Printer")
					db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" (ticket_id, name, armed_from) VALUES (%s, 'respond', '1000')`, db.P(1)), uuidColumn)
				}
				if c.row == "soft" {
					db.Exec(t, fmt.Sprintf(`UPDATE "ticket" SET deleted_at = %s`, db.P(1)), db.Timestamp(time.UnixMilli(5000)))
				}
				insertMessage(t, db, messageID, uuidColumn, "respond", 2500, "")
				layout := ticketLayout(true)
				var rec *recorded
				err := inTx(t, db, changeHost(script(t, goneUnit()), layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
					wrapped, r := record(store)
					rec = r
					return wrapped
				}, func(host rules.Host, _ *rulestest.Tx) error {
					_, err := rules.Execute(context.Background(), host, "Ticket", changeOf("delete"), nil)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				p := db.P
				wantStatements(t, "the gone row's unit", rec.applied[0], []rules.Statement{
					{Text: fmt.Sprintf(`DELETE FROM "ticket_deadline_queue" WHERE "row_key" = %s AND NOT EXISTS (SELECT 1 FROM "ticket" WHERE "id" = %s AND "deleted_at" IS NULL)`, p(1), p(1)), Values: []any{uuidColumn}, Expect: rules.AnyRows},
					{Text: fmt.Sprintf(`DELETE FROM "ticket_timers_deadlines" WHERE "ticket_id" = %s AND NOT EXISTS (SELECT 1 FROM "ticket" WHERE "id" = %s)`, p(1), p(1)), Values: []any{uuidColumn}, Expect: rules.AnyRows},
				})
				if got := len(messages(t, db, uuidColumn)); got != c.wantMessages {
					t.Errorf("the row keeps %d messages, want %d", got, c.wantMessages)
				}
				if got := len(db.Rows(t, `SELECT 1 FROM "ticket_timers_deadlines"`)); got != c.wantSubRows {
					t.Errorf("the row keeps %d sub-table rows, want %d", got, c.wantSubRows)
				}
			})
		}
	})
}

// insertChange writes a message of the change queue past the adapter:
// claimed with a token when token is set, else ready.
func insertChange(t *testing.T, db *rulestest.DB, id, row, token string) {
	t.Helper()
	state := "ready"
	var claim any
	if token != "" {
		state, claim = "claimed", token
	}
	db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_change_queue" (id, row_key, write, at, state, claim_token) VALUES (%s, %s, 'update', 6000, %s, %s)`,
		db.P(1), db.P(2), db.P(3), db.P(4)), id, row, state, claim)
}

// changeStates are the change queue's messages' states, oldest first.
func changeStates(t *testing.T, db *rulestest.DB) []string {
	t.Helper()
	var out []string
	for _, row := range db.Rows(t, `SELECT state FROM "ticket_change_queue" ORDER BY created_at, id`) {
		out = append(out, fmt.Sprint(row[0]))
	}
	return out
}

func TestRunChangeSpendsItsClaimAfterTheRowWrites(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		key := "TEXT"
		if db.Dialect == rules.DialectPostgres {
			key = "UUID"
		}
		for _, ddl := range db.ChangeQueue("ticket_change_queue", key) {
			db.Exec(t, ddl)
		}
		insertTicket(t, db, uuidColumn, "Printer")
		layout := ticketLayout(false)
		layout.Types["Ticket"].Changes = &rules.Changes{Queue: "TicketChange", Table: "ticket_change_queue", RowKey: "row_key", Write: "write", At: "at", Before: "before", After: "after"}
		change := rules.Change{Key: uuidColumn, Write: "update", At: 6000, Before: json.RawMessage(`{"title":"Desk"}`), After: json.RawMessage(`{"title":"Printer"}`)}
		run := func(core rules.Core, claim *rules.Claim, rec **recorded) (*rules.ChangeRun, error) {
			var out *rules.ChangeRun
			err := inTx(t, db, changeHost(core, layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
				wrapped, r := record(store)
				if rec != nil {
					*rec = r
				}
				return wrapped
			}, func(host rules.Host, _ *rulestest.Tx) error {
				var err error
				out, err = rules.RunChange(context.Background(), host, "Ticket", change, rules.Caller{Subject: "fixture-timers-api/TicketTimers"}, claim, nil)
				return err
			})
			return out, err
		}
		claim := &rules.Claim{ID: messageID, Token: claimToken}

		// Run for its claim: the row's write, then the spend, then the arm.
		insertChange(t, db, messageID, uuidColumn, claimToken)
		var rec *recorded
		ran, err := run(script(t, changed(fmt.Sprintf(`{"rows":[%s],"deadlines":[{"kind":"arm","row":%s,"useId":"Timers","name":"remind","at":9000,"payload":null}]}`, touched(1), ticketRef()))), claim, &rec)
		if err != nil {
			t.Fatal(err)
		}
		if !ran.Ran || ran.Result == nil || ran.Result.Key != uuidKey {
			t.Fatalf("run = %+v, want run on the ticket", ran)
		}
		spend := `DELETE FROM "ticket_change_queue" WHERE "id" = $1::uuid AND "claim_token" = $2::uuid AND "state" = 'claimed'`
		if db.Dialect == rules.DialectSQLite {
			spend = `DELETE FROM "ticket_change_queue" WHERE "id" = ?1 AND "claim_token" = ?2 AND "state" = 'claimed'`
		}
		if unit := rec.applied[0]; len(unit) != 4 {
			t.Fatalf("the unit = %v, want the row, the spend, and the arm's clear and insert", unit)
		}
		wantStatements(t, "the spend", rec.applied[0][1:2], []rules.Statement{{Text: spend, Values: []any{messageID, claimToken}, Expect: 1}})
		if got := changeStates(t, db); len(got) != 0 {
			t.Errorf("the change queue holds %v, want the claimed message spent", got)
		}
		if got := messages(t, db, uuidColumn); len(got) != 1 || got[0].At != 9000 {
			t.Errorf("the deadline messages = %+v, want remind armed at 9000", got)
		}

		// A claim another took over runs nothing, the row unwritten.
		insertChange(t, db, messageID, uuidColumn, otherToken)
		lost, err := run(script(t, changed(fmt.Sprintf(`{"rows":[%s]}`, touched(2)))), claim, nil)
		if err != nil {
			t.Fatal(err)
		}
		if lost.Ran || lost.Why != rules.WhyClaimLost {
			t.Errorf("run = %+v, want nothing run, the claim lost", lost)
		}
		if got := ticketVersion(t, db, uuidColumn); got != 2 {
			t.Errorf("the version = %d, want 2: nothing of the lost claim's run written", got)
		}
		db.Exec(t, `DELETE FROM "ticket_change_queue"`)

		// A refusal is the run's error, its message left claimed for the
		// worker to fail.
		insertChange(t, db, messageID, uuidColumn, claimToken)
		_, err = run(script(t, `{"done":{"refused":{"code":"forbidden","status":403,"class":"PermissionError","message":"fixture-timers-api/TicketTimers may not move Project p1"}}}`), claim, nil)
		if refusal := refusalOf(t, err); refusal.Code != rules.CodeForbidden {
			t.Errorf("refusal = %+v, want forbidden", *refusal)
		}
		if got := changeStates(t, db); len(got) != 1 || got[0] != "claimed" {
			t.Errorf("the change queue holds %v, want the message still claimed", got)
		}

		// Without a claim nothing is spent.
		ran, err = run(script(t, changed(fmt.Sprintf(`{"rows":[%s]}`, touched(2)))), nil, nil)
		if err != nil || !ran.Ran {
			t.Fatalf("run = %+v, %v, want run", ran, err)
		}
		if got := changeStates(t, db); len(got) != 1 || got[0] != "claimed" {
			t.Errorf("the change queue holds %v, want the message untouched", got)
		}

		// For a claim, a table whose layout names no change queue is the
		// adapter's own failure.
		layout.Types["Ticket"].Changes = nil
		_, err = run(script(t, changed("")), claim, nil)
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: a change of Ticket ran for a claim of its change queue, and the behaviors' layout gives Ticket no change queue"})
	})
}
