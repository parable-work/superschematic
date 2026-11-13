package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's @fenced counter (D118): a trigger that refuses an update that
// does not advance the fence as the declaration says, whatever writes the
// row. It is built as a @stateMachine's guard is (state_guard.go): on
// Postgres a plpgsql function and the trigger that executes it, on SQLite
// one trigger that holds the rule in its WHEN, so create.sql and the
// migration planner's models (internal/sqlmigrate) take them from here
// alike.
//
// The rule is one in both dialects. An update that changes a column the
// fence covers, or any update of the row when the fence lists no covers,
// sets the fence to the stored value plus one. Any other update keeps the
// fence or adds one: it never skips a value, moves it back or sets it
// null. A covered column the update sets to the value it holds has not
// changed, so a writer that adds one whenever it sets a covered column
// passes either way. An insert is not fenced.
//
// A fence refuses a write a server of the previous version makes, whose
// repositories advance nothing, so the planner places its trigger by the
// rule of a trigger that refuses a write (Trigger.Refuses): it is created
// in the contract phase, and a changed one is dropped in expand.

// fenceSuffix ends the name of a fence's function, after its table and
// column; its trigger's is that name after trg_.
const fenceSuffix = "_fence"

// Fence is a table's trigger of one @fenced declaration (D118).
type Fence struct {
	// Origin is the table's type and Field the fence's field.
	Origin string
	Field  string

	Table, QuotedTable string

	// Column is the fence's column, and QuotedColumn it as create.sql
	// quotes it.
	Column, QuotedColumn string

	// FunctionName is the Postgres trigger function,
	// delivery_plan_version_fence, which is also the constraint its error
	// names, and TriggerName the trigger in both dialects,
	// trg_delivery_plan_version_fence.
	FunctionName string
	TriggerName  string

	// Covers are the columns whose change the fence counts, in the order
	// the declaration lists their fields, a relation as its key's column.
	// Empty, the fence counts every update of the row.
	Covers []FenceColumn
}

// FenceColumn is a column a Fence covers: its name, and the name as
// create.sql quotes it.
type FenceColumn struct {
	Name, QuotedName string
}

// CoversRow reports whether the fence counts every update of the row.
func (f Fence) CoversRow() bool {
	return len(f.Covers) == 0
}

// buildFences builds the trigger of each @fenced declaration of the
// schema's tables from the tables as sqlgen built them, sorted by table,
// then column.
func buildFences(tableTypes []*ir.TypeDef, tables []Table) ([]Fence, error) {
	byName := map[string]*Table{}
	for i := range tables {
		byName[tables[i].OriginalName+"/"+tables[i].Name] = &tables[i]
	}
	var out []Fence
	for _, td := range tableTypes {
		for _, decl := range td.Fences {
			if decl == nil {
				continue
			}
			table := byName[td.Name+"/"+sqlutil.TableName(td)]
			if table == nil {
				return nil, fmt.Errorf("sqlgen: @fenced on %s.%s: %s has no table", td.Name, decl.Field, td.Name)
			}
			f, err := fence(td, decl, table)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b Fence) int {
		return strings.Compare(a.Table+"/"+a.Column, b.Table+"/"+b.Column)
	})
	return out, nil
}

func fence(td *ir.TypeDef, decl *ir.Fence, table *Table) (Fence, error) {
	// column is the column that stores a field of the table: a relation's
	// is its key's.
	column := func(field string) *Column {
		for i := range table.Columns {
			if table.Columns[i].Origin == td.Name+"."+field {
				return &table.Columns[i]
			}
		}
		return nil
	}
	counter := column(decl.Field)
	if counter == nil {
		return Fence{}, fmt.Errorf("sqlgen: @fenced on %s.%s: table %s has no column of field %s", td.Name, decl.Field, table.Name, decl.Field)
	}
	f := Fence{
		Origin:       td.Name,
		Field:        decl.Field,
		Table:        table.Name,
		QuotedTable:  table.QuotedName,
		Column:       counter.Name,
		QuotedColumn: counter.QuotedName,
		FunctionName: table.Name + "_" + counter.Name + fenceSuffix,
		TriggerName:  "trg_" + table.Name + "_" + counter.Name + fenceSuffix,
	}
	// The trigger's name is the longer of the two.
	if len(f.TriggerName) > sqlutil.PostgresIdentifierLimit {
		return f, fmt.Errorf("sqlgen: @fenced on %s.%s: the fence's trigger on table %s is %s, %d bytes, over PostgreSQL's %d-byte limit; give the table or the field's column a shorter name (D118)",
			td.Name, decl.Field, table.Name, f.TriggerName, len(f.TriggerName), sqlutil.PostgresIdentifierLimit)
	}
	for _, field := range decl.Covers {
		covered := column(field)
		if covered == nil {
			return f, fmt.Errorf("sqlgen: @fenced on %s.%s: covers lists %s, and table %s has no column of it", td.Name, decl.Field, field, table.Name)
		}
		f.Covers = append(f.Covers, FenceColumn{Name: covered.Name, QuotedName: covered.QuotedName})
	}
	return f, nil
}

// Trigger is the Postgres trigger that executes the function before each
// update of a row: of the row, not of the fence's column or of the covered
// ones, since a write that sets none of them may be one the fence refuses.
func (f Fence) Trigger() Trigger {
	return Trigger{
		Name:        f.TriggerName,
		Timing:      "BEFORE UPDATE",
		Table:       f.Table,
		QuotedTable: f.QuotedTable,
		Function:    f.FunctionName,
		Refuses:     true,
	}
}

// name is the fence's column as an error names it: table.column.
func (f Fence) name() string {
	return f.Table + "." + f.Column
}

// coveredRow is the covered columns of a row, NEW or OLD, as one row
// value.
func (f Fence) coveredRow(row string) string {
	columns := make([]string, len(f.Covers))
	for i, c := range f.Covers {
		columns[i] = row + "." + c.QuotedName
	}
	return "ROW(" + strings.Join(columns, ", ") + ")"
}

// FunctionSQL renders the Postgres trigger function. It returns the row of
// an update the fence allows and raises check_violation (23514) for any
// other, with the constraint FunctionName and the stored, the expected and
// the written value in the message. The covered columns are compared as
// one row with IS DISTINCT FROM, which reads a null as a value, and the
// fence with it too, so a write that sets the fence null is refused here.
// The stored value plus one is computed only for a write it decides, so a
// write that keeps a fence at its type's last value is not failed for it.
func (f Fence) FunctionSQL() string {
	before, after := "OLD."+f.QuotedColumn, "NEW."+f.QuotedColumn
	using := fmt.Sprintf("USING ERRCODE = 'check_violation', CONSTRAINT = %s;", stringLiteral(f.FunctionName))
	advance := func(indent string) string {
		return fmt.Sprintf("%sIF %s IS DISTINCT FROM %s + 1 THEN\n%s  RAISE EXCEPTION '%s must advance from %% to %%, not to %%', %s, %s + 1, %s\n%s    %s\n%sEND IF;\n",
			indent, after, before, indent, raiseFormat(f.name()), before, before, after, indent, using, indent)
	}
	var b strings.Builder
	if f.CoversRow() {
		fmt.Fprintf(&b, "BEGIN\n  -- The @fenced counter %s.%s (D118):\n  -- every update of a row sets it to the stored value plus one.\n", f.Origin, f.Field)
		b.WriteString(advance("  "))
	} else {
		fmt.Fprintf(&b, "BEGIN\n  -- The @fenced counter %s.%s (D118):\n  -- an update that changes a column it covers sets it to the stored value\n  -- plus one, and any other keeps it or adds one.\n", f.Origin, f.Field)
		fmt.Fprintf(&b, "  IF %s IS DISTINCT FROM %s THEN\n", f.coveredRow("NEW"), f.coveredRow("OLD"))
		b.WriteString(advance("    "))
		fmt.Fprintf(&b, "  ELSIF %s IS DISTINCT FROM %s THEN\n", after, before)
		fmt.Fprintf(&b, "    IF %s IS DISTINCT FROM %s + 1 THEN\n      RAISE EXCEPTION '%s must stay at %% or advance to %%, not move to %%', %s, %s + 1, %s\n        %s\n    END IF;\n",
			after, before, raiseFormat(f.name()), before, before, after, using)
		b.WriteString("  END IF;\n")
	}
	b.WriteString("  RETURN NEW;\nEND;\n")
	body := b.String()
	tag := dollarQuote(body)
	return fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS %s\n%s%s LANGUAGE plpgsql", sqlutil.QuoteIdentifier(f.FunctionName), tag, body, tag)
}

// SQLiteMessage is the message the fence's trigger fails a write with on
// SQLite. It is fixed, since RAISE takes a literal: it names the column
// and the rule, and not the three values.
func (f Fence) SQLiteMessage() string {
	if f.CoversRow() {
		return f.name() + " is fenced: an update of the row must add one to it"
	}
	return f.name() + " is fenced: an update that changes a column it covers must add one to it, and any other must keep it or add one"
}

// SQLiteTrigger renders the fence on SQLite, which has no trigger
// functions: one trigger, under the Postgres trigger's name, whose WHEN is
// the rule and whose body fails the write with SQLiteMessage. The WHEN is
// true of an update that does not set the fence to the stored value plus
// one and, when the fence lists covers, changes the fence or a covered
// column, each compared with IS NOT, SQLite's null-safe inequality, which
// is never null: so a null fence is refused, and no condition is left
// unmet for being null. quote quotes an identifier as the SQLite dialect
// does.
func (f Fence) SQLiteTrigger(quote func(string) string) SQLiteTrigger {
	changed := func(column string) string {
		return fmt.Sprintf("NEW.%s IS NOT OLD.%s", quote(column), quote(column))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TRIGGER %s BEFORE UPDATE ON %s FOR EACH ROW\n", quote(f.TriggerName), quote(f.Table))
	fmt.Fprintf(&b, "WHEN %s + 1", changed(f.Column))
	if !f.CoversRow() {
		conditions := []string{changed(f.Column)}
		for _, c := range f.Covers {
			conditions = append(conditions, changed(c.Name))
		}
		fmt.Fprintf(&b, " AND (\n  %s)", strings.Join(conditions, "\n  OR "))
	}
	fmt.Fprintf(&b, "\nBEGIN\n  SELECT RAISE(ABORT, %s);\nEND", stringLiteral(f.SQLiteMessage()))
	return SQLiteTrigger{Name: f.TriggerName, Table: f.Table, Definition: b.String(), Refuses: true}
}
