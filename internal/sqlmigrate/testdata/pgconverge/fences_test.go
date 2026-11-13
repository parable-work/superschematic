package pgconverge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fenceCase is the writes TestFences makes on a service's @fenced tables
// (D118), in order, as TestFencesOnPostgres in the sqlmigrate package
// writes them to fences.json beside the service's create.sql and drop.sql.
type fenceCase struct {
	Writes []fenceWrite `json:"writes"`
}

// fenceWrite is one statement and what it must do. A write a fence
// refuses names the fence's constraint and its whole message; a write a
// foreign key refuses, and no fence, names the key's constraint; one that
// passes names none of them. After either, Query's rows are Want: each
// row's columns joined by "|", NULL for a null, and the rows by "; ". A
// refused write's Want is what the rows held before it.
type fenceWrite struct {
	Name       string `json:"name"`
	SQL        string `json:"sql"`
	Constraint string `json:"constraint"`
	Message    string `json:"message"`
	ForeignKey string `json:"foreignKey"`
	Query      string `json:"query"`
	Want       string `json:"want"`
}

// The codes of a delete a foreign key refuses: foreign_key_violation,
// and restrict_violation, which later servers give a RESTRICT.
const foreignKeyViolation, restrictViolation = "23503", "23001"

// shownRows are a query's rows as a fenceWrite's Want writes them.
func shownRows(ctx context.Context, conn *pgx.Conn, query string) (string, error) {
	results, err := conn.PgConn().Exec(ctx, query).ReadAll()
	if err != nil {
		return "", err
	}
	var rows []string
	for _, result := range results {
		for _, row := range result.Rows {
			columns := make([]string, len(row))
			for i, value := range row {
				columns[i] = "NULL"
				if value != nil {
					columns[i] = string(value)
				}
			}
			rows = append(rows, strings.Join(columns, "|"))
		}
	}
	return strings.Join(rows, "; "), nil
}

// TestFences runs the writes of each case under PGCONVERGE_FENCES on a
// database its create.sql made (D118). A write a fence allows passes, and
// one it does not is refused with check_violation (23514), the fence's
// constraint and its message, which holds the stored, the expected and the
// written value; one a foreign key refuses fails with the key's
// constraint, and not with a fence's; either way the rows then hold what
// the case says, so a refused write leaves them as they were. drop.sql
// then leaves no fence.
func TestFences(t *testing.T) {
	casesDir := os.Getenv("PGCONVERGE_FENCES")
	if casesDir == "" {
		t.Skip("PGCONVERGE_FENCES is not set; run through TestFencesOnPostgres")
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
			var fc fenceCase
			if !readJSON(t, filepath.Join(dir, "fences.json"), &fc) {
				t.Fatal("the case has no fences.json")
			}
			passed, refused := 0, 0
			for _, w := range fc.Writes {
				_, err := conn.PgConn().Exec(ctx, w.SQL).ReadAll()
				var pgErr *pgconn.PgError
				switch {
				case w.ForeignKey != "" && err == nil:
					t.Errorf("%s passes, and the foreign key %s refuses it\n%s", w.Name, w.ForeignKey, w.SQL)
				case w.ForeignKey != "":
					refused++
					if !errors.As(err, &pgErr) || (pgErr.Code != foreignKeyViolation && pgErr.Code != restrictViolation) || pgErr.ConstraintName != w.ForeignKey {
						t.Errorf("%s fails with %v, want a violation of the foreign key %s\n%s", w.Name, err, w.ForeignKey, w.SQL)
					}
				case w.Constraint == "" && err != nil:
					t.Errorf("%s is refused: %v\n%s", w.Name, err, w.SQL)
				case w.Constraint == "":
					passed++
				case err == nil:
					t.Errorf("%s passes, and %s refuses it\n%s", w.Name, w.Constraint, w.SQL)
				default:
					refused++
					if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != w.Constraint || pgErr.Message != w.Message {
						t.Errorf("%s fails with %v, want check_violation (23514) of the constraint %s: %s\n%s", w.Name, err, w.Constraint, w.Message, w.SQL)
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

			execScript(t, ctx, conn, dir, "drop.sql")
			var left int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname LIKE '%\_fence'`).Scan(&left); err != nil || left != 0 {
				t.Errorf("drop.sql left %d fence functions (%v)", left, err)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no case under PGCONVERGE_FENCES")
	}
}
