// Package sqlite is the runner's SQLite driver, over the pure-Go
// modernc.org/sqlite, so neither the tests nor the binary need cgo. It runs
// a whole apply on one connection with foreign key enforcement on. Every
// write transaction is BEGIN IMMEDIATE, which is the lock that serializes
// runners: the runner checks the state again inside each one.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// DefaultBusyTimeout is how long a transaction waits for another
// connection's lock before it fails with SQLITE_BUSY and the runner retries
// it.
const DefaultBusyTimeout = 5 * time.Second

// Options configure a Driver.
type Options struct {
	// BusyTimeout is the connection's busy_timeout. Zero is
	// DefaultBusyTimeout.
	BusyTimeout time.Duration
}

// Driver is a migrate.Driver over one SQLite connection.
type Driver struct {
	conn *sql.Conn
	db   *sql.DB
}

// New binds the driver to conn, which it uses alone until the run ends. It
// sets the connection's busy_timeout and turns foreign keys on.
func New(ctx context.Context, conn *sql.Conn, opts Options) (*Driver, error) {
	timeout := opts.BusyTimeout
	if timeout <= 0 {
		timeout = DefaultBusyTimeout
	}
	for _, pragma := range []string{
		fmt.Sprintf("PRAGMA busy_timeout = %d", timeout.Milliseconds()),
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			return nil, fmt.Errorf("sqlite: %s: %w", pragma, err)
		}
	}
	return &Driver{conn: conn}, nil
}

// Open opens the database a sqlite: URL, a file: URI or a path names,
// creating the file when it is missing. Close the driver when the run ends.
func Open(ctx context.Context, url string, opts Options) (*Driver, error) {
	db, err := sql.Open("sqlite", DSN(url))
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: open %s: %w", url, err)
	}
	d, err := New(ctx, conn, opts)
	if err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, err
	}
	d.db = db
	return d, nil
}

// DSN turns a database URL into the name the driver opens: sqlite:PATH and
// sqlite://PATH are PATH (sqlite:///tmp/app.db is /tmp/app.db), or a file:
// URI when PATH carries a query; a file: URI and a path are kept as they
// are.
func DSN(url string) string {
	if len(url) < len("sqlite:") || !strings.EqualFold(url[:len("sqlite:")], "sqlite:") {
		return url
	}
	path := strings.TrimPrefix(url[len("sqlite:"):], "//")
	if strings.Contains(path, "?") {
		return "file:" + path
	}
	return path
}

// Close closes the connection, and the database Open opened.
func (d *Driver) Close(context.Context) error {
	err := d.conn.Close()
	if d.db != nil {
		err = errors.Join(err, d.db.Close())
	}
	return err
}

// Dialect is migrate.SQLite.
func (d *Driver) Dialect() migrate.Dialect { return migrate.SQLite }

// Lock returns at once: each write transaction is BEGIN IMMEDIATE.
func (d *Driver) Lock(context.Context, string) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}

// Transact runs fn in a BEGIN IMMEDIATE transaction, or a deferred one when
// opts.ReadOnly. With opts.ForeignKeysOff it turns foreign keys off before
// the transaction, which SQLite allows only outside one, runs PRAGMA
// foreign_key_check before the commit and fails on any row it returns, and
// turns foreign keys on again after.
func (d *Driver) Transact(ctx context.Context, opts migrate.TxOptions, fn func(ctx context.Context, conn migrate.Conn) error) (err error) {
	if opts.ForeignKeysOff {
		if _, err := d.conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return fmt.Errorf("sqlite: turn foreign keys off: %w", err)
		}
		defer func() {
			if _, onErr := d.conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON"); onErr != nil && err == nil {
				err = fmt.Errorf("sqlite: turn foreign keys on: %w", onErr)
			}
		}()
	}
	begin := "BEGIN IMMEDIATE"
	if opts.ReadOnly {
		begin = "BEGIN"
	}
	if _, err := d.conn.ExecContext(ctx, begin); err != nil {
		return fmt.Errorf("sqlite: %s: %w", begin, err)
	}
	// Some errors end the transaction on their own, and then ROLLBACK
	// finds none to end.
	rollback := func(cause error) error {
		_, rbErr := d.conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		if rbErr != nil && !strings.Contains(rbErr.Error(), "no transaction is active") {
			return fmt.Errorf("sqlite: roll back: %w (after %w)", rbErr, cause)
		}
		return cause
	}
	defer func() {
		if p := recover(); p != nil {
			_ = rollback(nil)
			panic(p)
		}
	}()
	c := conn{c: d.conn}
	if err := fn(ctx, c); err != nil {
		return rollback(err)
	}
	if opts.ForeignKeysOff {
		if err := foreignKeyCheck(ctx, c); err != nil {
			return rollback(err)
		}
	}
	if _, err := d.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return rollback(fmt.Errorf("sqlite: commit: %w", err))
	}
	return nil
}

// foreignKeyCheck fails when PRAGMA foreign_key_check returns a row.
func foreignKeyCheck(ctx context.Context, c conn) error {
	var violations []string
	err := c.Query(ctx, "PRAGMA foreign_key_check", nil, func(scan func(dest ...any) error) error {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int64
		if err := scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		if len(violations) < 5 {
			row := "without rowid"
			if rowid.Valid {
				row = fmt.Sprintf("%d", rowid.Int64)
			}
			violations = append(violations, fmt.Sprintf("%s row %s references a missing row of %s", table, row, parent))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sqlite: foreign_key_check: %w", err)
	}
	if len(violations) > 0 {
		return fmt.Errorf("foreign_key_check: %s", strings.Join(violations, "; "))
	}
	return nil
}

// Session fails: SQLite runs every step in a transaction.
func (d *Driver) Session(context.Context, func(ctx context.Context, conn migrate.Conn) error) error {
	return errors.New("sqlite: every step runs in a transaction")
}

// IsLockTimeout reports SQLITE_BUSY and its extended codes.
func (d *Driver) IsLockTimeout(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}

type conn struct {
	c *sql.Conn
}

func (c conn) Exec(ctx context.Context, sql string, args ...any) error {
	_, err := c.c.ExecContext(ctx, sql, args...)
	return err
}

func (c conn) Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error {
	rows, err := c.c.QueryContext(ctx, sql, args...)
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
