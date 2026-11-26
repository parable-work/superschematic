package sqlmigrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// variantFixture is the fixture whose Step holds a variant field (D149): its
// result is a VerifyResult while its kind is verify, a ReviewResult while it
// is review, and nothing while it is note.
var variantFixture = filepath.Join(sqlgenFixtures, "fixture-variant-field-db")

// variantRow is one row of step the variant check is asked about: the
// kind, the result's JSON text (nil for NULL), and whether the table takes
// it.
type variantRow struct {
	name   string
	kind   string
	result *string
	ok     bool
}

func text(s string) *string { return &s }

// variantRows are the rows TestVariantChecksOnSQLite and
// TestVariantChecksOnPostgres insert: a row whose result is the member its
// kind names, or none, passes, as one with keys the member does not declare,
// a null optional key or a value a field's own rule refuses, which are the
// validators' to refuse; a result while its kind names no member, a member
// missing a required key, a key of another JSON kind, an enum key's other
// value and a value that is no object are refused by ck_step_result.
var variantRows = []variantRow{
	{"a verify result", "verify", text(`{"passed": true}`), true},
	{"a verify result with its checks", "verify", text(`{"passed": false, "checks": [{"name": "lint", "ok": true}]}`), true},
	{"a review result", "review", text(`{"verdict": "approved", "notes": "fine"}`), true},
	{"a verify step with no result", "verify", nil, true},
	{"a verify step whose result is JSON null", "verify", text(`null`), true},
	{"a note with no result", "note", nil, true},
	{"a review result whose notes are null", "review", text(`{"verdict": "changes_requested", "notes": null}`), true},
	{"a verify result with a key it does not declare", "verify", text(`{"passed": true, "extra": 1}`), true},
	{"a review result whose notes break their maxLength", "review", text(`{"verdict": "approved", "notes": "` + strings.Repeat("n", 300) + `"}`), true},
	{"a note with a result", "note", text(`{"passed": true}`), false},
	{"a verify step with a review result", "verify", text(`{"verdict": "approved"}`), false},
	{"a review step with a verify result", "review", text(`{"passed": true}`), false},
	{"a verify result whose passed is a string", "verify", text(`{"passed": "yes"}`), false},
	{"a verify result whose checks are no list", "verify", text(`{"passed": true, "checks": 5}`), false},
	{"a review result whose verdict is no member", "review", text(`{"verdict": "maybe"}`), false},
	{"a review result whose notes are a number", "review", text(`{"verdict": "approved", "notes": 5}`), false},
	{"a verify result that is no object", "verify", text(`5`), false},
	{"a verify result that is a list", "verify", text(`[]`), false},
}

// variantCase is the rows.json of the case: step's rows, each with its
// kind and result, and the title every insert sets.
func variantCase(withCasts bool) map[string]any {
	rows := make([]map[string]any, len(variantRows))
	for i, row := range variantRows {
		rows[i] = map[string]any{
			"name":   row.name,
			"values": map[string]*string{"kind": text(row.kind), "result": row.result},
			"ok":     row.ok,
		}
	}
	rc := map[string]any{"table": "step", "insert": map[string]string{"title": "'a step'"}, "rows": rows, "constraint": "ck_step_result"}
	if withCasts {
		rc["casts"] = map[string]string{"result": "jsonb"}
	}
	return rc
}

// runRows runs the module's TestRows with env.
func runRows(t *testing.T, module string, env ...string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", module))); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "^TestRows$", "./...")
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOFLAGS=-mod=readonly"), env...)
	out, err := cmd.CombinedOutput()
	t.Logf("%s:\n%s", module, out)
	if err != nil {
		t.Fatalf("%s failed: %v", module, err)
	}
	if !strings.Contains(string(out), "--- PASS: TestRows ") {
		t.Fatalf("%s did not run TestRows", module)
	}
}

// TestVariantChecksOnSQLite inserts variantRows into step on SQLite, from
// the service's sqlite/create.sql (D149): ck_step_result takes the rows
// whose result is the member their kind names, or none, and refuses every
// other with SQLITE_CONSTRAINT_CHECK. testdata/sqliteconverge's TestRows
// holds the checks. It needs no database server.
func TestVariantChecksOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds the SQLite checks")
	}
	cases := t.TempDir()
	model := sqliteModel(t, "fixture-variant-field-db", planCase{base: variantFixture}.load(t, nil))
	writeFile(t, filepath.Join(cases, "variant", "create.sql"), []byte(sqliteCreateSQL(t, model)))
	writeJSON(t, filepath.Join(cases, "variant", "rows.json"), variantCase(false))
	runRows(t, "sqliteconverge", "CGO_ENABLED=0", "SQLITECONVERGE_ROWS="+cases)
}

// TestVariantChecksOnPostgres inserts variantRows into step on a real
// Postgres, from the service's create.sql, as TestVariantChecksOnSQLite
// does on SQLite: every row it refuses fails with check_violation (23514)
// on ck_step_result. testdata/pgconverge's TestRows holds the checks. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as for TestConvergenceOnPostgres.
func TestVariantChecksOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to run the variant check on Postgres")
	}
	cases := t.TempDir()
	out, err := sqlgen.Generate(planCase{base: variantFixture}.load(t, nil), sqlgen.Options{SchemaName: "fixture-variant-field-db"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cases, "variant")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sqlgen.WriteDDL(out, dir); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "rows.json"), variantCase(true))
	runRows(t, "pgconverge", "PGCONVERGE_DATABASE_URL="+dsn, "PGCONVERGE_ROWS="+cases)
}

// TestVariantCheckPlans: a plan places a variant field's check (D149) as it
// places any column's. A field that becomes a variant field over a column
// the previous version has gets the check in contract, data-dependent,
// whose reason names the field and what a row that breaks it holds; a
// change of an arm's member replaces it, dropped in expand and added in
// contract; and a field that stops being one drops it in expand. In both
// dialects; TestReadDatabaseRoundTrip reads the check back from a SQLite
// file as the schema's.
func TestVariantCheckPlans(t *testing.T) {
	// asJSON is the previous version: result is open JSON, no variant.
	asJSON := func(s *ir.Schema) {
		for _, f := range s.Types["Step"].Fields {
			if f.Name == "result" {
				f.TypeRef = ir.TypeRef{Name: "Generic.JSON"}
			}
		}
		s.Scalars["Generic.JSON"] = &ir.ScalarDef{Name: "Generic.JSON", LanguagePrimitive: ir.LanguageObject}
	}
	// reviewOnly drops verify's arm.
	reviewOnly := func(s *ir.Schema) {
		u := s.Unions["StepResult"]
		u.Types = []string{"ReviewResult"}
		u.Arms = map[string]string{"review": "ReviewResult"}
	}
	for _, dialect := range []Dialect{Postgres, SQLite} {
		t.Run(string(dialect), func(t *testing.T) {
			build := func(change func(*ir.Schema)) *Model {
				model, err := BuildModel(planCase{base: variantFixture}.load(t, change), sqlgen.Options{SchemaName: "fixture-variant-field-db"}, dialect)
				if err != nil {
					t.Fatal(err)
				}
				return model
			}
			plain, variant, narrowed := build(asJSON), build(nil), build(reviewOnly)
			// steps are the plan's steps, each as its phase, its op and its
			// subject, and its hazards' classes and reasons.
			steps := func(from, to *Model) string {
				t.Helper()
				plan, err := Diff(from, to, Options{})
				if err != nil {
					t.Fatal(err)
				}
				var out []string
				for _, step := range plan.Steps {
					line := string(step.Phase) + " " + step.Op + " " + step.Subject
					for _, h := range step.Hazards {
						line += " [" + string(h.Class) + ": " + h.Reason + "]"
					}
					out = append(out, line)
				}
				return strings.Join(out, "\n")
			}
			hazard := "[data-dependent: Step.result gets a CHECK in the database: validating ck_step_result fails when a row of step holds a value its kind names no member for"
			want := map[Dialect]struct{ added, replaced, dropped []string }{
				// Postgres adds a check NOT VALID and validates it in a step
				// of its own (D83).
				Postgres: {
					added:    []string{"contract addCheck table/step/constraint/ck_step_result", "contract validateCheck table/step/constraint/ck_step_result " + hazard, "(verify, review).]"},
					replaced: []string{"expand dropCheck table/step/constraint/ck_step_result", "contract addCheck table/step/constraint/ck_step_result", "(review).]"},
					dropped:  []string{"expand dropCheck table/step/constraint/ck_step_result"},
				},
				// SQLite keeps a check in its column's definition, so the
				// table is rebuilt to change it (D83).
				SQLite: {
					added:    []string{hazard, "(verify, review).]"},
					replaced: []string{"(review).]"},
					dropped:  []string{"table/step"},
				},
			}[dialect]
			for _, tc := range []struct {
				what     string
				from, to *Model
				want     []string
			}{
				{"the check over a column the previous version has", plain, variant, want.added},
				{"a change of the arms", variant, narrowed, want.replaced},
				{"a field that stops being a variant", variant, plain, want.dropped},
			} {
				got := steps(tc.from, tc.to)
				for _, text := range tc.want {
					if !strings.Contains(got, text) {
						t.Errorf("%s: the plan lacks %q:\n%s", tc.what, text, got)
					}
				}
			}
			if none := steps(variant, build(nil)); none != "" {
				t.Errorf("the same schema plans:\n%s", none)
			}
		})
	}
}
