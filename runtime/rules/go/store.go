package rules

import (
	"context"
	"errors"
)

// Dialect is the SQL a store's statements are written in.
type Dialect string

// The dialects a store speaks.
const (
	// DialectPostgres writes $n placeholders.
	DialectPostgres Dialect = "postgres"
	// DialectSQLite writes ?n placeholders.
	DialectSQLite Dialect = "sqlite"
)

// LockMode is how a store holds a lock a rule across rows asks for.
type LockMode string

// The ways a store holds a lock.
const (
	// LocksHeld: a lock the transaction holds until it ends, taken before
	// the reads it guards (Postgres's advisory lock).
	LocksHeld LockMode = "held"
	// LocksFenced: none; the adapter puts first in the unit that writes a
	// check of each read made under a lock, which moves the command when
	// another changed what it read (SQLite, whose one writer holds only
	// the unit that writes and not the reads before it).
	LocksFenced LockMode = "fenced"
)

// AnyRows is a Statement's Expect for a write that may change any number
// of rows.
const AnyRows = -1

// ErrBusy is what a store's error wraps when the database would not let
// the transaction read or write for another's lock: SQLite's SQLITE_BUSY,
// another connection holding its one writer or having written since the
// transaction began to read. A command whose store returns it is refused
// as busy (BusyRefusal) with nothing of it written, and the caller's
// transaction, rolled back, may be tried again.
var ErrBusy = errors.New("rules: the database is busy")

// Statement is one statement of a change set's apply, its placeholders
// the store's dialect's ($n or ?n).
type Statement struct {
	// Text is a write, or with Check a condition: an SQL boolean
	// expression, EXISTS (...) or NOT EXISTS (...).
	Text   string
	Values []any
	// Expect is the rows the write must change; the unit is moved, and
	// nothing of it applied, when it changes another number. AnyRows for
	// any. Its zero value is exactly 0 rows, not any, unlike an unset
	// expect in TypeScript's and Rust's adapters: a write of a store's own
	// that may change any rows sets AnyRows.
	Expect int64
	// Check makes Text a condition the unit holds to where it stands: the
	// unit is moved, and nothing of it applied, when it is false.
	Check bool
}

// Store is what the adapter reads and writes through: the caller's
// transaction (D103). A Store wraps one transaction and is not shared
// across goroutines.
type Store interface {
	Dialect() Dialect
	Locks() LockMode
	// Query runs a read in the transaction: each row's values in select
	// order, as the driver scans them into any.
	Query(ctx context.Context, text string, values ...any) ([][]any, error)
	// Apply runs statements as one unit, all of them or none: true when
	// each changed the rows it expects and each check held; false, with
	// none applied, when one did not; an error, with none applied, on a
	// failure of the database, wrapping ErrBusy when another's lock kept
	// it from the database. An empty unit applies with no statement at
	// all.
	Apply(ctx context.Context, statements []Statement) (bool, error)
	// Lock takes a lock the transaction holds until it ends, or none on a
	// store whose locks are fenced.
	Lock(ctx context.Context, key string) error
}
