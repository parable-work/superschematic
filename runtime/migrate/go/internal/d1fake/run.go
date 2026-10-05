package d1fake

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/libc"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// run runs a request's statements in one transaction on a new connection
// and returns a result per statement.
func (s *Server) run(ctx context.Context, statements []statement, raw bool) (results []any, err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, message(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	for _, st := range statements {
		if refusal := refused(st.sql); refusal != "" {
			return nil, errors.New(refusal)
		}
		result, err := execute(ctx, conn, st, raw)
		if err != nil {
			return nil, message(err)
		}
		results = append(results, result)
	}
	// A foreign key a deferred check finds broken fails the commit, and
	// the request with it.
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, message(err)
	}
	committed = true
	return results, nil
}

// execute runs one statement. Its meta carries changes, last_row_id,
// changed_db and duration; D1's other meta members are left out. changes
// is the statement's difference in total_changes(), which counts the rows
// foreign key actions change too (a guess at D1's count).
func execute(ctx context.Context, conn *sql.Conn, st statement, raw bool) (map[string]any, error) {
	var before int64
	if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return nil, err
	}
	began := time.Now()
	rows, err := conn.QueryContext(ctx, st.sql, st.params...)
	if err != nil {
		return nil, err
	}
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return nil, err
	}
	table := [][]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			_ = rows.Close()
			return nil, err
		}
		for i, v := range values {
			values[i] = jsonValue(v)
		}
		table = append(table, values)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	duration := time.Since(began)
	var after, lastRowID int64
	if err := conn.QueryRowContext(ctx, "SELECT total_changes(), last_insert_rowid()").Scan(&after, &lastRowID); err != nil {
		return nil, err
	}
	var results any = objects(columns, table)
	if raw {
		results = map[string]any{"columns": columns, "rows": table}
	}
	return map[string]any{
		"success": true,
		"results": results,
		"meta": map[string]any{
			"changes":     after - before,
			"last_row_id": lastRowID,
			"changed_db":  after != before,
			"duration":    float64(duration.Microseconds()) / 1000,
		},
	}, nil
}

// jsonValue is a column value as D1 writes it: a number, a string or null.
// A blob as an array of bytes is a guess.
func jsonValue(v any) any {
	switch v := v.(type) {
	case []byte:
		out := make([]int, len(v))
		for i, b := range v {
			out[i] = int(b)
		}
		return out
	case time.Time:
		return v.Format(time.RFC3339Nano)
	}
	return v
}

// objects are rows as the query endpoint writes them: an object per row,
// its members in column order.
func objects(columns []string, table [][]any) []json.RawMessage {
	out := make([]json.RawMessage, len(table))
	for r, values := range table {
		var b bytes.Buffer
		b.WriteByte('{')
		for i, column := range columns {
			if i > 0 {
				b.WriteByte(',')
			}
			name, _ := json.Marshal(column)
			value, _ := json.Marshal(values[i])
			b.Write(name)
			b.WriteByte(':')
			b.Write(value)
		}
		b.WriteByte('}')
		out[r] = b.Bytes()
	}
	return out
}

// pragmas are the PRAGMAs D1 lists as supported.
var pragmas = map[string]bool{
	"table_list": true, "table_info": true, "table_xinfo": true, "index_list": true, "index_info": true,
	"index_xinfo": true, "quick_check": true, "foreign_key_check": true, "foreign_key_list": true,
	"case_sensitive_like": true, "ignore_check_constraints": true, "legacy_alter_table": true,
	"recursive_triggers": true, "reverse_unordered_selects": true, "foreign_keys": true,
	"defer_foreign_keys": true, "optimize": true,
}

// refused returns the error D1 gives a statement it does not run, or "".
// Both messages are guesses: the first is the one Durable Objects' SQLite,
// which D1 runs on, is reported to give a transaction statement; the
// second is SQLite's authorizer refusal.
func refused(statement string) string {
	keyword, rest := firstWord(statement)
	switch keyword {
	case "BEGIN", "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE":
		return "To execute a transaction, please use the state.storage.transaction() or state.storage.transactionSync() APIs instead of the SQL BEGIN TRANSACTION or SAVEPOINT statements: SQLITE_AUTH"
	case "PRAGMA":
		name, _ := firstWord(rest)
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		if !pragmas[strings.ToLower(name)] {
			return "not authorized: SQLITE_AUTH"
		}
	}
	return ""
}

// firstWord returns a statement's first word, upper-cased, past white
// space and comments, and the text after it. A word may hold a dot, so a
// PRAGMA's schema stays with its name.
func firstWord(s string) (string, string) {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return "", ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return "", ""
		}
		break
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if c != '_' && c != '.' && (c < '0' || c > '9') && (c|0x20 < 'a' || c|0x20 > 'z') {
			break
		}
		end++
	}
	return strings.ToUpper(s[:end]), s[end:]
}

// splitStatements splits text into its statements, as SQLite's shell does:
// at each semicolon that sqlite3_complete says ends a statement.
func splitStatements(text string) []string {
	tls := libc.NewTLS()
	defer tls.Close()
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] != ';' || !complete(tls, text[start:i+1]) {
			continue
		}
		if statement := strings.TrimSpace(text[start:i]); statement != "" {
			out = append(out, statement)
		}
		start = i + 1
	}
	if statement := strings.TrimSpace(text[start:]); statement != "" {
		out = append(out, statement)
	}
	return out
}

func complete(tls *libc.TLS, text string) bool {
	p, err := libc.CString(text)
	if err != nil {
		panic(err)
	}
	defer libc.Xfree(tls, p)
	return sqlite3.Xsqlite3_complete(tls, p) != 0
}

// message turns a SQLite error into D1's form, "<message>: <code name>"
// (no such table: missing: SQLITE_ERROR).
func message(err error) error {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return err
	}
	msg := strings.TrimSuffix(sqliteErr.Error(), fmt.Sprintf(" (%d)", sqliteErr.Code()))
	// modernc writes "<sqlite3_errstr>: <sqlite3_errmsg>"; D1 writes the
	// second.
	if i := strings.Index(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	name, ok := codeNames[sqliteErr.Code()&0xff]
	if !ok {
		name = fmt.Sprintf("SQLITE_%d", sqliteErr.Code()&0xff)
	}
	return errors.New(msg + ": " + name)
}

var codeNames = map[int]string{
	sqlite3.SQLITE_ERROR:      "SQLITE_ERROR",
	sqlite3.SQLITE_BUSY:       "SQLITE_BUSY",
	sqlite3.SQLITE_LOCKED:     "SQLITE_LOCKED",
	sqlite3.SQLITE_TOOBIG:     "SQLITE_TOOBIG",
	sqlite3.SQLITE_CONSTRAINT: "SQLITE_CONSTRAINT",
	sqlite3.SQLITE_MISMATCH:   "SQLITE_MISMATCH",
	sqlite3.SQLITE_AUTH:       "SQLITE_AUTH",
	sqlite3.SQLITE_RANGE:      "SQLITE_RANGE",
}
