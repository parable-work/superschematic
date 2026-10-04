package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// formatScenario is a scenario of the given roots (raw JSON, or "" for
// none) and steps.
func formatScenario(roots string, steps ...string) string {
	member := ""
	if roots != "" {
		member = `"roots": ` + roots + `, `
	}
	return `{"name": "format", "description": "", ` + member + `"steps": [` + strings.Join(steps, ", ") + `]}`
}

// TestScenarioFormat reads scenarios that each break one rule of the format
// (runtime/versiongraph/README.md, "Scenarios"), and ones that keep them.
func TestScenarioFormat(t *testing.T) {
	const createPrimary = `{"op": "createPrimary", "root": "Bread", "name": "main"}`
	cases := []struct {
		name string
		text string
		// refused is a part of the error the scenario is refused with, or
		// "" when it reads.
		refused string
	}{
		{"a statement per backend", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"postgres": "SELECT 1", "sqlite": "SELECT 1"}}`), ""},
		{"a plain string statement", formatScenario(`["Bread"]`, `{"op": "sql", "statement": "SELECT 1"}`), "a statement is an object of one statement per backend"},
		{"a statement that is not text", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"postgres": 1}}`), "a statement is an object of one statement per backend"},
		{"an sql step without the runner's statement", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"sqlite": "SELECT 1"}}`), "the sql step has no postgres statement"},
		{"an sql step with no statement", formatScenario(`["Bread"]`, `{"op": "sql"}`), "the sql step has no postgres statement"},
		{"a statement for an unknown backend", formatScenario(`["Bread"]`, `{"op": "sql", "statement": {"postgres": "SELECT 1", "mysql": "SELECT 1"}}`), `a statement for unknown backend "mysql"`},
		{"an sql step for another backend, without the runner's statement", formatScenario(`["Bread"]`, `{"op": "sql", "backends": ["sqlite"], "statement": {"sqlite": "SELECT 1"}}`), ""},
		{"backends listing the runner's", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["sqlite", "postgres"]}`), ""},
		{"backends listing an unknown backend", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["postgres", "mysql"]}`), `backends lists unknown backend "mysql"`},
		{"an empty backends", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": []}`), "backends lists no backend"},
		{"backends listing a backend twice", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["postgres", "postgres"]}`), `backends lists "postgres" twice`},
		{"no roots", formatScenario("", createPrimary), "a scenario names its roots"},
		{"null roots", formatScenario("null", createPrimary), "a scenario names its roots"},
		{"empty roots", formatScenario("[]", createPrimary), "a scenario names at least one root"},
		{"a root named twice", formatScenario(`["Bread", "Soup", "Bread"]`, createPrimary), `the scenario names root "Bread" twice`},
		{"no steps", formatScenario(`["Bread"]`), "a scenario has steps"},
		{"an unknown scenario member", `{"name": "format", "description": "", "roots": ["Bread"], "backend": "postgres", "steps": [` + createPrimary + `]}`, `unknown field "backend"`},
		{"an unknown step member", formatScenario(`["Bread"]`, `{"op": "createPrimary", "root": "Bread", "name": "main", "backend": "postgres"}`), `unknown field "backend"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseScenario([]byte(c.text), backend)
			switch {
			case c.refused == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.refused != "" && err == nil:
				t.Fatalf("read, want it refused with %q", c.refused)
			case c.refused != "" && !strings.Contains(err.Error(), c.refused):
				t.Fatalf("refused with %q, want %q", err, c.refused)
			}
		})
	}
}

// TestScenarioFilesRead reads every scenario file as the format says,
// whether or not a database is there to run it on.
func TestScenarioFilesRead(t *testing.T) {
	for _, file := range scenarioFiles(t) {
		readScenario(t, file)
	}
}

// syntheticRunner is a runner over the fixture for a scenario a test
// writes. It skips the test without the database.
func syntheticRunner(t *testing.T) *runner {
	t.Helper()
	dsn := os.Getenv(databaseVariable)
	if dsn == "" {
		t.Skip("set " + databaseVariable + " to run the scenario against Postgres")
	}
	createSQL, err := os.ReadFile(filepath.Join(fixtureDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return newRunner(t, dsn, mustReadFixture(t), createSQL)
}

func mustParseScenario(t *testing.T, text string) scenario {
	t.Helper()
	s, err := parseScenario([]byte(text), backend)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestScenarioBackends runs a scenario whose steps list their backends: a
// save listed for SQLite alone is skipped and leaves no row, and a save
// listed for Postgres too writes its row.
func TestScenarioBackends(t *testing.T) {
	r := syntheticRunner(t)
	runScenario(t, r, mustParseScenario(t, formatScenario(`["Bread"]`,
		`{"op": "createPrimary", "root": "Bread", "name": "main", "as": "main"}`,
		`{"op": "branch", "from": "main", "name": "mix", "as": "mix"}`,
		`{"op": "save", "ref": "mix", "backends": ["sqlite"], "edits": {"step": {"upsert": [{"entity_key": "Mix", "position": 1, "instruction": "Mix", "timings": {}}]}}}`,
		`{"op": "rows", "ref": "mix", "kind": "step", "expect": {"rows": []}}`,
		`{"op": "save", "ref": "mix", "backends": ["sqlite", "postgres"], "edits": {"step": {"upsert": [{"entity_key": "Rest", "position": 2, "instruction": "Rest", "timings": {}}]}}}`,
		`{"op": "rows", "ref": "mix", "kind": "step", "expect": {"rows": [{"entity_key": "Rest"}]}}`,
	)))
}

// TestScenarioRoots runs a scenario of two roots: each has the recipe row
// the seeding sql steps gave it (its id, its name as the title and the
// default actor as its creator) and no other root has one, so a primary
// line of a root the scenario does not name fails on the foreign key from
// recipe_ref.root_id.
func TestScenarioRoots(t *testing.T) {
	r := syntheticRunner(t)
	runScenario(t, r, mustParseScenario(t, formatScenario(`["Soup", "Pie"]`,
		`{"op": "sql", "statement": {"postgres": "SELECT title, CASE id WHEN $1::uuid THEN 'Soup' WHEN $2::uuid THEN 'Pie' ELSE id::text END AS id, CASE created_by WHEN $3::uuid THEN 'Cook' ELSE created_by::text END AS created_by FROM recipe ORDER BY title"},
		  "args": [{"uuid": "Soup"}, {"uuid": "Pie"}, {"uuid": "Cook"}],
		  "expect": {"rows": [{"title": "Pie", "id": "Pie", "created_by": "Cook"}, {"title": "Soup", "id": "Soup", "created_by": "Cook"}]}}`,
		`{"op": "createPrimary", "root": "Soup", "name": "main"}`,
		`{"op": "createPrimary", "root": "Pie", "name": "main"}`,
	)))
	_, err := r.engine.CreatePrimary(r.ctx, defaultActor, "Bread", "main")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("a primary line of a root the scenario does not name: %v, want a foreign key violation", err)
	}
}
