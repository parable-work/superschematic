package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// OpenReadOnly opens the file a sqlite: URL, a file: URI or a path names
// for reading alone, and refuses one that does not exist: SQLite's mode=ro
// creates no file and takes no write, and the connection is query_only
// besides. `superschematic-migrate inspect` reads a file built by hand
// through it (D116). Close the driver when the read ends.
func OpenReadOnly(ctx context.Context, databaseURL string, opts Options) (*Driver, error) {
	dsn, err := ReadOnlyDSN(databaseURL)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: open %s to read it: %w", databaseURL, err)
	}
	d, err := New(ctx, conn, opts)
	if err == nil {
		if _, pragmaErr := conn.ExecContext(ctx, "PRAGMA query_only = ON"); pragmaErr != nil {
			err = fmt.Errorf("sqlite: PRAGMA query_only = ON: %w", pragmaErr)
		}
	}
	if err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, err
	}
	d.db = db
	return d, nil
}

// ReadOnlyDSN turns a database URL into a file: URI that opens the file
// with mode=ro. A path, and a sqlite: URL of one, must name a file that
// exists; a file: URI keeps its other parameters, and SQLite refuses it
// when its file does not exist. A database in memory names no file.
func ReadOnlyDSN(databaseURL string) (string, error) {
	dsn := DSN(databaseURL)
	if strings.HasPrefix(dsn, "file:") {
		name, rawQuery, _ := strings.Cut(dsn, "?")
		query, err := url.ParseQuery(rawQuery)
		if err != nil {
			return "", fmt.Errorf("sqlite: %s: %w", databaseURL, err)
		}
		query.Set("mode", "ro")
		return name + "?" + query.Encode(), nil
	}
	if dsn == "" || dsn == ":memory:" {
		return "", fmt.Errorf("sqlite: %q names no file to read", databaseURL)
	}
	path, err := filepath.Abs(dsn)
	if err != nil {
		return "", fmt.Errorf("sqlite: %s: %w", databaseURL, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("sqlite: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("sqlite: %s is a directory, not a SQLite file", path)
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
	return uri.String(), nil
}
