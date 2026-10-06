package engine_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// passTableName names the layout's tables and indexes in the pass that runs
// in the runner's transactions, so every statement the adapter builds runs
// under a name function other than the default.
func passTableName(local string) string {
	return "vg_" + local
}

// defaultNames finds each table of the layout under its default name in an
// sql step's statement.
var defaultNames = regexp.MustCompile(`\b` + regexp.QuoteMeta(sqlite.DefaultTableName("")) + `(` + strings.Join(sqlite.Tables(), "|") + `)\b`)

// newSQLiteRunner opens a file of the scenario's own through
// modernc.org/sqlite, creates the SQLite adapter's layout in it, and builds
// the adapter, graph recipe, and the engine at the fixture's schema epoch
// and snapshot interval. The pool pass binds the database/sql pool, on
// which the adapter runs transactions of its own. The other binds one
// connection, holds a database/sql transaction (BEGIN IMMEDIATE) for each of
// the adapter's, which runs inside it through DBTx, and names the layout
// with passTableName.
func newSQLiteRunner(t *testing.T, descriptor json.RawMessage, pass sqlitePass) *runner {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scenario.sqlite")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	opts := sqlite.Options{Graph: sqliteGraph}
	var client sqlite.Client = sqlite.DB(db)
	var q sqlQuerier = db
	statement := func(sql string) string { return sql }
	if pass.callerTransaction {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			t.Fatal(err)
		}
		opts.TableName = passTableName
		client, q = callerTransaction{conn: conn}, conn
		statement = func(sql string) string { return defaultNames.ReplaceAllString(sql, passTableName("${1}")) }
	}
	adapter, err := sqlite.New(descriptor, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateTables(ctx, client); err != nil {
		t.Fatal(err)
	}
	store, err := adapter.Storage(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(descriptor, store, engine.Options{SchemaEpoch: fixtureSchemaEpoch, SnapshotEvery: fixtureSnapshotEvery})
	if err != nil {
		t.Fatal(err)
	}
	return &runner{
		backend: sqliteBackend, ctx: ctx, descriptor: descriptor, store: store, engine: eng,
		refs: map[string]storage.Ref{}, commits: map[string]storage.Commit{}, releases: map[string]storage.Release{},
		sqlite: q, statement: statement,
	}
}

// callerTransaction is a client that holds each of the adapter's
// transactions as a caller would: it begins a database/sql transaction on
// its connection, runs the adapter's inside it through DBTx, a savepoint,
// and commits when that succeeds and rolls back when it fails.
type callerTransaction struct {
	conn *sql.Conn
}

func (c callerTransaction) Transact(ctx context.Context, fn func(ctx context.Context, conn sqlite.Conn) error) error {
	tx, err := c.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rolls back too when fn does not return, as when a step fails the
	// test inside a transaction.
	defer func() { _ = tx.Rollback() }()
	if err := sqlite.DBTx(tx).Transact(ctx, fn); err != nil {
		return err
	}
	return tx.Commit()
}

// sqliteSQL runs an sql step's statement on the scenario's file. With rows
// it returns the statement's rows in the order it returns them, each a JSON
// object of its columns read as text: a column that is not text or NULL is
// refused, so the statement casts what it selects, as on Postgres every
// column reads as text.
func (r *runner) sqliteSQL(ctx context.Context, statement string, args []any, rows bool) ([]json.RawMessage, error) {
	statement = r.statement(statement)
	if !rows {
		_, err := r.sqlite.ExecContext(ctx, statement, args...)
		return nil, err
	}
	result, err := r.sqlite.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = result.Close() }()
	columns, err := result.Columns()
	if err != nil {
		return nil, err
	}
	var out []json.RawMessage
	for result.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := result.Scan(dest...); err != nil {
			return nil, err
		}
		row := map[string]*string{}
		for i, column := range columns {
			switch v := values[i].(type) {
			case nil:
				row[column] = nil
			case string:
				row[column] = &v
			default:
				return nil, fmt.Errorf("column %s is %T, not text: cast it in the statement", column, v)
			}
		}
		text, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, result.Err()
}
