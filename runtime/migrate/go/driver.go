package migrate

import (
	"context"
	"strings"
)

// Driver is one database connection as the runner uses it. Package postgres
// and package sqlite implement it over one dedicated connection, so every
// statement of a run, and the session settings it makes, share it.
type Driver interface {
	// Dialect is the database the driver talks to.
	Dialect() Dialect
	// Lock returns once the caller holds the lock that serializes the
	// runs of service, and unlock releases it. A driver whose write
	// transactions serialize on their own (SQLite's BEGIN IMMEDIATE)
	// returns at once.
	Lock(ctx context.Context, service string) (unlock func(context.Context) error, err error)
	// Transact runs fn in one transaction, which waits at most the
	// driver's lock timeout for each lock. It commits when fn returns nil
	// and rolls back otherwise, returning fn's error.
	Transact(ctx context.Context, opts TxOptions, fn func(ctx context.Context, conn Conn) error) error
	// Session runs fn outside a transaction, so each statement commits on
	// its own, under the driver's lock timeout. A driver that runs every
	// step in a transaction returns an error.
	Session(ctx context.Context, fn func(ctx context.Context, conn Conn) error) error
	// IsLockTimeout reports whether err, or an error it wraps, is a lock
	// wait that timed out. The runner retries a step that fails with one.
	IsLockTimeout(err error) bool
}

// TxOptions shape one transaction.
type TxOptions struct {
	// ReadOnly transactions take no write lock.
	ReadOnly bool
	// ForeignKeysOff turns foreign key enforcement off around the
	// transaction and fails it before the commit if any foreign key is
	// violated (SQLite).
	ForeignKeysOff bool
}

// Conn runs statements on a driver's connection. Arguments are strings and
// int64s. Scan destinations are *string and *int64, and the runner's
// queries return no NULL. A statement with no arguments may hold several
// statements, as a plan's dollar-quoted function body does.
type Conn interface {
	// Exec runs a statement.
	Exec(ctx context.Context, sql string, args ...any) error
	// Query runs a statement and calls row once per result row; scan
	// reads the row's columns into its destinations.
	Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error
}

// URLDialect returns the dialect a database URL selects: a postgres:// or
// postgresql:// URL selects Postgres; a sqlite: URL, a file: URI or a path
// selects SQLite.
func URLDialect(url string) Dialect {
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return Postgres
	}
	return SQLite
}
