package crosstable

import (
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// The dialects a Host's statements run in (D186, #615), rowrefs' own: a
// Host runs Postgres's unless it is a DialectHost that says otherwise.
const (
	DialectPostgres = rowrefs.DialectPostgres
	DialectSQLite   = rowrefs.DialectSQLite
)

// DialectHost is a Host that names the dialect its statements run in: the
// generated ORM's SQLite database and its transactions answer
// DialectSQLite. The calendar and search then write each statement in
// SQLite's words, as the TypeScript runtime's calendar.ts and
// search-tables.ts do on SQLite and D1 (D186): a parameter ?n numbered by
// its place among the statement's values, a cast CAST(... AS TEXT), a time
// bound as the RFC 3339 text to the millisecond its schema's defaults
// write, a list of keys as one parameter each, and the search index's
// words an FTS5 table (Searchable.FTS) ranked by bm25. Its Conditions are
// the repositories' conditions on SQLite, their placeholders ?n by their
// place in args.
type DialectHost interface {
	Host
	Dialect() string
}

// onSQLite reports whether h's statements are SQLite's.
func onSQLite(h Host) bool {
	d, ok := h.(DialectHost)
	return ok && d.Dialect() == DialectSQLite
}

// sqliteTime is a time as SQLite holds it, RFC 3339 in UTC to the
// millisecond, which orders as text (D182).
func sqliteTime(t time.Time) string {
	return iso(t)
}

// sqliteNow is the database's clock on SQLite, as its schema's defaults
// write it.
const sqliteNow = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"
