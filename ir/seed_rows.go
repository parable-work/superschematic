package ir

import "slices"

// SeedRows are the rows a DB table's @seedRows declares (#625, #667): rows
// the table holds from its first migration on, so a default an app's
// policy needs (a channel kind's trust, everyone's notice preference) is
// data in the schema rather than code an app runs at deploy. create.sql
// inserts each; a migration plan inserts a row that is new, by its key,
// and never updates or deletes a seeded row: once it is in the table, it
// is the app's to edit. Each insert leaves a row the table already holds
// of the same key alone, so it may run again.
type SeedRows struct {
	// Key names the fields whose values identify a seeded row, as the
	// schema declares them; empty, [SeedRows.KeyOf] finds them.
	Key []string `json:"key,omitempty" yaml:"key,omitempty"`

	// Rows are the rows, in declaration order, each its fields' values by
	// field name: a string, a number, a boolean, null, a list of them for
	// a list field, or any JSON value for a JSON field. A field a row
	// leaves out takes its column's default.
	Rows []map[string]any `json:"rows" yaml:"rows"`
}

// KeyOf is the fields that identify a seeded row of td: the declared
// Key; else td's key fields when every row gives them; else the fields of
// the first unique field, then of the first unique index, of td that
// every row gives. Nil when none fits, which verification refuses.
func (s *SeedRows) KeyOf(td *TypeDef) []string {
	if s == nil || td == nil {
		return nil
	}
	if len(s.Key) > 0 {
		return slices.Clone(s.Key)
	}
	given := func(fields []string) bool {
		if len(fields) == 0 {
			return false
		}
		for _, row := range s.Rows {
			for _, f := range fields {
				if _, ok := row[f]; !ok {
					return false
				}
			}
		}
		return true
	}
	var key []string
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			key = append(key, fd.Name)
		}
	}
	if given(key) {
		return key
	}
	for _, fd := range td.Fields {
		if fd != nil && fd.Unique && given([]string{fd.Name}) {
			return []string{fd.Name}
		}
	}
	for _, idx := range td.Indexes {
		if idx.Unique && idx.Expression == "" && idx.Where == "" && given(idx.Keys) {
			return slices.Clone(idx.Keys)
		}
	}
	return nil
}
