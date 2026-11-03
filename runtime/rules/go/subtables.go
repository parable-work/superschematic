package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// A use's sub-tables (D103): the rows a behavior keeps per row of the
// table its use runs on, each in a child table of the row the layout names
// (Use.Tables), keyed within its row by the declared key. The core reads a
// row's rows of one of them (ReadKindSubRows) and its change set writes
// them; nothing else of the adapter's does, and the generated repositories
// have no write path for them. Every command that writes a sub-table row
// writes its row, fenced on its version, so a sub-table row write needs no
// fence of its own: it is held to change exactly one row, which only a raw
// SQL write of the sub-table, moving no version, can keep it from.

// subTable is the sub-table of a use the layout names, or the internal
// refusal naming what the layout lacks.
func subTable(layout *Layout, typeName, useID, name string) (*Table, *SubTable, error) {
	table, err := layout.table(typeName)
	if err != nil {
		return nil, nil, err
	}
	use := useOf(table, useID)
	if use == nil {
		return nil, nil, Internal("%s has no use %s in the behaviors' layout", typeName, useID)
	}
	for i := range use.Tables {
		if use.Tables[i].Name == name {
			return table, &use.Tables[i], nil
		}
	}
	return nil, nil, Internal("%s's use %s (%s) has no sub-table %s in the behaviors' layout", typeName, useID, use.Behavior, name)
}

// ReadSubRows reads the rows of one sub-table of one use on a row, as the
// core reads them: each by its key, in key order, its values by the
// declared field's name, a NULL column absent. A row with none, or a type
// the layout lacks, reads none.
func ReadSubRows(ctx context.Context, host Host, typeName, key, useID, table string) ([]SubRow, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	row, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	return readSubRows(ctx, host.Store, row, key, useID, table)
}

// readSubRows is the rows of a sub-table of a use on the row of table by
// key, ordered by the key's code points.
func readSubRows(ctx context.Context, store Store, table *Table, key, useID, name string) ([]SubRow, error) {
	use := useOf(table, useID)
	if use == nil {
		return nil, Internal("%s has no use %s in the behaviors' layout", table.Type, useID)
	}
	var sub *SubTable
	for i := range use.Tables {
		if use.Tables[i].Name == name {
			sub = &use.Tables[i]
		}
	}
	if sub == nil {
		return nil, Internal("%s's use %s (%s) has no sub-table %s in the behaviors' layout", table.Type, useID, use.Behavior, name)
	}
	dialect := store.Dialect()
	selected := []string{quote(sub.Key.Column.Column)}
	for _, field := range sub.Fields {
		selected = append(selected, quote(field.Column.Column))
	}
	order := quote(sub.Key.Column.Column)
	if dialect == DialectPostgres {
		// Code point order, as the core's BTreeMap keeps its keys; SQLite's
		// default collation is already binary.
		order += ` COLLATE "C"`
	}
	bound, err := keyValue(table, key)
	if err != nil {
		return nil, err
	}
	rows, err := store.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s = %s ORDER BY %s", strings.Join(selected, ", "), quote(sub.Table), quote(sub.Row), placeholder(dialect, 1), order), bound)
	if err != nil {
		return nil, err
	}
	out := make([]SubRow, 0, len(rows))
	for _, values := range rows {
		if len(values) != len(selected) {
			return nil, Internal("a read of %s selected %d columns and the store gave %d", sub.Table, len(selected), len(values))
		}
		if values[0] == nil {
			return nil, Internal("a row of %s has no key", sub.Table)
		}
		row := SubRow{Key: textOf(values[0]), Values: map[string]json.RawMessage{}}
		for i, field := range sub.Fields {
			value, present, err := fromColumn(values[i+1], field.Column, table)
			if err != nil {
				return nil, err
			}
			if present {
				row.Values[field.Name] = value
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// subRowStatement is the statement of one sub-table row write: an insert
// of its row, key and values (a row its key already holds left as it is),
// an update of the values it sets, or a delete, each held to change one
// row; nil for an update that sets nothing. A value of a field the layout's
// sub-table does not hold (the key's among them), or a kind of change the
// adapter does not know, refuses the command as internal.
func subRowStatement(layout *Layout, dialect Dialect, write SubRowWrite) (*Statement, error) {
	if write.Row.Key == nil {
		return nil, Internal("a write of %s's %s.%s reached the adapter without its row's key", write.Row.Type, write.UseID, write.Table)
	}
	table, sub, err := subTable(layout, write.Row.Type, write.UseID, write.Table)
	if err != nil {
		return nil, err
	}
	rowKey, err := keyValue(table, *write.Row.Key)
	if err != nil {
		return nil, err
	}
	values := []any{rowKey, write.Key}
	bind := func(value any) string {
		values = append(values, value)
		return placeholder(dialect, len(values))
	}
	where := fmt.Sprintf("%s = %s AND %s = %s", quote(sub.Row), placeholder(dialect, 1), quote(sub.Key.Column.Column), placeholder(dialect, 2))

	// assigned is the change's values by the layout's field order.
	assigned := func() ([]assignment, error) {
		known := map[string]bool{}
		for _, field := range sub.Fields {
			known[field.Name] = true
		}
		for name := range write.Change.Values {
			if !known[name] {
				return nil, Internal("sub-table %s has no field %s", sub.Table, name)
			}
		}
		var out []assignment
		for _, field := range sub.Fields {
			raw, ok := write.Change.Values[field.Name]
			if !ok {
				continue
			}
			value, err := toColumn(raw, field.Column, table, dialect)
			if err != nil {
				return nil, err
			}
			out = append(out, assignment{field.Column.Column, value})
		}
		return out, nil
	}

	switch write.Change.Kind {
	case SubRowInsert:
		set, err := assigned()
		if err != nil {
			return nil, err
		}
		columns := []string{quote(sub.Row), quote(sub.Key.Column.Column)}
		placeholders := []string{placeholder(dialect, 1), placeholder(dialect, 2)}
		for _, a := range set {
			columns = append(columns, quote(a.column))
			placeholders = append(placeholders, bind(a.value))
		}
		return &Statement{
			Text: fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s, %s) DO NOTHING",
				quote(sub.Table), strings.Join(columns, ", "), strings.Join(placeholders, ", "), quote(sub.Row), quote(sub.Key.Column.Column)),
			Values: values,
			Expect: 1,
		}, nil
	case SubRowUpdate:
		set, err := assigned()
		if err != nil {
			return nil, err
		}
		if len(set) == 0 {
			return nil, nil
		}
		sets := make([]string, len(set))
		for i, a := range set {
			sets[i] = quote(a.column) + " = " + bind(a.value)
		}
		return &Statement{
			Text:   fmt.Sprintf("UPDATE %s SET %s WHERE %s", quote(sub.Table), strings.Join(sets, ", "), where),
			Values: values,
			Expect: 1,
		}, nil
	case SubRowDelete:
		return &Statement{
			Text:   fmt.Sprintf("DELETE FROM %s WHERE %s", quote(sub.Table), where),
			Values: values,
			Expect: 1,
		}, nil
	}
	raw := write.Change.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(jsonText(write.Change))
	}
	return nil, Internal("the adapter takes no sub-table row change of this kind: %s", compactText(raw))
}
