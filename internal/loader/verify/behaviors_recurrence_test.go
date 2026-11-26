package verify

import (
	"encoding/json"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// recurrenceSchema is a DB schema with a holiday table, an agreement a
// duty's relation points at, and a duty that implements Recurrence with
// config, its calendar sources as given.
func recurrenceSchema(config string, sources ...ir.CalendarSourceDef) (*ir.Schema, *ir.TypeDef) {
	field := func(name, typ string) *ir.FieldDef { return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: typ}} }
	holiday := &ir.TypeDef{Name: "Holiday", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{field("id", "Identity.UUID"), field("calendar", "string"), field("date", "Temporal.Date")}}
	agreement := &ir.TypeDef{Name: "Agreement", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{field("id", "Identity.UUID"), field("endsOn", "Temporal.Date"), field("name", "string")}}
	duty := &ir.TypeDef{
		Name: "Duty", Role: ir.RoleDBTable,
		Fields:          []*ir.FieldDef{field("id", "Identity.UUID"), field("title", "string"), field("holidayCalendar", "string"), field("agreement", "Agreement"), field("startsOn", "Temporal.Date")},
		Behaviors:       []ir.BehaviorRef{{Name: recurrenceBehavior, Implements: true, Config: json.RawMessage(config)}},
		CalendarSources: sources,
	}
	schema := &ir.Schema{Name: "duties-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{"Holiday": holiday, "Agreement": agreement, "Duty": duty}}
	return schema, duty
}

func recurrenceProblemOf(config string) string {
	schema, duty := recurrenceSchema(config)
	var parsed recurrenceConfig
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		panic(err)
	}
	return recurrenceProblem(schema, duty, parsed)
}

// The build holds a Recurrence config to the schema (D152): its holiday
// table and fields, its relation and its date field, and relative given
// exactly when anchors lists it.
func TestRecurrenceConfigProblems(t *testing.T) {
	const holidays = `"holidays": {"table": "Holiday", "calendarField": "calendar", "dateField": "date", "field": "holidayCalendar"}`
	const relative = `"relative": {"via": "agreement", "type": "Agreement", "field": "endsOn"}`
	if why := recurrenceProblemOf(`{"anchors": ["calendar", "relative"], ` + holidays + `, ` + relative + `}`); why != "" {
		t.Fatalf("a config the schema takes: %s", why)
	}
	for _, tc := range []struct{ config, want string }{
		{`{"anchors": ["relative"]}`, "the relative anchor needs relative"},
		{`{"anchors": ["calendar"], ` + relative + `}`, "anchors does not list relative"},
		{`{"anchors": ["calendar"], "holidays": {"table": "Closure", "calendarField": "calendar", "dateField": "date", "field": "holidayCalendar"}}`, `holidays.table "Closure" is not a table of the schema`},
		{`{"anchors": ["calendar"], "holidays": {"table": "Holiday", "calendarField": "region", "dateField": "date", "field": "holidayCalendar"}}`, `holidays.calendarField "region" is not a field of Holiday`},
		{`{"anchors": ["calendar"], "holidays": {"table": "Holiday", "calendarField": "calendar", "dateField": "id", "field": "holidayCalendar"}}`, `holidays.dateField "id" is a Identity.UUID`},
		{`{"anchors": ["calendar"], "holidays": {"table": "Holiday", "calendarField": "calendar", "dateField": "date", "field": "region"}}`, `holidays.field "region" is not a field of Duty`},
		{`{"anchors": ["relative"], "relative": {"via": "title", "type": "Agreement", "field": "endsOn"}}`, `relative.via "title" is not a to-one relation of Duty`},
		{`{"anchors": ["relative"], "relative": {"via": "agreement", "type": "Contract", "field": "endsOn"}}`, `points at Agreement, and relative.type is Contract`},
		{`{"anchors": ["relative"], "relative": {"via": "agreement", "type": "Agreement", "field": "id"}}`, `relative.field "id" is a Identity.UUID`},
	} {
		if why := recurrenceProblemOf(tc.config); !strings.Contains(why, tc.want) {
			t.Errorf("%s: %q, want one with %q", tc.config, why, tc.want)
		}
	}
}

// The build holds each @calendarSource to its table (D152): a date field,
// a Recurrence to date occurrences by, fields of the table, and a name no
// other source has.
func TestCalendarSourceProblems(t *testing.T) {
	check := func(sources ...ir.CalendarSourceDef) []string {
		schema, _ := recurrenceSchema(`{"anchors": ["calendar"]}`, sources...)
		r := &Result{}
		checkCalendarSources(schema, r)
		var out []string
		for _, d := range r.Errors {
			out = append(out, d.Msg)
		}
		return out
	}
	if errs := check(ir.CalendarSourceDef{Occurrences: true, Title: "title", Kind: "duty"}, ir.CalendarSourceDef{Field: "startsOn", Title: "title", Kind: "start"}); len(errs) != 0 {
		t.Fatalf("sources the table takes: %v", errs)
	}
	for _, tc := range []struct {
		source ir.CalendarSourceDef
		want   string
	}{
		{ir.CalendarSourceDef{Field: "title", Title: "title", Kind: "x"}, `field "title" is a string`},
		{ir.CalendarSourceDef{Field: "endsOn", Title: "title", Kind: "x"}, `field "endsOn" is not a field of Duty`},
		{ir.CalendarSourceDef{Field: "startsOn", Title: "name", Kind: "x"}, `title "name" is not a field of Duty`},
	} {
		errs := check(tc.source)
		if len(errs) != 1 || !strings.Contains(errs[0], tc.want) {
			t.Errorf("%+v: %v, want one with %q", tc.source, errs, tc.want)
		}
	}
	// Two sources of one name.
	if errs := check(ir.CalendarSourceDef{Name: "due", Occurrences: true, Title: "title", Kind: "a"}, ir.CalendarSourceDef{Name: "due", Field: "startsOn", Title: "title", Kind: "b"}); len(errs) != 1 || !strings.Contains(errs[0], "its name is Duty's source's too") {
		t.Errorf("two sources named due: %v", errs)
	}
	// Occurrences of a table with no Recurrence.
	schema, duty := recurrenceSchema(`{"anchors": ["calendar"]}`, ir.CalendarSourceDef{Occurrences: true, Title: "title", Kind: "duty"})
	duty.Behaviors = nil
	r := &Result{}
	checkCalendarSources(schema, r)
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0].Msg, "implements none") {
		t.Errorf("occurrences with no Recurrence: %+v", r.Errors)
	}
}
