package sqliteconverge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

// fenceCase is the writes TestFences makes on a service's @fenced tables
// (D119), in order, as TestFencesOnSQLite in the sqlmigrate package writes
// them to fences.json beside the service's sqlite/create.sql.
type fenceCase struct {
	Writes []fenceWrite `json:"writes"`
}

// fenceWrite is one statement and what it must do. A write a fence
// refuses names the message the fence's trigger fails it with; a write a
// foreign key refuses, and no fence, names the key; one that passes names
// neither. After either, Query's rows are Want: each row's columns joined
// by "|", NULL for a null, and the rows by "; ". A refused write's Want is
// what the rows held before it.
type fenceWrite struct {
	Name       string `json:"name"`
	SQL        string `json:"sql"`
	Message    string `json:"message"`
	ForeignKey string `json:"foreignKey"`
	Query      string `json:"query"`
	Want       string `json:"want"`
}

// foreignKeyFailed is the message of a write a foreign key refuses, which
// names no constraint.
const foreignKeyFailed = "FOREIGN KEY constraint failed"

// shownRows are a query's rows as a fenceWrite's Want writes them.
func shownRows(ctx context.Context, conn *sql.Conn, query string) (string, error) {
	r, err := conn.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	names, err := r.Columns()
	if err != nil {
		return "", err
	}
	var rows []string
	for r.Next() {
		values, columns := make([]any, len(names)), make([]string, len(names))
		for i := range values {
			values[i] = new(any)
		}
		if err := r.Scan(values...); err != nil {
			return "", err
		}
		for i, value := range values {
			switch v := (*value.(*any)).(type) {
			case nil:
				columns[i] = "NULL"
			case []byte:
				columns[i] = string(v)
			default:
				columns[i] = fmt.Sprint(v)
			}
		}
		rows = append(rows, strings.Join(columns, "|"))
	}
	return strings.Join(rows, "; "), r.Err()
}

// TestFences runs the writes of each case under SQLITECONVERGE_FENCES on a
// database its create.sql made (D119). A write a fence allows passes, and
// one it does not fails with the fence's message as
// SQLITE_CONSTRAINT_TRIGGER (1811); one a foreign key refuses fails with
// the key's message, and not with a fence's; either way the rows then
// hold what the case says, so a refused write leaves them as they were.
func TestFences(t *testing.T) {
	casesDir := os.Getenv("SQLITECONVERGE_FENCES")
	if casesDir == "" {
		t.Skip("SQLITECONVERGE_FENCES is not set; run through TestFencesOnSQLite")
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
			conn := open(t, ctx, "fences.db")
			execScript(t, ctx, conn, dir, "create.sql")
			var fc fenceCase
			b, err := os.ReadFile(filepath.Join(dir, "fences.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &fc); err != nil {
				t.Fatal(err)
			}
			passed, refused := 0, 0
			for _, w := range fc.Writes {
				_, err := conn.ExecContext(ctx, w.SQL)
				switch {
				case w.ForeignKey != "" && err == nil:
					t.Errorf("%s passes, and the foreign key %s refuses it\n%s", w.Name, w.ForeignKey, w.SQL)
				case w.ForeignKey != "":
					refused++
					if !strings.Contains(err.Error(), foreignKeyFailed) {
						t.Errorf("%s fails with %v, want %q of the foreign key %s\n%s", w.Name, err, foreignKeyFailed, w.ForeignKey, w.SQL)
					}
				case w.Message == "" && err != nil:
					t.Errorf("%s is refused: %v\n%s", w.Name, err, w.SQL)
				case w.Message == "":
					passed++
				case err == nil:
					t.Errorf("%s passes, want it refused: %s\n%s", w.Name, w.Message, w.SQL)
				default:
					refused++
					var sqliteErr *sqlite.Error
					if !errors.As(err, &sqliteErr) || sqliteErr.Code() != constraintTrigger || !strings.Contains(err.Error(), w.Message) {
						t.Errorf("%s fails with %v, want %q as SQLITE_CONSTRAINT_TRIGGER (%d)\n%s", w.Name, err, w.Message, constraintTrigger, w.SQL)
					}
				}
				got, err := shownRows(ctx, conn, w.Query)
				if err != nil {
					t.Fatalf("%s: %v\n%s", w.Name, err, w.Query)
				}
				if got != w.Want {
					t.Errorf("after %s the rows hold %s, want %s", w.Name, got, w.Want)
				}
			}
			if passed == 0 || refused == 0 {
				t.Errorf("the case made %d writes that passed and %d that were refused", passed, refused)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no case under SQLITECONVERGE_FENCES")
	}
}
