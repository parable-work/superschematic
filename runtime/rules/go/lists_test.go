package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// A table's list columns through the adapter (D156): the layout marks a
// column that holds a list (list), a uuid[] or a text[] on Postgres and the
// JSON text of one on SQLite, as an @audience table's audience ids are. A
// row read holds each as a JSON list, a UUID in its base62 wire form; a
// write of one binds the array on Postgres and its JSON text on SQLite,
// each UUID hyphenated; and an operation of the core reads the row whole,
// its lists among it. Before the layout marked them, a read of a uuid[]
// refused every command on the row ("a []interface {} is no UUID").

// The UUIDs a note's watchers name: erin and dana, hyphenated as their
// columns hold them.
const (
	erinColumn = "1b4e28ba-2fa1-11d2-883f-0016d3cca427"
	danaColumn = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

// noteTable is Note's layout: its title, watchers, a list of UUIDs, and
// tags, a list of strings, among its own fields, and a Workflow on status.
func noteTable() *rules.Table {
	return &rules.Table{
		Type:    "Note",
		Table:   "note",
		Key:     rules.Column{Field: "id", Column: "id", Type: "Identity.UUID"},
		Version: "_version",
		Own: []rules.Column{
			{Field: "title", Column: "title", Type: "string"},
			{Field: "watchers", Column: "watchers", Type: "Identity.UUID", List: true},
			{Field: "tags", Column: "tags", Type: "string", List: true},
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
		Spec: rules.TypeSpec{
			Name: "Note",
			Uses: []rules.UseSpec{{Behavior: "Workflow", Config: json.RawMessage(workflowConfig), Anchor: rules.Anchor{Field: "status"}}},
			Fields: []rules.FieldSpec{
				{Name: "title", Type: "string"},
				{Name: "watchers", Type: "Identity.UUID"},
				{Name: "tags", Type: "string"},
			},
		},
	}
}

// createNotes writes Note's table as sqlgen writes it: its lists a uuid[]
// and a text[] on Postgres and TEXT on SQLite, with @optimistic's trigger.
func createNotes(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	uuid, uuids, texts := "TEXT", "TEXT", "TEXT"
	if db.Dialect == rules.DialectPostgres {
		uuid, uuids, texts = "UUID", "UUID[]", "TEXT[]"
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "note" (id %s PRIMARY KEY, title %s, watchers %s, tags %s, status %s, status_entered_at %s, _version %s)`,
		uuid, types.Text, uuids, texts, types.Text, types.Timestamp, types.Version))
	for _, ddl := range db.BumpVersion("note") {
		db.Exec(t, ddl)
	}
}

// heldList is a list as the dialect's column holds it, written past the
// adapter: the array on Postgres, the JSON text of it on SQLite.
func heldList(t *testing.T, db *rulestest.DB, list []string) any {
	t.Helper()
	if db.Dialect == rules.DialectPostgres {
		return list
	}
	return string(jsonOf(t, list))
}

// storedLists are a note's lists as its columns hold them, each as its
// text: Postgres's array text, SQLite's JSON.
func storedLists(t *testing.T, db *rulestest.DB, id string) (string, string) {
	t.Helper()
	rows := db.Rows(t, fmt.Sprintf(`SELECT CAST(watchers AS TEXT), CAST(tags AS TEXT) FROM "note" WHERE id = %s`, db.P(1)), id)
	if len(rows) != 1 {
		t.Fatalf("the note %s: %d rows", id, len(rows))
	}
	return fmt.Sprint(rows[0][0]), fmt.Sprint(rows[0][1])
}

func TestAListColumnReadsAndWritesAsAList(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createNotes(t, db)
		insert := fmt.Sprintf(`INSERT INTO "note" (id, title, watchers, tags, status) VALUES (%s, %s, %s, %s, 'draft')`, db.P(1), db.P(2), db.P(3), db.P(4))
		db.Exec(t, insert, uuidColumn, "Which lamp?", heldList(t, db, []string{erinColumn, danaColumn}), heldList(t, db, []string{"red", "blue"}))
		// An empty list is a list, and a NULL one is absent.
		db.Exec(t, insert, absentColumn, "Which rug?", heldList(t, db, []string{}), nil)
		layout := &rules.Layout{Types: map[string]*rules.Table{"Note": noteTable()}}
		ctx := context.Background()
		erin, dana := wire(t, erinColumn), wire(t, danaColumn)
		read := func(key string) *rules.Row {
			t.Helper()
			var row *rules.Row
			if err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				var err error
				row, err = rules.ReadRow(ctx, host, "Note", key)
				return err
			}); err != nil {
				t.Fatalf("read the note %s: %v", key, err)
			}
			return row
		}

		wantJSON(t, "the row read", read(uuidKey), fmt.Sprintf(`{"own":{"title":"Which lamp?","watchers":[%q,%q],"tags":["red","blue"]},"owned":{"status":{"status":"draft"}},"version":1}`, erin, dana))
		wantJSON(t, "a row with an empty list and none", read(absentKey), `{"own":{"title":"Which rug?","watchers":[]},"owned":{"status":{"status":"draft"}},"version":1}`)

		// The core's write of the lists, a UUID in either of its forms,
		// binds each hyphenated.
		core := script(t, `{"read":[{"kind":"row","type":"Note","key":"`+uuidKey+`"}]}`,
			changed(`{"rows":[{"kind":"update","type":"Note","key":"`+uuidKey+`","expectedVersion":1,"own":{"watchers":["`+dana+`","`+erinColumn+`"],"tags":["green"]}}]}`))
		if err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			host.Core = core
			_, err := rules.Execute(ctx, host, "Note", transition(uuidKey, "paid"), nil)
			return err
		}); err != nil {
			t.Fatalf("the core's write of the lists: %v", err)
		}
		watchers, tags := storedLists(t, db, uuidColumn)
		want := map[rules.Dialect][2]string{
			rules.DialectPostgres: {"{" + danaColumn + "," + erinColumn + "}", "{green}"},
			rules.DialectSQLite:   {`["` + danaColumn + `","` + erinColumn + `"]`, `["green"]`},
		}[db.Dialect]
		if watchers != want[0] || tags != want[1] {
			t.Errorf("the note's columns hold %s and %s, want %s and %s", watchers, tags, want[0], want[1])
		}
		wantJSON(t, "the row read after the write", read(uuidKey).Own, fmt.Sprintf(`{"title":"Which lamp?","watchers":[%q,%q],"tags":["green"]}`, dana, erin))

		// A list of UUIDs that holds no UUID is the adapter's own failure,
		// and writes nothing.
		core = script(t, `{"read":[{"kind":"row","type":"Note","key":"`+uuidKey+`"}]}`,
			changed(`{"rows":[{"kind":"update","type":"Note","key":"`+uuidKey+`","expectedVersion":2,"own":{"watchers":["o-1"]}}]}`))
		err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			host.Core = core
			_, err := rules.Execute(ctx, host, "Note", transition(uuidKey, "paid"), nil)
			return err
		})
		if refusal := refusalOf(t, err); refusal.Code != rules.CodeInternal {
			t.Errorf("a list of UUIDs that holds none = %+v, want internal", *refusal)
		}
		if got, _ := storedLists(t, db, uuidColumn); got != want[0] {
			t.Errorf("the refused write left the watchers %s, want %s", got, want[0])
		}

		// An operation of the core reads the row whole, its lists among it,
		// and runs.
		if err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			result, err := rules.Execute(ctx, host, "Note", transition(absentKey, "paid"), nil)
			if err != nil {
				return err
			}
			wantJSON(t, "the transition", result.Result, `{"from":"draft","to":"paid"}`)
			return nil
		}); err != nil {
			t.Fatalf("a transition of a row with lists: %v", err)
		}
		if got := read(absentKey); string(got.Owned["status"]["status"]) != `"paid"` {
			t.Errorf("the note's status after the transition = %s, want paid", got.Owned["status"]["status"])
		}
	})
}
