package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// recurrenceBehavior is the name of the core's Recurrence, which the
// behaviors' core alone implements, on a table (D152).
const recurrenceBehavior = "Recurrence"

// recurrenceConfig is a Recurrence config as the build reads it (D152).
type recurrenceConfig struct {
	Anchors  []string `json:"anchors"`
	Holidays *struct {
		Table         string `json:"table"`
		CalendarField string `json:"calendarField"`
		DateField     string `json:"dateField"`
		Field         string `json:"field"`
		WeekdayField  string `json:"weekdayField"`
	} `json:"holidays"`
	Relative *struct {
		Via   string `json:"via"`
		Type  string `json:"type"`
		Field string `json:"field"`
	} `json:"relative"`
}

// recurrenceDateTypes are the types a holiday's date and a relative
// anchor's date may be: a date, a date-time, or a string of a wall date.
var recurrenceDateTypes = []string{"Temporal.Date", "Temporal.DateTime", "string"}

// checkRecurrenceConfig holds the config of a Recurrence use the table td
// implements to its schema (D152): the holiday table is a table of the
// schema with the calendar and date fields it names, a date of a date
// type, and the field of td that names a row's calendar; the relative
// anchor's relation is a to-one relation of td to the type it names, whose
// date field is of a date type; and relative is given exactly when anchors
// lists it. Each problem is `type <T>: behavior Recurrence config:
// <reason>`, the first alone, as the core reports one. The core holds the
// zone and the spans when the table's first command defines it.
func checkRecurrenceConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, _ map[string]registry.Behavior, r *Result) {
	var config recurrenceConfig
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	if why := recurrenceProblem(schema, td, config); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
	}
}

// recurrenceProblem is the first problem of a table's Recurrence config,
// or "".
func recurrenceProblem(schema *ir.Schema, td *ir.TypeDef, config recurrenceConfig) string {
	relative := slices.Contains(config.Anchors, "relative")
	switch {
	case relative && config.Relative == nil:
		return "the relative anchor needs relative: the relation and the date field it reads"
	case !relative && config.Relative != nil:
		return "relative is given and anchors does not list relative"
	}
	if h := config.Holidays; h != nil {
		table := schema.Types[h.Table]
		if table == nil || table.Role != ir.RoleDBTable || table.JsonField {
			return fmt.Sprintf("holidays.table %q is not a table of the schema", h.Table)
		}
		if sqlutil.FieldNamed(table, h.CalendarField) == nil {
			return fmt.Sprintf("holidays.calendarField %q is not a field of %s", h.CalendarField, h.Table)
		}
		if why := dateFieldProblem(table, h.DateField, "holidays.dateField"); why != "" {
			return why
		}
		if sqlutil.FieldNamed(td, h.Field) == nil {
			return fmt.Sprintf("holidays.field %q is not a field of %s", h.Field, td.Name)
		}
		if why := weekdayFieldProblem(table, h.WeekdayField, h.DateField); why != "" {
			return why
		}
	}
	if rel := config.Relative; rel != nil {
		var names []string
		var via *ir.FieldDef
		for _, fd := range toOneRelations(schema, td) {
			names = append(names, fd.Name)
			if fd.Name == rel.Via {
				via = fd
			}
		}
		if via == nil {
			listed := "none"
			if len(names) > 0 {
				listed = strings.Join(names, ", ")
			}
			return fmt.Sprintf("relative.via %q is not a to-one relation of %s (its relations: %s)", rel.Via, td.Name, listed)
		}
		if via.TypeRef.Name != rel.Type {
			return fmt.Sprintf("relative.via %q points at %s, and relative.type is %s", rel.Via, via.TypeRef.Name, rel.Type)
		}
		if why := dateFieldProblem(schema.Types[rel.Type], rel.Field, "relative.field"); why != "" {
			return why
		}
	}
	return ""
}

// weekdayFieldProblem holds a holiday table's weekdayField (#729): a string
// field of the table, whose rows with a weekday and no date close that day
// every week, so the table's date field is nullable. "" with none given.
func weekdayFieldProblem(table *ir.TypeDef, name, dateField string) string {
	if name == "" {
		return ""
	}
	fd := sqlutil.FieldNamed(table, name)
	if fd == nil {
		return fmt.Sprintf("holidays.weekdayField %q is not a field of %s", name, table.Name)
	}
	if fd.TypeRef.IsArray || fd.TypeRef.Name != "string" {
		return fmt.Sprintf("holidays.weekdayField %q is a %s, and a weekday is a string (friday)", name, fd.TypeRef.Name)
	}
	if date := sqlutil.FieldNamed(table, dateField); date != nil && date.Required {
		return fmt.Sprintf("holidays.dateField %q of %s is not nullable, and a row that names a weekday has no date; make it Nullable", dateField, table.Name)
	}
	return ""
}

// dateFieldProblem holds a field a Recurrence config names to a date type.
func dateFieldProblem(td *ir.TypeDef, name, what string) string {
	fd := sqlutil.FieldNamed(td, name)
	if fd == nil {
		return fmt.Sprintf("%s %q is not a field of %s", what, name, td.Name)
	}
	if fd.TypeRef.IsArray || !slices.Contains(recurrenceDateTypes, fd.TypeRef.Name) {
		return fmt.Sprintf("%s %q is a %s, and a date is a Temporal.Date, a Temporal.DateTime or a string", what, name, fd.TypeRef.Name)
	}
	return ""
}
