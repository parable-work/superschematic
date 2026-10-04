// Package postgres is the runner's Postgres driver. It runs a whole apply on
// one dedicated pgx connection: the connection holds the service's session
// advisory lock for the run, and every step runs on it.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// DefaultLockTimeout is how long a step waits for a lock before it fails
// with SQLSTATE 55P03 and the runner retries it.
const DefaultLockTimeout = 5 * time.Second

// lockNotAvailable is the SQLSTATE of a lock_timeout.
const lockNotAvailable = "55P03"

// DefaultLockPoll is how often a runner waiting for another tries the
// service's advisory lock again.
const DefaultLockPoll = 500 * time.Millisecond

// Options configure a Driver.
type Options struct {
	// LockTimeout is the lock_timeout of every step. Zero is
	// DefaultLockTimeout.
	LockTimeout time.Duration
	// LockPoll is how often Lock tries a lock another runner holds. Zero
	// is DefaultLockPoll.
	LockPoll time.Duration
}

// Driver is a migrate.Driver over one pgx connection.
type Driver struct {
	conn        *pgx.Conn
	lockTimeout string
	lockPoll    time.Duration
}

// New binds the driver to conn, which it uses alone until the run ends.
func New(conn *pgx.Conn, opts Options) *Driver {
	timeout := opts.LockTimeout
	if timeout <= 0 {
		timeout = DefaultLockTimeout
	}
	poll := opts.LockPoll
	if poll <= 0 {
		poll = DefaultLockPoll
	}
	return &Driver{conn: conn, lockTimeout: fmt.Sprintf("%dms", timeout.Milliseconds()), lockPoll: poll}
}

// Open connects to a postgres:// URL. Close the driver when the run ends.
func Open(ctx context.Context, url string, opts Options) (*Driver, error) {
	config, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	if _, ok := config.RuntimeParams["application_name"]; !ok {
		config.RuntimeParams["application_name"] = "superschematic-migrate"
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	return New(conn, opts), nil
}

// Close closes the connection, which releases any lock it still holds.
func (d *Driver) Close(ctx context.Context) error {
	return d.conn.Close(ctx)
}

// Dialect is migrate.Postgres.
func (d *Driver) Dialect() migrate.Dialect { return migrate.Postgres }

// Lock takes the session advisory lock migrate.LockKey(service) names,
// waiting for a runner that holds it. It waits by trying the lock again
// every LockPoll rather than in pg_advisory_lock: a session waiting there
// holds a snapshot, and a CREATE INDEX CONCURRENTLY the holder runs waits
// for that snapshot, which Postgres reports as a deadlock.
func (d *Driver) Lock(ctx context.Context, service string) (func(context.Context) error, error) {
	key := migrate.LockKey(service)
	for {
		var locked bool
		if err := d.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&locked); err != nil {
			return nil, err
		}
		if locked {
			break
		}
		timer := time.NewTimer(d.lockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return func(ctx context.Context) error {
		_, err := d.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)
		return err
	}, nil
}

// Transact runs fn in a transaction whose lock_timeout is the driver's.
func (d *Driver) Transact(ctx context.Context, opts migrate.TxOptions, fn func(ctx context.Context, conn migrate.Conn) error) (err error) {
	if opts.ForeignKeysOff {
		return errors.New("postgres: a step cannot turn foreign keys off")
	}
	txOptions := pgx.TxOptions{}
	if opts.ReadOnly {
		txOptions.AccessMode = pgx.ReadOnly
	}
	tx, err := d.conn.BeginTx(ctx, txOptions)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()
	if !opts.ReadOnly {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '"+d.lockTimeout+"'"); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return fmt.Errorf("postgres: set lock_timeout: %w", err)
		}
	}
	if err := fn(ctx, conn{q: tx}); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("postgres: roll back: %w (after %w)", rbErr, err)
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// Session runs fn on the connection outside a transaction with the driver's
// lock_timeout, and resets lock_timeout after it.
func (d *Driver) Session(ctx context.Context, fn func(ctx context.Context, conn migrate.Conn) error) error {
	if _, err := d.conn.Exec(ctx, "SET lock_timeout = '"+d.lockTimeout+"'"); err != nil {
		return fmt.Errorf("postgres: set lock_timeout: %w", err)
	}
	err := fn(ctx, conn{q: d.conn})
	if _, resetErr := d.conn.Exec(context.WithoutCancel(ctx), "RESET lock_timeout"); resetErr != nil && err == nil {
		err = fmt.Errorf("postgres: reset lock_timeout: %w", resetErr)
	}
	return err
}

// IsLockTimeout reports SQLSTATE 55P03, lock_not_available.
func (d *Driver) IsLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable
}

// querier is what a pgx.Conn and a pgx.Tx share.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type conn struct {
	q querier
}

// Exec runs sql. With no arguments pgx sends it over the simple protocol,
// so a dollar-quoted body with semicolons in it runs as written.
func (c conn) Exec(ctx context.Context, sql string, args ...any) error {
	_, err := c.q.Exec(ctx, sql, args...)
	return err
}

func (c conn) Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error {
	rows, err := c.q.Query(ctx, sql, args...)
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
