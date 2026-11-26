package ir

// CalendarSourceDef is one @calendarSource of a DB table (D152): what of
// its rows appears on the calendar. A source dates each row by a date or
// date-time field (Field, and End when it spans), or each occurrence of
// the table's Recurrence (Occurrences); each entry is titled by a field of
// the row and carries the source's kind and colour key. The calendar read
// and the calendar feed keep to the rows the caller reads through the
// table's repository, its audience and grants among them.
type CalendarSourceDef struct {
	// Name is the source's name, which a read's sources and a feed's scope
	// name: the option name, else the table's name with its first letter
	// lowered, then the field's or "occurrences" after a dot when the table
	// has more than one source.
	Name string `json:"name" yaml:"name"`

	// Field is the date or date-time field each row is dated by; empty for
	// an occurrences source.
	Field string `json:"field,omitempty" yaml:"field,omitempty"`

	// End is the field the row's span ends at; empty for a point in time.
	End string `json:"end,omitempty" yaml:"end,omitempty"`

	// Occurrences dates each occurrence of the table's Recurrence by its
	// due time.
	Occurrences bool `json:"occurrences,omitempty" yaml:"occurrences,omitempty"`

	// Title is the field of the row an entry is titled by.
	Title string `json:"title" yaml:"title"`

	// Kind is what an entry of the source is, for a reader: deadline,
	// meeting, renewal.
	Kind string `json:"kind" yaml:"kind"`

	// Color is the key a reader colours its entries by; empty for none.
	Color string `json:"color,omitempty" yaml:"color,omitempty"`

	// Owner is the field that holds a row's owner, which a read's scope
	// "mine" keeps to the caller; empty for a source no such read takes.
	Owner string `json:"owner,omitempty" yaml:"owner,omitempty"`
}

// CalendarSourceName is the name of td's i-th calendar source (D152): its
// own Name, else td's name with its first letter lowered, and, when td has
// more than one source, its field's name, or "occurrences", after a dot.
func CalendarSourceName(td *TypeDef, i int) string {
	source := td.CalendarSources[i]
	if source.Name != "" {
		return source.Name
	}
	base := td.Name
	if base != "" {
		base = string(base[0]|0x20) + base[1:]
	}
	if len(td.CalendarSources) == 1 {
		return base
	}
	if source.Occurrences {
		return base + ".occurrences"
	}
	return base + "." + source.Field
}
