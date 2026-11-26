package verify

import (
	"fmt"
	"slices"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// calendarDateTypes are the types a calendar source dates its rows by
// (D152).
var calendarDateTypes = []string{"Temporal.Date", "Temporal.DateTime"}

// checkCalendarSources holds each @calendarSource of a schema's tables to
// its table (D152): a field source's field and end are date or date-time
// fields of the table; an occurrences source's table implements
// Recurrence; its title and owner are fields of the table; and no two
// sources of the schema share a name. Each problem is `type <T>:
// @calendarSource <name>: <reason>`.
func checkCalendarSources(schema *ir.Schema, r *Result) {
	names := map[string]string{}
	var types []string
	for name := range schema.Types {
		types = append(types, name)
	}
	sort.Strings(types)
	for _, typeName := range types {
		td := schema.Types[typeName]
		for i, source := range td.CalendarSources {
			name := ir.CalendarSourceName(td, i)
			fail := func(format string, args ...any) {
				r.errorf(td.Owner, "type %s: @calendarSource %s: %s", td.Name, name, fmt.Sprintf(format, args...))
			}
			if td.Role != ir.RoleDBTable {
				fail("only a DB table is a calendar source")
				continue
			}
			if other, taken := names[name]; taken {
				fail("its name is %s's source's too; name one of them", other)
				continue
			}
			names[name] = td.Name
			for _, field := range []struct{ what, name string }{{"field", source.Field}, {"end", source.End}} {
				if field.name == "" {
					continue
				}
				fd := sqlutil.FieldNamed(td, field.name)
				if fd == nil {
					fail("%s %q is not a field of %s", field.what, field.name, td.Name)
					continue
				}
				if fd.TypeRef.IsArray || !slices.Contains(calendarDateTypes, fd.TypeRef.Name) {
					fail("%s %q is a %s, and a calendar dates by a Temporal.Date or a Temporal.DateTime", field.what, field.name, fd.TypeRef.Name)
				}
			}
			for _, field := range []struct{ what, name string }{{"title", source.Title}, {"owner", source.Owner}} {
				if field.name != "" && sqlutil.FieldNamed(td, field.name) == nil {
					fail("%s %q is not a field of %s", field.what, field.name, td.Name)
				}
			}
			if source.Occurrences {
				if _, ok := implementedConfig(td, recurrenceBehavior); !ok {
					fail("occurrences dates the occurrences of a Recurrence, and %s implements none", td.Name)
				}
			}
		}
	}
}
