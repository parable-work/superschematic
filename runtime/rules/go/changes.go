package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A table's writes as the core hears them (D103): a table whose behaviors
// hear its writes, or whose rows another table's behaviors hear through a
// relation, has a change queue (the layout's Table.Changes), which a
// trigger of the table writes a message of for each insert, update and
// delete of a row, whoever makes it: the core, a repository's bulk or hard
// write, a script, psql. A behavior worker claims each message and runs it
// through the core as a change command (RunChange), in one unit with the
// message's completion. The message carries the write's images, the row's
// columns before and after it as JSON by column, which RunChange converts
// to the core's logical names as a row read converts each column. The core
// runs, on the row as it is now, each use of its type that hears its
// writes, and each use of another type that listens to it through a
// relation of the row; a row it finds gone is a RowGone, whose deadlines
// the unit deletes, and whose sub-table rows too when no row of its key is
// left at all, so a soft-deleted row keeps them.

// Change is a message of a table's change queue as its trigger wrote it
// (D103): the row's key, in the core's form or its column's (a UUID's
// base62 or hyphenated text), the write, its time in epoch milliseconds,
// and the row's columns before and after it, each a JSON object by column
// or null. On SQLite an image may be the JSON object's text, as a TEXT
// column holds it.
type Change struct {
	Key    string
	Write  string
	At     int64
	Before json.RawMessage
	After  json.RawMessage
}

// ChangeRun is what running a change came to: run, with its Result, or
// nothing run, Why saying why ("claim lost").
type ChangeRun struct {
	Ran    bool
	Why    string
	Result *Result
}

// The writes a change message names.
const (
	WriteInsert = "insert"
	WriteUpdate = "update"
	WriteDelete = "delete"
)

// RunChange runs a change of a type through the core in the store's
// transaction, as caller (a behavior worker's subject), at the host's time.
// With a claim, its unit deletes the claimed message of the table's change
// queue by its id and token, after the fenced row writes and before the
// sub-table rows and deadlines; a unit that did not apply because the claim
// no longer holds the message runs nothing ("claim lost"), and one that did
// not apply for a row that moved runs again. Without one, nothing is spent.
// A refusal comes back as the error, nothing of it applied, as does a
// failure of the database: the worker fails the message, which falls due
// again after its backoff (D68). A change whose write is no insert, update
// or delete, or whose image is no JSON object, is refused as internal.
func RunChange(ctx context.Context, host Host, typeName string, change Change, caller Caller, claim *Claim, opts *ExecuteOptions) (*ChangeRun, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	table, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	action, err := changeAction(table, host.Store.Dialect(), change)
	if err != nil {
		return nil, err
	}
	result, err := execute(ctx, host, typeName, commandOf(host, caller, action), opts, claim)
	switch {
	case err == nil:
		return &ChangeRun{Ran: true, Result: result}, nil
	case errors.Is(err, errClaimLost):
		return &ChangeRun{Why: WhyClaimLost}, nil
	}
	return nil, err
}

// changeAction is a change message as the core's change command takes it:
// its key in the core's form, its write, its time, and its images by the
// core's logical names.
func changeAction(table *Table, dialect Dialect, change Change) (Action, error) {
	key := change.Key
	if uuidTypes[table.Key.Type] {
		wire, err := uuidToWire(change.Key)
		if err != nil {
			return Action{}, Internal("%s's change of %s: its key is no UUID, hyphenated or base62", table.Type, change.Key)
		}
		key = wire
	}
	switch change.Write {
	case WriteInsert, WriteUpdate, WriteDelete:
	default:
		return Action{}, Internal("%s's change of %s is a write of %s, which is no insert, update or delete", table.Type, key, change.Write)
	}
	before, err := imageOf(table, dialect, key, change.Before)
	if err != nil {
		return Action{}, err
	}
	after, err := imageOf(table, dialect, key, change.After)
	if err != nil {
		return Action{}, err
	}
	return Action{Kind: ActionChange, Key: key, Change: &RowChange{Write: change.Write, At: change.At, Before: before, After: after}}, nil
}

// imageOf is a write's image as the core reads it: nil for none (absent or
// null); else each own column of the layout the JSON object holds as
// own[field], and each use's owned column as owned[use id][declared name],
// each value converted as a row read converts its column in the dialect,
// and every other member ignored. A value the conversion makes none, a
// null among them, is absent, as is a use with none of its fields.
func imageOf(table *Table, dialect Dialect, key string, raw json.RawMessage) (*RowImage, error) {
	if isNull(raw) {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if trimmed[0] == '"' {
		// A SQLite TEXT that holds the object.
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, Internal("%s's change of %s: its image %s is no JSON object", table.Type, key, compactText(trimmed))
		}
		trimmed = bytes.TrimSpace([]byte(text))
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &members); err != nil || members == nil {
		return nil, Internal("%s's change of %s: its image %s is no JSON object", table.Type, key, compactText(trimmed))
	}
	value := func(column Column) (json.RawMessage, bool, error) {
		member, ok := members[column.Column]
		if !ok {
			return nil, false, nil
		}
		scanned, err := imageValue(member, column, dialect)
		if err != nil {
			return nil, false, Internal("%s's change of %s: its image's %s: %v", table.Type, key, column.Column, err)
		}
		return fromColumn(scanned, column, table)
	}
	image := &RowImage{Own: map[string]json.RawMessage{}, Owned: map[string]map[string]json.RawMessage{}}
	for _, column := range table.Own {
		converted, present, err := value(column)
		if err != nil {
			return nil, err
		}
		if present {
			image.Own[column.Field] = converted
		}
	}
	for _, use := range table.Uses {
		fields := map[string]json.RawMessage{}
		for _, field := range use.Fields {
			converted, present, err := value(field.Column)
			if err != nil {
				return nil, err
			}
			if present {
				fields[field.Name] = converted
			}
		}
		if len(fields) > 0 {
			image.Owned[use.ID] = fields
		}
	}
	return image, nil
}

// imageValue is a member of an image as the dialect's driver scans its
// column, for fromColumn: nil for null. On Postgres jsonb_build_object
// wrote the column as to_jsonb writes it: a JSON column's value is its
// JSON, as the store reads a jsonb cell, and any other is the JSON value
// (a timestamp's ISO 8601 text, a UUID's hyphenated text, a boolean, a
// number of its text, an array). On SQLite json_object wrote the storage
// class: a string is TEXT, an integer INTEGER, any other number REAL, and
// true or false INTEGER 1 or 0.
func imageValue(raw json.RawMessage, column Column, dialect Dialect) (any, error) {
	if isNull(raw) {
		return nil, nil
	}
	if dialect == DialectPostgres && jsonTypes[column.Type] {
		return json.RawMessage(compactText(raw)), nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if dialect != DialectSQLite {
		return value, nil
	}
	switch v := value.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(v.String(), 64)
		if err != nil {
			return nil, err
		}
		return f, nil
	case bool:
		if v {
			return int64(1), nil
		}
		return int64(0), nil
	}
	return value, nil
}

// readLinked is one page of the rows of table whose own field
// request.Field holds request.Key, a soft-deletable table's live rows
// alone, in the key column's order, at most request.Limit, after the key
// request.After when it is set: each row as a row read gives it, with its
// key as the key column reads, and whether more follow. A field the layout
// gives the table no own column of refuses the command as internal.
func readLinked(ctx context.Context, store Store, table *Table, request ReadRequest) (*LinkedRows, error) {
	var column *Column
	for i := range table.Own {
		if table.Own[i].Field == request.Field {
			column = &table.Own[i]
		}
	}
	if column == nil {
		return nil, Internal("%s has no own field %s in the behaviors' layout", table.Type, request.Field)
	}
	dialect := store.Dialect()
	encoded, err := json.Marshal(request.Key)
	if err != nil {
		return nil, fmt.Errorf("rules: encode the key %q: %w", request.Key, err)
	}
	value, err := toColumn(encoded, *column, table, dialect)
	if err != nil {
		return nil, err
	}
	values := []any{value}
	where := fmt.Sprintf("%s = %s%s", quote(column.Column), placeholder(dialect, 1), live(table))
	if request.After != "" {
		after, err := keyValue(table, request.After)
		if err != nil {
			return nil, err
		}
		values = append(values, after)
		where += fmt.Sprintf(" AND %s > %s", quote(table.Key.Column), placeholder(dialect, 2))
	}
	limit := max(request.Limit, 0)
	selected := append([]string{quote(table.Key.Column) + " AS k"}, rowSelect(table)...)
	rows, err := store.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d",
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
	return out, nil
}

// keyOfColumn is a key column's value as the core's key: a UUID's base62
// wire form, a string itself, any other value its JSON text.
func keyOfColumn(table *Table, value any) (string, error) {
	converted, _, err := fromColumn(value, table.Key, table)
	if err != nil {
		return "", err
	}
	var text string
	if json.Unmarshal(converted, &text) == nil {
		return text, nil
	}
	return string(converted), nil
}

// goneStatements are the statements of a row a change command found gone
// (D103), with no fence and no expected count: its deadline messages,
// while no live row of its key is there, and each of its sub-table rows,
// while no row of its key is there at all, so a soft-deleted row keeps
// them, as a soft delete through the core does. Each is harmless when the
// row came back.
func goneStatements(table *Table, dialect Dialect, key string) ([]Statement, error) {
	bound, err := keyValue(table, key)
	if err != nil {
		return nil, err
	}
	p := placeholder(dialect, 1)
	var out []Statement
	if queue := table.Deadlines; queue != nil {
		out = append(out, Statement{
			Text: fmt.Sprintf("DELETE FROM %s WHERE %s = %s AND NOT EXISTS (SELECT 1 FROM %s WHERE %s = %s%s)",
				quote(queue.Table), quote(queue.RowKey), p, quote(table.Table), quote(table.Key.Column), p, live(table)),
			Values: []any{bound},
			Expect: AnyRows,
		})
	}
	for _, use := range table.Uses {
		for _, sub := range use.Tables {
			out = append(out, Statement{
				Text: fmt.Sprintf("DELETE FROM %s WHERE %s = %s AND NOT EXISTS (SELECT 1 FROM %s WHERE %s = %s)",
					quote(sub.Table), quote(sub.Row), p, quote(table.Table), quote(table.Key.Column), p),
				Values: []any{bound},
				Expect: AnyRows,
			})
		}
	}
	return out, nil
}
