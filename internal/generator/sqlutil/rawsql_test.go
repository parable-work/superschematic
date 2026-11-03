package sqlutil

import (
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestSplitStatements: a semicolon ends a statement only outside a string,
// a quoted identifier, a dollar-quoted body and a comment.
func TestSplitStatements(t *testing.T) {
	sql := `
		-- a leading comment; with a semicolon
		CREATE OR REPLACE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END $body$ LANGUAGE plpgsql;
		COMMENT ON TABLE t IS 'it''s; here';
		CREATE INDEX IF NOT EXISTS "odd;name" ON t (a) /* a; /* nested; */ comment */;
		DO $$ BEGIN PERFORM 1; END $$;
		SELECT E'a\';b';
		;  -- only a comment
	`
	got, err := SplitStatements(sql)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-- a leading comment; with a semicolon\n\t\tCREATE OR REPLACE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END $body$ LANGUAGE plpgsql",
		"COMMENT ON TABLE t IS 'it''s; here'",
		`CREATE INDEX IF NOT EXISTS "odd;name" ON t (a) /* a; /* nested; */ comment */`,
		"DO $$ BEGIN PERFORM 1; END $$",
		`SELECT E'a\';b'`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("SplitStatements =\n%q\nwant\n%q", got, want)
	}
	for _, bad := range []string{"SELECT 'open", `SELECT "open`, "DO $$ BEGIN", "SELECT 1 /* open"} {
		if _, err := SplitStatements(bad); err == nil {
			t.Errorf("SplitStatements(%q) accepted an unterminated token", bad)
		}
	}
	if got, _ := SplitStatements("SELECT $1, a$b"); len(got) != 1 {
		t.Errorf("a parameter or an identifier with $ is no body: %q", got)
	}
}

// TestStatementProblem: a statement a plan can run again passes; one it
// cannot, or one that runs CONCURRENTLY, is refused with the fix.
func TestStatementProblem(t *testing.T) {
	for stmt, ok := range map[string]bool{
		"CREATE OR REPLACE VIEW v AS SELECT 1":                         true,
		"CREATE MATERIALIZED VIEW IF NOT EXISTS mv AS SELECT 1":        true,
		"create unique index if not exists i on t (a)":                 true,
		"CREATE EXTENSION IF NOT EXISTS vector":                        true,
		"DROP MATERIALIZED VIEW IF EXISTS mv":                          true,
		"DROP TRIGGER IF EXISTS trg ON t":                              true,
		"REFRESH MATERIALIZED VIEW mv":                                 true,
		"COMMENT ON TABLE t IS 'x'":                                    true,
		"GRANT SELECT ON t TO reader":                                  true,
		"DO $$ BEGIN END $$":                                           true,
		"INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING":          true,
		"INSERT OR IGNORE INTO t (a) VALUES (1)":                       true,
		"CREATE MATERIALIZED VIEW mv AS SELECT 'IF NOT EXISTS'":        false,
		"CREATE INDEX i ON t (a)":                                      false,
		"CREATE TABLE t (a int, CHECK (a > 0))":                        false,
		"DROP TABLE t":                                                 false,
		"INSERT INTO t (a) VALUES (1)":                                 false,
		"ALTER TABLE t ADD COLUMN b int":                               false,
		"SELECT 1":                                                     false,
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS i ON t (a)":           false,
		"DROP INDEX CONCURRENTLY IF EXISTS i":                          false,
		"CREATE TABLE IF NOT EXISTS t (a int CHECK (a > 0))":           true,
		"REFRESH MATERIALIZED VIEW CONCURRENTLY mv":                    true,
		"CREATE OR REPLACE FUNCTION f() RETURNS int AS $$ x $$":        true,
		"CREATE TRIGGER IF NOT EXISTS trg AFTER INSERT ON t BEGIN END": true,
	} {
		problem := statementProblem(stmt)
		if (problem == "") != ok {
			t.Errorf("statementProblem(%q) = %q, want ok %v", stmt, problem, ok)
		}
	}
	if p := statementProblem("CREATE INDEX CONCURRENTLY IF NOT EXISTS i ON t (a)"); !strings.Contains(p, "runs CONCURRENTLY") {
		t.Errorf("CONCURRENTLY: %q", p)
	}
}

func rawSchema(pieces map[string][]ir.RawSQLDef) *ir.Schema {
	s := ir.NewSchema("raw-db", ir.SchemaKindDB)
	for name, defs := range pieces {
		s.Types[name] = &ir.TypeDef{Name: name, Role: ir.RoleDBTable, RawSQL: defs}
	}
	return s
}

// TestOrderRawSQL: the pieces that run after the tables come first, each
// position's in id order, each followed by the pieces that run after it;
// a dialect runs only the pieces that name it, or that name none.
func TestOrderRawSQL(t *testing.T) {
	s := rawSchema(map[string][]ir.RawSQLDef{
		"B": {
			{ID: "zeta", Up: "CREATE OR REPLACE VIEW z AS SELECT 1"},
			{ID: "alpha", Up: "CREATE OR REPLACE VIEW a AS SELECT 1"},
			{ID: "beta", After: "alpha", Up: "CREATE OR REPLACE VIEW b AS SELECT 1", Dialects: []string{"postgres"}},
		},
		"A": {
			{ID: "fn", After: ir.RawSQLAfterTables, Up: "CREATE OR REPLACE FUNCTION f() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql", Dialects: []string{"postgres"}},
			{ID: "gamma", After: "alpha", Up: "CREATE OR REPLACE VIEW g AS SELECT 1"},
		},
	})
	ids := func(dialect string) []string {
		pieces, err := OrderRawSQL(s, dialect)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range pieces {
			out = append(out, p.ID+"@"+p.Position)
		}
		return out
	}
	if got, want := ids("postgres"), []string{"fn@tables", "alpha@indexes", "beta@indexes", "gamma@indexes", "zeta@indexes"}; !slices.Equal(got, want) {
		t.Errorf("postgres order = %v, want %v", got, want)
	}
	if got, want := ids("sqlite"), []string{"alpha@indexes", "gamma@indexes", "zeta@indexes"}; !slices.Equal(got, want) {
		t.Errorf("sqlite order = %v, want %v", got, want)
	}
	pieces, _ := OrderRawSQL(s, "postgres")
	if pieces[1].Checksum != StatementsChecksum([]string{"CREATE OR REPLACE VIEW a AS SELECT 1"}) || pieces[1].Origin != "B" || pieces[2].After != "alpha" {
		t.Errorf("alpha and beta resolve to %+v and %+v", pieces[1], pieces[2])
	}
}

// TestCheckRawSQL: every piece the DDL could not place or run again is
// refused, naming the piece, its type and the fix.
func TestCheckRawSQL(t *testing.T) {
	s := rawSchema(map[string][]ir.RawSQLDef{
		"T": {
			{ID: "Bad-Id", Up: "SELECT 1"},
			{ID: "dup", Up: "CREATE OR REPLACE VIEW v AS SELECT 1"},
			{ID: "dup", Up: "CREATE OR REPLACE VIEW w AS SELECT 1"},
			{ID: "empty", Up: " -- nothing\n;"},
			{ID: "plain", Up: "CREATE OR REPLACE VIEW v AS SELECT 1; CREATE INDEX i ON t (a)"},
			{ID: "unclosed", Up: "CREATE OR REPLACE VIEW v AS SELECT 'x"},
			{ID: "lost", After: "nowhere", Up: "CREATE OR REPLACE VIEW v AS SELECT 1"},
			{ID: "loop_a", After: "loop_b", Up: "CREATE OR REPLACE VIEW a AS SELECT 1"},
			{ID: "loop_b", After: "loop_a", Up: "CREATE OR REPLACE VIEW b AS SELECT 1"},
			{ID: "pg", Dialects: []string{"postgres"}, Up: "CREATE OR REPLACE VIEW p AS SELECT 1"},
			{ID: "everywhere", After: "pg", Up: "CREATE OR REPLACE VIEW e AS SELECT 1"},
			{ID: "mysql", Dialects: []string{"mysql"}, Up: "CREATE OR REPLACE VIEW m AS SELECT 1"},
			{ID: "bad_down", Up: "CREATE OR REPLACE VIEW d AS SELECT 1", Down: "DROP VIEW d"},
		},
	})
	var msgs []string
	for _, err := range CheckRawSQL(s) {
		msgs = append(msgs, err.Error())
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		`T: @rawSql "Bad-Id" needs an id that is a lowercase identifier`,
		`T: @rawSql "dup" has the id of another piece, on T`,
		`T: @rawSql "empty" has no statement in its up`,
		`T: @rawSql "plain" up statement 2 cannot run again over what it made, which a plan does when the piece changes; write it as CREATE OR REPLACE, CREATE ... IF NOT EXISTS`,
		`T: @rawSql "unclosed" up: a string is not closed`,
		`T: @rawSql "lost" runs after "nowhere", which no @rawSql of the service declares`,
		`T: @rawSql "loop_a" runs after itself, through the pieces it runs after`,
		`T: @rawSql "loop_b" runs after itself`,
		`T: @rawSql "everywhere" runs on sqlite, and the piece it runs after, "pg", does not`,
		`T: @rawSql "mysql" names the dialect "mysql"`,
		`T: @rawSql "bad_down" down statement 1 cannot run again`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no error says %q:\n%s", want, all)
		}
	}
	if _, err := OrderRawSQL(s, "postgres"); err == nil {
		t.Error("OrderRawSQL ordered a schema CheckRawSQL refuses")
	}
}
