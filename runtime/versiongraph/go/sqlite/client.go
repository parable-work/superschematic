package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

// Client runs the adapter's statements. The adapter asks it for one
// transaction per engine operation. DB, DBConn and DBTx bind database/sql;
// another driver implements the two interfaces itself.
//
// On a connection of its own, a client turns the connection's foreign keys
// on and begins each transaction with BEGIN IMMEDIATE, which takes the
// file's write lock at once, and a transaction begun with the context
// another's function was given is a savepoint inside it, on the same Conn.
// Inside a transaction the caller holds, each transaction is a savepoint.
type Client interface {
	// Transact runs fn in one transaction. It commits when fn returns nil
	// and rolls back otherwise, returning fn's error.
	Transact(ctx context.Context, fn func(ctx context.Context, conn Conn) error) error
}

// Conn runs statements inside one transaction. A statement numbers its
// placeholders ?1, ?2, ..., and takes one argument for each: a string, an
// int64 or nil. Scan destinations are *string and *int64, and every
// statement returns non-NULL values. An error SQLite reports carries its
// extended result code, which Options.ResultCode reads.
type Conn interface {
	// Query runs a statement and calls row once per result row; scan reads
	// the row's columns into its destinations.
	Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error
	// Exec runs a statement and returns how many rows it changed.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

// savepoint names the savepoint of a transaction begun inside another.
// SQLite resolves ROLLBACK TO and RELEASE to the innermost savepoint of a
// name, so one name serves every depth.
const savepoint = "superschematic_versiongraph"

// DB is a Client over a database/sql pool. Each transaction takes one
// connection from the pool for its whole length, turns its foreign keys on
// and begins with BEGIN IMMEDIATE; a transaction begun inside it is a
// savepoint on that connection.
//
// Give the pool's connections a busy timeout (modernc.org/sqlite:
// _pragma=busy_timeout(5000) in the DSN, which sets none by default), or a
// transaction that finds another connection holding the write lock fails
// SQLITE_BUSY at once instead of waiting for it. Open a file: over a pool,
// :memory: gives each connection a database of its own. And begin a nested
// transaction with the context the outer one's function was given: one
// begun with a fresh context takes another connection, so with
// SetMaxOpenConns(1) it waits for one until its context ends.
func DB(db *sql.DB) Client {
	return &dbClient{db: db}
}

// DBConn is a Client over one database/sql connection, which runs one
// transaction at a time. Each transaction turns the connection's foreign
// keys on and begins with BEGIN IMMEDIATE; a transaction begun inside it is
// a savepoint.
func DBConn(conn *sql.Conn) Client {
	return &connClient{conn: conn}
}

// DBTx is a Client inside a database/sql transaction the caller holds. Each
// transaction is a savepoint inside it, so what the adapter writes commits
// or rolls back with the caller's transaction. The caller's connection has
// its foreign keys on, which SQLite lets a connection change only outside a
// transaction, and the caller begins it as it wants the file locked (BEGIN
// IMMEDIATE, so its writes order as its times do).
func DBTx(tx *sql.Tx) Client {
	return &txClient{conn: &sqlConn{q: tx}}
}

// querier is what a database/sql connection and transaction share.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// sqlConn is the Conn over a database/sql connection or transaction.
type sqlConn struct {
	q querier
}

func (c *sqlConn) Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error {
	rows, err := c.q.QueryContext(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := row(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (c *sqlConn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	result, err := c.q.ExecContext(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// openKey keys, in a transaction's context, the Conn of the transaction a
// client began on owner: a *sql.DB or a *sql.Conn.
type openKey struct {
	owner any
}

type dbClient struct {
	db *sql.DB
}

func (c *dbClient) Transact(ctx context.Context, fn func(ctx context.Context, conn Conn) error) (err error) {
	if open, ok := ctx.Value(openKey{c.db}).(*sqlConn); ok {
		return inSavepoint(ctx, open, fn)
	}
	pinned, err := c.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlite: take a connection: %w", err)
	}
	returned := false
	defer func() {
		if !returned || errors.Is(err, errConnState) {
			// The connection's transaction may not have ended, so no other
			// transaction may take it: the pool closes it.
			_ = pinned.Raw(func(any) error { return driver.ErrBadConn })
		}
		if closeErr := pinned.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("sqlite: return the connection: %w", closeErr)
		}
	}()
	err = begin(ctx, &sqlConn{q: pinned}, openKey{c.db}, fn)
	returned = true
	return err
}

type connClient struct {
	conn *sql.Conn
}

func (c *connClient) Transact(ctx context.Context, fn func(ctx context.Context, conn Conn) error) error {
	if open, ok := ctx.Value(openKey{c.conn}).(*sqlConn); ok {
		return inSavepoint(ctx, open, fn)
	}
	return begin(ctx, &sqlConn{q: c.conn}, openKey{c.conn}, fn)
}

type txClient struct {
	conn *sqlConn
}

func (c *txClient) Transact(ctx context.Context, fn func(ctx context.Context, conn Conn) error) error {
	return inSavepoint(ctx, c.conn, fn)
}

// errConnState marks an error after which a connection's transaction may
// still be open: a rollback that failed.
var errConnState = errors.New("the connection's transaction may still be open")

// rollbackStatement ends a transaction a binding began. A test swaps it for
// one that fails (export_test.go), since no real ROLLBACK fails on demand.
var rollbackStatement = "ROLLBACK"

// errUnfinished is the cause of a rollback whose function did not return.
var errUnfinished = errors.New("the transaction's function did not return")

// begin runs fn in a transaction of its own on conn: it turns the
// connection's foreign keys on, which SQLite ignores inside a transaction,
// begins with BEGIN IMMEDIATE, and hands fn a context in which a
// transaction begun on the same owner is a savepoint on conn.
func begin(ctx context.Context, conn *sqlConn, key openKey, fn func(ctx context.Context, conn Conn) error) (err error) {
	if _, err := conn.Exec(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("sqlite: turn foreign keys on: %w", err)
	}
	if _, err := conn.Exec(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	// Some errors end the transaction on their own, and then ROLLBACK finds
	// none to end.
	rollback := func(cause error) error {
		_, rbErr := conn.Exec(context.WithoutCancel(ctx), rollbackStatement)
		if rbErr != nil && !strings.Contains(rbErr.Error(), "no transaction is active") {
			return fmt.Errorf("sqlite: roll back: %w (after %w, %w)", rbErr, cause, errConnState)
		}
		return cause
	}
	returned := false
	defer func() {
		if !returned {
			// fn panicked, and the panic goes on, or its goroutine exited
			// (runtime.Goexit, as a test's FailNow does).
			_ = rollback(errUnfinished)
		}
	}()
	err = fn(context.WithValue(ctx, key, conn), conn)
	returned = true
	if err != nil {
		return rollback(err)
	}
	if _, err := conn.Exec(ctx, "COMMIT"); err != nil {
		// A COMMIT that fails busy leaves the transaction open.
		return rollback(fmt.Errorf("sqlite: commit: %w", err))
	}
	return nil
}

// inSavepoint runs fn in a savepoint on conn, which rolls back alone when
// fn fails.
func inSavepoint(ctx context.Context, conn *sqlConn, fn func(ctx context.Context, conn Conn) error) error {
	if _, err := conn.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
		return fmt.Errorf("sqlite: savepoint: %w", err)
	}
	rollback := func(cause error) error {
		ctx := context.WithoutCancel(ctx)
		for _, statement := range []string{"ROLLBACK TO " + savepoint, "RELEASE " + savepoint} {
			if _, err := conn.Exec(ctx, statement); err != nil {
				return fmt.Errorf("sqlite: roll back the savepoint: %w (after %w)", err, cause)
			}
		}
		return cause
	}
	returned := false
	defer func() {
		if !returned {
			_ = rollback(errUnfinished)
		}
	}()
	err := fn(ctx, conn)
	returned = true
	if err != nil {
		return rollback(err)
	}
	if _, err := conn.Exec(ctx, "RELEASE "+savepoint); err != nil {
		return rollback(fmt.Errorf("sqlite: release the savepoint: %w", err))
	}
	return nil
}
