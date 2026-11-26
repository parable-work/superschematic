package tsgen

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// ORMCalendarSource is one @calendarSource of a table as the generated
// repository's definition carries it (D152): the runtime's calendar read
// and feed (runtime/http/typescript/src/calendar.ts) date the table's rows
// by Field (to End), or its Recurrence's occurrences by their due, through
// Occurrences' sub-table.
type ORMCalendarSource struct {
	Name, Kind, Title, Color, Field, End, Owner string
	Occurrences                                 *ORMCalendarOccurrences
}

// ORMCalendarOccurrences is where an occurrences source's occurrences are:
// the Recurrence's occurrences sub-table, unquoted, its column of the
// row's key, and the columns of an occurrence's key, wall due, due instant
// and state.
type ORMCalendarOccurrences struct {
	Table, Row, Key, Due, DueAt, State string
}

// calendarSourcesOf is td's calendar sources, each named
// (ir.CalendarSourceName), an occurrences source's sub-table found among
// tables; nil for a table with none.
func calendarSourcesOf(schema *ir.Schema, td *ir.TypeDef, tables []sqlutil.TableModel) ([]ORMCalendarSource, error) {
	var out []ORMCalendarSource
	for i, source := range td.CalendarSources {
		entry := ORMCalendarSource{
			Name:  ir.CalendarSourceName(td, i),
			Kind:  source.Kind,
			Title: source.Title,
			Color: source.Color,
			Field: source.Field,
			End:   source.End,
			Owner: source.Owner,
		}
		if source.Occurrences {
			occurrences, err := occurrencesTableOf(schema, td, tables)
			if err != nil {
				return nil, err
			}
			entry.Occurrences = occurrences
		}
		out = append(out, entry)
	}
	return out, nil
}

// occurrencesTableOf is the occurrences sub-table of td's Recurrence.
func occurrencesTableOf(schema *ir.Schema, td *ir.TypeDef, tables []sqlutil.TableModel) (*ORMCalendarOccurrences, error) {
	var sub *ir.TypeDef
	for _, other := range schema.Types {
		if m := other.BehaviorTable; m != nil && m.Row == td.Name && m.Use == "Recurrence" && m.Table == "occurrences" {
			sub = other
		}
	}
	if sub == nil {
		return nil, fmt.Errorf("tsgen: table %s's @calendarSource dates occurrences, and it keeps no Recurrence occurrences", td.Name)
	}
	for _, model := range tables {
		if model.Type != sub.Name {
			continue
		}
		out := &ORMCalendarOccurrences{Table: model.Name}
		for _, c := range model.Columns {
			if c.Def == nil {
				continue
			}
			switch c.Def.Name {
			case sub.BehaviorTable.RowField:
				out.Row = c.Name
			case "key":
				out.Key = c.Name
			case "due":
				out.Due = c.Name
			case "dueAt":
				out.DueAt = c.Name
			case "state":
				out.State = c.Name
			}
		}
		if out.Row == "" || out.Key == "" || out.DueAt == "" || out.State == "" {
			return nil, fmt.Errorf("tsgen: %s's occurrences table %s lacks a column the calendar reads", td.Name, model.Name)
		}
		return out, nil
	}
	return nil, fmt.Errorf("tsgen: %s's occurrences are the type %s, which is no table of the schema", td.Name, sub.Name)
}
