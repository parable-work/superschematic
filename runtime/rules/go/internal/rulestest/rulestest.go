// Package rulestest gives the behaviors' adapter's tests a database of
// each dialect (D103): a SQLite file of the test's own through
// modernc.org/sqlite, and a schema of the test's own on the Postgres
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL names, dropped when the test
// ends; a transaction of each as a rules.Store; raw SQL past the adapter;
// and the DDL a table that composes a behavior takes, with @optimistic's
// version trigger. Only tests import it.
package rulestest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	rulespostgres "github.com/parable-work/superschematic/runtime/rules/go/postgres"
	rulessqlite "github.com/parable-work/superschematic/runtime/rules/go/sqlite"
)

// DatabaseVariable names the Postgres the adapter's tests run against;
// they skip their Postgres cases without it.
const DatabaseVariable = "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL"

// SkipMessage is what a Postgres case says when it skips.
const SkipMessage = "set " + DatabaseVariable + " to run the behaviors' adapter on Postgres"

// DB is a database of a dialect a test runs on.
type DB struct {
	// Name is the dialect's name, as a subtest names it: SQLite or Postgres.
	Name    string
	Dialect rules.Dialect
	// Schema is the Postgres schema of the test's own; "" on SQLite.
	Schema string

	sqlite *sql.DB
	pool   *pgxpool.Pool
}

var counter atomic.Int64

// SQLite opens a SQLite file under the test's temporary directory, closed
// when the test ends, its transactions begun IMMEDIATE, as the SQLite
// store asks (_txlock=immediate): a transaction holds SQLite's one writer
// from its start, and another's BEGIN waits for it.
func SQLite(t testing.TB) *DB {
	t.Helper()
	return openSQLite(t, "_txlock=immediate")
}

// SQLiteDeferred opens a SQLite file as SQLite does but in WAL mode, its
// transactions begun DEFERRED, database/sql's default: a transaction that
// misses the store's requirement, whose snapshot another connection's
// commit can move past before its unit writes.
func SQLiteDeferred(t testing.TB) *DB {
	t.Helper()
	return openSQLite(t, "_txlock=deferred&_pragma=journal_mode(WAL)")
}

func openSQLite(t testing.TB, params string) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&"+params)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &DB{Name: "SQLite", Dialect: rules.DialectSQLite, sqlite: db}
}

// Postgres creates a schema of the test's own on the Postgres
// DatabaseVariable names, every connection of its pool searching it, and
// drops it when the test ends; it skips the test without the variable.
func Postgres(t testing.TB) *DB {
	t.Helper()
	dsn := os.Getenv(DatabaseVariable)
	if dsn == "" {
		t.Skip(SkipMessage)
	}
	ctx := context.Background()
	schema := fmt.Sprintf("rules_go_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), counter.Add(1))
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create schema %s: %v", schema, err)
	}
	_ = admin.Close(ctx)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the database URL: %v", err)
	}
	config.MaxConns = 8
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open a pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Errorf("connect to drop schema %s: %v", schema, err)
			return
		}
		defer func() { _ = admin.Close(ctx) }()
		if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	return &DB{Name: "Postgres", Dialect: rules.DialectPostgres, Schema: schema, pool: pool}
}

// Each runs fn as a subtest on SQLite and on Postgres, the Postgres case
// skipped without DatabaseVariable.
func Each(t *testing.T, fn func(t *testing.T, db *DB)) {
	t.Helper()
	t.Run("SQLite", func(t *testing.T) { fn(t, SQLite(t)) })
	t.Run("Postgres", func(t *testing.T) { fn(t, Postgres(t)) })
}

// P is the nth placeholder in the database's dialect.
func (db *DB) P(n int) string {
	if db.Dialect == rules.DialectPostgres {
		return fmt.Sprintf("$%d", n)
	}
	return fmt.Sprintf("?%d", n)
}

// Exec runs DDL or a raw write past the adapter, committed at once, and
// returns the rows it changed.
func (db *DB) Exec(t testing.TB, text string, values ...any) int64 {
	t.Helper()
	n, err := db.TryExec(text, values...)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return n
}

// TryExec is Exec returning its error.
func (db *DB) TryExec(text string, values ...any) (int64, error) {
	ctx := context.Background()
	if db.pool != nil {
		tag, err := db.pool.Exec(ctx, text, values...)
		if err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	}
	result, err := db.sqlite.ExecContext(ctx, text, values...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Rows reads rows past the adapter: each row's values in select order.
func (db *DB) Rows(t testing.TB, text string, values ...any) [][]any {
	t.Helper()
	ctx := context.Background()
	var out [][]any
	if db.pool != nil {
		rows, err := db.pool.Query(ctx, text, values...)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		defer rows.Close()
		for rows.Next() {
			row, err := rows.Values()
			if err != nil {
				t.Fatalf("%s: %v", text, err)
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		return out
	}
	rows, err := db.sqlite.QueryContext(ctx, text, values...)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	for rows.Next() {
		row := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range row {
			targets[i] = &row[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return out
}

// Tx is a transaction a test runs a store over: its store, and raw SQL in
// the same transaction.
type Tx struct {
	db  *DB
	pg  pgx.Tx
	sql *sql.Tx
}

// Store is a rules.Store over the transaction.
func (tx *Tx) Store() rules.Store {
	if tx.pg != nil {
		return rulespostgres.NewStore(tx.pg)
	}
	return rulessqlite.NewStore(tx.sql)
}

// Postgres is the transaction on Postgres; nil on SQLite.
func (tx *Tx) Postgres() pgx.Tx { return tx.pg }

// SQLite is the transaction on SQLite; nil on Postgres.
func (tx *Tx) SQLite() *sql.Tx { return tx.sql }

// Exec runs a write in the transaction and returns the rows it changed.
func (tx *Tx) Exec(ctx context.Context, text string, values ...any) (int64, error) {
	if tx.pg != nil {
		tag, err := tx.pg.Exec(ctx, text, values...)
		if err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	}
	result, err := tx.sql.ExecContext(ctx, text, values...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// InTx runs fn in a transaction of its own, committed when fn returns nil
// and rolled back when it returns an error, which InTx returns.
func (db *DB) InTx(ctx context.Context, fn func(tx *Tx) error) error {
	if db.pool != nil {
		pg, err := db.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if err := fn(&Tx{db: db, pg: pg}); err != nil {
			return errors.Join(err, ignoreClosed(pg.Rollback(ctx)))
		}
		return pg.Commit(ctx)
	}
	tx, err := db.sqlite.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Tx{db: db, sql: tx}); err != nil {
		return errors.Join(err, ignoreClosed(tx.Rollback()))
	}
	return tx.Commit()
}

func ignoreClosed(err error) error {
	if errors.Is(err, pgx.ErrTxClosed) || errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

// Types are the SQL types of a test table's columns in a dialect.
type Types struct {
	Key, Text, Timestamp, Version, Number, Boolean, JSON string
}

// ColumnTypes is the SQL types of a dialect, as sqlgen writes them for the
// tests' tables.
func (db *DB) ColumnTypes() Types {
	if db.Dialect == rules.DialectPostgres {
		return Types{Key: "TEXT PRIMARY KEY", Text: "TEXT", Timestamp: "TIMESTAMPTZ", Version: "BIGINT DEFAULT 1 NOT NULL", Number: "DOUBLE PRECISION", Boolean: "BOOLEAN", JSON: "JSONB"}
	}
	return Types{Key: "TEXT PRIMARY KEY", Text: "TEXT", Timestamp: "TEXT", Version: "INTEGER DEFAULT 1 NOT NULL", Number: "REAL", Boolean: "INTEGER", JSON: "TEXT"}
}

// BumpVersion is @optimistic's trigger, which bumps _version on every
// update of a table: on Postgres a BEFORE UPDATE trigger (sqlgen's), on
// SQLite an AFTER UPDATE trigger that runs only when the update left the
// version as it was (sqlmigrate's), so a write that moves it itself, as
// the adapter's do, moves it once.
func (db *DB) BumpVersion(table string) []string {
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	if db.Dialect == rules.DialectPostgres {
		return []string{
			fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s_bump_version() RETURNS trigger AS $$
BEGIN
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`, table),
			fmt.Sprintf("CREATE TRIGGER trg_%s_bump_version BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION %s_bump_version()", table, quoted, table),
		}
	}
	return []string{
		fmt.Sprintf(`CREATE TRIGGER "trg_%s_bump_version" AFTER UPDATE ON %s FOR EACH ROW WHEN NEW._version = OLD._version BEGIN UPDATE %s SET _version = OLD._version + 1 WHERE rowid = NEW.rowid; END`, table, quoted, quoted),
	}
}

// Timestamp is an instant as the dialect binds a timestamp column's value:
// a time.Time on Postgres, Date.prototype.toISOString's text on SQLite.
func (db *DB) Timestamp(t time.Time) any {
	if db.Dialect == rules.DialectPostgres {
		return t.UTC()
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
