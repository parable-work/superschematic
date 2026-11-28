package notifications

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// The dialects a Host's statements run in (#618, as D189), rowrefs' own: a
// Host runs Postgres's unless it is a DialectHost that says otherwise.
const (
	DialectPostgres = rowrefs.DialectPostgres
	DialectSQLite   = rowrefs.DialectSQLite
)

// DialectHost is a Host that names the dialect its statements run in: the
// generated ORM's host over a SQLite database, or a transaction of it,
// answers DialectSQLite. The notifier, the inbound path and the dispatcher
// then write each statement over their own tables in SQLite's words, as
// the TypeScript runtime's untyped repositories do on SQLite and D1
// (D189): a parameter ?n, a list bound as its JSON text and read through
// json_each, a time bound as the RFC 3339 text to the millisecond its
// schema's defaults write, a boolean 1 or 0, and a JSON value its text;
// each cell read back as the Postgres driver reads its column
// (SQLiteCell), by how its Column says SQLite holds it.
type DialectHost interface {
	Dialect() string
}

// OnSQLite reports whether h, a Host or a host's statements, runs on
// SQLite.
func OnSQLite(h any) bool {
	d, ok := h.(DialectHost)
	return ok && d.Dialect() == DialectSQLite
}

// How a SQLite column holds a column's value (Column.SQLite): a JSON value
// or a list of anything but UUIDs as its JSON text, a list of UUIDs as the
// JSON text of their hyphenated forms, a date-time as RFC 3339 text in UTC
// to the millisecond, a boolean as 1 or 0, and a UUID that Column.UUID
// does not mark (a to-one relation's column) as its hyphenated text. A
// column of none of these holds its value as Postgres does: text, a
// number, or a UUID Column.UUID marks, as its hyphenated text.
const (
	SQLiteJSON  = "json"
	SQLiteUUIDs = "uuids"
	SQLiteTime  = "time"
	SQLiteBool  = "bool"
	SQLiteUUID  = "uuid"
)

var postgresParameter = regexp.MustCompile(`\$(\d+)`)

// Statement is sql and args as h runs them: as written on Postgres, and on
// SQLite with each $n numbered ?n and each value bound as SQLiteValue
// writes it.
func Statement(h any, sql string, args []any) (string, []any) {
	if !OnSQLite(h) {
		return sql, args
	}
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = SQLiteValue(arg)
	}
	return postgresParameter.ReplaceAllString(sql, "?$1"), out
}

// InList is the condition that column is one of the list bound as the
// parameter placeholder: = ANY on Postgres, and on SQLite one of the
// elements of the JSON text the list binds as (SQLiteValue).
func InList(sqlite bool, column, placeholder string) string {
	if sqlite {
		return column + " IN (SELECT value FROM json_each(" + placeholder + "))"
	}
	return column + " = ANY(" + placeholder + ")"
}

// SQLiteTimeText is a time as SQLite holds it, RFC 3339 in UTC to the
// millisecond, the form its schema's defaults write, which orders as text.
func SQLiteTimeText(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// SQLiteValue is v as a parameter of a statement on SQLite: a time its
// text (SQLiteTimeText), a boolean 1 or 0, JSON text as itself, and a
// list, a map or a struct its JSON text; text, a number and nil as they
// are.
func SQLiteValue(v any) any {
	switch value := v.(type) {
	case nil, string, int, int32, int64, float64:
		return v
	case time.Time:
		return SQLiteTimeText(value)
	case *time.Time:
		if value == nil {
			return nil
		}
		return SQLiteTimeText(*value)
	case bool:
		if value {
			return int64(1)
		}
		return int64(0)
	case json.RawMessage:
		if value == nil {
			return nil
		}
		return string(value)
	case []byte:
		return v
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return SQLiteValue(rv.Elem().Interface())
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		if (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.IsNil() {
			return nil
		}
		data, err := json.Marshal(v)
		if err != nil {
			return v
		}
		return string(data)
	}
	return v
}

// SQLiteArg is v as the column c holds it on SQLite: a JSON column's value
// its JSON text, a string as itself, which is JSON text already, as
// Postgres reads a text parameter of a jsonb column; any other as
// SQLiteValue writes it.
func SQLiteArg(c Column, v any) any {
	switch c.SQLite {
	case SQLiteJSON, SQLiteUUIDs:
		switch value := v.(type) {
		case nil, string:
			return v
		case json.RawMessage:
			if value == nil {
				return nil
			}
			return string(value)
		}
		rv := reflect.ValueOf(v)
		if (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map || rv.Kind() == reflect.Pointer) && rv.IsNil() {
			return nil
		}
		data, err := json.Marshal(v)
		if err != nil {
			return v
		}
		return string(data)
	case SQLiteUUID:
		if s, ok := v.(string); ok {
			if s == "" {
				return nil
			}
			if id := rowrefs.UUIDToDB(s); id != "" {
				return id
			}
		}
	}
	return SQLiteValue(v)
}

// SQLiteCell is cell, what SQLite returned for the column c, as the
// Postgres driver reads that column: a JSON column's text decoded, a list
// of UUIDs decoded with each in base62, a date-time a time.Time, a boolean
// true or false, a UUID in base62, and text a string, which SQLite may
// return as bytes.
func SQLiteCell(c Column, cell any) any {
	if raw, ok := cell.([]byte); ok {
		cell = string(raw)
	}
	if cell == nil {
		return nil
	}
	switch c.SQLite {
	case SQLiteJSON, SQLiteUUIDs:
		text, ok := cell.(string)
		if !ok {
			return cell
		}
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
		if decoder.Decode(&decoded) != nil {
			return cell
		}
		if list, ok := decoded.([]any); ok && c.SQLite == SQLiteUUIDs {
			for i, item := range list {
				if s, ok := item.(string); ok {
					list[i] = rowrefs.UUIDFromDB(s)
				}
			}
		}
		return decoded
	case SQLiteTime:
		if text, ok := cell.(string); ok {
			if at, err := time.Parse(time.RFC3339Nano, text); err == nil {
				return at.UTC()
			}
		}
		return cell
	case SQLiteBool:
		switch value := cell.(type) {
		case int64:
			return value != 0
		case string:
			b, err := strconv.ParseBool(value)
			if err == nil {
				return b
			}
		}
		return cell
	}
	if c.UUID || c.SQLite == SQLiteUUID {
		if text, ok := cell.(string); ok {
			return rowrefs.UUIDFromDB(text)
		}
	}
	return cell
}
