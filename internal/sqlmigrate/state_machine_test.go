package sqlmigrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// The tests of a @stateMachine's guards (D118): what they refuse on a real
// Postgres and a real SQLite, and where a plan places a trigger that
// refuses a write (Trigger.Refuses).

// stateMachineFixture is the service whose table Ticket declares four
// machines, and whose @jsonField class SignOff declares one no database
// guards.
var stateMachineFixture = filepath.Join(sqlgenFixtures, "fixture-state-machine-db")

// guardCase is one table whose guards the checks of testdata/pgconverge
// and testdata/sqliteconverge run (TestGuards in each): the table, its key
// column, the other columns an insert sets with their literals, the
// assignment of an update that sets no guarded column, and the guarded
// columns.
type guardCase struct {
	Table   string            `json:"table"`
	Key     string            `json:"key"`
	Insert  map[string]string `json:"insert"`
	Touch   string            `json:"touch"`
	Columns []guardColumn     `json:"columns"`
}

// guardColumn is one guarded column: the values it takes, null among them
// when it is nullable, the moves its machine lists, and the message
// SQLite's guard fails any other move with.
type guardColumn struct {
	Column  string       `json:"column"`
	Values  []*string    `json:"values"`
	Allowed [][2]*string `json:"allowed"`
	Message string       `json:"message"`
}

// move is a move from one state to another; "" is null.
type move [2]string

// guarded is a guarded column of table that takes states, and null when
// nullable, and whose machine lists moves.
func guarded(table, column string, nullable bool, states []string, moves ...move) guardColumn {
	value := func(state string) *string {
		if state == "" {
			return nil
		}
		return &state
	}
	c := guardColumn{Column: column, Message: table + "." + column + " cannot make this move: its state machine does not list it"}
	if nullable {
		c.Values = append(c.Values, nil)
	}
	for _, state := range states {
		c.Values = append(c.Values, value(state))
	}
	for _, m := range moves {
		c.Allowed = append(c.Allowed, [2]*string{value(m[0]), value(m[1])})
	}
	return c
}

// ticketGuards are the moves the fixture's Ticket declares, as its schema
// file writes them, by column: state may be reopened until it is closed;
// review is null until a pass reviews it, and a retry returns it to null;
// escalation, once acknowledged, only returns to null; and accountStatus
// is typed by a dependency's enum. spell respells a state of state, so the
// same moves are checked of states that hold a quote, a percent sign and a
// dollar quote.
func ticketGuards(stateColumn string, spell func(string) string) guardCase {
	open, inProgress, resolved, closed := spell("open"), spell("in_progress"), spell("resolved"), spell("closed")
	return guardCase{
		Table: "ticket", Key: "id", Insert: map[string]string{"subject": "'A ticket'"}, Touch: "subject = 'Another'",
		Columns: []guardColumn{
			guarded("ticket", stateColumn, false, []string{open, inProgress, resolved, closed},
				move{open, inProgress}, move{open, resolved}, move{open, closed},
				move{inProgress, resolved}, move{inProgress, open},
				move{resolved, open}, move{resolved, closed}),
			guarded("ticket", "review", true, []string{"RETRY", "APPROVED", "REJECTED"},
				move{"", "RETRY"}, move{"", "APPROVED"}, move{"", "REJECTED"},
				move{"RETRY", ""}, move{"RETRY", "APPROVED"}, move{"RETRY", "REJECTED"}),
			guarded("ticket", "escalation", true, []string{"raised", "acknowledged"},
				move{"", "raised"}, move{"raised", "acknowledged"}, move{"acknowledged", ""}),
			guarded("ticket", "account_status", false, []string{"active", "suspended", "closed"},
				move{"active", "suspended"}, move{"active", "closed"},
				move{"suspended", "active"}, move{"suspended", "closed"}),
		},
	}
}

// oddSpelling respells the states of Ticket.state that are not its
// default: with a quote, with a percent sign, which a plpgsql RAISE reads
// as a parameter's place, and with the dollar quote a function body ends
// at.
func oddSpelling(state string) string {
	switch state {
	case "in_progress":
		return "it's on"
	case "resolved":
		return "100% done"
	case "closed":
		return "a$$b"
	}
	return state
}

// respellTicketState gives Ticket.state's enum, and its machine, the
// states spell makes of them, and stores the field in the column order, a
// keyword in both dialects.
func respellTicketState(s *ir.Schema, spell func(string) string) {
	enum := s.Enums["TicketState"]
	for i := range enum.Values {
		enum.Values[i].SerializedAs = spell(enum.Values[i].Serialized())
	}
	all := func(states []string) {
		for i := range states {
			states[i] = spell(states[i])
		}
	}
	machine := typeNamed(s, "Ticket").StateMachineOf("state")
	all(machine.Terminal)
	for _, tr := range machine.Transitions {
		tr.From = spell(tr.From)
		all(tr.To)
	}
	fieldNamed(s, "Ticket", "state").SQLName = "order"
}

// guardCases are the services whose guards run: the fixture, and the
// fixture with Ticket.state's states respelled (oddSpelling) in a column
// named by a keyword.
func guardCases(t *testing.T) map[string]struct {
	schema *ir.Schema
	guards guardCase
} {
	t.Helper()
	load := planCase{base: stateMachineFixture}.load
	return map[string]struct {
		schema *ir.Schema
		guards guardCase
	}{
		"fixture":    {load(t, nil), ticketGuards("state", func(state string) string { return state })},
		"odd-states": {load(t, func(s *ir.Schema) { respellTicketState(s, oddSpelling) }), ticketGuards("order", oddSpelling)},
	}
}

// runGuards runs TestGuards of the module under testdata/<module> with env
// added to the environment.
func runGuards(t *testing.T, module string, env ...string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", module))); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "^TestGuards$", "./...")
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOFLAGS=-mod=readonly"), env...)
	out, err := cmd.CombinedOutput()
	t.Logf("%s:\n%s", module, out)
	if err != nil {
		t.Fatalf("%s failed: %v", module, err)
	}
	if !strings.Contains(string(out), "--- PASS: TestGuards ") {
		t.Fatalf("%s did not run TestGuards", module)
	}
}

// TestStateMachineGuardsOnPostgres runs each machine of the fixture on a
// real Postgres, from the service's create.sql (D118): every pair of a
// column's values, null among them, is tried, and exactly the moves the
// machine lists and the ones that change nothing pass; any other is
// refused with check_violation (23514) and the two values; an insert in any
// state passes; and drop.sql drops the guards. testdata/pgconverge's
// TestGuards holds the checks. Set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL
// as for TestConvergenceOnPostgres.
func TestStateMachineGuardsOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to run the state machine guards on Postgres")
	}
	cases := t.TempDir()
	for name, c := range guardCases(t) {
		out, err := sqlgen.Generate(c.schema, sqlgen.Options{SchemaName: "fixture-state-machine-db"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		dir := filepath.Join(cases, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := sqlgen.WriteDDL(out, dir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		writeJSON(t, filepath.Join(dir, "guards.json"), c.guards)
	}
	runGuards(t, "pgconverge", "PGCONVERGE_DATABASE_URL="+dsn, "PGCONVERGE_GUARDS="+cases)
}

// TestStateMachineGuardsOnSQLite runs each machine of the fixture on
// SQLite, from the service's sqlite/create.sql, as
// TestStateMachineGuardsOnPostgres runs them on Postgres: a move the
// machine does not list fails with the guard's fixed message, as
// SQLITE_CONSTRAINT_TRIGGER. testdata/sqliteconverge's TestGuards holds
// the checks. It needs no database server.
func TestStateMachineGuardsOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds the SQLite checks")
	}
	cases := t.TempDir()
	for name, c := range guardCases(t) {
		model := sqliteModel(t, "fixture-state-machine-db", c.schema)
		writeFile(t, filepath.Join(cases, name, "create.sql"), []byte(sqliteCreateSQL(t, model)))
		writeJSON(t, filepath.Join(cases, name, "guards.json"), c.guards)
	}
	runGuards(t, "sqliteconverge", "CGO_ENABLED=0", "SQLITECONVERGE_GUARDS="+cases)
}

// planCaseNamed is the plan case named name, planned for dialect.
func planCaseNamed(t *testing.T, name string, dialect Dialect) planCase {
	t.Helper()
	for _, pc := range planCases {
		if pc.name != name {
			continue
		}
		if dialect == SQLite {
			return forSQLite(pc)
		}
		return pc
	}
	t.Fatalf("no plan case %s", name)
	return planCase{}
}

// stepLines are a plan's steps, each as its phase, its op and its subject.
func stepLines(plan *Plan) []string {
	lines := make([]string, len(plan.Steps))
	for i, step := range plan.Steps {
		lines[i] = fmt.Sprintf("%s %s %s", step.Phase, step.Op, step.Subject)
	}
	return lines
}

// TestStateMachineGuardPhases: a plan places a machine's guard by the rule
// of a trigger that refuses a write (D118), in both dialects. A new guard
// is created in contract, when no server of the previous version is left;
// Postgres creates its function in expand, where nothing executes it. A
// changed one is dropped in expand, before its function is replaced, and
// created in contract. A removed one is dropped in contract.
func TestStateMachineGuardPhases(t *testing.T) {
	const function, trigger = "function/order_status_transition_guard", "trigger/order/trg_order_status_transition_guard"
	for _, tc := range []struct {
		plan    string
		dialect Dialect
		want    []string
	}{
		{"add-state-machine", Postgres, []string{"expand createFunction " + function, "contract createTrigger " + trigger}},
		{"add-state-machine", SQLite, []string{"contract createTrigger " + trigger}},
		{"change-state-machine", Postgres, []string{"expand dropTrigger " + trigger, "expand replaceFunction " + function, "contract createTrigger " + trigger}},
		{"change-state-machine", SQLite, []string{"expand dropTrigger " + trigger, "contract createTrigger " + trigger}},
		{"drop-state-machine", Postgres, []string{"contract dropTrigger " + trigger, "contract dropFunction " + function}},
		{"drop-state-machine", SQLite, []string{"contract dropTrigger " + trigger}},
	} {
		t.Run(tc.plan+"/"+string(tc.dialect), func(t *testing.T) {
			plan := planCaseNamed(t, tc.plan, tc.dialect).plan(t)
			if got := stepLines(plan); !slices.Equal(got, tc.want) {
				t.Errorf("the plan's steps are\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
			for _, step := range plan.Steps {
				if len(step.Hazards) > 0 {
					t.Errorf("step %d %s %s has hazards: %+v", step.Index, step.Op, step.Subject, step.Hazards[0])
				}
			}
		})
	}
}

// TestSQLiteRebuildPlacesGuards: a rebuild creates the triggers its phase
// leaves on the table (D80), and a guard is left by contract alone
// (D118). Widening the enum rebuilds order in expand, without the guard,
// which contract creates in a step of its own; narrowing it rebuilds order
// in contract, with the new guard, which expand dropped.
func TestSQLiteRebuildPlacesGuards(t *testing.T) {
	const guard = `CREATE TRIGGER "trg_order_status_transition_guard" `
	guards := func(step *Step) int {
		return len(slices.DeleteFunc(slices.Clone(step.Statements), func(s string) bool { return !strings.HasPrefix(s, guard) }))
	}
	const trigger = "trigger/order/trg_order_status_transition_guard"

	widen := planCaseNamed(t, "widen-state-machine", SQLite).plan(t)
	if got, want := stepLines(widen), []string{"expand copyTable table/order", "contract createTrigger " + trigger}; !slices.Equal(got, want) {
		t.Fatalf("widening plans %v, want %v", got, want)
	}
	if guards(widen.Steps[0]) != 0 {
		t.Errorf("the rebuild in expand creates the guard:\n%s", strings.Join(widen.Steps[0].Statements, ";\n"))
	}
	if !strings.Contains(widen.Steps[1].Statements[0], `NEW."status" IN ('shipped', 'cancelled')`) {
		t.Errorf("contract creates the guard %s", widen.Steps[1].Statements[0])
	}

	narrow := planCaseNamed(t, "narrow-state-machine", SQLite).plan(t)
	if got, want := stepLines(narrow), []string{"expand dropTrigger " + trigger, "contract copyTable table/order"}; !slices.Equal(got, want) {
		t.Fatalf("narrowing plans %v, want %v", got, want)
	}
	rebuild := narrow.Steps[1]
	if guards(rebuild) != 1 || !strings.HasPrefix(rebuild.Statements[len(rebuild.Statements)-1], guard) {
		t.Errorf("the rebuild in contract does not end by creating the guard:\n%s", strings.Join(rebuild.Statements, ";\n"))
	}
}

// TestStateMachineGuardFollowsRenames: a guard is named after its table
// and its column, and its rule names the column. A rename of either
// changes it: the plan drops the guard under its old name, once the rename
// has run and before a function is created or replaced, and creates the
// new one in contract. Postgres's function of a renamed column is another
// function, the old one dropped in contract; of a renamed table it is
// renamed, then replaced once the guard is dropped.
func TestStateMachineGuardFollowsRenames(t *testing.T) {
	for _, tc := range []struct {
		plan    string
		dialect Dialect
		// renames are the steps the guard's drop follows, and functions the
		// ones that follow it.
		renames   []string
		drop      string
		functions []string
		create    string
	}{
		{plan: "rename-state-machine-column", dialect: Postgres,
			renames: []string{"expand renameColumn table/order/column/stage"},
			drop:    "expand dropTrigger trigger/order/trg_order_status_transition_guard",
			functions: []string{"expand createFunction function/order_stage_transition_guard",
				"contract dropFunction function/order_status_transition_guard"},
			create: "contract createTrigger trigger/order/trg_order_stage_transition_guard"},
		{plan: "rename-state-machine-column", dialect: SQLite,
			renames: []string{"expand renameColumn table/order/column/stage"},
			drop:    "expand dropTrigger trigger/order/trg_order_status_transition_guard",
			create:  "contract createTrigger trigger/order/trg_order_stage_transition_guard"},
		// @table names the table purchaseOrder as written, so its guard's
		// names are quoted.
		{plan: "rename-state-machine-table", dialect: Postgres,
			renames: []string{"expand renameTable table/purchaseOrder",
				"expand renameFunction function/purchaseOrder_status_transition_guard"},
			drop:      "expand dropTrigger trigger/purchaseOrder/trg_order_status_transition_guard",
			functions: []string{"expand replaceFunction function/purchaseOrder_status_transition_guard"},
			create:    "contract createTrigger trigger/purchaseOrder/trg_purchaseOrder_status_transition_guard"},
		{plan: "rename-state-machine-table", dialect: SQLite,
			renames: []string{"expand renameTable table/purchaseOrder"},
			drop:    "expand dropTrigger trigger/purchaseOrder/trg_order_status_transition_guard",
			create:  "contract createTrigger trigger/purchaseOrder/trg_purchaseOrder_status_transition_guard"},
	} {
		t.Run(tc.plan+"/"+string(tc.dialect), func(t *testing.T) {
			lines := stepLines(planCaseNamed(t, tc.plan, tc.dialect).plan(t))
			at := func(line string) int {
				i := slices.Index(lines, line)
				if i < 0 {
					t.Fatalf("the plan has no step %q:\n  %s", line, strings.Join(lines, "\n  "))
				}
				return i
			}
			drop := at(tc.drop)
			for _, rename := range tc.renames {
				if i := at(rename); i > drop {
					t.Errorf("%q is step %d, after the guard is dropped in step %d", rename, i+1, drop+1)
				}
			}
			for _, function := range tc.functions {
				if i := at(function); i < drop {
					t.Errorf("%q is step %d, before the guard is dropped in step %d", function, i+1, drop+1)
				}
			}
			at(tc.create)
			guards := 0
			for _, line := range lines {
				if strings.Contains(line, "Trigger trigger/") && strings.HasSuffix(line, "_transition_guard") {
					guards++
				}
			}
			if guards != 2 {
				t.Errorf("the plan has %d steps of the guard's trigger, want its drop and its creation:\n  %s", guards, strings.Join(lines, "\n  "))
			}
		})
	}
}

// refusingModel is a SQLite model of the tables named, with one trigger
// that refuses a write on each table that definition gives a statement.
func refusingModel(refuses bool, definitions map[string]string, tables ...string) *Model {
	m := sqliteModelOf()
	for _, name := range tables {
		m.Tables = append(m.Tables, refTable(name))
		if definition := definitions[name]; definition != "" {
			m.Triggers = append(m.Triggers, &Trigger{Name: "trg_" + name + "_fence", Table: name, Definition: definition, Origin: strings.ToUpper(name), Refuses: refuses})
		}
	}
	return m
}

// TestRefusingTriggerPhases: the rule of a trigger that refuses a write is
// Trigger.Refuses's, whatever the trigger is and whatever it is named
// (D118). On a table the previous model has it is created in contract,
// and one that changes is dropped in expand and created in contract; one
// the new model lacks is dropped in contract; one that stays as it was has
// no step; and on a table the plan creates it is created with the table, in
// expand. The same triggers without Refuses are created and replaced in
// expand, as they were before the rule.
func TestRefusingTriggerPhases(t *testing.T) {
	const one, two = `CREATE TRIGGER "trg_a_fence" BEFORE UPDATE ON "a" BEGIN SELECT RAISE(ABORT, 'one'); END`,
		`CREATE TRIGGER "trg_a_fence" BEFORE UPDATE ON "a" BEGIN SELECT RAISE(ABORT, 'two'); END`
	const onB = `CREATE TRIGGER "trg_b_fence" BEFORE UPDATE ON "b" BEGIN SELECT RAISE(ABORT, 'one'); END`
	const subject = "trigger/a/trg_a_fence"
	for _, tc := range []struct {
		name     string
		from, to map[string]string
		refusing []string
		plain    []string
	}{
		{name: "added", to: map[string]string{"a": one},
			refusing: []string{"contract createTrigger " + subject},
			plain:    []string{"expand createTrigger " + subject}},
		{name: "changed", from: map[string]string{"a": one}, to: map[string]string{"a": two},
			refusing: []string{"expand dropTrigger " + subject, "contract createTrigger " + subject},
			plain:    []string{"expand replaceTrigger " + subject}},
		{name: "removed", from: map[string]string{"a": one},
			refusing: []string{"contract dropTrigger " + subject},
			plain:    []string{"contract dropTrigger " + subject}},
		{name: "kept", from: map[string]string{"a": one}, to: map[string]string{"a": one}},
	} {
		for _, refuses := range []bool{true, false} {
			want := tc.plain
			if refuses {
				want = tc.refusing
			}
			plan, err := Diff(refusingModel(refuses, tc.from, "a"), refusingModel(refuses, tc.to, "a"), Options{})
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got := stepLines(plan); !slices.Equal(got, want) {
				t.Errorf("%s, refuses %v: the plan's steps are %v, want %v", tc.name, refuses, got, want)
			}
		}
	}

	// A trigger on a table the plan creates, and every trigger of a plan
	// from an empty database, is created in expand: no server of a previous
	// version writes the table.
	created, err := Diff(refusingModel(true, nil, "a"), refusingModel(true, map[string]string{"b": onB}, "a", "b"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepLines(created), []string{"expand createTable table/b", "expand createTrigger trigger/b/trg_b_fence"}; !slices.Equal(got, want) {
		t.Errorf("a refusing trigger on a new table plans %v, want %v", got, want)
	}

	// A trigger that starts to refuse, or stops, is changed: the previous
	// one is dropped in expand either way, and the new one is created in
	// contract when it refuses and in expand when it does not.
	starts, err := Diff(refusingModel(false, map[string]string{"a": one}, "a"), refusingModel(true, map[string]string{"a": one}, "a"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepLines(starts), []string{"expand dropTrigger " + subject, "contract createTrigger " + subject}; !slices.Equal(got, want) {
		t.Errorf("a trigger that starts to refuse plans %v, want %v", got, want)
	}
	stops, err := Diff(refusingModel(true, map[string]string{"a": one}, "a"), refusingModel(false, map[string]string{"a": one}, "a"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepLines(stops), []string{"expand dropTrigger " + subject, "expand createTrigger " + subject}; !slices.Equal(got, want) {
		t.Errorf("a trigger that stops refusing plans %v, want %v", got, want)
	}

	// Between the phases the database holds neither a trigger contract
	// creates nor one expand dropped.
	changed, err := Diff(refusingModel(true, map[string]string{"a": one}, "a"), refusingModel(true, map[string]string{"a": two}, "a"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if expanded := decodeModel(t, changed.ExpandedModel); len(expanded.Triggers) != 0 {
		t.Errorf("the model between the phases holds the trigger %+v", expanded.Triggers[0])
	}
}

// TestGuardsOfAnEmptyDatabase: the plan from an empty database, which
// create.sql is, creates every guard in expand, with its table, and has no
// contract step (D118).
func TestGuardsOfAnEmptyDatabase(t *testing.T) {
	schema := planCase{base: stateMachineFixture}.load(t, nil)
	for _, dialect := range []Dialect{Postgres, SQLite} {
		model, err := BuildModel(schema, sqlgen.Options{SchemaName: "fixture-state-machine-db"}, dialect)
		if err != nil {
			t.Fatal(err)
		}
		refusing := 0
		for _, trigger := range model.Triggers {
			if !trigger.Refuses || trigger.Origin != "Ticket" || !strings.HasSuffix(trigger.Name, "_transition_guard") {
				t.Errorf("%s: the model's trigger %s of %s has refuses %v", dialect, trigger.Name, trigger.Origin, trigger.Refuses)
			}
			refusing++
		}
		if refusing != 4 {
			t.Errorf("%s: the model has %d guards, want Ticket's four", dialect, refusing)
		}
		plan, err := Diff(nil, model, Options{})
		if err != nil {
			t.Fatal(err)
		}
		created := 0
		for _, step := range plan.Steps {
			if step.Phase != Expand {
				t.Errorf("%s: step %d %s %s is in %s", dialect, step.Index, step.Op, step.Subject, step.Phase)
			}
			if step.Op == string(opCreateTrigger) {
				created++
			}
		}
		if created != 4 {
			t.Errorf("%s: the plan creates %d triggers, want the four guards", dialect, created)
		}
	}
}

// TestEnqueueTriggerPhasesUnchanged: a trigger that refuses no write is
// placed as it was before D118's rule, in both dialects: an
// @enqueueOnChange's is created in expand, changed in expand, in place, and
// dropped in contract.
func TestEnqueueTriggerPhasesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		plan    string
		dialect Dialect
		want    []string
	}{
		{"add-enqueue-on-change", Postgres, []string{
			"expand createFunction function/order_enqueue_order_shipped",
			"expand createTrigger trigger/order/trg_order_enqueue_order_shipped"}},
		{"add-enqueue-on-change", SQLite, []string{
			"expand createTrigger trigger/order/trg_order_enqueue_order_shipped_insert",
			"expand createTrigger trigger/order/trg_order_enqueue_order_shipped_update"}},
		{"change-enqueue-on-change", Postgres, []string{
			"expand replaceFunction function/order_enqueue_order_shipped",
			"expand replaceTrigger trigger/order/trg_order_enqueue_order_shipped"}},
		{"change-enqueue-on-change", SQLite, []string{
			"expand createTrigger trigger/order/trg_order_enqueue_order_shipped_delete",
			"expand replaceTrigger trigger/order/trg_order_enqueue_order_shipped_update"}},
		{"drop-enqueue-on-change", Postgres, []string{
			"contract dropTrigger trigger/order/trg_order_enqueue_order_shipped",
			"contract dropFunction function/order_enqueue_order_shipped"}},
		{"drop-enqueue-on-change", SQLite, []string{
			"contract dropTrigger trigger/order/trg_order_enqueue_order_shipped_insert",
			"contract dropTrigger trigger/order/trg_order_enqueue_order_shipped_update"}},
	} {
		plan := planCaseNamed(t, tc.plan, tc.dialect).plan(t)
		if got := stepLines(plan); !slices.Equal(got, tc.want) {
			t.Errorf("%s on %s plans\n  %s\nwant\n  %s", tc.plan, tc.dialect, strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
		}
		for _, trigger := range decodeModel(t, plan.ToModel).Triggers {
			if trigger.Refuses {
				t.Errorf("%s on %s: the trigger %s refuses a write", tc.plan, tc.dialect, trigger.Name)
			}
		}
	}
}

// TestSQLiteTriggerRefuses: a read of a SQLite file takes whether a trigger
// refuses a write from its statement (D116, D118): it does when the
// statement calls RAISE, in any case, and does not when RAISE is text or a
// name; and the statements the model writes say what the model says.
func TestSQLiteTriggerRefuses(t *testing.T) {
	for statement, want := range map[string]bool{
		`CREATE TRIGGER t BEFORE UPDATE OF a ON x WHEN NEW.a IS NOT OLD.a BEGIN SELECT RAISE(ABORT, 'no'); END`:      true,
		`CREATE TRIGGER t BEFORE UPDATE ON x BEGIN SELECT CASE WHEN NEW.a < OLD.a THEN raise (fail, 'no') END; END`:  true,
		`CREATE TRIGGER t BEFORE INSERT ON x BEGIN SELECT RAISE(IGNORE); END`:                                        true,
		`CREATE TRIGGER t AFTER UPDATE ON x BEGIN INSERT INTO log (note) VALUES ('RAISE(ABORT, ''no'')'); END`:       false,
		`CREATE TRIGGER t AFTER UPDATE ON x BEGIN UPDATE x SET "raise" = ("raise") + 1 WHERE rowid = NEW.rowid; END`: false,
	} {
		if got := sqliteTriggerRefuses(statement); got != want {
			t.Errorf("sqliteTriggerRefuses(%s) = %v, want %v", statement, got, want)
		}
	}
	for name, schema := range sqliteModelFixtures(t) {
		for _, trigger := range sqliteModel(t, name, schema).Triggers {
			if got := sqliteTriggerRefuses(trigger.Definition); got != trigger.Refuses {
				t.Errorf("%s: the statement of %s reads as refuses %v, and the model says %v", name, trigger.Name, got, trigger.Refuses)
			}
		}
	}
}
