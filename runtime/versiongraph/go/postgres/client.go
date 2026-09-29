package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Client runs the adapter's statements. The adapter asks it for one
// transaction per engine operation. Pgx binds pgx; another driver implements
// the two interfaces itself.
type Client interface {
	// Transact runs fn in one transaction. It commits when fn returns nil
	// and rolls back otherwise, returning fn's error.
	Transact(ctx context.Context, fn func(ctx context.Context, conn Conn) error) error
}

// Conn runs statements inside one transaction. Arguments are strings,
// int64s, booleans, []string, []int64 and nil; a statement casts each to its
// column's type. Scan destinations are *string, *int64 and *bool, and every
// statement returns non-NULL values.
type Conn interface {
	// Query runs a statement and calls row once per result row; scan reads
	// the row's columns into its destinations.
	Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error
	// Exec runs a statement and returns how many rows it affected.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

// Beginner is what Pgx binds: a *pgx.Conn, a *pgxpool.Pool, or a pgx.Tx,
// whose Begin starts a savepoint, so the adapter runs inside a transaction
// the caller holds.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Pgx is the default Client: pgx over a connection, a pool or an open
// transaction.
func Pgx(db Beginner) Client {
	return pgxClient{db: db}
}

type pgxClient struct {
	db Beginner
}

func (c pgxClient) Transact(ctx context.Context, fn func(ctx context.Context, conn Conn) error) (err error) {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()
	if err := fn(ctx, pgxConn{tx: tx}); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("postgres: roll back: %w (after %w)", rbErr, err)
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

type pgxConn struct {
	tx pgx.Tx
}

func (c pgxConn) Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error {
	rows, err := c.tx.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := row(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (c pgxConn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := c.tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
