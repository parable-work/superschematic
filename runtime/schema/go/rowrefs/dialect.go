package rowrefs

import (
	"context"
	"regexp"
	"time"
)

// The dialects a Host's statements run in (D182, #611). A Host runs
// Postgres's unless it is a DialectHost that says otherwise.
const (
	DialectPostgres = "postgres"
	DialectSQLite   = "sqlite"
)

// DialectHost is a Host that names the dialect its statements run in: the
// generated ORM's SQLite database and its transactions answer
// DialectSQLite. References and Attachments then write each statement in
// SQLite's words, as the TypeScript runtime's rowfeatures.ts does (sqlOf):
// a parameter ?n, a cast CAST(... AS TEXT), the database's clock as RFC
// 3339 text to the millisecond, a ref block found through json_each, a
// time bound as that text, and no row or advisory lock, since a
// transaction on SQLite holds the database's one write lock from its BEGIN
// IMMEDIATE.
type DialectHost interface {
	Host
	Dialect() string
}

// sqlWords are the words of a statement that differ between Postgres and
// SQLite.
type sqlWords struct {
	sqlite bool
}

// wordsOf is the words of h's dialect.
func wordsOf(h Host) sqlWords {
	if d, ok := h.(DialectHost); ok && d.Dialect() == DialectSQLite {
		return sqlWords{sqlite: true}
	}
	return sqlWords{}
}

// text is expr as text, as a key or an id compares.
func (w sqlWords) text(expr string) string {
	if w.sqlite {
		return "CAST(" + expr + " AS TEXT)"
	}
	return expr + "::text"
}

// int is expr as an integer, as a count reads.
func (w sqlWords) int(expr string) string {
	if w.sqlite {
		return "CAST(" + expr + " AS INTEGER)"
	}
	return expr + "::int"
}

// bigint is expr as a 64-bit integer, as a message's id reads.
func (w sqlWords) bigint(expr string) string {
	if w.sqlite {
		return "CAST(" + expr + " AS INTEGER)"
	}
	return expr + "::bigint"
}

// now is the database's clock: on SQLite the RFC 3339 text to the
// millisecond its schema's defaults write, which orders as text.
func (w sqlWords) now() string {
	if w.sqlite {
		return "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"
	}
	return "now()"
}

// forShare is a share lock of the rows a read reads, none on SQLite.
func (w sqlWords) forShare() string {
	if w.sqlite {
		return ""
	}
	return " FOR SHARE"
}

// locks reports whether a statement may take an advisory lock: on SQLite
// the transaction holds the database's write lock already.
func (w sqlWords) locks() bool {
	return !w.sqlite
}

// sqliteTimeText is a time as SQLite holds it, RFC 3339 in UTC to the
// millisecond, the form its schema's defaults write.
func sqliteTimeText(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

var postgresParameter = regexp.MustCompile(`\$(\d+)`)

// sqliteHost is a Host whose statements are written in Postgres's
// parameters and run on SQLite: each $n becomes ?n, and each time it binds
// its text.
type sqliteHost struct {
	Host
}

// inDialect is h with its statements written in its dialect's words: h
// itself on Postgres, and on SQLite a host that numbers each parameter ?n
// and binds a time as its text.
func inDialect(h Host) Host {
	if h == nil || !wordsOf(h).sqlite {
		return h
	}
	if _, ok := h.(sqliteHost); ok {
		return h
	}
	return sqliteHost{Host: h}
}

func (h sqliteHost) Dialect() string { return DialectSQLite }

func sqliteArgs(args []any) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case time.Time:
			out[i] = sqliteTimeText(v)
		case *time.Time:
			if v == nil {
				out[i] = nil
			} else {
				out[i] = sqliteTimeText(*v)
			}
		default:
			out[i] = arg
		}
	}
	return out
}

func (h sqliteHost) Query(ctx context.Context, sql string, args ...any) ([]Row, error) {
	return h.Host.Query(ctx, postgresParameter.ReplaceAllString(sql, "?$1"), sqliteArgs(args)...)
}

func (h sqliteHost) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	return h.Host.Exec(ctx, postgresParameter.ReplaceAllString(sql, "?$1"), sqliteArgs(args)...)
}

func (h sqliteHost) Transaction(ctx context.Context, fn func(Host) error) error {
	return h.Host.Transaction(ctx, func(inner Host) error { return fn(inDialect(inner)) })
}
