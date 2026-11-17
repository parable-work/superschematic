package pgconverge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// guardCase is one table whose @stateMachine guards TestGuards runs
// (D118), as TestStateMachineGuardsOnPostgres in the sqlmigrate package
// writes it to guards.json beside the service's create.sql and drop.sql.
type guardCase struct {
	Table string `json:"table"`
	// Key is the table's key column, a UUID the test gives each row.
	Key string `json:"key"`
	// Insert are the other columns an insert sets, each with its literal.
	Insert map[string]string `json:"insert"`
	// Touch is the assignment of an update that sets no guarded column.
	Touch   string        `json:"touch"`
	Columns []guardColumn `json:"columns"`
}

// guardColumn is one guarded column: the values it takes, null among them
// when it is nullable, and the moves its machine lists. Every other move
// between two of the values is refused, but one that changes nothing.
type guardColumn struct {
	Column  string       `json:"column"`
	Values  []*string    `json:"values"`
	Allowed [][2]*string `json:"allowed"`
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

// shown is a value as an error and plpgsql's RAISE write it.
func shown(v *string) string {
	if v == nil {
		return "<NULL>"
	}
	return *v
}

// TestGuards runs the guards of each case under PGCONVERGE_GUARDS on a
// database its create.sql made (D118). For every guarded column and every
// pair of its values, null among them, it inserts a row in the first
// state, which no guard holds, updates another column, which no guard
// hears, and moves the column to the second: a move the machine lists and
// one that changes nothing pass, and any other is refused with
// check_violation (23514) and a message that names the column and the two
// values, leaving the row as it was. drop.sql then leaves no guard.
func TestGuards(t *testing.T) {
	casesDir := os.Getenv("PGCONVERGE_GUARDS")
	if casesDir == "" {
		t.Skip("PGCONVERGE_GUARDS is not set; run through TestStateMachineGuardsOnPostgres")
	}
	adminURL := env(t, "PGCONVERGE_DATABASE_URL")
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	ran := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ran++
		dir := filepath.Join(casesDir, entry.Name())
		t.Run(entry.Name(), func(t *testing.T) {
			conn := scratchDatabase(t, ctx, admin, adminURL)
			execScript(t, ctx, conn, dir, "create.sql")
			var gc guardCase
			if !readJSON(t, filepath.Join(dir, "guards.json"), &gc) {
				t.Fatal("the case has no guards.json")
			}
			table, key := pgx.Identifier{gc.Table}.Sanitize(), pgx.Identifier{gc.Key}.Sanitize()
			rows, refused := 0, 0
			for _, col := range gc.Columns {
				column := pgx.Identifier{col.Column}.Sanitize()
				names, literals := []string{key, column}, []string{"$1", "$2"}
				for name, literal := range gc.Insert {
					names, literals = append(names, pgx.Identifier{name}.Sanitize()), append(literals, literal)
				}
				insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(names, ", "), strings.Join(literals, ", "))
				for _, from := range col.Values {
					for _, to := range col.Values {
						rows++
						id := fmt.Sprintf("00000000-0000-4000-8000-%012d", rows)
						move := fmt.Sprintf("%s.%s from %s to %s", gc.Table, col.Column, shown(from), shown(to))
						if _, err := conn.Exec(ctx, insert, id, from); err != nil {
							t.Fatalf("insert a row whose %s is %s: %v", col.Column, shown(from), err)
						}
						if _, err := conn.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE %s = $1", table, gc.Touch, key), id); err != nil {
							t.Errorf("%s: an update of another column of the row: %v", move, err)
						}
						tag, err := conn.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s = $2 WHERE %s = $1", table, column, key), id, to)
						want := to
						unchanged := (from == nil) == (to == nil) && shown(from) == shown(to)
						switch pass := unchanged || col.allows(from, to); {
						case pass && err != nil:
							t.Errorf("%s is refused: %v", move, err)
							continue
						case pass && tag.RowsAffected() != 1:
							t.Errorf("%s updated %d rows", move, tag.RowsAffected())
						case pass:
						case err == nil:
							t.Errorf("%s passes, and the machine does not list it", move)
						default:
							refused++
							want = from
							var pgErr *pgconn.PgError
							if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
								t.Errorf("%s fails with %v, want check_violation (23514)", move, err)
							} else if msg := pgErr.Message; !strings.HasPrefix(msg, gc.Table+"."+col.Column+" ") || !strings.Contains(msg, shown(from)) || !strings.Contains(msg, shown(to)) {
								t.Errorf("%s fails with %q, which does not name the column and the two values", move, msg)
							}
						}
						var got *string
						if err := conn.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1", column, table, key), id).Scan(&got); err != nil {
							t.Fatal(err)
						}
						if shown(got) != shown(want) {
							t.Errorf("after %s the row holds %s, want %s", move, shown(got), shown(want))
						}
					}
				}
			}
			if rows == 0 || refused == 0 {
				t.Errorf("the case made %d moves and %d were refused", rows, refused)
			}

			execScript(t, ctx, conn, dir, "drop.sql")
			var left int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname LIKE '%\_transition\_guard'`).Scan(&left); err != nil || left != 0 {
				t.Errorf("drop.sql left %d guard functions (%v)", left, err)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no case under PGCONVERGE_GUARDS")
	}
}
