package sqlutil

import (
	"fmt"
	"strings"
)

// The statements of a transaction both ORMs open with options and of one
// nested in it (D89). pgx and pg write a plain BEGIN, COMMIT and ROLLBACK
// themselves; a transaction with an isolation level or read only begins
// with BeginTransaction's statement, and a nested one is a savepoint,
// named for its depth, which the Go ORM writes from these constants and
// the TypeScript runtime's transaction as they say.

// The isolation levels a transaction takes, as both ORMs name them.
const (
	ReadCommitted  = "read committed"
	RepeatableRead = "repeatable read"
	Serializable   = "serializable"
)

// IsolationLevel is an isolation level, as both ORMs name it, and its SQL.
type IsolationLevel struct {
	Name string
	SQL  string
}

// IsolationLevels are the levels a transaction takes, in the order their
// strength rises.
func IsolationLevels() []IsolationLevel {
	levels := []string{ReadCommitted, RepeatableRead, Serializable}
	out := make([]IsolationLevel, len(levels))
	for i, level := range levels {
		out[i] = IsolationLevel{Name: level, SQL: strings.ToUpper(level)}
	}
	return out
}

// The parts of BeginTransaction's statement: Begin, then Isolation of the
// level's SQL, then ReadOnly.
const (
	Begin     = "BEGIN"
	Isolation = " ISOLATION LEVEL %s"
	ReadOnly  = " READ ONLY"
)

// BeginTransaction is the statement that begins a transaction at the
// isolation level, the server's default when it is empty, read only or
// not: BEGIN ISOLATION LEVEL SERIALIZABLE READ ONLY.
func BeginTransaction(level string, readOnly bool) (string, error) {
	begin := Begin
	if level != "" {
		found := false
		for _, l := range IsolationLevels() {
			if l.Name == level {
				begin += fmt.Sprintf(Isolation, l.SQL)
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("sqlutil: a transaction's isolation is read committed, repeatable read or serializable, not %q", level)
		}
	}
	if readOnly {
		begin += ReadOnly
	}
	return begin, nil
}

// The statements of a transaction nested in another: a savepoint, named
// SavepointName of its depth, released when it ends and rolled back to,
// then released, when it fails.
const (
	Savepoint           = "SAVEPOINT %s"
	ReleaseSavepoint    = "RELEASE SAVEPOINT %s"
	RollbackToSavepoint = "ROLLBACK TO SAVEPOINT %s"
	SavepointPrefix     = "sp_"
)

// SavepointName is the savepoint of a transaction nested depth deep, 1 for
// one in a transaction.
func SavepointName(depth int) string {
	return fmt.Sprintf("%s%d", SavepointPrefix, depth)
}
