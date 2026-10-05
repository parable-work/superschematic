// Package testdb gives each of the runner's tests a database of its own:
// a SQLite file in a temporary directory, a Postgres database created on
// the server SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL names and dropped when
// the test ends, or a D1 database a fake D1 server (package d1fake) serves
// over a SQLite file. Only tests import it.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/d1"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/d1fake"
	"github.com/parable-work/superschematic/runtime/migrate/go/sqlite"
)

// EnvURL names the Postgres server the tests run against. Its role must be
// able to create databases.
const EnvURL = "SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL"

var counter atomic.Int64

// Backend is a database the tests run on.
type Backend string

const (
	// SQLite is a SQLite file.
	SQLite Backend = "sqlite"
	// Postgres is a database on the server EnvURL names.
	Postgres Backend = "postgres"
	// D1 is a D1 database a fake D1 server serves.
	D1 Backend = "d1"
)

// Backends are the backends the tests run on.
var Backends = []Backend{SQLite, Postgres, D1}

// Dialect is the dialect of the plans the backend runs.
func (b Backend) Dialect() migrate.Dialect {
	if b == Postgres {
		return migrate.Postgres
	}
	return migrate.SQLite
}

// New returns the URL of a new, empty database on backend. A Postgres test
// skips when EnvURL is unset.
func New(t testing.TB, backend Backend) string {
	t.Helper()
	switch backend {
	case Postgres:
		return NewPostgres(t)
	case D1:
		return NewD1(t)
	}
	return NewSQLite(t)
}

// D1Token is the API token every fake D1 server takes.
const D1Token = "fake-d1-token"

var (
	d1Mu      sync.Mutex
	d1Servers = map[string]*d1fake.Server{}
)

// NewD1 starts a fake D1 server over a new SQLite file, stops it when the
// test ends, and returns its database's d1:// URL. D1Options reaches it.
func NewD1(t testing.TB) string {
	t.Helper()
	server, err := d1fake.New(filepath.Join(t.TempDir(), "d1.db"), d1fake.Options{
		Token:      D1Token,
		AccountID:  "fakeaccount",
		DatabaseID: fmt.Sprintf("database-%d", counter.Add(1)),
	})
	if err != nil {
		t.Fatal(err)
	}
	dbURL := server.DatabaseURL()
	d1Mu.Lock()
	d1Servers[dbURL] = server
	d1Mu.Unlock()
	t.Cleanup(func() {
		d1Mu.Lock()
		delete(d1Servers, dbURL)
		d1Mu.Unlock()
		server.Close()
	})
	return dbURL
}

func d1Server(t testing.TB, dbURL string) *d1fake.Server {
	t.Helper()
	d1Mu.Lock()
	defer d1Mu.Unlock()
	server, ok := d1Servers[dbURL]
	if !ok {
		t.Fatalf("no fake D1 server serves %s", dbURL)
	}
	return server
}

// D1Options are the options of a D1 driver that reaches the fake server of
// dbURL: its base URL, its client and its token. Lock tries a held lease
// again every 10ms.
func D1Options(t testing.TB, dbURL string) d1.Options {
	t.Helper()
	server := d1Server(t, dbURL)
	return d1.Options{
		Token:      D1Token,
		BaseURL:    server.BaseURL(),
		HTTPClient: server.Client(),
		LockPoll:   10 * time.Millisecond,
	}
}

// sqliteName is the name database/sql opens for a SQLite URL, or for the
// file behind a fake D1 database.
func sqliteName(t testing.TB, dbURL string) string {
	t.Helper()
	if d1.IsURL(dbURL) {
		return "file:" + d1Server(t, dbURL).Path() + "?_pragma=busy_timeout(5000)"
	}
	return sqlite.DSN(dbURL)
}

// NewSQLite returns the path of a database file that does not exist yet.
func NewSQLite(t testing.TB) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

// NewPostgres creates a database on the server EnvURL names, drops it when
// the test ends, and returns its URL. It skips the test when EnvURL is
// unset.
func NewPostgres(t testing.TB) string {
	t.Helper()
	server := os.Getenv(EnvURL)
	if server == "" {
		t.Skipf("set %s to run the runner against Postgres", EnvURL)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, server)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvURL, err)
	}
	defer func() { _ = admin.Close(ctx) }()
	name := fmt.Sprintf("migrate_test_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), counter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		admin, err := pgx.Connect(ctx, server)
		if err != nil {
			t.Errorf("connect to drop database %s: %v", name, err)
			return
		}
		defer func() { _ = admin.Close(ctx) }()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	u, err := url.Parse(server)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// SQLiteDB opens a database/sql handle on a SQLite database, or on the file
// behind a fake D1 database, and closes it when the test ends. Its
// connections have foreign keys off, as SQLite's connections do unless
// asked.
func SQLiteDB(t testing.TB, dbURL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteName(t, dbURL))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// PostgresConn opens a connection to a Postgres database and closes it when
// the test ends.
func PostgresConn(t testing.TB, dbURL string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// Exec runs statements on url, each on its own. On D1 it runs them on the
// fake's file directly, with foreign keys off.
func Exec(t testing.TB, dbURL string, statements ...string) {
	t.Helper()
	ctx := context.Background()
	if migrate.URLDialect(dbURL) == migrate.Postgres {
		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		for _, statement := range statements {
			if _, err := conn.Exec(ctx, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
		return
	}
	db, err := sql.Open("sqlite", sqliteName(t, dbURL))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// Strings runs a query on url and returns its first column as text; NULL
// is "".
func Strings(t testing.TB, dbURL, query string, args ...any) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	if migrate.URLDialect(dbURL) == migrate.Postgres {
		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		rows, err := conn.Query(ctx, query, args...)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer rows.Close()
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			if values[0] == nil {
				out = append(out, "")
			} else {
				out = append(out, fmt.Sprint(values[0]))
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	db, err := sql.Open("sqlite", sqliteName(t, dbURL))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		out = append(out, value.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

// Catalog returns the schema of the database at url, without the runner's
// state tables, as sorted lines: what two databases must agree on to hold
// the same schema.
func Catalog(t testing.TB, dbURL string) []string {
	t.Helper()
	query := sqliteCatalog
	if migrate.URLDialect(dbURL) == migrate.Postgres {
		query = postgresCatalog
	}
	lines := Strings(t, dbURL, query)
	sort.Strings(lines)
	return lines
}

const sqliteCatalog = `SELECT type || ' ' || name || ' on ' || tbl_name || ': ' || COALESCE(sql, '')
FROM sqlite_master
WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' AND name NOT LIKE 'superschematic\_%' ESCAPE '\'`

// postgresCatalog reads every object a plan makes, by name and definition,
// in every schema but the system's.
const postgresCatalog = `WITH ns AS (
  SELECT oid, nspname FROM pg_namespace
  WHERE nspname NOT IN ('pg_catalog', 'information_schema') AND nspname NOT LIKE 'pg\_toast%' AND nspname NOT LIKE 'pg\_temp%'
), rel AS (
  SELECT c.oid, ns.nspname, c.relname, c.relkind FROM pg_class c JOIN ns ON ns.oid = c.relnamespace
  WHERE c.relname NOT LIKE 'superschematic\_%'
)
SELECT 'schema ' || nspname FROM ns
UNION ALL
SELECT 'extension ' || extname FROM pg_extension
UNION ALL
SELECT 'relation ' || nspname || '.' || relname || ' kind ' || relkind::text FROM rel WHERE relkind IN ('r', 'p', 'v', 'm', 'S')
UNION ALL
SELECT 'column ' || rel.nspname || '.' || rel.relname || '.' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
  || CASE WHEN a.attnotnull THEN ' not null' ELSE '' END
  || COALESCE(' default ' || pg_get_expr(d.adbin, d.adrelid), '')
  || CASE WHEN a.attgenerated <> '' THEN ' generated' ELSE '' END
FROM pg_attribute a JOIN rel ON rel.oid = a.attrelid
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped AND rel.relkind IN ('r', 'p', 'v', 'm')
UNION ALL
SELECT 'constraint ' || rel.nspname || '.' || rel.relname || ' ' || con.conname || ' ' || pg_get_constraintdef(con.oid)
  || CASE WHEN con.convalidated THEN '' ELSE ' not valid' END
FROM pg_constraint con JOIN rel ON rel.oid = con.conrelid
UNION ALL
SELECT 'index ' || pg_get_indexdef(i.indexrelid) || CASE WHEN i.indisvalid THEN '' ELSE ' invalid' END
FROM pg_index i JOIN rel ON rel.oid = i.indrelid
UNION ALL
SELECT 'view ' || schemaname || '.' || viewname || ' ' || definition FROM pg_views
WHERE schemaname IN (SELECT nspname FROM ns)
UNION ALL
SELECT 'function ' || pg_get_functiondef(p.oid) FROM pg_proc p JOIN ns ON ns.oid = p.pronamespace
WHERE p.prokind IN ('f', 'p') AND NOT EXISTS (SELECT 1 FROM pg_depend dep WHERE dep.objid = p.oid AND dep.deptype = 'e')
UNION ALL
SELECT 'trigger ' || pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN rel ON rel.oid = t.tgrelid WHERE NOT t.tgisinternal
UNION ALL
SELECT 'comment ' || rel.nspname || '.' || rel.relname || ' ' || ds.objsubid::text || ' ' || ds.description
FROM pg_description ds JOIN rel ON rel.oid = ds.objoid WHERE ds.classoid = 'pg_class'::regclass`
