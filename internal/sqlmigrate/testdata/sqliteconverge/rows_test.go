package sqliteconverge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

// rowsCase is one table TestRows inserts rows into, as TestVariantChecksOnSQLite
// in the sqlmigrate package writes it to rows.json beside the service's
// sqlite/create.sql: the table, the columns every insert sets, each with
// its literal, and the rows.
type rowsCase struct {
	Table  string            `json:"table"`
	Insert map[string]string `json:"insert"`
	Rows   []caseRow         `json:"rows"`
}

// caseRow is one insert: the values of the columns it sets beside the
// case's (nil is NULL), and whether the table takes it.
type caseRow struct {
	Name   string             `json:"name"`
	Values map[string]*string `json:"values"`
	OK     bool               `json:"ok"`
}

// constraintCheck is SQLITE_CONSTRAINT_CHECK, the error of a CHECK.
const constraintCheck = 275

// TestRows inserts each row of each case under SQLITECONVERGE_ROWS into a
// database its create.sql made: a row the case says the table takes is
// inserted, and any other fails with SQLITE_CONSTRAINT_CHECK (275).
func TestRows(t *testing.T) {
	casesDir := os.Getenv("SQLITECONVERGE_ROWS")
	if casesDir == "" {
		t.Skip("SQLITECONVERGE_ROWS is not set; run through TestVariantChecksOnSQLite")
	}
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ran := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ran++
		dir := filepath.Join(casesDir, entry.Name())
		t.Run(entry.Name(), func(t *testing.T) {
			conn := open(t, ctx, "rows.db")
			execScript(t, ctx, conn, dir, "create.sql")
			var rc rowsCase
			b, err := os.ReadFile(filepath.Join(dir, "rows.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &rc); err != nil {
				t.Fatal(err)
			}
			refused := 0
			for i, row := range rc.Rows {
				var names, literals []string
				var args []any
				for name, literal := range rc.Insert {
					names, literals = append(names, quoted(name)), append(literals, literal)
				}
				for name, value := range row.Values {
					names, literals = append(names, quoted(name)), append(literals, "?")
					if value == nil {
						args = append(args, nil)
					} else {
						args = append(args, *value)
					}
				}
				names, literals = append(names, quoted("id")), append(literals, "?")
				args = append(args, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1))
				insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoted(rc.Table), strings.Join(names, ", "), strings.Join(literals, ", "))
				_, err := conn.ExecContext(ctx, insert, args...)
				var sqliteErr *sqlite.Error
				switch {
				case row.OK && err != nil:
					t.Errorf("%s is refused: %v", row.Name, err)
				case row.OK:
				case err == nil:
					t.Errorf("%s is taken, and the check should refuse it", row.Name)
				case !errors.As(err, &sqliteErr) || sqliteErr.Code() != constraintCheck:
					t.Errorf("%s fails with %v, want SQLITE_CONSTRAINT_CHECK (%d)", row.Name, err, constraintCheck)
				default:
					refused++
				}
			}
			if len(rc.Rows) == 0 || refused == 0 {
				t.Errorf("the case inserted %d rows and %d were refused", len(rc.Rows), refused)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no case under SQLITECONVERGE_ROWS")
	}
}
