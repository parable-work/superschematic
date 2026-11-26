// Package sqlitefile opens a database's SQLite file for a generated Go
// server or job (D116): the file a stack's database field names as
// sqlite:<path>, which the local target derives for a database whose DB
// service lists sqlite first. The generated ORM's OpenSQLite opens its
// repositories over what Open answers, and the identity store and the
// idempotency keys run on it in their SQLite dialects.
//
// The driver is modernc.org/sqlite, which needs no C toolchain. Each
// connection of the pool runs in write-ahead logging, so readers read
// beside the one writer; waits up to BusyTimeout for another connection's
// write lock; enforces foreign keys; and begins its transactions
// IMMEDIATE, so a transaction that reads before it writes takes the write
// lock as it begins, rather than failing on a stale snapshot.
package sqlitefile

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	// The driver Open opens the file with.
	_ "modernc.org/sqlite"

	"github.com/parable-work/superschematic/runtime/http/go/stackconfig"
)

// BusyTimeout is how long a statement waits for another connection's
// write lock before it fails as busy.
const BusyTimeout = 5 * time.Second

// Driver is the database/sql driver Open opens the file with.
const Driver = "sqlite"

// DSN is the data source name of the file at path: the pragmas of every
// connection, as modernc.org/sqlite reads them, and IMMEDIATE transactions.
// It is the generated ORM's SQLiteDSN.
func DSN(path string) string {
	return fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate", path, BusyTimeout.Milliseconds())
}

// Path is the file a database's URL names: sqlite:<path>, or
// sqlite://<path>.
func Path(url string) (string, error) {
	path, ok := strings.CutPrefix(url, "sqlite://")
	if !ok {
		path, ok = strings.CutPrefix(url, "sqlite:")
	}
	if !ok || path == "" {
		return "", fmt.Errorf("the database's URL is no sqlite:<path>, and its DB service lists sqlite first, so the API's database is a SQLite file")
	}
	return path, nil
}

// Open opens the file the database field names, which exists: a migration
// creates it before a server opens it, so a missing file is an error rather
// than an empty database. It pings the file once.
func Open(db stackconfig.Database) (*sql.DB, error) {
	if db.CloudSQL != nil {
		return nil, errors.New("the database is a Cloud SQL instance, and its DB service lists sqlite first, so the API's database is a SQLite file")
	}
	path, err := Path(db.URL)
	if err != nil {
		return nil, err
	}
	return OpenFile(path)
}

// OpenFile opens the SQLite file at path, which exists.
func OpenFile(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("the SQLite file: %w", err)
	}
	out, err := sql.Open(Driver, DSN(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := out.Ping(); err != nil {
		_ = out.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return out, nil
}
