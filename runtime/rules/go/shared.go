package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// A use's shared tables (D103's step 4): the rows a behavior keeps for the
// table as a whole rather than per row (Numbering's counts), each in a table
// the layout names (Use.Shared), keyed by the declared key alone, with no
// column of a row's key, each row with a version of its own. The core reads
// one by its key (ReadKindSharedRow) and its change set inserts it or
// updates it held to the version it read (ChangeSet.SharedRows). A row's
// delete leaves them. Each write is held to change one row, so a concurrent
// writer that inserted the key first or moved the row on moves the unit,
// and the command runs again from fresh state, on Postgres, SQLite and D1
// alike, with no lock.

// SharedRow is one row of a use's shared table as the host read it: its
// key, its values by the declared field's name, the key's field left out
// and a NULL column absent, and the version its next write is held to.
type SharedRow struct {
	Key     string                     `json:"key"`
	Values  map[string]json.RawMessage `json:"values"`
	Version int64                      `json:"version"`
}

// MarshalJSON writes the row with its values, {} when nil.
func (r SharedRow) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Key     string                     `json:"key"`
		Values  map[string]json.RawMessage `json:"values"`
		Version int64                      `json:"version"`
	}{r.Key, objectOf(r.Values), r.Version})
}

// SharedRowChangeKind is what a SharedRowChange does. The list is open: a
// kind the adapter does not know refuses the command as internal.
type SharedRowChangeKind string

// The kinds of SharedRowChange the adapter applies.
const (
	SharedRowInsert SharedRowChangeKind = "insert"
	SharedRowUpdate SharedRowChangeKind = "update"
)

// SharedRowChange is what happens to a row of a shared table: an insert of
// its values at version 1, or an update of the values it sets held to the
// version ExpectedVersion, which it moves on by one. Values are by the
// declared field's name.
type SharedRowChange struct {
	Kind            SharedRowChangeKind        `json:"kind"`
	ExpectedVersion int64                      `json:"expectedVersion,omitempty"`
	Values          map[string]json.RawMessage `json:"values,omitempty"`
	// Raw is the change as the core wrote it, for an unknown kind's
	// message.
	Raw json.RawMessage `json:"-"`
}

// MarshalJSON writes the change as serde writes the core's.
func (c SharedRowChange) MarshalJSON() ([]byte, error) {
	switch c.Kind {
	case SharedRowInsert:
		return json.Marshal(struct {
			Kind   SharedRowChangeKind        `json:"kind"`
			Values map[string]json.RawMessage `json:"values"`
		}{c.Kind, objectOf(c.Values)})
	case SharedRowUpdate:
		return json.Marshal(struct {
			Kind            SharedRowChangeKind        `json:"kind"`
			ExpectedVersion int64                      `json:"expectedVersion"`
			Values          map[string]json.RawMessage `json:"values"`
		}{c.Kind, c.ExpectedVersion, objectOf(c.Values)})
	}
	if len(c.Raw) > 0 {
		return c.Raw, nil
	}
	return nil, fmt.Errorf("rules: no shared-row change of kind %q", c.Kind)
}

// UnmarshalJSON reads a shared-row change of any kind, keeping its JSON in
// Raw.
func (c *SharedRowChange) UnmarshalJSON(data []byte) error {
	type plain SharedRowChange
	var out plain
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	out.Raw = append(json.RawMessage(nil), data...)
	*c = SharedRowChange(out)
	return nil
}

// SharedRowWrite is one write of a row of a use's shared table: the type,
// the use that owns the table, the table's declared name, the row's key in
// it, and the change.
type SharedRowWrite struct {
	Type   string          `json:"type"`
	UseID  string          `json:"useId"`
	Table  string          `json:"table"`
	Key    string          `json:"key"`
	Change SharedRowChange `json:"change"`
}

// SharedTable is a table a use keeps for the type's table as a whole
// (D103's step 4): keyed by its declared key alone, its rows shared by every
// row of the type, each held to a version of its own.
type SharedTable struct {
	// Name is the declared name (counts), Table the table, unquoted.
	Name  string `json:"name"`
	Table string `json:"table"`
	// Key is the declared key field and its column, the table's primary
	// key; Fields are the other declared fields, in the declaration's
	// order.
	Key    OwnedField   `json:"key"`
	Fields []OwnedField `json:"fields"`
	// Version is the column its rows' versions are in.
	Version string `json:"version"`
}

// sharedTableOf is the shared table of a use of table the layout names, or
// the internal refusal naming what the layout lacks.
func sharedTableOf(table *Table, useID, name string) (*SharedTable, error) {
	for i := range table.Uses {
		use := &table.Uses[i]
		if use.ID != useID {
			continue
		}
		for j := range use.Shared {
			if use.Shared[j].Name == name {
				return &use.Shared[j], nil
			}
		}
		return nil, Internal("%s's use %s (%s) keeps no shared table %s in the behaviors' layout", table.Type, useID, use.Behavior, name)
	}
	return nil, Internal("%s has no use %s in the behaviors' layout", table.Type, useID)
}

// ReadSharedRow reads the row of a key of a use's shared table, as the core
// reads it: its values by the declared field's name, a NULL column absent,
// and its version; nil when the table holds no row of the key.
func ReadSharedRow(ctx context.Context, host Host, typeName, useID, table, key string) (*SharedRow, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	row, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	return readSharedRow(ctx, host.Store, row, useID, table, key)
}

// readSharedRow is the row of a key of a shared table of a use of table.
func readSharedRow(ctx context.Context, store Store, table *Table, useID, name, key string) (*SharedRow, error) {
	shared, err := sharedTableOf(table, useID, name)
	if err != nil {
		return nil, err
	}
	dialect := store.Dialect()
	selected := []string{quote(shared.Version)}
	for _, field := range shared.Fields {
		selected = append(selected, quote(field.Column.Column))
	}
	rows, err := store.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s = %s", strings.Join(selected, ", "), quote(shared.Table), quote(shared.Key.Column.Column), placeholder(dialect, 1)), key)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	values := rows[0]
	if len(values) != len(selected) {
		return nil, Internal("a read of %s selected %d columns and the store gave %d", shared.Table, len(selected), len(values))
	}
	version, err := int64Of(values[0])
	if err != nil {
		return nil, Internal("a row of %s holds a version that is no integer: %v", shared.Table, err)
	}
	out := &SharedRow{Key: key, Values: map[string]json.RawMessage{}, Version: version}
	for i, field := range shared.Fields {
		value, present, err := fromColumn(values[i+1], field.Column, table)
		if err != nil {
			return nil, err
		}
		if present {
			out.Values[field.Name] = value
		}
	}
	return out, nil
}

// sharedRowStatement is the statement of one write of a shared table's
// row: an insert of its key, its values and version 1, a key the table
// holds left as it is, or an update of the values it sets and the next
// version where the row is at the version expected, each held to change one
// row. A value of a field the layout's shared table does not hold (the
// key's among them), or a kind of change the adapter does not know,
// refuses the command as internal.
func sharedRowStatement(layout *Layout, dialect Dialect, write SharedRowWrite) (*Statement, error) {
	table, err := layout.table(write.Type)
	if err != nil {
		return nil, err
	}
	shared, err := sharedTableOf(table, write.UseID, write.Table)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, field := range shared.Fields {
		known[field.Name] = true
	}
	for name := range write.Change.Values {
		if !known[name] {
			return nil, Internal("shared table %s has no field %s", shared.Table, name)
		}
	}
	var set []assignment
	for _, field := range shared.Fields {
		raw, ok := write.Change.Values[field.Name]
		if !ok {
			continue
		}
		value, err := toColumn(raw, field.Column, table, dialect)
		if err != nil {
			return nil, err
		}
		set = append(set, assignment{field.Column.Column, value})
	}
	switch write.Change.Kind {
	case SharedRowInsert:
		values := []any{write.Key, int64(1)}
		columns := []string{quote(shared.Key.Column.Column), quote(shared.Version)}
		placeholders := []string{placeholder(dialect, 1), placeholder(dialect, 2)}
		for _, a := range set {
			values = append(values, a.value)
			columns = append(columns, quote(a.column))
			placeholders = append(placeholders, placeholder(dialect, len(values)))
		}
		return &Statement{
			Text: fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO NOTHING",
				quote(shared.Table), strings.Join(columns, ", "), strings.Join(placeholders, ", "), quote(shared.Key.Column.Column)),
			Values: values,
			Expect: 1,
		}, nil
	case SharedRowUpdate:
		values := []any{write.Key, write.Change.ExpectedVersion}
		sets := make([]string, 0, len(set)+1)
		for _, a := range set {
			values = append(values, a.value)
			sets = append(sets, quote(a.column)+" = "+placeholder(dialect, len(values)))
		}
		sets = append(sets, fmt.Sprintf("%s = %s + 1", quote(shared.Version), quote(shared.Version)))
		return &Statement{
			Text: fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s AND %s = %s",
				quote(shared.Table), strings.Join(sets, ", "), quote(shared.Key.Column.Column), placeholder(dialect, 1), quote(shared.Version), placeholder(dialect, 2)),
			Values: values,
			Expect: 1,
		}, nil
	}
	raw := write.Change.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(jsonText(write.Change))
	}
	return nil, Internal("the adapter takes no shared-table row change of this kind: %s", compactText(raw))
}
