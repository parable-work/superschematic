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

// A table whose uses take a command's preconditions, keep owned columns
// of every declared type and a sub-table, arm a deadline and are read by
// the value one of their owned fields holds (D104), through the adapter on
// each dialect: Job as the generator lays it out for fixture-agents-db,
// implementing Lease beside Retries. Nothing of the adapter's names a
// behavior: it hands the core the caller's preconditions as the command
// presents them (or the options give them), answers a holding read by the
// layout's column of the use's field, reads a Generic.Int64 column back as
// a JSON number and a Generic.JSON one as the JSON it holds, and applies a
// deadline cleared and armed again in one change set in its order. The
// cases that run Lease on the archive's core hold the core's own refusal
// of a malformed precondition, made before the row is read.

// leaseConfig is a Lease that fences writes by its token when one is
// presented, and takes none unasked.
const leaseConfig = `{}`

// jobTable is Job's layout as behaviorlayout writes it: its own column,
// Lease's owned columns and its directives, and, with retries, Retries'
// owned columns and its attempts, each sub-table a child of the row, and
// the table's deadline queue; soft-deletable when live. Its spec is Lease's
// alone, which the archive's core runs.
func jobTable(retries, live bool) *rules.Table {
	column := func(field, name, typ string) rules.Column {
		out := rules.Column{Field: field, Column: name, Type: typ}
		if typ == "Temporal.DateTime" {
			out.Convert = rules.ConvertEpochMillis
		}
		return out
	}
	owned := func(name, field, col, typ string) rules.OwnedField {
		return rules.OwnedField{Name: name, Column: column(field, col, typ)}
	}
	lease := rules.Use{
		ID: "Lease", Behavior: "Lease", Member: "lease",
		Fields: []rules.OwnedField{
			owned("holder", "leaseHolder", "lease_holder", "string"),
			owned("token", "leaseToken", "lease_token", "Generic.Int64"),
			owned("acquiredAt", "leaseAcquiredAt", "lease_acquired_at", "Temporal.DateTime"),
			owned("renewedAt", "leaseRenewedAt", "lease_renewed_at", "Temporal.DateTime"),
			owned("expiresAt", "leaseExpiresAt", "lease_expires_at", "Temporal.DateTime"),
			owned("expiries", "leaseExpiries", "lease_expiries", "Generic.Int64"),
			owned("ended", "leaseEnded", "lease_ended", "Generic.JSON"),
		},
		Operations: []rules.Operation{
			{Name: "acquire", Writes: true}, {Name: "heartbeat", Writes: true}, {Name: "release", Writes: true},
			{Name: "expire", Writes: true}, {Name: "direct", Writes: true}, {Name: "acknowledge", Writes: true},
			{Name: "resetExpiries", Writes: true},
		},
		Tables: []rules.SubTable{{
			Name: "directives", Table: "job_lease_directives", Row: "job_id",
			Key: owned("number", "number", "number", "string"),
			Fields: []rules.OwnedField{
				owned("token", "token", "token", "Generic.Int64"),
				owned("name", "name", "name", "string"),
				owned("data", "data", "data", "Generic.JSON"),
				owned("dedupeKey", "dedupeKey", "dedupe_key", "string"),
				owned("sentBy", "sentBy", "sent_by", "string"),
				owned("sentAt", "sentAt", "sent_at", "Temporal.DateTime"),
				owned("acknowledgedAt", "acknowledgedAt", "acknowledged_at", "Temporal.DateTime"),
			},
		}},
	}
	table := &rules.Table{
		Type:    "Job",
		Table:   "job",
		Key:     column("id", "id", "string"),
		Version: "_version",
		Own:     []rules.Column{column("title", "title", "string")},
		Uses:    []rules.Use{lease},
		Spec: rules.TypeSpec{
			Name:   "Job",
			Uses:   []rules.UseSpec{{Behavior: "Lease", Config: json.RawMessage(leaseConfig)}},
			Fields: []rules.FieldSpec{{Name: "title", Type: "string"}},
		},
		Deadlines: &rules.Deadlines{Queue: "JobDeadline", Table: "job_deadline_queue", RowKey: "row_key", Use: "use_id", Name: "name", At: "at", Payload: "payload"},
	}
	if retries {
		table.Uses = append(table.Uses, rules.Use{
			ID: "Retries", Behavior: "Retries", Member: "retries",
			Fields: []rules.OwnedField{
				owned("total", "retriesTotal", "retries_total", "Generic.Int64"),
				owned("classAttempts", "retriesClassAttempts", "retries_class_attempts", "Generic.JSON"),
				owned("bestScore", "retriesBestScore", "retries_best_score", "number"),
				owned("exhausted", "retriesExhausted", "retries_exhausted", "boolean"),
				owned("retryAt", "retriesRetryAt", "retries_retry_at", "Temporal.DateTime"),
			},
			Operations: []rules.Operation{{Name: "recordAttempt", Writes: true}},
			Tables: []rules.SubTable{{
				Name: "attempts", Table: "job_retries_attempts", Row: "job_id",
				Key: owned("number", "number", "number", "string"),
				Fields: []rules.OwnedField{
					owned("at", "at", "at", "Temporal.DateTime"),
					owned("score", "score", "score", "number"),
					owned("kept", "kept", "kept", "boolean"),
					owned("detail", "detail", "detail", "Generic.JSON"),
				},
			}},
		})
	}
	if live {
		table.Live = &rules.Live{Column: "deleted_at"}
	}
	return table
}

// jobLayout is a fresh layout of Job, so no test's define check is
// another's.
func jobLayout(retries, live bool) *rules.Layout {
	return &rules.Layout{Types: map[string]*rules.Table{"Job": jobTable(retries, live)}}
}

// createJobs writes Job's tables as sqlgen writes them: the row's table
// with every owned column of both uses, a Generic.Int64 an integer column
// and a Generic.JSON one JSON, with @optimistic's trigger; each sub-table a
// child of the row with a surrogate id and the unique index of (row,
// number); and the deadline queue.
func createJobs(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	surrogate := "TEXT DEFAULT (lower(hex(randomblob(16))))"
	if db.Dialect == rules.DialectPostgres {
		surrogate = "UUID DEFAULT gen_random_uuid()"
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "job" (
  id %s,
  title %s,
  lease_holder %s,
  lease_token %s,
  lease_acquired_at %s,
  lease_renewed_at %s,
  lease_expires_at %s,
  lease_expiries %s,
  lease_ended %s,
  retries_total %s,
  retries_class_attempts %s,
  retries_best_score %s,
  retries_exhausted %s,
  retries_retry_at %s,
  deleted_at %s,
  _version %s
)`, types.Key, types.Text, types.Text, types.Integer, types.Timestamp, types.Timestamp, types.Timestamp, types.Integer, types.JSON,
		types.Integer, types.JSON, types.Number, types.Boolean, types.Timestamp, types.Timestamp, types.Version))
	for _, ddl := range db.BumpVersion("job") {
		db.Exec(t, ddl)
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "job_lease_directives" (
  id %s NOT NULL PRIMARY KEY,
  job_id %s NOT NULL REFERENCES "job" (id) ON DELETE CASCADE,
  number %s NOT NULL,
  token %s,
  name %s,
  data %s,
  dedupe_key %s,
  sent_by %s,
  sent_at %s,
  acknowledged_at %s,
  UNIQUE (job_id, number)
)`, surrogate, types.Text, types.Text, types.Integer, types.Text, types.JSON, types.Text, types.Text, types.Timestamp, types.Timestamp))
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "job_retries_attempts" (
  id %s NOT NULL PRIMARY KEY,
  job_id %s NOT NULL REFERENCES "job" (id) ON DELETE CASCADE,
  number %s NOT NULL,
  at %s,
  score %s,
  kept %s,
  detail %s,
  UNIQUE (job_id, number)
)`, surrogate, types.Text, types.Text, types.Timestamp, types.Number, types.Boolean, types.JSON))
	for _, ddl := range db.DeadlineQueue("job_deadline_queue", types.Text) {
		db.Exec(t, ddl)
	}
}

// insertJob writes a row of Job past the adapter: its holder (none for ""),
// its token, and whether it is soft-deleted.
func insertJob(t *testing.T, db *rulestest.DB, id, title, holder string, token int64, deleted bool) {
	t.Helper()
	var holderValue, deletedAt any
	if holder != "" {
		holderValue = holder
	}
	if deleted {
		deletedAt = db.Timestamp(time.UnixMilli(5000))
	}
	db.Exec(t, fmt.Sprintf(`INSERT INTO "job" (id, title, lease_holder, lease_token, deleted_at) VALUES (%s, %s, %s, %s, %s)`, db.P(1), db.P(2), db.P(3), db.P(4), db.P(5)), id, title, holderValue, token, deletedAt)
}

func jobHost(core rules.Core, layout *rules.Layout) rules.Host {
	return rules.Host{Core: core, Layout: layout, Now: func() int64 { return 1000000 }, Request: func() string { return "r1" }}
}

// presented is the preconditions core's first command presented, as JSON;
// absent for none.
func presented(t *testing.T, core *scripted) string {
	t.Helper()
	if len(core.commands) == 0 {
		t.Fatal("the core ran no command")
	}
	if len(core.commands[0].Preconditions) == 0 {
		return "absent"
	}
	return string(jsonOf(t, core.commands[0].Preconditions))
}

func TestACommandPresentsTheCallersPreconditions(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createJobs(t, db)
		insertJob(t, db, "j1", "Build", "", 0, false)
		ctx := context.Background()
		token := rules.Preconditions{"Lease": struct {
			Token int64 `json:"token"`
		}{3}}
		run := func(fn func(host rules.Host) error) *scripted {
			t.Helper()
			core := script(t, changed(""))
			if err := inTx(t, db, jobHost(core, jobLayout(false, false)), nil, func(host rules.Host, _ *rulestest.Tx) error { return fn(host) }); err != nil {
				t.Fatal(err)
			}
			return core
		}
		want := `{"Lease":{"token":3}}`

		// An update, a delete and an operation present the options'
		// preconditions as the command's.
		core := run(func(host rules.Host) error {
			return rules.RunUpdate(ctx, host, "Job", "j1", map[string]any{"title": "Mine"}, alice, &rules.ExecuteOptions{Preconditions: token})
		})
		if got := presented(t, core); got != want {
			t.Errorf("an update presented %s, want %s", got, want)
		}
		core = run(func(host rules.Host) error {
			return rules.RunDelete(ctx, host, "Job", "j1", alice, &rules.ExecuteOptions{Preconditions: token})
		})
		if got := presented(t, core); got != want {
			t.Errorf("a delete presented %s, want %s", got, want)
		}
		core = run(func(host rules.Host) error {
			_, err := rules.RunOperation(ctx, host, "Job", "j1", "Lease", "heartbeat", nil, alice, &rules.ExecuteOptions{Preconditions: token})
			return err
		})
		if got := presented(t, core); got != want {
			t.Errorf("an operation presented %s, want %s", got, want)
		}

		// A command that presents its own takes the options' too, use by
		// use, the options' entry standing in for its own of the same use,
		// as TypeScript's and Rust's adapters merge them; one with none, and
		// options with none, presents none.
		core = run(func(host rules.Host) error {
			command := rules.Command{Caller: alice, Now: 1000000, Action: rules.Action{Kind: rules.ActionDelete, Key: "j1"}, Preconditions: rules.Preconditions{"Lease": json.RawMessage(`{"token":7}`)}}
			_, err := rules.Execute(ctx, host, "Job", command, &rules.ExecuteOptions{Preconditions: token})
			return err
		})
		if got := presented(t, core); got != `{"Lease":{"token":3}}` {
			t.Errorf("a command presenting its own beside the options' presented %s", got)
		}
		core = run(func(host rules.Host) error {
			command := rules.Command{Caller: alice, Now: 1000000, Action: rules.Action{Kind: rules.ActionDelete, Key: "j1"}, Preconditions: rules.Preconditions{"Other": json.RawMessage(`{"seen":1}`)}}
			_, err := rules.Execute(ctx, host, "Job", command, &rules.ExecuteOptions{Preconditions: token})
			return err
		})
		if got := presented(t, core); got != `{"Lease":{"token":3},"Other":{"seen":1}}` {
			t.Errorf("a command presenting another use's beside the options' presented %s", got)
		}
		core = run(func(host rules.Host) error {
			return rules.RunUpdate(ctx, host, "Job", "j1", map[string]any{"title": "Mine"}, alice, nil)
		})
		if got := presented(t, core); got != "absent" {
			t.Errorf("an update with no preconditions presented %s", got)
		}
		if text := string(jsonOf(t, core.commands[0])); text != `{"caller":{"subject":"alice"},"now":1000000,"action":{"kind":"update","key":"j1","patch":{"title":"Mine"}},"request":"r1"}` {
			t.Errorf("an update with no preconditions = %s, want no preconditions member", text)
		}

		// A create and an upsert's prepared create present them too, for
		// the core to refuse.
		core = run(func(host rules.Host) error {
			_, err := rules.RunCreate(ctx, host, "Job", map[string]any{"title": "New"}, alice, &rules.CreateOptions{ID: "j2", ExecuteOptions: rules.ExecuteOptions{Preconditions: token}})
			return err
		})
		if got := presented(t, core); got != want {
			t.Errorf("a create presented %s, want %s", got, want)
		}
		prepared := script(t, changed(`{"rows":[{"kind":"create","type":"Job","key":"j3","own":{"title":"New"}}]}`))
		err := inTx(t, db, jobHost(prepared, jobLayout(false, false)), nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.PrepareCreate(ctx, host, "Job", map[string]any{"title": "New"}, alice, &rules.CreateOptions{ID: "j3", ExecuteOptions: rules.ExecuteOptions{Preconditions: token}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := presented(t, prepared); got != want {
			t.Errorf("a prepared create presented %s, want %s", got, want)
		}
	})
}

func TestTheCoreReadsTheRowsWhoseUsesFieldHoldsAValue(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createJobs(t, db)
		insertJob(t, db, "j5", "e", "wren", 2, false)
		insertJob(t, db, "j2", "b", "wren", 1, false)
		insertJob(t, db, "j4", "d", "otto", 1, false)
		insertJob(t, db, "j1", "a", "wren", 3, false)
		insertJob(t, db, "j3", "c", "wren", 1, true)
		layout := jobLayout(false, true)
		holding := func(field, value, after string, limit int) string {
			request := map[string]any{"kind": "holding", "type": "Job", "useId": "Lease", "field": field, "value": json.RawMessage(value), "limit": limit}
			if after != "" {
				request["after"] = after
			}
			return string(jsonOf(t, request))
		}
		read := func(lock bool, requests ...string) (*scripted, error) {
			steps := []string{}
			if lock {
				steps = append(steps, `{"lock":["Job/x"]}`)
			}
			text := "["
			for i, request := range requests {
				if i > 0 {
					text += ","
				}
				text += request
			}
			steps = append(steps, `{"read":`+text+`]}`, changed(""))
			core := script(t, steps...)
			err := inTx(t, db, jobHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Job", rules.Command{Caller: alice, Now: 1000000, Action: rules.Action{Kind: rules.ActionOperation, Key: "j1", UseID: "Lease", Operation: "expire"}}, nil)
				return err
			})
			return core, err
		}
		row := func(id, title string, token int) string {
			return fmt.Sprintf(`{"key":%q,"row":{"own":{"title":%q},"owned":{"Lease":{"holder":"wren","token":%d}},"version":1}}`, id, title, token)
		}

		// The first page holds two, in key order, and more follow; the next
		// page, after the last key, holds the rest, a soft-deleted row left
		// out, each answered as a linked read is. A number compares as the
		// integer column holds it. A type the layout lacks has none.
		other := `{"kind":"holding","type":"Session","useId":"Lease","field":"holder","value":"wren","limit":500}`
		core, err := read(false, holding("holder", `"wren"`, "", 2), holding("holder", `"wren"`, "j2", 2), holding("token", `1`, "", 500), other)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "the answers", core.answers[1], `{"reads":[
			{"request":`+holding("holder", `"wren"`, "", 2)+`,"answer":{"linked":{"rows":[`+row("j1", "a", 3)+`,`+row("j2", "b", 1)+`],"more":true}},"held":0},
			{"request":`+holding("holder", `"wren"`, "j2", 2)+`,"answer":{"linked":{"rows":[`+row("j5", "e", 2)+`],"more":false}},"held":0},
			{"request":`+holding("token", `1`, "", 500)+`,"answer":{"linked":{"rows":[`+row("j2", "b", 1)+`,{"key":"j4","row":{"own":{"title":"d"},"owned":{"Lease":{"holder":"otto","token":1}},"version":1}}],"more":false}},"held":0},
			{"request":`+other+`,"answer":{"linked":{"rows":[],"more":false}},"held":0}]}`)

		// A use or a field the layout does not give the table is the
		// adapter's own failure.
		_, err = read(false, `{"kind":"holding","type":"Job","useId":"Retries","field":"total","value":1,"limit":500}`)
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: Job has no use Retries in the behaviors' layout"})
		_, err = read(false, `{"kind":"holding","type":"Job","useId":"Lease","field":"active","value":true,"limit":500}`)
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: Job's use Lease (Lease) has no field active in the behaviors' layout"})

		// Under a lock, a store whose locks are fenced has no check of it
		// yet.
		_, err = read(true, holding("holder", `"wren"`, "", 500))
		if db.Dialect == rules.DialectSQLite {
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: a read of the rows whose behavior's field holds a value under a lock has no check on SQLite or D1 yet (D103)"})
		} else if err != nil {
			t.Errorf("a holding read under a lock on Postgres: %v", err)
		}
	})
}

func TestOwnedColumnsAndSubTableRowsOfEveryDeclaredTypeRoundTrip(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createJobs(t, db)
		ctx := context.Background()
		layout := jobLayout(true, false)
		apply := func(command rules.Command, steps ...string) {
			t.Helper()
			core := script(t, steps...)
			if err := inTx(t, db, jobHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(ctx, host, "Job", command, nil)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}

		// A create whose owned columns hold an integer, a JSON object, a
		// fraction and a boolean, with a sub-table row of each, and its
		// expiry armed, cleared and armed again in one change set.
		apply(rules.Command{Caller: alice, Now: 1000000, Action: rules.Action{Kind: rules.ActionCreate, ID: "j1", Own: map[string]json.RawMessage{"title": json.RawMessage(`"Build"`)}}},
			changed(`{"rows":[{"kind":"create","type":"Job","key":"j1","own":{"title":"Build"},"owned":{
				"Lease":{"holder":"wren","token":9007199254740991,"expiresAt":1060000,"expiries":0,"ended":{"reason":"ttl","at":5}},
				"Retries":{"total":0,"classAttempts":{"timeout":0,"rejected":0},"bestScore":0.5,"exhausted":false}}}],
			"subRows":[
				{"row":{"type":"Job","key":"j1"},"useId":"Lease","table":"directives","key":"0000000001","change":{"kind":"insert","values":{"token":1,"name":"rebase","data":{"onto":"main","n":[1,2]},"sentBy":"bob","sentAt":1000000}}},
				{"row":{"type":"Job","key":"j1"},"useId":"Retries","table":"attempts","key":"0000000001","change":{"kind":"insert","values":{"at":1000000,"score":1.5,"kept":true,"detail":{"exit":2}}}},
				{"row":{"type":"Job","key":"j1"},"useId":"Retries","table":"attempts","key":"0000000002","change":{"kind":"insert","values":{"at":1000000,"score":2,"kept":false}}}],
			"deadlines":[
				{"kind":"arm","row":{"type":"Job","key":"j1"},"useId":"Lease","name":"expire","at":1060000,"payload":null},
				{"kind":"clear","row":{"type":"Job","key":"j1"},"useId":"Lease","name":"expire"},
				{"kind":"arm","row":{"type":"Job","key":"j1"},"useId":"Lease","name":"expire","at":1070000,"payload":null}]}`))
		var row *rules.Row
		var directives, attempts []rules.SubRow
		var armed []rules.ArmedDeadline
		readBack := func() {
			t.Helper()
			if err := inTx(t, db, jobHost(nil, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				var err error
				if row, err = rules.ReadRow(ctx, host, "Job", "j1"); err != nil {
					return err
				}
				if directives, err = rules.ReadSubRows(ctx, host, "Job", "j1", "Lease", "directives"); err != nil {
					return err
				}
				if attempts, err = rules.ReadSubRows(ctx, host, "Job", "j1", "Retries", "attempts"); err != nil {
					return err
				}
				armed, err = rules.ReadDeadlines(ctx, host, "Job", "j1")
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		readBack()
		wantJSON(t, "the row", row, `{"own":{"title":"Build"},"owned":{
			"Lease":{"holder":"wren","token":9007199254740991,"expiresAt":1060000,"expiries":0,"ended":{"reason":"ttl","at":5}},
			"Retries":{"total":0,"classAttempts":{"timeout":0,"rejected":0},"bestScore":0.5,"exhausted":false}},"version":1}`)
		wantJSON(t, "the directives", directives, `[{"key":"0000000001","values":{"token":1,"name":"rebase","data":{"onto":"main","n":[1,2]},"sentBy":"bob","sentAt":1000000}}]`)
		wantJSON(t, "the attempts", attempts, `[{"key":"0000000001","values":{"at":1000000,"score":1.5,"kept":true,"detail":{"exit":2}}},{"key":"0000000002","values":{"at":1000000,"score":2,"kept":false}}]`)
		wantJSON(t, "the armed deadlines", armed, `[{"useId":"Lease","name":"expire","at":1070000,"payload":null}]`)

		// An update clearing the owned columns and setting others, a
		// sub-table row's update and delete, and the expiry cleared.
		apply(rules.Command{Caller: alice, Now: 1010000, Action: rules.Action{Kind: rules.ActionOperation, Key: "j1", UseID: "Lease", Operation: "release"}},
			`{"read":[{"kind":"row","type":"Job","key":"j1"}]}`,
			changed(`{"rows":[{"kind":"update","type":"Job","key":"j1","expectedVersion":1,"owned":{
				"Lease":{"holder":null,"token":2,"expiresAt":null,"ended":{"reason":"release","at":1010000}},
				"Retries":{"total":1,"bestScore":-0.25,"exhausted":true}}}],
			"subRows":[
				{"row":{"type":"Job","key":"j1"},"useId":"Lease","table":"directives","key":"0000000001","change":{"kind":"delete"}},
				{"row":{"type":"Job","key":"j1"},"useId":"Retries","table":"attempts","key":"0000000002","change":{"kind":"update","values":{"kept":true,"detail":[]}}}],
			"deadlines":[{"kind":"clear","row":{"type":"Job","key":"j1"},"useId":"Lease","name":"expire"}]}`))
		readBack()
		wantJSON(t, "the row", row, `{"own":{"title":"Build"},"owned":{
			"Lease":{"token":2,"expiries":0,"ended":{"reason":"release","at":1010000}},
			"Retries":{"total":1,"classAttempts":{"timeout":0,"rejected":0},"bestScore":-0.25,"exhausted":true}},"version":2}`)
		wantJSON(t, "the directives", directives, `[]`)
		wantJSON(t, "the attempts", attempts, `[{"key":"0000000001","values":{"at":1000000,"score":1.5,"kept":true,"detail":{"exit":2}}},{"key":"0000000002","values":{"at":1000000,"score":2,"kept":true,"detail":[]}}]`)
		wantJSON(t, "the armed deadlines", armed, `[]`)
	})
}

// TestThePreconditionsReachTheCoresLease runs Job's Lease on the archive's
// core: the core checks a command's preconditions before it reads the row,
// so a malformed one is refused as PreconditionsError, a key no row holds
// among them; a stale token is Lease's veto; and the current one goes
// through an update, an operation and a delete. A create that presents one
// is refused.
func TestThePreconditionsReachTheCoresLease(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createJobs(t, db)
		ctx := context.Background()
		layout := jobLayout(false, false)
		host := rules.Host{Layout: layout, Now: func() int64 { return 1000000 }}
		wren := rules.Caller{Subject: "wren"}
		token := func(n int64) *rules.ExecuteOptions {
			return &rules.ExecuteOptions{Preconditions: rules.Preconditions{"Lease": map[string]int64{"token": n}}}
		}
		do := func(fn func(host rules.Host) error) error {
			t.Helper()
			return inTx(t, db, host, nil, func(host rules.Host, _ *rulestest.Tx) error { return fn(host) })
		}
		if err := do(func(host rules.Host) error {
			_, err := rules.RunCreate(ctx, host, "Job", map[string]any{"title": "Build"}, alice, &rules.CreateOptions{ID: "j1"})
			return err
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
		var acquired json.RawMessage
		if err := do(func(host rules.Host) error {
			var err error
			acquired, err = rules.RunOperation(ctx, host, "Job", "j1", "Lease", "acquire", nil, wren, nil)
			return err
		}); err != nil {
			t.Fatalf("acquire: %v", err)
		}
		wantJSON(t, "acquire", acquired, `{"token":1,"expiresAt":1060000,"heartbeatMs":20000}`)

		// Malformed: an entry its schema refuses, and one for a use the
		// type lacks, refused before the row is read, so a key no row holds
		// is refused alike.
		missing := &rules.ExecuteOptions{Preconditions: rules.Preconditions{"Lease": map[string]any{}}}
		for _, key := range []string{"j1", "j9"} {
			err := do(func(host rules.Host) error {
				return rules.RunUpdate(ctx, host, "Job", key, map[string]any{"title": "Mine"}, wren, missing)
			})
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeInvalidArgument, Status: 400, Class: "PreconditionsError", Message: "preconditions of Job: /Lease: must have required property 'token'"})
		}
		err := do(func(host rules.Host) error {
			return rules.RunDelete(ctx, host, "Job", "j1", wren, &rules.ExecuteOptions{Preconditions: rules.Preconditions{"Retries": map[string]int64{"token": 1}}})
		})
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInvalidArgument, Status: 400, Class: "PreconditionsError", Message: "preconditions of Job: /Retries: Job has no behavior use Retries (its uses that take preconditions: Lease)"})
		err = do(func(host rules.Host) error {
			_, err := rules.RunCreate(ctx, host, "Job", map[string]any{"title": "New"}, wren, &rules.CreateOptions{ID: "j2", ExecuteOptions: *token(1)})
			return err
		})
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInvalidArgument, Status: 400, Class: "PreconditionsError", Message: "preconditions of Job: a create takes no preconditions: an update, a delete or an operation of a row presents them"})

		// A stale token is Lease's veto, whoever presents it; the current
		// one goes through.
		err = do(func(host rules.Host) error {
			return rules.RunUpdate(ctx, host, "Job", "j1", map[string]any{"title": "Late"}, wren, token(0))
		})
		if refusal := refusalOf(t, err); refusal.Code != rules.CodeVetoed || !sameJSON(refusal.Details, []byte(`{"behavior":"Lease","action":"update","reason":"token 0 is stale: the lease is at token 1","vetoCode":"token_stale","vetoDetails":{"token":0,"current":1}}`)) {
			t.Errorf("a stale token = %+v %s, want Lease's token_stale", refusal, refusal.Details)
		}
		if err := do(func(host rules.Host) error {
			return rules.RunUpdate(ctx, host, "Job", "j1", map[string]any{"title": "Mine"}, wren, token(1))
		}); err != nil {
			t.Fatalf("an update presenting the token: %v", err)
		}
		var renewed json.RawMessage
		if err := do(func(host rules.Host) error {
			var err error
			renewed, err = rules.RunOperation(ctx, host, "Job", "j1", "Lease", "heartbeat", nil, wren, token(1))
			return err
		}); err != nil {
			t.Fatalf("a heartbeat presenting the token: %v", err)
		}
		wantJSON(t, "heartbeat", renewed, `{"expiresAt":1060000,"directives":[]}`)
		if err := do(func(host rules.Host) error {
			return rules.RunDelete(ctx, host, "Job", "j1", wren, token(1))
		}); err != nil {
			t.Fatalf("a delete presenting the token: %v", err)
		}
		if rows := db.Rows(t, `SELECT id FROM "job"`); len(rows) != 0 {
			t.Errorf("the job is still there: %v", rows)
		}
	})
}
