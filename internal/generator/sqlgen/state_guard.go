package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's @stateMachine guard (D118): a trigger that refuses an update
// of the machine's column that makes a move the machine does not declare,
// whatever writes the row. It is built as @enqueueOnChange's trigger is
// (enqueue.go): on Postgres a plpgsql function and the trigger that
// executes it, on SQLite one trigger that holds the rule in its WHEN, so
// create.sql and the migration planner's models (internal/sqlmigrate) take
// them from here alike.
//
// The rule is one in both dialects. An update that leaves the column's
// value as it was is no move and passes. Any other is refused when the old
// value is terminal, and otherwise unless the machine lists the pair: a
// null's move to a state of fromNull, a state's move to one of its
// targets, or its return to null. An insert is not held. Every state is
// the value the column holds, the enum member's serialized value, so the
// rule compares the column with literals.
//
// A guard refuses a write a server of the previous version may make, so
// the planner places it by a rule of its own (Trigger.Refuses): it is
// created in the contract phase, and a changed one is dropped in expand.

// stateGuardSuffix ends the name of a guard's function, after its table
// and column; its trigger's is that name after trg_.
const stateGuardSuffix = "_transition_guard"

// StateGuard is a table's guard of one @stateMachine (D118).
type StateGuard struct {
	// Origin is the table's type and Field the field the machine governs.
	Origin string
	Field  string

	Table, QuotedTable string

	// Column is the field's column, and QuotedColumn it as create.sql
	// quotes it.
	Column, QuotedColumn string

	// FunctionName is the Postgres trigger function,
	// ticket_state_transition_guard, and TriggerName the trigger in both
	// dialects, trg_ticket_state_transition_guard.
	FunctionName string
	TriggerName  string

	// Terminal are the states no update leaves, FromNull the states a null
	// takes, and Moves each state that has a move with where it goes, as
	// the IR has them.
	Terminal []string
	FromNull []string
	Moves    []StateGuardMove
}

// StateGuardMove is one state of a StateGuard and where it may go: the
// states of To, and null when ToNull is set.
type StateGuardMove struct {
	From   string
	To     []string
	ToNull bool
}

// buildStateGuards builds the guard of each @stateMachine of the schema's
// tables from the tables as sqlgen built them, sorted by table, then
// column. A class that is no table is not among tableTypes, so it gets no
// guard.
func buildStateGuards(tableTypes []*ir.TypeDef, tables []Table) ([]StateGuard, error) {
	byName := map[string]*Table{}
	for i := range tables {
		byName[tables[i].OriginalName+"/"+tables[i].Name] = &tables[i]
	}
	var out []StateGuard
	for _, td := range tableTypes {
		for _, machine := range td.StateMachines {
			if machine == nil {
				continue
			}
			table := byName[td.Name+"/"+sqlutil.TableName(td)]
			if table == nil {
				return nil, fmt.Errorf("sqlgen: @stateMachine on %s.%s: %s has no table", td.Name, machine.Field, td.Name)
			}
			guard, err := stateGuard(td, machine, table)
			if err != nil {
				return nil, err
			}
			out = append(out, guard)
		}
	}
	slices.SortStableFunc(out, func(a, b StateGuard) int {
		return strings.Compare(a.Table+"/"+a.Column, b.Table+"/"+b.Column)
	})
	return out, nil
}

func stateGuard(td *ir.TypeDef, machine *ir.StateMachine, table *Table) (StateGuard, error) {
	var column *Column
	for i := range table.Columns {
		if table.Columns[i].Origin == td.Name+"."+machine.Field {
			column = &table.Columns[i]
			break
		}
	}
	if column == nil {
		return StateGuard{}, fmt.Errorf("sqlgen: @stateMachine on %s.%s: table %s has no column of field %s", td.Name, machine.Field, table.Name, machine.Field)
	}
	g := StateGuard{
		Origin:       td.Name,
		Field:        machine.Field,
		Table:        table.Name,
		QuotedTable:  table.QuotedName,
		Column:       column.Name,
		QuotedColumn: column.QuotedName,
		FunctionName: table.Name + "_" + column.Name + stateGuardSuffix,
		TriggerName:  "trg_" + table.Name + "_" + column.Name + stateGuardSuffix,
		Terminal:     slices.Clone(machine.Terminal),
		FromNull:     slices.Clone(machine.FromNull),
	}
	// The trigger's name is the longer of the two.
	if len(g.TriggerName) > sqlutil.PostgresIdentifierLimit {
		return g, fmt.Errorf("sqlgen: @stateMachine on %s.%s: the guard's trigger on table %s is %s, %d bytes, over PostgreSQL's %d-byte limit; give the table or the field's column a shorter name (D118)",
			td.Name, machine.Field, table.Name, g.TriggerName, len(g.TriggerName), sqlutil.PostgresIdentifierLimit)
	}
	for _, tr := range machine.Transitions {
		if tr == nil {
			continue
		}
		g.Moves = append(g.Moves, StateGuardMove{From: tr.From, To: slices.Clone(tr.To), ToNull: tr.ToNull})
	}
	return g, nil
}

// Trigger is the Postgres trigger that executes the function before each
// update that sets the column.
func (g StateGuard) Trigger() Trigger {
	return Trigger{
		Name:        g.TriggerName,
		Timing:      "BEFORE UPDATE OF " + g.QuotedColumn,
		Table:       g.Table,
		QuotedTable: g.QuotedTable,
		Function:    g.FunctionName,
		Refuses:     true,
	}
}

// stateLiterals are states as a SQL list of string literals.
func stateLiterals(states []string) string {
	literals := make([]string, len(states))
	for i, state := range states {
		literals[i] = stringLiteral(state)
	}
	return strings.Join(literals, ", ")
}

// listed are the moves the machine lists, each a condition on the column's
// value before the update and after it, before and after: the null's, then
// each state's in the IR's order. same compares a value with a state, = on
// Postgres and IS on SQLite. A condition is false or null of a pair the
// machine does not list, never true.
func (g StateGuard) listed(before, after, same string) []string {
	var out []string
	if len(g.FromNull) > 0 {
		out = append(out, fmt.Sprintf("(%s IS NULL AND %s IN (%s))", before, after, stateLiterals(g.FromNull)))
	}
	for _, move := range g.Moves {
		var to string
		switch {
		case move.ToNull && len(move.To) > 0:
			to = fmt.Sprintf("(%s IS NULL OR %s IN (%s))", after, after, stateLiterals(move.To))
		case move.ToNull:
			to = after + " IS NULL"
		case len(move.To) > 0:
			to = fmt.Sprintf("%s IN (%s)", after, stateLiterals(move.To))
		default:
			continue
		}
		out = append(out, fmt.Sprintf("(%s %s %s AND %s)", before, same, stringLiteral(move.From), to))
	}
	return out
}

// name is the guarded column as an error names it: table.column.
func (g StateGuard) name() string {
	return g.Table + "." + g.Column
}

// raiseFormat is s as the format string of a plpgsql RAISE, which reads a
// % as a parameter's place.
func raiseFormat(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "'", "''"), "%", "%%")
}

// dollarQuote is the dollar-quote tag that opens and closes a function
// body: $$, or the first of $guard$, $guard1$ and so on that the body does
// not hold, since a state may hold any text.
func dollarQuote(body string) string {
	tag := "$$"
	for n := 0; strings.Contains(body, tag); n++ {
		tag = "$guard$"
		if n > 0 {
			tag = fmt.Sprintf("$guard%d$", n)
		}
	}
	return tag
}

// FunctionSQL renders the Postgres trigger function. It returns the row of
// an update that leaves the column as it was or makes a move the machine
// lists, and raises check_violation (23514) for any other, with the two
// values in the message. A condition that is null is not met, so a pair
// the machine does not list is always refused.
func (g StateGuard) FunctionSQL() string {
	before, after := "OLD."+g.QuotedColumn, "NEW."+g.QuotedColumn
	var b strings.Builder
	fmt.Fprintf(&b, "BEGIN\n  -- The @stateMachine of %s.%s (D118): an update that leaves the\n  -- column's value is no move, and any other must be one the machine lists.\n", g.Origin, g.Field)
	fmt.Fprintf(&b, "  IF %s IS NOT DISTINCT FROM %s THEN\n    RETURN NEW;\n  END IF;\n", after, before)
	if len(g.Terminal) > 0 {
		fmt.Fprintf(&b, "  IF %s IN (%s) THEN\n    RAISE EXCEPTION '%s is terminal at %%: it cannot move to %%', %s, %s\n      USING ERRCODE = 'check_violation';\n  END IF;\n",
			before, stateLiterals(g.Terminal), raiseFormat(g.name()), before, after)
	}
	if listed := g.listed(before, after, "="); len(listed) > 0 {
		fmt.Fprintf(&b, "  IF %s\n  THEN\n    RETURN NEW;\n  END IF;\n", strings.Join(listed, "\n    OR "))
	}
	fmt.Fprintf(&b, "  RAISE EXCEPTION '%s cannot move from %% to %%', %s, %s\n    USING ERRCODE = 'check_violation';\nEND;\n", raiseFormat(g.name()), before, after)
	body := b.String()
	tag := dollarQuote(body)
	return fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS %s\n%s%s LANGUAGE plpgsql", sqlutil.QuoteIdentifier(g.FunctionName), tag, body, tag)
}

// SQLiteMessage is the message the guard's trigger fails a write with on
// SQLite. It is fixed, since RAISE takes a literal: it names the column
// and not the two values.
func (g StateGuard) SQLiteMessage() string {
	return g.name() + " cannot make this move: its state machine does not list it"
}

// SQLiteTrigger renders the guard on SQLite, which has no trigger
// functions: one trigger, under the Postgres trigger's name, whose WHEN is
// the rule and whose body fails the write with SQLiteMessage. The WHEN is
// true of an update that changes the column, by IS NOT, SQLite's null-safe
// inequality, from a terminal state or by a move the machine does not
// list; COALESCE reads a null condition as one not met, as plpgsql's IF
// does. quote quotes an identifier as the SQLite dialect does.
func (g StateGuard) SQLiteTrigger(quote func(string) string) SQLiteTrigger {
	column := quote(g.Column)
	before, after := "OLD."+column, "NEW."+column
	refused := "1"
	if listed := g.listed(before, after, "IS"); len(listed) > 0 {
		refused = "NOT COALESCE(\n    " + strings.Join(listed, "\n    OR ") + ", 0)"
	}
	if len(g.Terminal) > 0 {
		refused = fmt.Sprintf("%s IN (%s)\n  OR %s", before, stateLiterals(g.Terminal), refused)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TRIGGER %s BEFORE UPDATE OF %s ON %s FOR EACH ROW\n", quote(g.TriggerName), column, quote(g.Table))
	fmt.Fprintf(&b, "WHEN %s IS NOT %s AND (\n  %s)\n", after, before, refused)
	fmt.Fprintf(&b, "BEGIN\n  SELECT RAISE(ABORT, %s);\nEND", stringLiteral(g.SQLiteMessage()))
	return SQLiteTrigger{Name: g.TriggerName, Table: g.Table, Definition: b.String(), Refuses: true}
}
