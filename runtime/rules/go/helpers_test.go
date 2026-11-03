package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// What the adapter's tests share: Order as the generator writes its layout
// (status a Workflow, rules.test.ts's), its table in each dialect, a store
// that records what the adapter asks of it, and a core that answers each
// run with the next of the steps a test scripts.

// workflowConfig is rules.test.ts's flow: a transition that names a
// permission, and shipped terminal.
const workflowConfig = `{"states":["draft","paid","shipped"],"initial":"draft","transitions":[{"from":"draft","to":"paid"},{"from":"paid","to":"shipped","permission":"orders.ship"}]}`

// A UUID key in its two forms: base62 on the wire and in the core,
// hyphenated in the column.
const (
	uuidKey    = "6fe1XoZ0UJpMNNdUOSMC2X"
	uuidColumn = "db360f43-5530-c407-56da-9cd8e0c971dd"
	// absentKey is a key no row holds, and absentColumn its column's form.
	absentKey    = "1fe1XoZ0UJpMNNdUOSMC2X"
	absentColumn = "36ed48cd-b2e1-b6ce-3b26-89443d6971dd"
)

var alice = rules.Caller{Subject: "alice"}

// orderTable is Order's layout on a table of a name, its key of a type,
// soft-deletable when live, with the spec's config.
func orderTable(name, keyType string, live bool, config string) *rules.Table {
	table := &rules.Table{
		Type:    "Order",
		Table:   name,
		Key:     rules.Column{Field: "id", Column: "id", Type: keyType},
		Version: "_version",
		Own:     []rules.Column{{Field: "title", Column: "title", Type: "string"}},
		Uses: []rules.Use{{
			ID:       "status",
			Behavior: "Workflow",
			Anchor:   "status",
			Fields: []rules.OwnedField{
				{Name: "status", Column: rules.Column{Field: "status", Column: "status", Type: "string"}, Value: true},
				{Name: "enteredAt", Column: rules.Column{Field: "statusEnteredAt", Column: "status_entered_at", Type: "Temporal.DateTime", Convert: rules.ConvertEpochMillis}},
			},
			Operations: []rules.Operation{{Name: "transition", Writes: true}},
		}},
		Spec: rules.TypeSpec{Name: "Order", Uses: []rules.UseSpec{{Behavior: "Workflow", Config: json.RawMessage(config), Anchor: rules.Anchor{Field: "status"}}}},
	}
	if live {
		table.Live = &rules.Live{Column: "deleted_at"}
	}
	return table
}

// orderLayout is a fresh layout of Order on the table "order", so no
// test's define check is another's.
func orderLayout(keyType string, live bool) *rules.Layout {
	return &rules.Layout{Types: map[string]*rules.Table{"Order": orderTable("order", keyType, live, workflowConfig)}}
}

// createOrders writes Order's table as sqlgen writes one that composes
// Workflow on status, beside the audit columns a repository stamps and
// deleted_at, with @optimistic's trigger.
func createOrders(t *testing.T, db *rulestest.DB, table string) {
	t.Helper()
	types := db.ColumnTypes()
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "%s" (
  id %s,
  title %s,
  status %s,
  status_entered_at %s,
  created_by %s,
  updated_at %s,
  deleted_at %s,
  deleted_by %s,
  data %s,
  _version %s
)`, table, types.Key, types.Text, types.Text, types.Timestamp, types.Text, types.Timestamp, types.Timestamp, types.Text, types.JSON, types.Version))
	for _, ddl := range db.BumpVersion(table) {
		db.Exec(t, ddl)
	}
}

// insertOrder writes a row of Order past the adapter.
func insertOrder(t *testing.T, db *rulestest.DB, id, title, status string, enteredAt int64) {
	t.Helper()
	var at any
	if enteredAt != 0 {
		at = db.Timestamp(time.UnixMilli(enteredAt))
	}
	db.Exec(t, fmt.Sprintf(`INSERT INTO "order" (id, title, status, status_entered_at) VALUES (%s, %s, %s, %s)`, db.P(1), db.P(2), db.P(3), db.P(4)), id, title, status, at)
}

// stored is a row of Order as the database holds it: its title, status,
// when it entered it in epoch milliseconds (0 for none), and version.
type stored struct {
	Title, Status string
	EnteredAt     int64
	Version       int64
	Deleted       bool
}

func storedOrder(t *testing.T, db *rulestest.DB, id string) *stored {
	t.Helper()
	rows := db.Rows(t, fmt.Sprintf(`SELECT title, status, status_entered_at, _version, deleted_at FROM "order" WHERE id = %s`, db.P(1)), id)
	if len(rows) == 0 {
		return nil
	}
	return storedOf(t, rows[0])
}

func storedOf(t *testing.T, row []any) *stored {
	t.Helper()
	out := &stored{}
	if row[0] != nil {
		out.Title = fmt.Sprint(row[0])
	}
	if row[1] != nil {
		out.Status = fmt.Sprint(row[1])
	}
	switch at := row[2].(type) {
	case time.Time:
		out.EnteredAt = at.UnixMilli()
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			t.Fatalf("status_entered_at %q: %v", at, err)
		}
		out.EnteredAt = parsed.UnixMilli()
	}
	switch v := row[3].(type) {
	case int64:
		out.Version = v
	case int32:
		out.Version = int64(v)
	default:
		t.Fatalf("_version of %T", row[3])
	}
	out.Deleted = row[4] != nil
	return out
}

// inTx runs fn on a host over a transaction of db, committed unless fn
// returns an error; wrap, when given, wraps the transaction's store.
func inTx(t *testing.T, db *rulestest.DB, host rules.Host, wrap func(tx *rulestest.Tx, store rules.Store) rules.Store, fn func(host rules.Host, tx *rulestest.Tx) error) error {
	t.Helper()
	return db.InTx(context.Background(), func(tx *rulestest.Tx) error {
		store := tx.Store()
		if wrap != nil {
			store = wrap(tx, store)
		}
		host.Store = store
		return fn(host, tx)
	})
}

// recorded is what a recording store was asked: each query, each unit it
// applied, and each lock.
type recorded struct {
	mu      sync.Mutex
	queries []rules.Statement
	applied [][]rules.Statement
	locks   []string
}

// recording is a store that records what the adapter asks of the store it
// wraps. applies, when it holds an entry for an apply, answers it without
// applying (false: the unit moved).
type recording struct {
	rules.Store
	rec     *recorded
	applies []bool
	before  func(ctx context.Context, n int) error
}

func record(store rules.Store) (*recording, *recorded) {
	rec := &recorded{}
	return &recording{Store: store, rec: rec}, rec
}

func (r *recording) Query(ctx context.Context, text string, values ...any) ([][]any, error) {
	r.rec.mu.Lock()
	r.rec.queries = append(r.rec.queries, rules.Statement{Text: text, Values: values})
	r.rec.mu.Unlock()
	return r.Store.Query(ctx, text, values...)
}

func (r *recording) Apply(ctx context.Context, statements []rules.Statement) (bool, error) {
	r.rec.mu.Lock()
	n := len(r.rec.applied)
	r.rec.applied = append(r.rec.applied, statements)
	r.rec.mu.Unlock()
	if r.before != nil {
		if err := r.before(ctx, n); err != nil {
			return false, err
		}
	}
	if n < len(r.applies) && !r.applies[n] {
		return false, nil
	}
	return r.Store.Apply(ctx, statements)
}

func (r *recording) Lock(ctx context.Context, key string) error {
	r.rec.mu.Lock()
	r.rec.locks = append(r.rec.locks, key)
	r.rec.mu.Unlock()
	return r.Store.Lock(ctx, key)
}

// moving is a store whose first moves applies each see the row moved by a
// write through the same transaction first, as a write committed between
// the read and the apply: the title gains a "!" and the version moves on.
func moving(db *rulestest.DB, id string, moves int) func(tx *rulestest.Tx, store rules.Store) rules.Store {
	return func(tx *rulestest.Tx, store rules.Store) rules.Store {
		wrapped, _ := record(store)
		wrapped.before = func(ctx context.Context, n int) error {
			if n >= moves {
				return nil
			}
			_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE "order" SET title = title || '!', _version = _version + 1 WHERE id = %s`, db.P(1)), id)
			return err
		}
		return wrapped
	}
}

// scripted is a core that answers each run with the next of its steps
// (the last once they run out), recording the answers and the command it
// was given, and that takes every spec.
type scripted struct {
	mu       sync.Mutex
	steps    []rules.Step
	at       int
	answers  []rules.Answers
	commands []rules.Command
	checks   int
}

func script(t *testing.T, steps ...string) *scripted {
	t.Helper()
	core := &scripted{}
	for _, text := range steps {
		var step rules.Step
		if err := json.Unmarshal([]byte(text), &step); err != nil {
			t.Fatalf("step %s: %v", text, err)
		}
		core.steps = append(core.steps, step)
	}
	return core
}

func (s *scripted) Run(_ rules.TypeSpec, command rules.Command, answers rules.Answers) (rules.Step, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A copy through JSON, so a later answer the adapter replaces in place
	// does not change the record of this run.
	var copied rules.Answers
	text, err := json.Marshal(answers)
	if err != nil {
		return rules.Step{}, err
	}
	if err := json.Unmarshal(text, &copied); err != nil {
		return rules.Step{}, err
	}
	s.answers = append(s.answers, copied)
	s.commands = append(s.commands, command)
	step := s.steps[min(s.at, len(s.steps)-1)]
	s.at++
	return step, nil
}

func (s *scripted) Declarations() ([]rules.Declaration, error) { return nil, nil }

func (s *scripted) Check(specs []rules.TypeSpec) ([]*rules.Refusal, error) {
	s.mu.Lock()
	s.checks++
	s.mu.Unlock()
	return make([]*rules.Refusal, len(specs)), nil
}

// changed is a done step of a change set with members over the empty one's.
func changed(members string) string {
	base := map[string]json.RawMessage{
		"rows": json.RawMessage(`[]`), "subRows": json.RawMessage(`[]`), "events": json.RawMessage(`[]`), "deadlines": json.RawMessage(`[]`), "result": json.RawMessage(`null`),
	}
	var extra map[string]json.RawMessage
	if members != "" {
		if err := json.Unmarshal([]byte(members), &extra); err != nil {
			panic(err)
		}
	}
	for name, value := range extra {
		base[name] = value
	}
	out, _ := json.Marshal(map[string]any{"done": map[string]any{"changed": base}})
	return string(out)
}

// counting is a core that counts its define checks.
type counting struct {
	rules.Core
	mu     sync.Mutex
	checks int
}

func (c *counting) Check(specs []rules.TypeSpec) ([]*rules.Refusal, error) {
	c.mu.Lock()
	c.checks++
	c.mu.Unlock()
	return c.Core.Check(specs)
}

// refusalOf is the refusal err holds, failing the test when it holds none.
func refusalOf(t *testing.T, err error) *rules.Refusal {
	t.Helper()
	refusal, ok := rules.AsRefusal(err)
	if !ok {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return refusal
}

// sameJSON reports whether two JSON texts hold the same value, member
// order aside.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// wantJSON fails unless value's JSON is want's, member order aside.
func wantJSON(t *testing.T, what string, value any, want string) {
	t.Helper()
	var got []byte
	switch v := value.(type) {
	case json.RawMessage:
		got = v
	case []byte:
		got = v
	default:
		var err error
		got, err = json.Marshal(value)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	if !sameJSON(got, []byte(want)) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

// wantRefusal fails unless err is the refusal of a code, status, class and
// message, with details when want's are given.
func wantRefusal(t *testing.T, err error, want rules.Refusal) {
	t.Helper()
	got := refusalOf(t, err)
	if got.Code != want.Code || got.Status != want.Status || got.Class != want.Class || got.Message != want.Message {
		t.Errorf("refusal = %+v\nwant      %+v", *got, want)
	}
	if want.Details != nil && !sameJSON(got.Details, want.Details) {
		t.Errorf("details = %s, want %s", got.Details, want.Details)
	}
}

// statementText is a statement as a test compares it: its text, its
// values with each instant as its toISOString text, its expect and check.
func statementText(s rules.Statement) string {
	values := make([]any, len(s.Values))
	for i, value := range s.Values {
		if at, ok := value.(time.Time); ok {
			value = "time:" + at.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		values[i] = value
	}
	return fmt.Sprintf("%s %#v expect=%d check=%v", s.Text, values, s.Expect, s.Check)
}

// at is an instant as the dialect's statements bind it, as statementText
// writes it.
func at(db *rulestest.DB, millis int64) any {
	t := time.UnixMilli(millis).UTC()
	if db.Dialect == rules.DialectPostgres {
		return t
	}
	return t.Format("2006-01-02T15:04:05.000Z")
}

func wantStatements(t *testing.T, what string, got []rules.Statement, want []rules.Statement) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d statements, want %d:\n%v", what, len(got), len(want), got)
	}
	for i := range got {
		if g, w := statementText(got[i]), statementText(want[i]); g != w {
			t.Errorf("%s[%d] =\n  %s\nwant\n  %s", what, i, g, w)
		}
	}
}
