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

// rowsCase is one table TestRows inserts rows into, as
// TestVariantChecksOnPostgres in the sqlmigrate package writes it to
// rows.json beside the service's create.sql: the table, the columns every
// insert sets, each with its literal, each column's type the values are
// cast to, and the rows.
type rowsCase struct {
	Table  string            `json:"table"`
	Insert map[string]string `json:"insert"`
	Casts  map[string]string `json:"casts"`
	Rows   []caseRow         `json:"rows"`
	// Constraint is the check a refused row breaks.
	Constraint string `json:"constraint"`
}

// caseRow is one insert: the values of the columns it sets beside the
// case's (nil is NULL), and whether the table takes it.
type caseRow struct {
	Name   string             `json:"name"`
	Values map[string]*string `json:"values"`
	OK     bool               `json:"ok"`
}

// TestRows inserts each row of each case under PGCONVERGE_ROWS into a
// database its create.sql made: a row the case says the table takes is
// inserted, and any other fails with check_violation (23514) on the case's
// constraint.
func TestRows(t *testing.T) {
	casesDir := os.Getenv("PGCONVERGE_ROWS")
	if casesDir == "" {
		t.Skip("PGCONVERGE_ROWS is not set; run through TestVariantChecksOnPostgres")
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
			var rc rowsCase
			if !readJSON(t, filepath.Join(dir, "rows.json"), &rc) {
				t.Fatal("the case has no rows.json")
			}
			refused := 0
			for _, row := range rc.Rows {
				var names, literals []string
				var args []any
				for name, literal := range rc.Insert {
					names, literals = append(names, pgx.Identifier{name}.Sanitize()), append(literals, literal)
				}
				for name, value := range row.Values {
					args = append(args, value)
					placeholder := fmt.Sprintf("$%d", len(args))
					if cast := rc.Casts[name]; cast != "" {
						placeholder += "::" + cast
					}
					names, literals = append(names, pgx.Identifier{name}.Sanitize()), append(literals, placeholder)
				}
				insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", pgx.Identifier{rc.Table}.Sanitize(), strings.Join(names, ", "), strings.Join(literals, ", "))
				_, err := conn.Exec(ctx, insert, args...)
				var pgErr *pgconn.PgError
				switch {
				case row.OK && err != nil:
					t.Errorf("%s is refused: %v", row.Name, err)
				case row.OK:
				case err == nil:
					t.Errorf("%s is taken, and the check should refuse it", row.Name)
				case !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != rc.Constraint:
					t.Errorf("%s fails with %v, want check_violation (23514) on %s", row.Name, err, rc.Constraint)
				default:
					refused++
				}
			}
			if len(rc.Rows) == 0 || refused == 0 {
				t.Errorf("the case inserted %d rows and %d were refused", len(rc.Rows), refused)
			}
			execScript(t, ctx, conn, dir, "drop.sql")
		})
	}
	if ran == 0 {
		t.Fatal("no case under PGCONVERGE_ROWS")
	}
}
