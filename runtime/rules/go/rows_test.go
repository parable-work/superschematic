package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// A command without a row and a read across a table (D104), with cores a
// test scripts over a real database of each dialect: a page of a table's
// live rows, each own field a request names held to its value (a string, a
// UUID in either form, a null), in key order with more past the page, a
// soft-deleted row left out, each row with its rows of the sub-tables the
// request names, an empty list for a row that has none; a value no UUID
// column can hold matching no row; a field, a use or a sub-table the layout
// lacks refused as the adapter's own failure, and so is the read under a
// lock on SQLite; and an operation of the table as a whole, which names no
// key, its result returned, a read-only one applying an empty unit and a
// writing one's rows fenced on their versions, run again when one moved.

const (
	account1 = "00000000-0000-4000-8000-000000000001"
	account2 = "00000000-0000-4000-8000-000000000002"
	account3 = "00000000-0000-4000-8000-000000000003"
	account4 = "00000000-0000-4000-8000-000000000004"
	account5 = "00000000-0000-4000-8000-000000000005"
	ownerA   = "00000000-0000-4000-8000-0000000000aa"
	ownerB   = "00000000-0000-4000-8000-0000000000bb"
)

// accountLayout is Account's layout: its owner, vendor and rank, a Pool
// whose pick is an operation of the table, and a Budget whose meters are a
// sub-table; soft-deletable.
func accountLayout() *rules.Layout {
	return &rules.Layout{Types: map[string]*rules.Table{"Account": {
		Type:    "Account",
		Table:   "account",
		Key:     rules.Column{Field: "id", Column: "id", Type: "Identity.UUID"},
		Version: "_version",
		Live:    &rules.Live{Column: "deleted_at"},
		Own: []rules.Column{
			{Field: "owner", Column: "owner", Type: "Identity.UUID"},
			{Field: "vendor", Column: "vendor", Type: "string"},
			{Field: "rank", Column: "rank", Type: "Generic.Int64"},
		},
		Uses: []rules.Use{
			{
				ID: "Pool", Behavior: "Pool", Member: "pool",
				Fields:     []rules.OwnedField{{Name: "availability", Column: rules.Column{Field: "poolAvailability", Column: "pool_availability", Type: "string"}}},
				Operations: []rules.Operation{{Name: "markOut", Writes: true}, {Name: "pick", Table: true}},
			},
			{
				ID: "Budget", Behavior: "Budget", Member: "budget", Fields: []rules.OwnedField{}, Operations: []rules.Operation{},
				Tables: []rules.SubTable{{
					Name: "meters", Table: "account_budget_meters", Row: "account_id",
					Key: rules.OwnedField{Name: "name", Column: rules.Column{Field: "name", Column: "name", Type: "string"}},
					Fields: []rules.OwnedField{
						{Name: "used", Column: rules.Column{Field: "used", Column: "used", Type: "Generic.Int64"}},
						{Name: "resetAt", Column: rules.Column{Field: "resetAt", Column: "reset_at", Type: "Temporal.DateTime", Convert: rules.ConvertEpochMillis}},
					},
				}},
			},
		},
		Spec: rules.TypeSpec{Name: "Account"},
	}}}
}

// createAccounts writes Account's table and its meters' sub-table as sqlgen
// writes them, with @optimistic's trigger, and its five rows: a1 and a2 of
// owner A at anthropic, a3 of owner A at openai with no rank, a4 of owner B,
// and a5 of owner A soft-deleted; a1's meters questions and tokens.
func createAccounts(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	key := "TEXT"
	if db.Dialect == rules.DialectPostgres {
		key = "UUID"
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "account" (id %s PRIMARY KEY, owner %s, vendor %s, rank %s, pool_availability %s, deleted_at %s, _version %s)`,
		key, key, types.Text, types.Integer, types.Text, types.Timestamp, types.Version))
	for _, ddl := range db.BumpVersion("account") {
		db.Exec(t, ddl)
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "account_budget_meters" (account_id %s NOT NULL REFERENCES "account" (id) ON DELETE CASCADE, name %s NOT NULL, used %s, reset_at %s, PRIMARY KEY (account_id, name))`,
		key, types.Text, types.Integer, types.Timestamp))
	insert := func(id, owner, vendor string, rank any, availability any, deleted bool) {
		var deletedAt any
		if deleted {
			deletedAt = db.Timestamp(time.UnixMilli(5000))
		}
		db.Exec(t, fmt.Sprintf(`INSERT INTO "account" (id, owner, vendor, rank, pool_availability, deleted_at) VALUES (%s, %s, %s, %s, %s, %s)`, db.P(1), db.P(2), db.P(3), db.P(4), db.P(5), db.P(6)),
			id, owner, vendor, rank, availability, deletedAt)
	}
	insert(account4, ownerB, "anthropic", 4, nil, false)
	insert(account2, ownerA, "anthropic", 2, nil, false)
	insert(account5, ownerA, "anthropic", 5, nil, true)
	insert(account1, ownerA, "anthropic", 1, "open", false)
	insert(account3, ownerA, "openai", nil, nil, false)
	meter := func(account, name string, used int64, resetAt any) {
		db.Exec(t, fmt.Sprintf(`INSERT INTO "account_budget_meters" (account_id, name, used, reset_at) VALUES (%s, %s, %s, %s)`, db.P(1), db.P(2), db.P(3), db.P(4)), account, name, used, resetAt)
	}
	meter(account1, "tokens", 100, db.Timestamp(time.UnixMilli(3600000)))
	meter(account1, "questions", 2, nil)
	meter(account5, "tokens", 7, nil)
}

// pick is the command a test runs Account's reads under: Pool's pick, an
// operation of the table as a whole.
func pick() rules.Command {
	return rules.Command{Caller: alice, Now: 7000, Request: "p1", Action: rules.Action{Kind: rules.ActionTableOperation, UseID: "Pool", Operation: "pick"}}
}

func TestTheCoreReadsATablesRows(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createAccounts(t, db)
		layout := accountLayout()
		read := func(requests ...string) (*scripted, error) {
			core := script(t, `{"read":[`+strings.Join(requests, ",")+`]}`, changed(""))
			err := inTx(t, db, changeHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Account", pick(), nil)
				return err
			})
			return core, err
		}
		row := func(id, owner, vendor, rank, availability string) string {
			own := fmt.Sprintf(`"owner":%q,"vendor":%q`, wire(t, owner), vendor)
			if rank != "" {
				own += `,"rank":` + rank
			}
			owned := ""
			if availability != "" {
				owned = fmt.Sprintf(`,"owned":{"Pool":{"availability":%q}}`, availability)
			}
			return fmt.Sprintf(`{"key":%q,"row":{"own":{%s}%s,"version":1}`, wire(t, id), own, owned)
		}
		meters := func(rows string) string {
			return `,"tables":{"Budget":{"meters":[` + rows + `]}}}`
		}
		a1Meters := `{"key":"questions","values":{"used":2}},{"key":"tokens","values":{"used":100,"resetAt":3600000}}`

		// The first page of owner A's rows holds two, in key order, with
		// their meters, a row with none an empty list, and more follow; the
		// next, after the last key, holds the rest, a soft-deleted row left
		// out.
		first := fmt.Sprintf(`{"kind":"rows","type":"Account","equal":{"owner":%q},"tables":[{"useId":"Budget","table":"meters"}],"limit":2}`, wire(t, ownerA))
		next := fmt.Sprintf(`{"kind":"rows","type":"Account","equal":{"owner":%q},"tables":[{"useId":"Budget","table":"meters"}],"after":%q,"limit":2}`, wire(t, ownerA), wire(t, account2))
		// A UUID in its column's form matches as its wire form does, beside
		// a string field.
		vendor := fmt.Sprintf(`{"kind":"rows","type":"Account","equal":{"owner":%q,"vendor":"anthropic"},"limit":500}`, ownerA)
		// A null matches a field that holds no value.
		unranked := `{"kind":"rows","type":"Account","equal":{"rank":null},"limit":500}`
		// No equal reads every live row; a type the layout lacks has none,
		// and a value no UUID column can hold matches none.
		every := `{"kind":"rows","type":"Account","limit":500}`
		other := `{"kind":"rows","type":"Note","limit":500}`
		nobody := `{"kind":"rows","type":"Account","equal":{"owner":"nobody"},"limit":500}`
		core, err := read(first, next, vendor, unranked, every, other, nobody)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "the answers", core.answers[1], `{"reads":[
			{"request":`+first+`,"answer":{"linked":{"rows":[`+row(account1, ownerA, "anthropic", "1", "open")+meters(a1Meters)+`,`+row(account2, ownerA, "anthropic", "2", "")+meters("")+`],"more":true}},"held":0},
			{"request":`+next+`,"answer":{"linked":{"rows":[`+row(account3, ownerA, "openai", "", "")+meters("")+`],"more":false}},"held":0},
			{"request":`+vendor+`,"answer":{"linked":{"rows":[`+row(account1, ownerA, "anthropic", "1", "open")+`},`+row(account2, ownerA, "anthropic", "2", "")+`}],"more":false}},"held":0},
			{"request":`+unranked+`,"answer":{"linked":{"rows":[`+row(account3, ownerA, "openai", "", "")+`}],"more":false}},"held":0},
			{"request":`+every+`,"answer":{"linked":{"rows":[`+row(account1, ownerA, "anthropic", "1", "open")+`},`+row(account2, ownerA, "anthropic", "2", "")+`},`+row(account3, ownerA, "openai", "", "")+`},`+row(account4, ownerB, "anthropic", "4", "")+`}],"more":false}},"held":0},
			{"request":`+other+`,"answer":{"linked":{"rows":[],"more":false}},"held":0},
			{"request":`+nobody+`,"answer":{"linked":{"rows":[],"more":false}},"held":0}]}`)

		// A field that is no own column of the type, and a use or a
		// sub-table the layout does not give it, are the adapter's own
		// failures.
		_, err = read(`{"kind":"rows","type":"Account","equal":{"score":1},"limit":500}`)
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: Account has no own field score in the behaviors' layout"})
		_, err = read(`{"kind":"rows","type":"Account","tables":[{"useId":"Budget","table":"holds"}],"limit":500}`)
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: Account's use Budget (Budget) has no sub-table holds in the behaviors' layout"})
		_, err = read(`{"kind":"rows","type":"Account","tables":[{"useId":"Lease","table":"holds"}],"limit":500}`)
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: Account has no use Lease in the behaviors' layout"})

		// Under a lock, a store whose locks are fenced has no check of it
		// yet.
		core = script(t, `{"lock":["Account/pool"]}`, `{"read":[`+every+`]}`, changed(""))
		err = inTx(t, db, changeHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Account", pick(), nil)
			return err
		})
		if db.Dialect == rules.DialectSQLite {
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: a read of a table's rows under a lock has no check on SQLite or D1 yet (D103)"})
		} else if err != nil {
			t.Errorf("a read of a table's rows under a lock on Postgres: %v", err)
		}
	})
}

func TestATableOperationRunsWithNoRow(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createAccounts(t, db)
		layout := accountLayout()
		ctx := context.Background()
		availability := func(id string) (string, int64) {
			rows := db.Rows(t, fmt.Sprintf(`SELECT pool_availability, _version FROM "account" WHERE id = %s`, db.P(1)), id)
			var version int64
			switch v := rows[0][1].(type) {
			case int64:
				version = v
			case int32:
				version = int64(v)
			}
			text, _ := rows[0][0].(string)
			return text, version
		}

		// A read-only one: the command names no key and presents no
		// preconditions, its params {} for none, and its unit is empty.
		core := script(t, changed(`{"result":{"member":null,"until":9000}}`))
		var rec *recorded
		var result json.RawMessage
		err := inTx(t, db, changeHost(core, layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			result, err = rules.RunTableOperation(ctx, host, "Account", "Pool", "pick", nil, alice, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "pick's result", result, `{"member":null,"until":9000}`)
		wantJSON(t, "the command", core.commands[0], `{"caller":{"subject":"alice"},"now":7000,"action":{"kind":"tableOperation","useId":"Pool","operation":"pick","params":{}},"request":"c1"}`)
		if len(rec.applied) != 1 || len(rec.applied[0]) != 0 {
			t.Errorf("a read-only table operation applied %v, want one empty unit", rec.applied)
		}

		// A writing one: each row its calls wrote is held to its version,
		// and the result is the command's, with no key of its own.
		write := changed(fmt.Sprintf(`{"rows":[{"kind":"update","type":"Account","key":%q,"expectedVersion":1,"owned":{"Pool":{"availability":"out"}}}],"result":{"expired":1}}`, wire(t, account2)))
		core = script(t, write)
		var applied *rules.Result
		err = inTx(t, db, changeHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			applied, err = rules.Execute(ctx, host, "Account", rules.Command{Caller: alice, Now: 7000, Action: rules.Action{Kind: rules.ActionTableOperation, UseID: "Pool", Operation: "sweep", Params: json.RawMessage(`{"holder":"w"}`)}}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "the result", applied.Result, `{"expired":1}`)
		if applied.Key != "" || applied.Attempts != 1 {
			t.Errorf("the table operation's key is %q after %d attempts, want none after one", applied.Key, applied.Attempts)
		}
		if got, version := availability(account2); got != "out" || version != 2 {
			t.Errorf("a2 is %q at version %d, want out at 2", got, version)
		}

		// The same write now finds a2 moved past the version it holds: the
		// command runs again from fresh state until it is refused as busy,
		// nothing of it written.
		core = script(t, write)
		err = inTx(t, db, changeHost(core, layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.RunTableOperation(ctx, host, "Account", "Pool", "sweep", map[string]any{"holder": "w"}, alice, nil)
			return err
		})
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeBusy, Status: 503, Class: "EngineError", Message: "sweep of Account: the row kept moving while the command ran; it ran 8 times and nothing was written, so it may be tried again"})
		if got, version := availability(account2); got != "out" || version != 2 {
			t.Errorf("a2 is %q at version %d after the busy command, want out at 2", got, version)
		}
		if len(core.commands) != rules.MaxAttempts {
			t.Errorf("the core ran %d times, want %d", len(core.commands), rules.MaxAttempts)
		}
	})
}
