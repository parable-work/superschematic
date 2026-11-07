package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// A read across a table (D104, ReadKindRows): one page of the live rows of a
// type, which a rule over a table's rows reads to the end (Pool's pick).
// Each own field the request's Equal names is held to its value by an
// equality the adapter binds as it binds a command's own value of the field,
// or IS NULL for a null: equality over own columns is all the adapter learns
// of the rule, and what a behavior keeps, prefers or orders by stays the
// core's. Each row of the page comes with its rows of each sub-table the
// request's Tables names, read by one query per sub-table for the whole
// page, so a contract between behaviors (Capacity) is answered over the rows
// read with no read per row.

// readRows is one page of the live rows of table each own field
// request.Equal names holds the value of, in the key column's order, at
// most request.Limit, after the key request.After when it is set: each row
// as a row read gives it, with its key as the key column reads, with the
// rows of each sub-table request.Tables names, and whether more follow. A
// value of a UUID field that is no UUID, which no column of it can hold,
// matches no row. A field that is no own column of the table, or a use or
// sub-table the layout does not give it, refuses the command as internal.
func readRows(ctx context.Context, store Store, table *Table, request ReadRequest) (*LinkedRows, error) {
	if request.Limit < 0 {
		return nil, Internal("a read of %s's rows asks for %d rows", table.Type, request.Limit)
	}
	subs := make([]*SubTable, len(request.Tables))
	for i, ref := range request.Tables {
		_, sub, err := subTableOf(table, ref.UseID, ref.Table)
		if err != nil {
			return nil, err
		}
		subs[i] = sub
	}
	dialect := store.Dialect()
	var values []any
	bind := func(value any) string {
		values = append(values, value)
		return placeholder(dialect, len(values))
	}
	names := make([]string, 0, len(request.Equal))
	for name := range request.Equal {
		names = append(names, name)
	}
	sort.Strings(names)
	var conditions []string
	for _, name := range names {
		var column *Column
		for i := range table.Own {
			if table.Own[i].Field == name {
				column = &table.Own[i]
			}
		}
		if column == nil {
			return nil, Internal("%s has no own field %s in the behaviors' layout", table.Type, name)
		}
		raw := request.Equal[name]
		if isNull(raw) {
			conditions = append(conditions, quote(column.Column)+" IS NULL")
			continue
		}
		if uuidTypes[column.Type] {
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return &LinkedRows{Rows: []KeyedRow{}}, nil
			}
			if _, ok := uuidValue(text); !ok {
				return &LinkedRows{Rows: []KeyedRow{}}, nil
			}
		}
		value, err := toColumn(raw, *column, table, dialect)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, quote(column.Column)+" = "+bind(value))
	}
	if table.Live != nil {
		conditions = append(conditions, quote(table.Live.Column)+" IS NULL")
	}
	if request.After != "" {
		after, err := keyValue(table, request.After)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, quote(table.Key.Column)+" > "+bind(after))
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	limit := request.Limit
	selected := append([]string{quote(table.Key.Column) + " AS k"}, rowSelect(table)...)
	rows, err := store.Query(ctx, fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT %d",
		strings.Join(selected, ", "), quote(table.Table), where, quote(table.Key.Column), limit+1), values...)
	if err != nil {
		return nil, err
	}
	out := &LinkedRows{Rows: []KeyedRow{}, More: len(rows) > limit}
	if out.More {
		rows = rows[:limit]
	}
	for _, values := range rows {
		if len(values) == 0 || values[0] == nil {
			return nil, Internal("a row of %s has no key", table.Table)
		}
		key, err := keyOfColumn(table, values[0])
		if err != nil {
			return nil, err
		}
		row, err := rowOf(table, values[1:], key)
		if err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, KeyedRow{Key: key, Row: *row})
	}
	if len(subs) == 0 {
		return out, nil
	}
	for i := range out.Rows {
		out.Rows[i].Tables = map[string]map[string][]SubRow{}
		for _, ref := range request.Tables {
			if out.Rows[i].Tables[ref.UseID] == nil {
				out.Rows[i].Tables[ref.UseID] = map[string][]SubRow{}
			}
			out.Rows[i].Tables[ref.UseID][ref.Table] = []SubRow{}
		}
	}
	if len(out.Rows) == 0 {
		return out, nil
	}
	for i, ref := range request.Tables {
		if err := readPageSubRows(ctx, store, table, subs[i], ref, out.Rows); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readPageSubRows reads the rows of one sub-table of each of a page's rows
// with one query, each row's in key order by code point, as readSubRows
// orders one row's, and adds them to each row's Tables under ref.
func readPageSubRows(ctx context.Context, store Store, table *Table, sub *SubTable, ref TableRef, page []KeyedRow) error {
	dialect := store.Dialect()
	at := make(map[string]int, len(page))
	keys := make([]any, len(page))
	among := make([]string, len(page))
	for i, row := range page {
		bound, err := keyValue(table, row.Key)
		if err != nil {
			return err
		}
		at[row.Key] = i
		keys[i] = bound
		among[i] = placeholder(dialect, i+1)
	}
	selected := []string{quote(sub.Row) + " AS r", quote(sub.Key.Column.Column) + " AS k"}
	for i, field := range sub.Fields {
		selected = append(selected, fmt.Sprintf("%s AS c%d", quote(field.Column.Column), i))
	}
	order := quote(sub.Key.Column.Column)
	if dialect == DialectPostgres {
		// Code point order, as the core's BTreeMap keeps its keys; SQLite's
		// default collation is already binary.
		order += ` COLLATE "C"`
	}
	rows, err := store.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s IN (%s) ORDER BY %s, %s",
		strings.Join(selected, ", "), quote(sub.Table), quote(sub.Row), strings.Join(among, ", "), quote(sub.Row), order), keys...)
	if err != nil {
		return err
	}
	for _, values := range rows {
		if len(values) != len(selected) {
			return Internal("a read of %s selected %d columns and the store gave %d", sub.Table, len(selected), len(values))
		}
		if values[0] == nil || values[1] == nil {
			return Internal("a row of %s has no key", sub.Table)
		}
		rowKey, err := keyOfColumn(table, values[0])
		if err != nil {
			return err
		}
		i, ok := at[rowKey]
		if !ok {
			continue
		}
		read := SubRow{Key: textOf(values[1]), Values: map[string]json.RawMessage{}}
		for j, field := range sub.Fields {
			value, present, err := fromColumn(values[j+2], field.Column, table)
			if err != nil {
				return err
			}
			if present {
				read.Values[field.Name] = value
			}
		}
		page[i].Tables[ref.UseID][ref.Table] = append(page[i].Tables[ref.UseID][ref.Table], read)
	}
	return nil
}

// subTableOf is a sub-table of a use of table the layout names, or the
// internal refusal naming what the layout lacks, in subTable's words.
func subTableOf(table *Table, useID, name string) (*Use, *SubTable, error) {
	use := useOf(table, useID)
	if use == nil {
		return nil, nil, Internal("%s has no use %s in the behaviors' layout", table.Type, useID)
	}
	for i := range use.Tables {
		if use.Tables[i].Name == name {
			return use, &use.Tables[i], nil
		}
	}
	return nil, nil, Internal("%s's use %s (%s) has no sub-table %s in the behaviors' layout", table.Type, useID, use.Behavior, name)
}
