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

// guardCase is one table whose @stateMachine guards TestGuards runs
// (D117), as TestStateMachineGuardsOnSQLite in the sqlmigrate package
// writes it to guards.json beside the service's sqlite/create.sql.
type guardCase struct {
	Table string `json:"table"`
	// Key is the table's key column, which the test gives each row.
	Key string `json:"key"`
	// Insert are the other columns an insert sets, each with its literal.
	Insert map[string]string `json:"insert"`
	// Touch is the assignment of an update that sets no guarded column.
	Touch   string        `json:"touch"`
	Columns []guardColumn `json:"columns"`
}

// guardColumn is one guarded column: the values it takes, null among them
// when it is nullable, the moves its machine lists, and the message its
// guard fails any other move with, but one that changes nothing.
type guardColumn struct {
	Column  string       `json:"column"`
	Values  []*string    `json:"values"`
	Allowed [][2]*string `json:"allowed"`
	Message string       `json:"message"`
}

func (c guardColumn) allows(from, to *string) bool {
	same := func(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	for _, move := range c.Allowed {
		if same(move[0], from) && same(move[1], to) {
			return true
		}
	}
	return false
}

// shown is a value as an error writes it.
func shown(v *string) string {
	if v == nil {
		return "NULL"
	}
	return "'" + *v + "'"
}

// quoted is an identifier as SQLite quotes it.
func quoted(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// constraintTrigger is SQLITE_CONSTRAINT_TRIGGER, the error of a trigger's
// RAISE(ABORT).
const constraintTrigger = 1811

// TestGuards runs the guards of each case under SQLITECONVERGE_GUARDS on a
// database its create.sql made (D117). For every guarded column and every
// pair of its values, null among them, it inserts a row in the first
// state, which no guard holds, updates another column, which no guard
// hears, and moves the column to the second: a move the machine lists and
// one that changes nothing pass, and any other fails with the guard's
// message as SQLITE_CONSTRAINT_TRIGGER (1811), leaving the row as it was.
func TestGuards(t *testing.T) {
	casesDir := os.Getenv("SQLITECONVERGE_GUARDS")
	if casesDir == "" {
		t.Skip("SQLITECONVERGE_GUARDS is not set; run through TestStateMachineGuardsOnSQLite")
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
			conn := open(t, ctx, "guards.db")
			execScript(t, ctx, conn, dir, "create.sql")
			var gc guardCase
			b, err := os.ReadFile(filepath.Join(dir, "guards.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &gc); err != nil {
				t.Fatal(err)
			}
			table, key := quoted(gc.Table), quoted(gc.Key)
			made, refused := 0, 0
			for _, col := range gc.Columns {
				column := quoted(col.Column)
				names, literals := []string{key, column}, []string{"?", "?"}
				for name, literal := range gc.Insert {
					names, literals = append(names, quoted(name)), append(literals, literal)
				}
				insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(names, ", "), strings.Join(literals, ", "))
				for _, from := range col.Values {
					for _, to := range col.Values {
						made++
						id := fmt.Sprintf("00000000-0000-4000-8000-%012d", made)
						move := fmt.Sprintf("%s.%s from %s to %s", gc.Table, col.Column, shown(from), shown(to))
						if _, err := conn.ExecContext(ctx, insert, id, from); err != nil {
							t.Fatalf("insert a row whose %s is %s: %v", col.Column, shown(from), err)
						}
						if _, err := conn.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?", table, gc.Touch, key), id); err != nil {
							t.Errorf("%s: an update of another column of the row: %v", move, err)
						}
						_, err := conn.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", table, column, key), to, id)
						want := to
						unchanged := shown(from) == shown(to)
						switch pass := unchanged || col.allows(from, to); {
						case pass && err != nil:
							t.Errorf("%s is refused: %v", move, err)
							continue
						case pass:
						case err == nil:
							t.Errorf("%s passes, and the machine does not list it", move)
						default:
							refused++
							want = from
							var sqliteErr *sqlite.Error
							if !errors.As(err, &sqliteErr) || sqliteErr.Code() != constraintTrigger || !strings.Contains(err.Error(), col.Message) {
								t.Errorf("%s fails with %v, want %q as SQLITE_CONSTRAINT_TRIGGER (%d)", move, err, col.Message, constraintTrigger)
							}
						}
						var got sql.NullString
						if err := conn.QueryRowContext(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", column, table, key), id).Scan(&got); err != nil {
							t.Fatal(err)
						}
						if (want == nil) == got.Valid || want != nil && *want != got.String {
							t.Errorf("after %s the row holds %+v, want %s", move, got, shown(want))
						}
					}
				}
			}
			if made == 0 || refused == 0 {
				t.Errorf("the case made %d moves and %d were refused", made, refused)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no case under SQLITECONVERGE_GUARDS")
	}
}
