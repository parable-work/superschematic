package sqlgen

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A relative recurrence's target trigger (#626): a table that implements
// Recurrence with the relative anchor reads its date from a field of the
// row its to-one relation names (relative: { via, type, field }), a row of
// another table, whose write the recurring table's own change trigger
// never sees. After an update of a target row that moves the date field
// (or its live column, a soft delete or a restore), whoever makes it, the
// trigger writes a message of the recurring table's change queue for each
// live row whose relation names the target row: an update whose images
// before and after are both the row as it is, so it moves nothing of it.
// The worker runs it through the core's change command, and Recurrence
// takes a change that moves nothing as its target's write and plans the
// row again, as setRecurrence with the rule it holds does. The core reads
// nothing the other way.
//
// It is built as the change trigger is (changes.go), its message and its
// image alike: on Postgres a plpgsql function and the trigger that
// executes it, on SQLite one update trigger, D1 included, which create.sql
// and the migration planner's models take from here. It has no backfill.
// One trigger serves each pair of a target table and a recurring table,
// over every date field the recurring table's Recurrence uses read there.

// TargetChangeTrigger is the trigger a relative recurrence's target table
// keeps for one recurring table (#626).
type TargetChangeTrigger struct {
	// Origin is the recurring table's type, Filing; Target is the target's,
	// Contract; Queue is the recurring table's change queue, FilingChange.
	Origin, Target, Queue string

	// Table and QuotedTable are the target's table, the trigger's; Hearer
	// the recurring table's, unquoted, and QueueTable its change queue's.
	Table, QuotedTable string
	Hearer             string
	QueueTable         string

	// FunctionName is the Postgres trigger function,
	// contracts_filings_dates, and TriggerName the trigger,
	// trg_contracts_filings_dates, SQLite's too.
	FunctionName string
	TriggerName  string

	// TargetKey is the target's column the relation holds; Via the
	// recurring table's column that holds it; HearerKey the recurring
	// table's key column; each unquoted.
	TargetKey, Via, HearerKey string

	// Dates are the target's date columns the recurrences read, unquoted,
	// in column order, and Live its live column, deleted_at of a
	// soft-deletable table, or "".
	Dates []string
	Live  string

	// HearerLive is the recurring table's live column, or "": a deleted
	// row hears nothing.
	HearerLive string

	// image is the recurring row's image, built as its change trigger's.
	image ChangeTrigger
}

// hearerRow is the alias of the recurring table's row in the trigger's
// select.
const hearerRow = "h"

// relativeConfig is the part of a Recurrence config the trigger reads.
type relativeConfig struct {
	Relative *struct {
		Via   string `json:"via"`
		Type  string `json:"type"`
		Field string `json:"field"`
	} `json:"relative"`
}

// buildTargetChangeTriggers builds the target trigger of each table that
// implements Recurrence with a relative config whose target is a table of
// the schema and that has a change queue, sorted by target and recurring
// table. A target outside the schema has none: the app's setRecurrence
// with the same rule follows its date.
func buildTargetChangeTriggers(tableTypes []*ir.TypeDef, tables []Table, changes []ChangeTrigger, schema *ir.Schema) ([]TargetChangeTrigger, error) {
	byName := map[string]*Table{}
	for i := range tables {
		byName[tables[i].OriginalName+"/"+tables[i].Name] = &tables[i]
	}
	changeOf := map[string]ChangeTrigger{}
	for _, c := range changes {
		changeOf[c.Origin] = c
	}
	type pair struct{ hearer, target string }
	built := map[pair]*TargetChangeTrigger{}
	var order []pair
	for _, td := range tableTypes {
		for _, ref := range td.TableBehaviors() {
			if ref.Name != "Recurrence" || len(ref.Config) == 0 {
				continue
			}
			var config relativeConfig
			if json.Unmarshal(ref.Config, &config) != nil || config.Relative == nil {
				continue
			}
			rel := config.Relative
			targetDef := schema.Types[rel.Type]
			change, heard := changeOf[td.Name]
			if targetDef == nil || targetDef.Role != ir.RoleDBTable || !heard {
				continue
			}
			hearer := byName[td.Name+"/"+sqlutil.TableName(td)]
			target := byName[targetDef.Name+"/"+sqlutil.TableName(targetDef)]
			if hearer == nil || target == nil {
				continue
			}
			via := columnOf(hearer, td.Name+"."+rel.Via)
			date := columnOf(target, targetDef.Name+"."+rel.Field)
			if via == "" || date == "" {
				return nil, fmt.Errorf("sqlgen: table %s: its Recurrence's relative anchor reads %s.%s through %s, and the tables have no such columns", td.Name, rel.Type, rel.Field, rel.Via)
			}
			refColumn := ""
			for _, fk := range hearer.ForeignKeys {
				if fk.Column == via {
					refColumn = fk.RefColumn
				}
			}
			if refColumn == "" {
				refColumn = sqlutil.Unquote(target.PrimaryKey)
			}
			key := pair{td.Name, targetDef.Name}
			t := built[key]
			if t == nil {
				t = &TargetChangeTrigger{
					Origin:       td.Name,
					Target:       targetDef.Name,
					Queue:        change.Queue,
					Table:        target.Name,
					QuotedTable:  target.QuotedName,
					Hearer:       hearer.Name,
					QueueTable:   change.QueueTable,
					FunctionName: target.Name + "_" + hearer.Name + "_dates",
					TriggerName:  "trg_" + target.Name + "_" + hearer.Name + "_dates",
					TargetKey:    refColumn,
					Via:          via,
					HearerKey:    sqlutil.Unquote(hearer.PrimaryKey),
					Live:         behaviorlayout.LiveColumn(schema, targetDef),
					HearerLive:   change.Live,
					image:        change,
				}
				for _, name := range []string{t.FunctionName, t.TriggerName} {
					if len(name) > sqlutil.PostgresIdentifierLimit {
						return nil, fmt.Errorf("sqlgen: table %s: the trigger %s its relative recurrence's target %s keeps is %d bytes, over PostgreSQL's %d-byte limit; give a table a shorter name (#626)", td.Name, name, targetDef.Name, len(name), sqlutil.PostgresIdentifierLimit)
					}
				}
				built[key] = t
				order = append(order, key)
			} else if t.Via != via {
				return nil, fmt.Errorf("sqlgen: table %s: its Recurrence uses read %s through two relations, %s and %s; a table's relative recurrences read one target through one relation (#626)", td.Name, rel.Type, t.Via, via)
			}
			if !slices.Contains(t.Dates, date) {
				t.Dates = append(t.Dates, date)
			}
		}
	}
	out := make([]TargetChangeTrigger, 0, len(order))
	for _, key := range order {
		t := built[key]
		position := func(column string) int {
			return slices.IndexFunc(byName[t.Target+"/"+t.Table].Columns, func(c Column) bool { return c.Name == column })
		}
		slices.SortFunc(t.Dates, func(a, b string) int { return position(a) - position(b) })
		out = append(out, *t)
	}
	slices.SortStableFunc(out, func(a, b TargetChangeTrigger) int {
		return strings.Compare(a.Table+"/"+a.Hearer, b.Table+"/"+b.Hearer)
	})
	return out, nil
}

// columnOf is the name of the table's column that stores the field origin
// (Filing.contract), or "".
func columnOf(t *Table, origin string) string {
	for _, c := range t.Columns {
		if c.Origin == origin {
			return c.Name
		}
	}
	return ""
}

// guarded are the target's columns an update's guard compares: its date
// columns, then its live column.
func (t TargetChangeTrigger) guarded() []string {
	if t.Live == "" {
		return t.Dates
	}
	return append(slices.Clone(t.Dates), t.Live)
}

// moved is true of an update that moves a guarded column, compared with
// distinct, each column quoted by quote.
func (t TargetChangeTrigger) moved(distinct string, quote func(string) string) string {
	columns := t.guarded()
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = "OLD." + quote(c) + " " + distinct + " NEW." + quote(c)
	}
	return strings.Join(parts, " OR ")
}

// where keeps the recurring table's live rows whose relation names the
// target row.
func (t TargetChangeTrigger) where(quote func(string) string) string {
	out := hearerRow + "." + quote(t.Via) + " = NEW." + quote(t.TargetKey)
	if t.HearerLive != "" {
		out += " AND " + hearerRow + "." + quote(t.HearerLive) + " IS NULL"
	}
	return out
}

// Trigger is the Postgres trigger that executes the function after each
// update of a target row.
func (t TargetChangeTrigger) Trigger() Trigger {
	return Trigger{
		Name:        t.TriggerName,
		Timing:      "AFTER UPDATE",
		Table:       t.Table,
		QuotedTable: t.QuotedTable,
		Function:    t.FunctionName,
	}
}

// FunctionSQL renders the Postgres trigger function: an update that moves
// no date column nor the live column returns at once, and every other
// inserts one message per recurring row that names the target, created
// and due at the statement's clock.
func (t TargetChangeTrigger) FunctionSQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$\nDECLARE\n  %s timestamptz := clock_timestamp();\nBEGIN\n", pgQuote(t.FunctionName), pgWritten)
	fmt.Fprintf(&b, "  -- Writes a message of %s for each row of %s whose relation\n  -- names the row of %s whose date its relative recurrence reads,\n  -- when that date moves, whoever writes it (#626).\n", t.Queue, t.Hearer, t.Table)
	fmt.Fprintf(&b, "  IF NOT (%s) THEN\n    RETURN NULL;\n  END IF;\n", t.moved("IS DISTINCT FROM", pgQuote))
	image := t.image.pgImage(hearerRow)
	fmt.Fprintf(&b, "  INSERT INTO %s (%s)\n  SELECT %s.%s, '%s', %s, %s, %s, %s, %s\n  FROM %s %s\n  WHERE %s;\n", pgQuote(t.QueueTable), changeColumns(pgQuote, true), hearerRow, pgQuote(t.HearerKey), ir.EnqueueOnUpdate, pgAt, image, image, pgWritten, pgWritten, pgQuote(t.Hearer), hearerRow, t.where(pgQuote))
	b.WriteString("  RETURN NULL;\nEND;\n$$ LANGUAGE plpgsql")
	return b.String()
}

// SQLiteTrigger renders the trigger on SQLite: one update trigger whose
// WHEN compares the date columns and the live column with IS NOT, and
// whose insert selects the recurring rows. The message's id, due_at and
// created_at take the queue table's defaults. quote quotes an identifier
// as the SQLite dialect does.
func (t TargetChangeTrigger) SQLiteTrigger(quote func(string) string) SQLiteTrigger {
	image := t.image.sqliteImage(hearerRow, quote)
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TRIGGER %s AFTER UPDATE ON %s FOR EACH ROW\nWHEN %s\n", quote(t.TriggerName), quote(t.Table), t.moved("IS NOT", quote))
	fmt.Fprintf(&b, "BEGIN\n  INSERT INTO %s (%s)\n  SELECT %s.%s, '%s', %s, %s, %s\n  FROM %s %s\n  WHERE %s;\nEND", quote(t.QueueTable), changeColumns(quote, false), hearerRow, quote(t.HearerKey), ir.EnqueueOnUpdate, sqliteAt, image, image, quote(t.Hearer), hearerRow, t.where(quote))
	return SQLiteTrigger{Name: t.TriggerName, Table: t.Table, Definition: b.String()}
}
