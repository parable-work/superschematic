// Package sqlite is the behaviors' adapter's store over a SQLite
// transaction in database/sql (D103), with any driver: what rules.Execute
// reads and writes through in the caller's transaction, for a hand-written
// server (the generated Go ORM is Postgres's alone).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// Querier is the transaction a store reads and writes through: a *sql.Tx,
// or a *sql.Conn in a transaction; never a *sql.DB, whose statements each
// take a connection of the pool.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Savepoint is the savepoint a store applies a change set in.
const Savepoint = "rules_apply"

// NewStore is a Store over tx: DialectSQLite, LocksFenced. Apply runs in a
// savepoint, rules_apply, checking each write's RowsAffected and each
// check as SELECT CASE WHEN (<text>) THEN 1 ELSE 0 END, rolling back to it
// and releasing it when one fails. Lock takes nothing: the adapter checks
// in the unit each read made under a lock.
//
// Go's SQLite transactions are interactive, so the unit is a savepoint,
// not D1's batch; the fences stay, as D103 has them, since a caller's
// reads may come before the unit.
//
// tx is to begin IMMEDIATE (database/sql's default is DEFERRED; the DSN
// parameter _txlock=immediate makes BeginTx begin IMMEDIATE in
// modernc.org/sqlite and in mattn/go-sqlite3). The transaction then holds
// SQLite's one writer from its start, so two commands on one database run
// one after the other and each reads what the one before it committed. A
// DEFERRED transaction reads a snapshot another connection may write past
// before the unit: its fences check that snapshot, and its write is refused
// with SQLITE_BUSY, which the store wraps as rules.ErrBusy, so the command
// is refused as busy, nothing written, for the caller to roll back and try
// again.
func NewStore(tx Querier) rules.Store {
	return &store{tx: tx}
}

type store struct {
	tx Querier
}

func (*store) Dialect() rules.Dialect { return rules.DialectSQLite }

func (*store) Locks() rules.LockMode { return rules.LocksFenced }

func (s *store) Query(ctx context.Context, text string, values ...any) ([][]any, error) {
	rows, err := s.tx.QueryContext(ctx, text, values...)
	if err != nil {
		return nil, failed(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, failed(err)
	}
	var out [][]any
	for rows.Next() {
		row := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range row {
			targets[i] = &row[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, failed(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, failed(err)
	}
	return out, nil
}

func (s *store) Apply(ctx context.Context, statements []rules.Statement) (bool, error) {
	if len(statements) == 0 {
		return true, nil
	}
	if _, err := s.tx.ExecContext(ctx, "SAVEPOINT "+Savepoint); err != nil {
		return false, failed(err)
	}
	for _, statement := range statements {
		held, err := s.run(ctx, statement)
		if err != nil {
			s.undo(ctx)
			return false, failed(err)
		}
		if !held {
			s.undo(ctx)
			return false, nil
		}
	}
	if _, err := s.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+Savepoint); err != nil {
		return false, failed(err)
	}
	return true, nil
}

// sqliteBusy is SQLite's primary result code SQLITE_BUSY, which its
// extended codes (SQLITE_BUSY_SNAPSHOT, ...) hold in their low byte.
const sqliteBusy = 5

// failed is a driver's error as the store returns it: wrapped, and
// wrapping rules.ErrBusy too when it is SQLITE_BUSY in any of its extended
// forms. A driver's error with a Code() int, as modernc.org/sqlite's, says
// so by its code; any other by the text sqlite3_errstr gives the code,
// "database is locked", which every driver's message carries.
func failed(err error) error {
	var coded interface{ Code() int }
	busy := false
	if errors.As(err, &coded) {
		busy = coded.Code()&0xff == sqliteBusy
	} else {
		busy = strings.Contains(err.Error(), "database is locked")
	}
	if busy {
		return fmt.Errorf("%w: %w", rules.ErrBusy, err)
	}
	return fmt.Errorf("rules: %w", err)
}

// run runs one statement of a unit: whether its check held, or whether its
// write changed the rows it expects (the rows it changed itself, which a
// trigger's writes do not count among).
func (s *store) run(ctx context.Context, statement rules.Statement) (bool, error) {
	if statement.Check {
		rows, err := s.tx.QueryContext(ctx, "SELECT CASE WHEN ("+statement.Text+") THEN 1 ELSE 0 END AS held", statement.Values...)
		if err != nil {
			return false, err
		}
		defer func() { _ = rows.Close() }()
		held := false
		if rows.Next() {
			var value sql.NullInt64
			if err := rows.Scan(&value); err != nil {
				return false, err
			}
			held = value.Valid && value.Int64 == 1
		}
		if err := rows.Close(); err != nil {
			return false, err
		}
		return held, rows.Err()
	}
	result, err := s.tx.ExecContext(ctx, statement.Text, statement.Values...)
	if err != nil {
		return false, err
	}
	if statement.Expect == rules.AnyRows {
		return true, nil
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed == statement.Expect, nil
}

// undo rolls a unit back to its savepoint and releases it. A rollback that
// fails is let go: the error that undid the unit is the one returned.
func (s *store) undo(ctx context.Context) {
	if _, err := s.tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+Savepoint); err != nil {
		return
	}
	_, _ = s.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+Savepoint)
}

// Lock takes nothing: the unit holds a lock instead, the adapter checking
// in it each read made under one (LocksFenced).
func (*store) Lock(context.Context, string) error {
	return nil
}
