package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// A use's sub-tables and a table's deadline queue through the adapter
// (D103), with cores a test scripts over a real database of each dialect:
// Ticket implementing Timers as the generator lays it out, its sub-table
// ticket_timers_deadlines and its queue ticket_deadline_queue. Each
// statement is held to the contract's text, and each unit's effect read
// back past the adapter: sub-table rows inserted, updated and deleted; the
// core's read of them; an arm replacing the live message, a claimed one
// among them; a clear; a delete taking the row's messages and, hard, its
// sub-table rows; and a deadline command spending its arming, by a
// worker's claim or by the time it names, before the arm the fired command
// makes again.

// Message ids and claim tokens as the queue client gives them.
const (
	messageID  = "0b9c6f0e-5d55-4f2a-9a43-6a1f3c0c1001"
	claimToken = "6c1d4a77-0a5e-4d43-8c6a-2a5c7e3d2002"
	otherToken = "6c1d4a77-0a5e-4d43-8c6a-2a5c7e3d2003"
)

// ticketTable is Ticket's layout: its title, Timers by implements with
// its deadlines' sub-table, and its deadline queue; soft-deletable when
// live.
func ticketTable(live bool) *rules.Table {
	dateTime := func(name, column string) rules.OwnedField {
		return rules.OwnedField{Name: name, Column: rules.Column{Field: name, Column: column, Type: "Temporal.DateTime", Convert: rules.ConvertEpochMillis}}
	}
	plain := func(name, column, typ string) rules.OwnedField {
		return rules.OwnedField{Name: name, Column: rules.Column{Field: name, Column: column, Type: typ}}
	}
	table := &rules.Table{
		Type:    "Ticket",
		Table:   "ticket",
		Key:     rules.Column{Field: "id", Column: "id", Type: "Identity.UUID"},
		Version: "_version",
		Own:     []rules.Column{{Field: "title", Column: "title", Type: "string"}},
		Uses: []rules.Use{{
			ID:         "Timers",
			Behavior:   "Timers",
			Member:     "timers",
			Fields:     []rules.OwnedField{},
			Operations: []rules.Operation{{Name: "fireTimer", Writes: true}},
			Tables: []rules.SubTable{{
				Name:  "deadlines",
				Table: "ticket_timers_deadlines",
				Row:   "ticket_id",
				Key:   plain("name", "name", "string"),
				Fields: []rules.OwnedField{
					dateTime("baseAt", "base_at"),
					plain("armedFrom", "armed_from", "string"),
					dateTime("dueAt", "due_at"),
					plain("fired", "fired", "boolean"),
					dateTime("firedAt", "fired_at"),
					plain("outcome", "outcome", "string"),
				},
			}},
		}},
		Spec:      rules.TypeSpec{Name: "Ticket", Uses: []rules.UseSpec{{Behavior: "Timers", Config: json.RawMessage(`{"deadlines":{}}`)}}},
		Deadlines: &rules.Deadlines{Queue: "TicketDeadline", Table: "ticket_deadline_queue", RowKey: "row_key", Use: "use_id", Name: "name", At: "at", Payload: "payload"},
	}
	if live {
		table.Live = &rules.Live{Column: "deleted_at"}
	}
	return table
}

func ticketLayout(live bool) *rules.Layout {
	return &rules.Layout{Types: map[string]*rules.Table{"Ticket": ticketTable(live)}}
}

// createTickets writes Ticket's tables as sqlgen writes them: the row's
// table with @optimistic's trigger, its sub-table, a child of the row
// deleted with it, and its deadline queue.
func createTickets(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	key := "TEXT"
	uuidDefault := "TEXT"
	if db.Dialect == rules.DialectPostgres {
		key = "UUID"
		uuidDefault = "UUID DEFAULT gen_random_uuid()"
	} else {
		uuidDefault = "TEXT DEFAULT (lower(hex(randomblob(16))))"
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "ticket" (id %s PRIMARY KEY, title %s, deleted_at %s, _version %s)`, key, types.Text, types.Timestamp, types.Version))
	for _, ddl := range db.BumpVersion("ticket") {
		db.Exec(t, ddl)
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "ticket_timers_deadlines" (
  id %s NOT NULL PRIMARY KEY,
  ticket_id %s NOT NULL REFERENCES "ticket" (id) ON DELETE CASCADE,
  name %s NOT NULL,
  base_at %s,
  armed_from %s,
  due_at %s,
  fired %s,
  fired_at %s,
  outcome %s,
  UNIQUE (ticket_id, name)
)`, uuidDefault, key, types.Text, types.Timestamp, types.Text, types.Timestamp, types.Boolean, types.Timestamp, types.Text))
	for _, ddl := range db.DeadlineQueue("ticket_deadline_queue", key) {
		db.Exec(t, ddl)
	}
}

// insertTicket writes a row of Ticket past the adapter, at version 1.
func insertTicket(t *testing.T, db *rulestest.DB, id, title string) {
	t.Helper()
	db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket" (id, title) VALUES (%s, %s)`, db.P(1), db.P(2)), id, title)
}

// insertMessage writes a message of the deadline queue past the adapter:
// claimed with a token when token is set, else ready.
func insertMessage(t *testing.T, db *rulestest.DB, id, row, name string, at int64, token string) {
	t.Helper()
	state := "ready"
	var claim any
	if token != "" {
		state, claim = "claimed", token
	}
	db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_deadline_queue" (id, row_key, use_id, name, at, state, claim_token) VALUES (%s, %s, 'Timers', %s, %s, %s, %s)`,
		db.P(1), db.P(2), db.P(3), db.P(4), db.P(5), db.P(6)), id, row, name, at, state, claim)
}

// message is a message of the queue as the database holds it.
type message struct {
	Name, State string
	At          int64
	DueAt       int64
	Payload     string
}

// messages are the queue's messages of a row, by name and state.
func messages(t *testing.T, db *rulestest.DB, row string) []message {
	t.Helper()
	var out []message
	for _, values := range db.Rows(t, fmt.Sprintf(`SELECT name, state, at, due_at, payload FROM "ticket_deadline_queue" WHERE row_key = %s ORDER BY name, state`, db.P(1)), row) {
		m := message{Name: fmt.Sprint(values[0]), State: fmt.Sprint(values[1])}
		switch at := values[2].(type) {
		case int64:
			m.At = at
		case int32:
			m.At = int64(at)
		}
		switch due := values[3].(type) {
		case time.Time:
			m.DueAt = due.UnixMilli()
		case string:
			parsed, err := time.Parse(time.RFC3339Nano, due)
			if err != nil {
				t.Fatalf("due_at %q: %v", due, err)
			}
			m.DueAt = parsed.UnixMilli()
		}
		switch payload := values[4].(type) {
		case nil:
		case string:
			m.Payload = payload
		default:
			text, _ := json.Marshal(payload)
			m.Payload = string(text)
		}
		out = append(out, m)
	}
	return out
}

// subRows are the row's deadlines' sub-table rows as the adapter reads
// them, as JSON.
func subRows(t *testing.T, db *rulestest.DB, layout *rules.Layout, key string) string {
	t.Helper()
	var rows []rules.SubRow
	err := db.InTx(context.Background(), func(tx *rulestest.Tx) error {
		var err error
		rows, err = rules.ReadSubRows(context.Background(), rules.Host{Layout: layout, Store: tx.Store()}, "Ticket", key, "Timers", "deadlines")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(jsonOf(t, rows))
}

// ticketVersion is the row's version as the database holds it.
func ticketVersion(t *testing.T, db *rulestest.DB, id string) int64 {
	t.Helper()
	rows := db.Rows(t, fmt.Sprintf(`SELECT _version FROM "ticket" WHERE id = %s`, db.P(1)), id)
	if len(rows) == 0 {
		return 0
	}
	switch v := rows[0][0].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	}
	t.Fatalf("_version of %T", rows[0][0])
	return 0
}

// fire is a fireTimer of the row of key, as a caller's operation.
func fire(key string) rules.Command {
	return rules.Command{
		Caller:  alice,
		Now:     5000,
		Request: "r5",
		Action:  rules.Action{Kind: rules.ActionOperation, Key: key, UseID: "Timers", Operation: "fireTimer", Params: json.RawMessage(`{"deadline":"respond"}`)},
	}
}

// touched is a change set's write of the Ticket of key at version, which
// every command that writes a sub-table row or a deadline makes.
func touched(version int) string {
	return fmt.Sprintf(`{"kind":"update","type":"Ticket","key":%q,"expectedVersion":%d,"own":{},"owned":{}}`, uuidKey, version)
}

func ticketRef() string {
	return fmt.Sprintf(`{"type":"Ticket","key":%q}`, uuidKey)
}

func TestSubTableRowsApplyByTheLayout(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		insertTicket(t, db, uuidColumn, "Printer")
		layout := ticketLayout(false)
		p := db.P
		run := func(changes string) ([]rules.Statement, error) {
			t.Helper()
			var rec *recorded
			err := inTx(t, db, rules.Host{Core: script(t, changed(changes)), Layout: layout}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
				wrapped, r := record(store)
				rec = r
				return wrapped
			}, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
				return err
			})
			if len(rec.applied) == 0 {
				return nil, err
			}
			return rec.applied[0], err
		}

		unit, err := run(fmt.Sprintf(`{"rows":[%s],"subRows":[
			{"row":%s,"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"insert","values":{"baseAt":1000,"armedFrom":"1000","dueAt":2500,"fired":false}}},
			{"row":%s,"useId":"Timers","table":"deadlines","key":"idle","change":{"kind":"insert","values":{"baseAt":1000,"armedFrom":"open","dueAt":null}}}
		]}`, touched(1), ticketRef(), ticketRef()))
		if err != nil {
			t.Fatal(err)
		}
		insert := fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" ("ticket_id", "name", "base_at", "armed_from", "due_at", "fired") VALUES (%s, %s, %s, %s, %s, %s) ON CONFLICT ("ticket_id", "name") DO NOTHING`, p(1), p(2), p(3), p(4), p(5), p(6))
		falsy := any(false)
		if db.Dialect == rules.DialectSQLite {
			falsy = int64(0)
		}
		wantStatements(t, "the insert unit", unit, []rules.Statement{
			{Text: fmt.Sprintf(`UPDATE "ticket" SET "_version" = "_version" + 1 WHERE "id" = %s AND "_version" = %s`, p(1), p(2)), Values: []any{uuidColumn, int64(1)}, Expect: 1},
			{Text: insert, Values: []any{uuidColumn, "respond", at(db, 1000), "1000", at(db, 2500), falsy}, Expect: 1},
			{Text: fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" ("ticket_id", "name", "base_at", "armed_from", "due_at") VALUES (%s, %s, %s, %s, %s) ON CONFLICT ("ticket_id", "name") DO NOTHING`, p(1), p(2), p(3), p(4), p(5)), Values: []any{uuidColumn, "idle", at(db, 1000), "open", nil}, Expect: 1},
		})
		if got, want := subRows(t, db, layout, uuidKey), `[{"key":"idle","values":{"armedFrom":"open","baseAt":1000}},{"key":"respond","values":{"armedFrom":"1000","baseAt":1000,"dueAt":2500,"fired":false}}]`; !sameJSON([]byte(got), []byte(want)) {
			t.Errorf("the sub-table rows = %s, want %s", got, want)
		}

		unit, err = run(fmt.Sprintf(`{"rows":[%s],"subRows":[
			{"row":%s,"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"update","values":{"fired":true,"firedAt":4000,"outcome":"moved","dueAt":2500}}},
			{"row":%s,"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"update","values":{}}},
			{"row":%s,"useId":"Timers","table":"deadlines","key":"idle","change":{"kind":"delete"}}
		]}`, touched(2), ticketRef(), ticketRef(), ticketRef()))
		if err != nil {
			t.Fatal(err)
		}
		truthy := any(true)
		if db.Dialect == rules.DialectSQLite {
			truthy = int64(1)
		}
		wantStatements(t, "the update unit", unit[1:], []rules.Statement{
			{Text: fmt.Sprintf(`UPDATE "ticket_timers_deadlines" SET "due_at" = %s, "fired" = %s, "fired_at" = %s, "outcome" = %s WHERE "ticket_id" = %s AND "name" = %s`, p(3), p(4), p(5), p(6), p(1), p(2)), Values: []any{uuidColumn, "respond", at(db, 2500), truthy, at(db, 4000), "moved"}, Expect: 1},
			{Text: fmt.Sprintf(`DELETE FROM "ticket_timers_deadlines" WHERE "ticket_id" = %s AND "name" = %s`, p(1), p(2)), Values: []any{uuidColumn, "idle"}, Expect: 1},
		})
		if got, want := subRows(t, db, layout, uuidKey), `[{"key":"respond","values":{"armedFrom":"1000","baseAt":1000,"dueAt":2500,"fired":true,"firedAt":4000,"outcome":"moved"}}]`; !sameJSON([]byte(got), []byte(want)) {
			t.Errorf("the sub-table rows = %s, want %s", got, want)
		}

		// A value of a field the sub-table does not hold, its key's among
		// them, is the adapter's own failure.
		for _, field := range []string{"name", "snoozes"} {
			_, err = run(fmt.Sprintf(`{"rows":[%s],"subRows":[{"row":%s,"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"update","values":{%q:1}}}]}`, touched(3), ticketRef(), field))
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: sub-table ticket_timers_deadlines has no field " + field})
		}

		// An update of a row the sub-table does not hold changes none: the
		// unit moves, the command runs again, and after its last attempt it
		// is busy, nothing of it written.
		_, err = run(fmt.Sprintf(`{"rows":[%s],"subRows":[{"row":%s,"useId":"Timers","table":"deadlines","key":"remind","change":{"kind":"update","values":{"fired":true}}}]}`, touched(3), ticketRef()))
		if refusal := refusalOf(t, err); refusal.Code != rules.CodeBusy {
			t.Errorf("refusal = %+v, want busy", *refusal)
		}
		if got := ticketVersion(t, db, uuidColumn); got != 3 {
			t.Errorf("the version = %d, want 3: nothing of the busy command written", got)
		}
	})
}

func TestTheCoreReadsARowsSubTableRows(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		insertTicket(t, db, uuidColumn, "Printer")
		db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" (ticket_id, name, base_at, armed_from, due_at, fired) VALUES (%s, 'respond', %s, '1000', %s, %s), (%s, 'Idle', %s, 'open', NULL, NULL)`,
			db.P(1), db.P(2), db.P(3), db.P(4), db.P(1), db.P(2)), uuidColumn, db.Timestamp(time.UnixMilli(1000)), db.Timestamp(time.UnixMilli(2500)), map[rules.Dialect]any{rules.DialectPostgres: false, rules.DialectSQLite: int64(0)}[db.Dialect])
		layout := ticketLayout(false)
		read := fmt.Sprintf(`{"kind":"subRows","type":"Ticket","key":%q,"useId":"Timers","table":"deadlines"}`, uuidKey)
		core := script(t, `{"read":[`+read+`]}`, changed(""))
		err := inTx(t, db, rules.Host{Core: core, Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		// In key order by code point: an upper-case key first.
		wantJSON(t, "the answers", core.answers[1], `{"reads":[{"request":`+read+`,"answer":{"subRows":[
			{"key":"Idle","values":{"armedFrom":"open","baseAt":1000}},
			{"key":"respond","values":{"armedFrom":"1000","baseAt":1000,"dueAt":2500,"fired":false}}
		]},"held":0}]}`)

		// Under a lock, a store whose locks are fenced has no check of it
		// yet.
		core = script(t, `{"lock":["Ticket/x"]}`, `{"read":[`+read+`]}`, changed(""))
		err = inTx(t, db, rules.Host{Core: core, Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		if db.Dialect == rules.DialectSQLite {
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: a read of sub-table rows under a lock has no check on SQLite or D1 yet (D103)"})
		} else if err != nil {
			t.Errorf("a read of sub-table rows under a lock on Postgres: %v", err)
		}
	})
}

func TestAnArmReplacesTheLiveMessageAndAClearDeletesIt(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		insertTicket(t, db, uuidColumn, "Printer")
		// A worker holds the claim of respond's message: the arm takes it.
		insertMessage(t, db, messageID, uuidColumn, "respond", 2500, claimToken)
		layout := ticketLayout(false)
		p := db.P
		var rec *recorded
		err := inTx(t, db, rules.Host{Core: script(t, changed(fmt.Sprintf(`{"rows":[%s],"deadlines":[
			{"kind":"arm","row":%s,"useId":"Timers","name":"respond","at":9000,"payload":{"n":1}},
			{"kind":"arm","row":%s,"useId":"Timers","name":"idle","at":3600000,"payload":null}
		]}`, touched(1), ticketRef(), ticketRef()))), Layout: layout}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
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
		clear := fmt.Sprintf(`DELETE FROM "ticket_deadline_queue" WHERE "row_key" = %s AND "use_id" = %s AND "name" = %s`, p(1), p(2), p(3))
		insert := `INSERT INTO "ticket_deadline_queue" ("row_key", "use_id", "name", "at", "payload", "due_at") VALUES ($1, $2, $3, $4, $5::jsonb, to_timestamp($4::bigint / 1000.0))`
		if db.Dialect == rules.DialectSQLite {
			insert = `INSERT INTO "ticket_deadline_queue" ("row_key", "use_id", "name", "at", "payload", "due_at") VALUES (?1, ?2, ?3, ?4, ?5, strftime('%Y-%m-%dT%H:%M:%fZ', ?4 / 1000.0, 'unixepoch'))`
		}
		wantStatements(t, "the arm unit", rec.applied[0][1:], []rules.Statement{
			{Text: clear, Values: []any{uuidColumn, "Timers", "respond"}, Expect: rules.AnyRows},
			{Text: insert, Values: []any{uuidColumn, "Timers", "respond", int64(9000), `{"n":1}`}, Expect: 1},
			{Text: clear, Values: []any{uuidColumn, "Timers", "idle"}, Expect: rules.AnyRows},
			{Text: insert, Values: []any{uuidColumn, "Timers", "idle", int64(3600000), nil}, Expect: 1},
		})
		got := messages(t, db, uuidColumn)
		want := []message{{Name: "idle", State: "ready", At: 3600000, DueAt: 3600000}, {Name: "respond", State: "ready", At: 9000, DueAt: 9000, Payload: `{"n":1}`}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("the messages = %+v, want %+v", got, want)
		}
		err = db.InTx(context.Background(), func(tx *rulestest.Tx) error {
			armed, err := rules.ReadDeadlines(context.Background(), rules.Host{Layout: layout, Store: tx.Store()}, "Ticket", uuidKey)
			wantJSON(t, "the armed deadlines", armed, `[{"useId":"Timers","name":"idle","at":3600000,"payload":null},{"useId":"Timers","name":"respond","at":9000,"payload":{"n":1}}]`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		// A clear deletes the live message; one of a row the change set
		// deletes is left to the delete.
		err = inTx(t, db, rules.Host{Core: script(t, changed(fmt.Sprintf(`{"rows":[%s],"deadlines":[{"kind":"clear","row":%s,"useId":"Timers","name":"respond"}]}`, touched(2), ticketRef()))), Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := messages(t, db, uuidColumn); len(got) != 1 || got[0].Name != "idle" {
			t.Errorf("the messages = %+v, want idle's alone", got)
		}
	})
}

// A dead arming is superseded by the next arm of its deadline, which
// deletes it with the live one, so a deadline holds at most one message
// and the operator's retry of every dead message of the queue
// (`stack queue retry --all`) never meets a live message of the same row,
// use and name under the queue's partial unique index.
func TestAnArmSupersedesADeadArming(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		insertTicket(t, db, uuidColumn, "Printer")
		dead := func(name string, at int64) {
			db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_deadline_queue" (row_key, use_id, name, at, state) VALUES (%s, 'Timers', %s, %s, 'dead')`, db.P(1), db.P(2), db.P(3)), uuidColumn, name, at)
		}
		dead("respond", 1000)
		dead("idle", 2000)
		layout := ticketLayout(false)
		arm := func(version int) {
			t.Helper()
			err := inTx(t, db, rules.Host{Core: script(t, changed(fmt.Sprintf(`{"rows":[%s],"deadlines":[{"kind":"arm","row":%s,"useId":"Timers","name":"respond","at":9000,"payload":null}]}`, touched(version), ticketRef()))), Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		held := func() string {
			var out []string
			for _, m := range messages(t, db, uuidColumn) {
				out = append(out, fmt.Sprintf("%s %s %d", m.Name, m.State, m.At))
			}
			return fmt.Sprint(out)
		}
		arm(1)
		if got, want := held(), "[idle dead 2000 respond ready 9000]"; got != want {
			t.Errorf("the messages = %s, want %s: idle's dead arming kept, respond's superseded", got, want)
		}
		// The new arming dies too, and the next arm supersedes it.
		db.Exec(t, `UPDATE "ticket_deadline_queue" SET state = 'dead' WHERE name = 'respond'`)
		arm(2)
		db.Exec(t, `UPDATE "ticket_deadline_queue" SET state = 'dead' WHERE name = 'respond'`)
		db.Exec(t, `UPDATE "ticket_deadline_queue" SET state = 'ready', attempts = 0 WHERE state = 'dead'`)
		if got, want := held(), "[idle ready 2000 respond ready 9000]"; got != want {
			t.Errorf("after a retry of every dead message, the messages = %s, want %s", got, want)
		}
	})
}

func TestADeleteTakesTheRowsMessagesAndHardItsSubTableRows(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		for _, soft := range []bool{false, true} {
			t.Run(map[bool]string{false: "hard", true: "soft"}[soft], func(t *testing.T) {
				createTickets(t, db)
				t.Cleanup(func() {
					for _, table := range []string{"ticket_deadline_queue", "ticket_timers_deadlines", "ticket"} {
						db.Exec(t, `DROP TABLE "`+table+`"`)
					}
				})
				insertTicket(t, db, uuidColumn, "Printer")
				db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" (ticket_id, name, armed_from) VALUES (%s, 'respond', '1000')`, db.P(1)), uuidColumn)
				insertMessage(t, db, messageID, uuidColumn, "respond", 2500, claimToken)
				db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_deadline_queue" (row_key, use_id, name, at, state) VALUES (%s, 'Timers', 'respond', 1000, 'dead')`, db.P(1)), uuidColumn)
				layout := ticketLayout(soft)
				var opts *rules.ExecuteOptions
				if soft {
					opts = &rules.ExecuteOptions{SoftDelete: map[string]any{"deleted_at": db.Timestamp(time.UnixMilli(5000))}}
				}
				var rec *recorded
				err := inTx(t, db, rules.Host{Core: script(t, changed(fmt.Sprintf(`{"rows":[{"kind":"delete","type":"Ticket","key":%q,"expectedVersion":1}],
					"subRows":[{"row":%s,"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"delete"}}],
					"deadlines":[{"kind":"clear","row":%s,"useId":"Timers","name":"respond"}]}`, uuidKey, ticketRef(), ticketRef()))), Layout: layout}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
					wrapped, r := record(store)
					rec = r
					return wrapped
				}, func(host rules.Host, _ *rulestest.Tx) error {
					_, err := rules.Execute(context.Background(), host, "Ticket", rules.Command{Caller: alice, Now: 5000, Action: rules.Action{Kind: rules.ActionDelete, Key: uuidKey}}, opts)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				p := db.P
				want := []rules.Statement{{Text: fmt.Sprintf(`DELETE FROM "ticket_deadline_queue" WHERE "row_key" = %s`, p(1)), Values: []any{uuidColumn}, Expect: rules.AnyRows}}
				if !soft {
					want = append(want, rules.Statement{Text: fmt.Sprintf(`DELETE FROM "ticket_timers_deadlines" WHERE "ticket_id" = %s`, p(1)), Values: []any{uuidColumn}, Expect: rules.AnyRows})
				}
				wantStatements(t, "the delete unit", rec.applied[0][1:], want)
				if got := messages(t, db, uuidColumn); len(got) != 0 {
					t.Errorf("the messages = %+v, want none", got)
				}
				left := len(db.Rows(t, `SELECT 1 FROM "ticket_timers_deadlines"`))
				if soft && left != 1 {
					t.Errorf("a soft delete left %d sub-table rows, want its 1", left)
				}
				if !soft && left != 0 {
					t.Errorf("a hard delete left %d sub-table rows, want none", left)
				}
			})
		}
	})
}

// deadlineHost is a host on a scripted core at the time 4000.
func deadlineHost(core rules.Core, layout *rules.Layout) rules.Host {
	return rules.Host{Core: core, Layout: layout, Now: func() int64 { return 4000 }, Request: func() string { return "d1" }}
}

// respondDue is respond's deadline of the Ticket, armed to fall due at 5000.
var respondDue = rules.Deadline{Key: uuidKey, UseID: "Timers", Name: "respond", At: 5000}

// fired is the change set of a fire of respond that arms it again: the
// row's write, the sub-table row's update, and respond armed at 9000.
func fired(version int) string {
	return changed(fmt.Sprintf(`{"rows":[%s],
		"subRows":[{"row":%s,"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"update","values":{"fired":true}}}],
		"deadlines":[{"kind":"arm","row":%s,"useId":"Timers","name":"respond","at":9000,"payload":null}],
		"result":{"fired":true}}`, touched(version), ticketRef(), ticketRef()))
}

func TestRunDeadlineSpendsItsClaimBeforeTheArmItsCommandMakes(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		insertTicket(t, db, uuidColumn, "Printer")
		db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" (ticket_id, name) VALUES (%s, 'respond')`, db.P(1)), uuidColumn)
		insertMessage(t, db, messageID, uuidColumn, "respond", 5000, claimToken)
		layout := ticketLayout(false)
		core := script(t, fired(1))
		var rec *recorded
		var run *rules.DeadlineRun
		err := inTx(t, db, deadlineHost(core, layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			run, err = rules.RunDeadline(context.Background(), host, "Ticket", respondDue, rules.Caller{Subject: "fixture-timers-api/TicketTimers"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !run.Ran || run.Result == nil || string(run.Result.Result) != `{"fired":true}` {
			t.Fatalf("run = %+v, want run with its result", run)
		}
		// The command runs at the deadline's time, after the host's.
		wantJSON(t, "the command", core.commands[0], fmt.Sprintf(`{"caller":{"subject":"fixture-timers-api/TicketTimers"},"now":5000,"action":{"kind":"deadline","key":%q,"useId":"Timers","name":"respond","at":5000,"payload":null},"request":"d1"}`, uuidKey))
		spend := `DELETE FROM "ticket_deadline_queue" WHERE "id" = $1::uuid AND "claim_token" = $2::uuid AND "state" = 'claimed'`
		if db.Dialect == rules.DialectSQLite {
			spend = `DELETE FROM "ticket_deadline_queue" WHERE "id" = ?1 AND "claim_token" = ?2 AND "state" = 'claimed'`
		}
		unit := rec.applied[0]
		if len(unit) != 5 {
			t.Fatalf("the unit = %v, want the row, the spend, the sub-table row and the arm", unit)
		}
		wantStatements(t, "the spend", unit[1:2], []rules.Statement{{Text: spend, Values: []any{messageID, claimToken}, Expect: 1}})
		// The arm the fired command made survives the spend.
		if got := messages(t, db, uuidColumn); len(got) != 1 || got[0].At != 9000 || got[0].State != "ready" {
			t.Errorf("the messages = %+v, want respond ready at 9000", got)
		}
		if got := ticketVersion(t, db, uuidColumn); got != 2 {
			t.Errorf("the version = %d, want 2", got)
		}
	})
}

func TestRunDeadlineOnALostClaimRunsNothing(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		insertTicket(t, db, uuidColumn, "Printer")
		db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" (ticket_id, name) VALUES (%s, 'respond')`, db.P(1)), uuidColumn)
		// Another command re-armed respond after the worker claimed it: the
		// worker's message is gone, a new one ready, and the row moved on.
		insertMessage(t, db, "0b9c6f0e-5d55-4f2a-9a43-6a1f3c0c1009", uuidColumn, "respond", 7200000, "")
		db.Exec(t, `UPDATE "ticket" SET title = 'Printer!'`)
		layout := ticketLayout(false)
		var run *rules.DeadlineRun
		err := inTx(t, db, deadlineHost(script(t, fired(2)), layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			run, err = rules.RunDeadline(context.Background(), host, "Ticket", respondDue, rules.Caller{Subject: "w"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if run.Ran || run.Why != rules.WhyClaimLost {
			t.Errorf("run = %+v, want nothing run, the claim lost", run)
		}
		if got := messages(t, db, uuidColumn); len(got) != 1 || got[0].At != 7200000 {
			t.Errorf("the messages = %+v, want the new arming alone", got)
		}
		if got := ticketVersion(t, db, uuidColumn); got != 2 {
			t.Errorf("the version = %d, want 2: nothing of the deadline written", got)
		}

		// A claim another worker took over after the lease ran out is
		// another token's: the first worker's run spends nothing.
		insertMessage(t, db, messageID, uuidColumn, "remind", 5000, otherToken)
		err = inTx(t, db, deadlineHost(script(t, changed(fmt.Sprintf(`{"rows":[%s]}`, touched(2)))), layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			run, err = rules.RunDeadline(context.Background(), host, "Ticket", rules.Deadline{Key: uuidKey, UseID: "Timers", Name: "remind", At: 5000}, rules.Caller{Subject: "w"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if run.Ran || run.Why != rules.WhyClaimLost {
			t.Errorf("run = %+v, want nothing run, the claim lost", run)
		}
	})
}

func TestRunDeadlineWhoseRowMovedRunsAgain(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		insertTicket(t, db, uuidColumn, "Printer")
		db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" (ticket_id, name) VALUES (%s, 'respond')`, db.P(1)), uuidColumn)
		insertMessage(t, db, messageID, uuidColumn, "respond", 5000, claimToken)
		layout := ticketLayout(false)
		// The first run sees the row at version 1; a write moves it on
		// before its unit, and the run after reads version 2.
		core := script(t, fired(1), fired(2))
		var run *rules.DeadlineRun
		err := inTx(t, db, deadlineHost(core, layout), func(tx *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, _ := record(store)
			wrapped.before = func(ctx context.Context, n int) error {
				if n > 0 {
					return nil
				}
				_, err := tx.Exec(ctx, `UPDATE "ticket" SET title = 'Printer!'`)
				return err
			}
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			run, err = rules.RunDeadline(context.Background(), host, "Ticket", respondDue, rules.Caller{Subject: "w"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !run.Ran || run.Result.Attempts != 2 {
			t.Errorf("run = %+v, want run on its second attempt", run)
		}
		if got := messages(t, db, uuidColumn); len(got) != 1 || got[0].At != 9000 {
			t.Errorf("the messages = %+v, want respond armed again at 9000", got)
		}
	})
}

func TestRunDeadlineOfAGoneRowSpendsItsMessage(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		layout := ticketLayout(false)
		// The row went by a raw delete, which leaves its messages.
		insertMessage(t, db, messageID, uuidColumn, "respond", 5000, claimToken)
		notFound := `{"done":{"refused":{"code":"not_found","status":404,"class":"EngineError","message":"Ticket ` + uuidKey + ` not found"}}}`
		var run *rules.DeadlineRun
		err := inTx(t, db, deadlineHost(script(t, notFound), layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			run, err = rules.RunDeadline(context.Background(), host, "Ticket", respondDue, rules.Caller{Subject: "w"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if run.Ran || run.Why != rules.WhyGone || run.Refusal == nil || run.Refusal.Code != rules.CodeNotFound {
			t.Errorf("run = %+v, want nothing run, the row gone", run)
		}
		if got := messages(t, db, uuidColumn); len(got) != 0 {
			t.Errorf("the messages = %+v, want the claimed one spent", got)
		}

		// Run again on the spent claim, it finds nothing to spend.
		err = inTx(t, db, deadlineHost(script(t, notFound), layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			run, err = rules.RunDeadline(context.Background(), host, "Ticket", respondDue, rules.Caller{Subject: "w"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if err != nil || run.Ran || run.Why != rules.WhyClaimLost {
			t.Errorf("run = %+v, %v, want the claim lost", run, err)
		}

		// A not_found while the row and the use are there is of something
		// a rule reached: the run's error, its message left claimed for
		// the worker to fail and run again.
		insertTicket(t, db, uuidColumn, "Printer")
		insertMessage(t, db, messageID, uuidColumn, "respond", 5000, claimToken)
		reached := `{"done":{"refused":{"code":"not_found","status":404,"class":"EngineError","message":"Shelf 7 not found"}}}`
		err = inTx(t, db, deadlineHost(script(t, reached), layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.RunDeadline(context.Background(), host, "Ticket", respondDue, rules.Caller{Subject: "w"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if refusal := refusalOf(t, err); refusal.Code != rules.CodeNotFound || refusal.Message != "Shelf 7 not found" {
			t.Errorf("refusal = %+v, want the core's not_found", *refusal)
		}
		if got := messages(t, db, uuidColumn); len(got) != 1 || got[0].State != "claimed" {
			t.Errorf("the messages = %+v, want respond's still claimed", got)
		}

		// A use the layout no longer lists is gone with the row there.
		gone := rules.Deadline{Key: uuidKey, UseID: "Lapsed", Name: "respond", At: 5000}
		err = inTx(t, db, deadlineHost(script(t, notFound), layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			run, err = rules.RunDeadline(context.Background(), host, "Ticket", gone, rules.Caller{Subject: "w"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if err != nil || run.Ran || run.Why != rules.WhyGone {
			t.Errorf("run = %+v, %v, want nothing run, the use gone", run, err)
		}

		// Any other refusal is the run's error, its message left claimed
		// for the worker to fail.
		insertMessage(t, db, messageID, uuidColumn, "respond", 5000, claimToken)
		vetoed := `{"done":{"refused":{"code":"vetoed","status":409,"class":"BehaviorVetoError","message":"held"}}}`
		err = inTx(t, db, deadlineHost(script(t, vetoed), layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.RunDeadline(context.Background(), host, "Ticket", respondDue, rules.Caller{Subject: "w"}, &rules.Claim{ID: messageID, Token: claimToken}, nil)
			return err
		})
		if refusal := refusalOf(t, err); refusal.Code != rules.CodeVetoed {
			t.Errorf("refusal = %+v, want vetoed", *refusal)
		}
		if got := messages(t, db, uuidColumn); len(got) != 1 || got[0].State != "claimed" {
			t.Errorf("the messages = %+v, want respond's still claimed", got)
		}
	})
}

func TestADeadlineCommandWithoutAClaimRunsOnlyWhileArmed(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTickets(t, db)
		insertTicket(t, db, uuidColumn, "Printer")
		db.Exec(t, fmt.Sprintf(`INSERT INTO "ticket_timers_deadlines" (ticket_id, name) VALUES (%s, 'respond')`, db.P(1)), uuidColumn)
		layout := ticketLayout(false)
		command := func(at, now int64) rules.Command {
			return rules.Command{Caller: rules.Caller{Subject: "runner"}, Now: now, Request: "d1", Action: rules.Action{Kind: rules.ActionDeadline, Key: uuidKey, UseID: "Timers", Name: "respond", At: at}}
		}
		execute := func(command rules.Command, rec **recorded) error {
			return inTx(t, db, rules.Host{Core: script(t, fired(1)), Layout: layout}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
				wrapped, r := record(store)
				if rec != nil {
					*rec = r
				}
				return wrapped
			}, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Ticket", command, nil)
				return err
			})
		}
		notArmed := rules.Refusal{Code: rules.CodeNotFound, Status: 404, Class: "EngineError", Message: "Ticket " + uuidKey + " has no deadline Timers.respond armed at 5000"}
		wantRefusal(t, execute(command(5000, 6000), nil), notArmed)
		insertMessage(t, db, messageID, uuidColumn, "respond", 4000, "")
		wantRefusal(t, execute(command(5000, 6000), nil), notArmed)
		db.Exec(t, `UPDATE "ticket_deadline_queue" SET at = 5000`)
		wantRefusal(t, execute(command(5000, 4500), nil), rules.Refusal{Code: rules.CodeInvalidArgument, Status: 400, Class: "EngineError", Message: "deadline Timers.respond of Ticket " + uuidKey + " falls due at 5000, after 4500"})

		var rec *recorded
		if err := execute(command(5000, 6000), &rec); err != nil {
			t.Fatal(err)
		}
		p := db.P
		wantStatements(t, "the arming's read", rec.queries[:1], []rules.Statement{{
			Text:   fmt.Sprintf(`SELECT "at", "payload" FROM "ticket_deadline_queue" WHERE "row_key" = %s AND "use_id" = %s AND "name" = %s AND "state" IN ('ready', 'claimed')`, p(1), p(2), p(3)),
			Values: []any{uuidColumn, "Timers", "respond"},
		}})
		wantStatements(t, "the spend", rec.applied[0][1:2], []rules.Statement{{
			Text:   fmt.Sprintf(`DELETE FROM "ticket_deadline_queue" WHERE "row_key" = %s AND "use_id" = %s AND "name" = %s AND "at" = %s AND "state" IN ('ready', 'claimed')`, p(1), p(2), p(3), p(4)),
			Values: []any{uuidColumn, "Timers", "respond", int64(5000)},
			Expect: 1,
		}})
		if got := messages(t, db, uuidColumn); len(got) != 1 || got[0].At != 9000 {
			t.Errorf("the messages = %+v, want respond armed again at 9000 alone", got)
		}
	})
}
