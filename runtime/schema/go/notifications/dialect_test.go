package notifications

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

type sqliteStatements struct{}

func (sqliteStatements) Dialect() string { return DialectSQLite }

// TestStatementIsWrittenInItsDialect: a statement on Postgres is as
// written; on SQLite each $n is ?n, a time its RFC 3339 text to the
// millisecond, a boolean 1 or 0, a list and a map their JSON text, and a
// list condition reads its elements through json_each (#618).
func TestStatementIsWrittenInItsDialect(t *testing.T) {
	at := time.Date(2026, 3, 8, 9, 30, 0, 123456789, time.FixedZone("x", 3600))
	args := []any{"a", at, true, []string{"x", "y"}, map[string]any{"k": 1}, nil, int64(4)}
	sql, out := Statement(struct{}{}, "SELECT 1 WHERE a = $1 AND b > $2 AND c = $10", args)
	if sql != "SELECT 1 WHERE a = $1 AND b > $2 AND c = $10" || !reflect.DeepEqual(out, args) {
		t.Fatalf("on Postgres: %s %v", sql, out)
	}
	sql, out = Statement(sqliteStatements{}, "SELECT 1 WHERE a = $1 AND b > $2 AND c = $10", args)
	if sql != "SELECT 1 WHERE a = ?1 AND b > ?2 AND c = ?10" {
		t.Fatalf("on SQLite: %s", sql)
	}
	want := []any{"a", "2026-03-08T08:30:00.123Z", int64(1), `["x","y"]`, `{"k":1}`, nil, int64(4)}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("on SQLite the values: %#v, want %#v", out, want)
	}
	if got := InList(false, `"state"`, "$3"); got != `"state" = ANY($3)` {
		t.Errorf("a list on Postgres: %s", got)
	}
	if got := InList(true, `"state"`, "$3"); got != `"state" IN (SELECT value FROM json_each($3))` {
		t.Errorf("a list on SQLite: %s", got)
	}
}

// TestSQLiteColumnsReadAsPostgresReadsThem: a value bound to a column on
// SQLite is as its column holds it, and a cell SQLite returns reads as the
// Postgres driver reads that column (#618).
func TestSQLiteColumnsReadAsPostgresReadsThem(t *testing.T) {
	id := "6f1d2a3b-4c5d-4e6f-8a9b-0c1d2e3f4a5b"
	base62 := rowrefs.UUIDFromDB(id)
	if got := SQLiteArg(Column{SQLite: SQLiteJSON}, map[string]any{"a": true}); got != `{"a":true}` {
		t.Errorf("a JSON value: %#v", got)
	}
	if got := SQLiteArg(Column{SQLite: SQLiteJSON}, `[{"path":"owner"}]`); got != `[{"path":"owner"}]` {
		t.Errorf("JSON text: %#v", got)
	}
	if got := SQLiteArg(Column{SQLite: SQLiteJSON}, true); got != "true" {
		t.Errorf("a JSON boolean: %#v", got)
	}
	if got := SQLiteArg(Column{SQLite: SQLiteUUID}, base62); got != id {
		t.Errorf("a relation's key: %#v", got)
	}
	if got := SQLiteArg(Column{SQLite: SQLiteBool}, false); got != int64(0) {
		t.Errorf("a boolean: %#v", got)
	}
	for _, c := range []struct {
		column Column
		cell   any
		want   any
	}{
		{Column{SQLite: SQLiteJSON}, `{"title":"Low"}`, map[string]any{"title": "Low"}},
		{Column{SQLite: SQLiteJSON}, []byte(`["sms","email"]`), []any{"sms", "email"}},
		{Column{SQLite: SQLiteUUIDs}, `["` + id + `"]`, []any{base62}},
		{Column{SQLite: SQLiteTime}, "2026-03-08T08:30:00.123Z", time.Date(2026, 3, 8, 8, 30, 0, 123000000, time.UTC)},
		{Column{SQLite: SQLiteBool}, int64(1), true},
		{Column{UUID: true}, id, base62},
		{Column{SQLite: SQLiteUUID}, id, base62},
		{Column{}, int64(7), int64(7)},
		{Column{}, []byte("text"), "text"},
		{Column{SQLite: SQLiteTime}, nil, nil},
	} {
		got := SQLiteCell(c.column, c.cell)
		if !reflect.DeepEqual(got, c.want) {
			gotJSON, _ := json.Marshal(got)
			t.Errorf("%+v of %#v: %s (%#v), want %#v", c.column, c.cell, gotJSON, got, c.want)
		}
	}
}
