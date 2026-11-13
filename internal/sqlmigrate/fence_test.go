package sqlmigrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// The tests of a @fenced counter's triggers (D118): what they refuse on a
// real Postgres and a real SQLite, and where a plan places them, by the
// rule of a trigger that refuses a write (Trigger.Refuses, D117).

// fencedFixture is the service whose tables declare fences: Parcel one
// over the row, Delivery two, each over its own columns, a relation's key
// among them, and Depot one over a name and a nullable timestamp. Courier
// has none.
var fencedFixture = filepath.Join(sqlgenFixtures, "fixture-fenced-db")

// fenceWrite is one statement TestFences of testdata/pgconverge and of
// testdata/sqliteconverge runs, and what it must do. A write a fence
// refuses names the fence, as Type.field, and what its Postgres message
// says after the column's name; one that passes names neither. After
// either, Query's rows are Want: each row's columns joined by "|", NULL
// for a null, and the rows by "; ". A refused write's Want is what the
// rows held before it.
type fenceWrite struct {
	Name  string `json:"name"`
	SQL   string `json:"sql"`
	Fence string `json:"-"`
	Says  string `json:"-"`
	Query string `json:"query"`
	Want  string `json:"want"`

	// Constraint is the constraint Postgres's error names, and Message the
	// dialect's message: Postgres's whole, and SQLite's fixed one. The
	// test fills them from the fence sqlgen built.
	Constraint string `json:"constraint,omitempty"`
	Message    string `json:"message,omitempty"`
}

// fenceScript collects a case's writes, and remembers what each query's
// rows held last, which a refused write must leave.
type fenceScript struct {
	writes []fenceWrite
	held   map[string]string
}

// pass adds a write every fence allows, after which query's rows are want.
func (s *fenceScript) pass(name, sql, query, want string) {
	if s.held == nil {
		s.held = map[string]string{}
	}
	s.held[query] = want
	s.writes = append(s.writes, fenceWrite{Name: name, SQL: sql, Query: query, Want: want})
}

// refuse adds a write the fence refuses, saying says, which leaves
// query's rows as they were.
func (s *fenceScript) refuse(name, sql, fence, says, query string) {
	s.writes = append(s.writes, fenceWrite{Name: name, SQL: sql, Fence: fence, Says: says, Query: query, Want: s.held[query]})
}

// fenceWrites are the writes the fixture's fences are tried with, in both
// dialects, every identifier quoted: among them the statements the
// generated repositories send, which add one to a fence whenever they set
// a column it covers, the value written or not, and write a caller's own
// value for the fence as it stands.
func fenceWrites() []fenceWrite {
	id := func(n int) string { return fmt.Sprintf("'00000000-0000-4000-8000-%012d'", n) }
	bare := func(literal string) string { return strings.Trim(literal, "'") }
	p1, p2, p3 := id(1), id(2), id(3)
	d1, d2 := id(11), id(12)
	c1, c2 := id(21), id(22)
	e1 := id(31)
	var s fenceScript

	// Parcel.version lists no covers: every update of a row adds one.
	const parcels = `SELECT "version", "weight_grams", "note" FROM "parcel" ORDER BY "id"`
	parcel := func(set string) string { return `UPDATE "parcel" SET ` + set + ` WHERE "id" = ` + p1 }
	s.pass("an insert of a parcel at the default",
		`INSERT INTO "parcel" ("id", "tracking_code", "weight_grams") VALUES (`+p1+`, 'T-1', 120)`, parcels, "0|120|NULL")
	s.pass("an insert of a parcel at any counter value",
		`INSERT INTO "parcel" ("id", "tracking_code", "weight_grams", "version") VALUES (`+p2+`, 'T-2', 500, 41)`, parcels, "0|120|NULL; 41|500|NULL")
	s.refuse("an update of a parcel that leaves version", parcel(`"note" = 'fragile'`),
		"Parcel.version", "must advance from 0 to 1, not to 0", parcels)
	s.pass("an update of a parcel that adds one", parcel(`"note" = 'fragile', "version" = "version" + 1`), parcels, "1|120|fragile; 41|500|NULL")
	s.pass("an update of a parcel that sets a column to the value it holds and adds one",
		parcel(`"weight_grams" = 120, "version" = "version" + 1`), parcels, "2|120|fragile; 41|500|NULL")
	s.pass("an update of version alone, by one", parcel(`"version" = "version" + 1`), parcels, "3|120|fragile; 41|500|NULL")
	s.refuse("an update of a parcel that adds two", parcel(`"note" = 'heavy', "version" = "version" + 2`),
		"Parcel.version", "must advance from 3 to 4, not to 5", parcels)
	s.refuse("an update of a parcel that moves version back", parcel(`"note" = 'heavy', "version" = "version" - 1`),
		"Parcel.version", "must advance from 3 to 4, not to 2", parcels)
	s.refuse("an update of version to the value it holds", parcel(`"version" = "version"`),
		"Parcel.version", "must advance from 3 to 4, not to 3", parcels)
	s.refuse("an update of version to null", parcel(`"version" = NULL`),
		"Parcel.version", "must advance from 3 to 4, not to <NULL>", parcels)
	s.pass("a writer that read version 3 and writes 4 where it is 3",
		parcel(`"note" = 'signed', "version" = 4`)+` AND "version" = 3`, parcels, "4|120|signed; 41|500|NULL")
	upsert := `INSERT INTO "parcel" ("id", "tracking_code", "weight_grams") VALUES (` + p3 + `, 'T-1', 130) ON CONFLICT ("tracking_code") DO UPDATE SET "weight_grams" = excluded."weight_grams"`
	s.refuse("an upsert whose update leaves version", upsert, "Parcel.version", "must advance from 4 to 5, not to 4", parcels)
	s.pass("an upsert whose update adds one", upsert+`, "version" = "parcel"."version" + 1`, parcels, "5|130|signed; 41|500|NULL")
	s.pass("an update of every parcel that adds one to each",
		`UPDATE "parcel" SET "note" = 'counted', "version" = "version" + 1`, parcels, "6|130|counted; 42|500|counted")
	s.refuse("an update of every parcel that adds one to the first alone",
		`UPDATE "parcel" SET "note" = 'recount', "version" = CASE WHEN "id" = `+p1+` THEN "version" + 1 ELSE "version" END`,
		"Parcel.version", "must advance from 42 to 43, not to 42", parcels)

	// Delivery.planVersion covers address and courier, a relation, and
	// Delivery.runVersion attempts and proof; note and updatedAt are no
	// fence's.
	columns := `SELECT "plan_version", "run_version", "address", "courier_id", "attempts", "proof", "note" FROM "delivery" WHERE "id" = `
	first, second := columns+d1, columns+d2
	delivery := func(set string) string { return `UPDATE "delivery" SET ` + set + ` WHERE "id" = ` + d1 }
	s.pass("an insert of the couriers", `INSERT INTO "courier" ("id", "name") VALUES (`+c1+`, 'Ada'), (`+c2+`, 'Bo')`, first, "")
	s.pass("an insert of a delivery at the defaults",
		`INSERT INTO "delivery" ("id", "address", "courier_id", "updated_at") VALUES (`+d1+`, '1 Quay', `+c1+`, '2026-01-01T00:00:00Z')`,
		first, "0|0|1 Quay|"+bare(c1)+"|0|NULL|NULL")
	s.pass("an insert of a delivery at any counter values",
		`INSERT INTO "delivery" ("id", "address", "plan_version", "run_version", "updated_at") VALUES (`+d2+`, '9 Mill', 7, 3, '2026-01-01T00:00:00Z')`,
		second, "7|3|9 Mill|NULL|0|NULL|NULL")
	s.pass("an update of the columns no fence covers",
		delivery(`"note" = 'ring twice', "updated_at" = '2026-01-02T00:00:00Z'`), first, "0|0|1 Quay|"+bare(c1)+"|0|NULL|ring twice")
	s.refuse("a change of address that leaves planVersion", delivery(`"address" = '2 Quay'`),
		"Delivery.planVersion", "must advance from 0 to 1, not to 0", first)
	s.pass("a change of address that adds one to planVersion",
		delivery(`"address" = '2 Quay', "plan_version" = "plan_version" + 1`), first, "1|0|2 Quay|"+bare(c1)+"|0|NULL|ring twice")
	s.refuse("a change of address that adds two", delivery(`"address" = '3 Quay', "plan_version" = "plan_version" + 2`),
		"Delivery.planVersion", "must advance from 1 to 2, not to 3", first)
	s.refuse("a change of address that moves planVersion back", delivery(`"address" = '3 Quay', "plan_version" = "plan_version" - 1`),
		"Delivery.planVersion", "must advance from 1 to 2, not to 0", first)
	s.refuse("a change of address that adds one to runVersion in its place", delivery(`"address" = '3 Quay', "run_version" = "run_version" + 1`),
		"Delivery.planVersion", "must advance from 1 to 2, not to 1", first)
	s.pass("a change of attempts that adds one to runVersion, and leaves planVersion",
		delivery(`"attempts" = 1, "run_version" = "run_version" + 1`), first, "1|1|2 Quay|"+bare(c1)+"|1|NULL|ring twice")
	s.refuse("a change of attempts that leaves runVersion", delivery(`"attempts" = 2`),
		"Delivery.runVersion", "must advance from 1 to 2, not to 1", first)
	s.refuse("a change of address and attempts that adds one to planVersion alone",
		delivery(`"address" = '3 Quay', "attempts" = 2, "plan_version" = "plan_version" + 1`),
		"Delivery.runVersion", "must advance from 1 to 2, not to 1", first)
	s.refuse("a change of address and attempts that adds one to runVersion alone",
		delivery(`"address" = '3 Quay', "attempts" = 2, "run_version" = "run_version" + 1`),
		"Delivery.planVersion", "must advance from 1 to 2, not to 1", first)
	s.pass("a change of address and attempts that adds one to each",
		delivery(`"address" = '3 Quay', "attempts" = 2, "plan_version" = "plan_version" + 1, "run_version" = "run_version" + 1`),
		first, "2|2|3 Quay|"+bare(c1)+"|2|NULL|ring twice")
	s.refuse("a change of the courier, a relation's key, that leaves planVersion", delivery(`"courier_id" = `+c2),
		"Delivery.planVersion", "must advance from 2 to 3, not to 2", first)
	s.pass("a change of the courier that adds one",
		delivery(`"courier_id" = `+c2+`, "plan_version" = "plan_version" + 1`), first, "3|2|3 Quay|"+bare(c2)+"|2|NULL|ring twice")
	s.refuse("a courier set to null that leaves planVersion", delivery(`"courier_id" = NULL`),
		"Delivery.planVersion", "must advance from 3 to 4, not to 3", first)
	s.pass("a courier set to null that adds one",
		delivery(`"courier_id" = NULL, "plan_version" = "plan_version" + 1`), first, "4|2|3 Quay|NULL|2|NULL|ring twice")
	s.refuse("a courier set from null that leaves planVersion", delivery(`"courier_id" = `+c1),
		"Delivery.planVersion", "must advance from 4 to 5, not to 4", first)
	s.pass("a courier set from null that adds one",
		delivery(`"courier_id" = `+c1+`, "plan_version" = "plan_version" + 1`), first, "5|2|3 Quay|"+bare(c1)+"|2|NULL|ring twice")
	s.refuse("a proof set from null that leaves runVersion", delivery(`"proof" = 'photo'`),
		"Delivery.runVersion", "must advance from 2 to 3, not to 2", first)
	s.pass("a proof set from null that adds one",
		delivery(`"proof" = 'photo', "run_version" = "run_version" + 1`), first, "5|3|3 Quay|"+bare(c1)+"|2|photo|ring twice")
	s.refuse("a proof set to null that leaves runVersion", delivery(`"proof" = NULL`),
		"Delivery.runVersion", "must advance from 3 to 4, not to 3", first)
	s.pass("a proof set to null that adds one",
		delivery(`"proof" = NULL, "run_version" = "run_version" + 1`), first, "5|4|3 Quay|"+bare(c1)+"|2|NULL|ring twice")
	// What a generated repository sends: the bump with a covered column,
	// the value it holds or another, and a caller's own value of the fence
	// as it stands.
	s.pass("a covered column set to the value it holds, with the bump",
		delivery(`"address" = '3 Quay', "plan_version" = "plan_version" + 1`), first, "6|4|3 Quay|"+bare(c1)+"|2|NULL|ring twice")
	s.pass("a covered column set to the value it holds, without it",
		delivery(`"address" = '3 Quay'`), first, "6|4|3 Quay|"+bare(c1)+"|2|NULL|ring twice")
	s.pass("an update of planVersion alone, by one",
		delivery(`"plan_version" = "plan_version" + 1`), first, "7|4|3 Quay|"+bare(c1)+"|2|NULL|ring twice")
	s.refuse("an update of planVersion alone, by two", delivery(`"plan_version" = "plan_version" + 2`),
		"Delivery.planVersion", "must stay at 7 or advance to 8, not move to 9", first)
	s.refuse("an update of planVersion alone, back", delivery(`"plan_version" = "plan_version" - 1`),
		"Delivery.planVersion", "must stay at 7 or advance to 8, not move to 6", first)
	s.refuse("an update of planVersion alone, to null", delivery(`"plan_version" = NULL`),
		"Delivery.planVersion", "must stay at 7 or advance to 8, not move to <NULL>", first)
	s.pass("an update of a column no fence covers that adds one to runVersion",
		delivery(`"note" = 'gate code 4', "run_version" = "run_version" + 1`), first, "7|5|3 Quay|"+bare(c1)+"|2|NULL|gate code 4")
	s.refuse("an update of a column no fence covers that moves runVersion back", delivery(`"note" = 'no gate', "run_version" = 0`),
		"Delivery.runVersion", "must stay at 5 or advance to 6, not move to 0", first)
	other := func(set string) string { return `UPDATE "delivery" SET ` + set + ` WHERE "id" = ` + d2 }
	s.pass("a writer that read planVersion 7 and writes 8 where it is 7",
		other(`"address" = '10 Mill', "plan_version" = 8`)+` AND "plan_version" = 7`, second, "8|3|10 Mill|NULL|0|NULL|NULL")
	s.refuse("a writer that writes the planVersion the row holds", other(`"address" = '11 Mill', "plan_version" = 8`),
		"Delivery.planVersion", "must advance from 8 to 9, not to 8", second)
	// The key's ON DELETE SET NULL is an update of courier_id that adds
	// nothing, so the fence refuses the delete of a courier a delivery
	// names, in both dialects.
	s.refuse("a delete of a courier a delivery names, which sets its courier to null", `DELETE FROM "courier" WHERE "id" = `+c1,
		"Delivery.planVersion", "must advance from 7 to 8, not to 7", first)
	s.pass("a delete of a courier no delivery names", `DELETE FROM "courier" WHERE "id" = `+c2,
		`SELECT "name" FROM "courier"`, "Ada")

	// Depot.revision covers name and deletedAt, which a soft delete sets.
	const depots = `SELECT "revision", "name", "capacity", CASE WHEN "deleted_at" IS NULL THEN 'live' ELSE 'deleted' END FROM "depot"`
	depot := func(set string) string { return `UPDATE "depot" SET ` + set + ` WHERE "id" = ` + e1 }
	s.pass("an insert of a depot", `INSERT INTO "depot" ("id", "name", "capacity") VALUES (`+e1+`, 'North', 40)`, depots, "0|North|40|live")
	s.pass("a change of capacity, which the fence does not cover", depot(`"capacity" = 60`), depots, "0|North|60|live")
	s.refuse("a soft delete that leaves revision", depot(`"deleted_at" = '2026-01-03T00:00:00Z'`),
		"Depot.revision", "must advance from 0 to 1, not to 0", depots)
	s.pass("a soft delete that adds one", depot(`"deleted_at" = '2026-01-03T00:00:00Z', "revision" = "revision" + 1`), depots, "1|North|60|deleted")
	s.refuse("a restore that leaves revision", depot(`"deleted_at" = NULL`),
		"Depot.revision", "must advance from 1 to 2, not to 1", depots)
	s.pass("a restore that adds one", depot(`"deleted_at" = NULL, "revision" = "revision" + 1`), depots, "2|North|60|live")
	s.refuse("a rename that leaves revision", depot(`"name" = 'South'`),
		"Depot.revision", "must advance from 2 to 3, not to 2", depots)
	s.pass("a rename that adds one", depot(`"name" = 'South', "revision" = "revision" + 1`), depots, "3|South|60|live")
	return s.writes
}

// oddFenceNames are the names respellFences gives the fixture's tables and
// columns, each quoted, by the name the fixture has: a keyword in both
// dialects for a table, for a fence's column and for a covered one, and a
// fence's column that holds a quote, a space and a percent sign, which a
// plpgsql RAISE reads as a parameter's place.
var oddFenceNames = []string{
	`"parcel"`, `"order"`,
	`"version"`, `"it's 100%"`,
	`"plan_version"`, `"select"`,
	`"address"`, `"group"`,
}

// respellFences stores the fixture's tables and columns under
// oddFenceNames.
func respellFences(s *ir.Schema) {
	typeNamed(s, "Parcel").SQLName = "order"
	fieldNamed(s, "Parcel", "version").SQLName = "it's 100%"
	fieldNamed(s, "Delivery", "planVersion").SQLName = "select"
	fieldNamed(s, "Delivery", "address").SQLName = "group"
}

// optimisticFences makes the fixture's fenced tables @optimistic, whose
// _version the database bumps on every update: on Postgres in a BEFORE
// trigger, and on SQLite in an update of its own after the write, which a
// fence's trigger hears and must pass, since it changes no covered column
// and leaves the fence. So a fence without covers is refused beside
// @optimistic in a service that lists sqlite, and Parcel is @optimistic on
// Postgres alone.
func optimisticFences(dialect Dialect) func(*ir.Schema) {
	return func(s *ir.Schema) {
		typeNamed(s, "Delivery").Optimistic = true
		typeNamed(s, "Depot").Optimistic = true
		typeNamed(s, "Parcel").Optimistic = dialect == Postgres
	}
}

// fenceCases are the services whose fences run, each with its writes: the
// fixture, the fixture under oddFenceNames, and the fixture with its
// tables @optimistic. A refused write's constraint and messages are those
// of the fence sqlgen builds.
func fenceCases(t *testing.T, dialect Dialect) map[string]struct {
	schema *ir.Schema
	writes []fenceWrite
} {
	t.Helper()
	load := planCase{base: fencedFixture}.load
	out := map[string]struct {
		schema *ir.Schema
		writes []fenceWrite
	}{}
	for name, c := range map[string]struct {
		change  func(*ir.Schema)
		respell *strings.Replacer
	}{
		"fixture":    {nil, strings.NewReplacer()},
		"odd-names":  {respellFences, strings.NewReplacer(oddFenceNames...)},
		"optimistic": {optimisticFences(dialect), strings.NewReplacer()},
	} {
		schema := load(t, c.change)
		ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: "fixture-fenced-db"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fences := map[string]sqlgen.Fence{}
		for _, f := range ddl.Fences {
			fences[f.Origin+"."+f.Field] = f
		}
		writes := fenceWrites()
		for i := range writes {
			w := &writes[i]
			w.SQL, w.Query = c.respell.Replace(w.SQL), c.respell.Replace(w.Query)
			if w.Fence == "" {
				continue
			}
			f, ok := fences[w.Fence]
			if !ok {
				t.Fatalf("%s: the service has no fence %s", name, w.Fence)
			}
			w.Constraint, w.Message = f.FunctionName, f.Table+"."+f.Column+" "+w.Says
			if dialect == SQLite {
				w.Constraint, w.Message = "", f.SQLiteMessage()
			}
		}
		out[name] = struct {
			schema *ir.Schema
			writes []fenceWrite
		}{schema, writes}
	}
	return out
}

// runFences runs TestFences of the module under testdata/<module> with env
// added to the environment.
func runFences(t *testing.T, module string, env ...string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", module))); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "^TestFences$", "./...")
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOFLAGS=-mod=readonly"), env...)
	out, err := cmd.CombinedOutput()
	t.Logf("%s:\n%s", module, out)
	if err != nil {
		t.Fatalf("%s failed: %v", module, err)
	}
	if !strings.Contains(string(out), "--- PASS: TestFences ") {
		t.Fatalf("%s did not run TestFences", module)
	}
}

// TestFencesOnPostgres runs the fixture's fences on a real Postgres, from
// the service's create.sql (D118). A write that changes a covered column
// passes when it sets the fence to the stored value plus one, and is
// refused when it leaves the fence, adds two or moves it back, with
// check_violation (23514), the fence's constraint and the three values,
// and the row is left as it was. A write of columns no fence covers passes
// with the fences untouched; each of Delivery's fences counts its own
// columns, and a write of both parties' needs both advanced; a relation's
// key, a null set and a null cleared are changes; a fence without covers
// counts every update; an insert passes at any value; and drop.sql drops
// the fences. testdata/pgconverge's TestFences holds the checks. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as for
// TestConvergenceOnPostgres.
func TestFencesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to run the fences on Postgres")
	}
	cases := t.TempDir()
	for name, c := range fenceCases(t, Postgres) {
		out, err := sqlgen.Generate(c.schema, sqlgen.Options{SchemaName: "fixture-fenced-db"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		dir := filepath.Join(cases, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := sqlgen.WriteDDL(out, dir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		writeJSON(t, filepath.Join(dir, "fences.json"), map[string]any{"writes": c.writes})
	}
	runFences(t, "pgconverge", "PGCONVERGE_DATABASE_URL="+dsn, "PGCONVERGE_FENCES="+cases)
}

// TestFencesOnSQLite runs the fixture's fences on SQLite, from the
// service's sqlite/create.sql, as TestFencesOnPostgres runs them on
// Postgres: a write a fence does not allow fails with the fence's fixed
// message, as SQLITE_CONSTRAINT_TRIGGER. testdata/sqliteconverge's
// TestFences holds the checks. It needs no database server.
func TestFencesOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds the SQLite checks")
	}
	cases := t.TempDir()
	for name, c := range fenceCases(t, SQLite) {
		model := sqliteModel(t, "fixture-fenced-db", c.schema)
		writeFile(t, filepath.Join(cases, name, "create.sql"), []byte(sqliteCreateSQL(t, model)))
		writeJSON(t, filepath.Join(cases, name, "fences.json"), map[string]any{"writes": c.writes})
	}
	runFences(t, "sqliteconverge", "CGO_ENABLED=0", "SQLITECONVERGE_FENCES="+cases)
}

// TestFencePhases: a plan places a fence's trigger by the rule of a
// trigger that refuses a write (D117, D118), in both dialects. A new one
// is created in contract, when no server of the previous version, whose
// repositories advance nothing, is left; Postgres creates its function in
// expand, where nothing executes it. A fence whose covers change is
// dropped in expand, before its function is replaced, and created in
// contract. A removed one is dropped in contract. No step has a hazard.
func TestFencePhases(t *testing.T) {
	const function, trigger = "function/order_revision_fence", "trigger/order/trg_order_revision_fence"
	for _, tc := range []struct {
		plan    string
		dialect Dialect
		want    []string
	}{
		{"add-fence", Postgres, []string{"expand createFunction " + function, "contract createTrigger " + trigger}},
		{"add-fence", SQLite, []string{"contract createTrigger " + trigger}},
		{"change-fence-covers", Postgres, []string{"expand dropTrigger " + trigger, "expand replaceFunction " + function, "contract createTrigger " + trigger}},
		{"change-fence-covers", SQLite, []string{"expand dropTrigger " + trigger, "contract createTrigger " + trigger}},
		{"drop-fence", Postgres, []string{"contract dropTrigger " + trigger, "contract dropFunction " + function}},
		{"drop-fence", SQLite, []string{"contract dropTrigger " + trigger}},
	} {
		t.Run(tc.plan+"/"+string(tc.dialect), func(t *testing.T) {
			plan := planCaseNamed(t, tc.plan, tc.dialect).plan(t)
			if got := stepLines(plan); !slices.Equal(got, tc.want) {
				t.Errorf("the plan's steps are\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
			for _, step := range plan.Steps {
				if len(step.Hazards) > 0 {
					t.Errorf("step %d %s %s has hazards: %+v", step.Index, step.Op, step.Subject, step.Hazards[0])
				}
			}
			// Between the phases the database holds no fence: neither the
			// one contract creates nor the one expand dropped.
			if len(plan.ExpandedModel) > 0 && tc.plan != "drop-fence" {
				for _, tr := range decodeModel(t, plan.ExpandedModel).Triggers {
					if strings.HasSuffix(tr.Name, "_fence") {
						t.Errorf("the model between the phases holds the trigger %s", tr.Name)
					}
				}
			}
		})
	}
}

// TestFenceFollowsRenames: a fence's trigger is named after its table and
// its column, and its rule names the columns it covers. A rename of any of
// them changes it: the plan drops the trigger under the name it had, once
// the rename has run and before a function is created or replaced, and
// creates the new one in contract. A covered column's rename keeps both
// names: on Postgres the plan knows the trigger changed by the function it
// executes, which is replaced. Postgres's function of a renamed fence
// column is another function, the old one dropped in contract; of a
// renamed table it is renamed, then replaced once the trigger is dropped.
func TestFenceFollowsRenames(t *testing.T) {
	for _, tc := range []struct {
		plan    string
		dialect Dialect
		// renames are the steps the trigger's drop follows, and functions
		// the ones that follow it.
		renames   []string
		drop      string
		functions []string
		create    string
		// rule is text the new trigger's rule holds: its statement on
		// SQLite, and its function's on Postgres.
		rule string
	}{
		{plan: "rename-fence-covered-column", dialect: Postgres,
			renames:   []string{"expand renameColumn table/order/column/remark"},
			drop:      "expand dropTrigger trigger/order/trg_order_revision_fence",
			functions: []string{"expand replaceFunction function/order_revision_fence"},
			create:    "contract createTrigger trigger/order/trg_order_revision_fence",
			rule:      "ROW(NEW.remark, NEW.customer_id) IS DISTINCT FROM ROW(OLD.remark, OLD.customer_id)"},
		{plan: "rename-fence-covered-column", dialect: SQLite,
			renames: []string{"expand renameColumn table/order/column/remark"},
			drop:    "expand dropTrigger trigger/order/trg_order_revision_fence",
			create:  "contract createTrigger trigger/order/trg_order_revision_fence",
			rule:    `OR NEW."remark" IS NOT OLD."remark"`},
		{plan: "rename-fence-column", dialect: Postgres,
			renames: []string{"expand renameColumn table/order/column/rev"},
			drop:    "expand dropTrigger trigger/order/trg_order_revision_fence",
			functions: []string{"expand createFunction function/order_rev_fence",
				"contract dropFunction function/order_revision_fence"},
			create: "contract createTrigger trigger/order/trg_order_rev_fence",
			rule:   "IF NEW.rev IS DISTINCT FROM OLD.rev + 1 THEN"},
		{plan: "rename-fence-column", dialect: SQLite,
			renames: []string{"expand renameColumn table/order/column/rev"},
			drop:    "expand dropTrigger trigger/order/trg_order_revision_fence",
			create:  "contract createTrigger trigger/order/trg_order_rev_fence",
			rule:    `WHEN NEW."rev" IS NOT OLD."rev" + 1 AND (`},
		// @table names the table purchaseOrder as written, so its fence's
		// names are quoted.
		{plan: "rename-fence-table", dialect: Postgres,
			renames: []string{"expand renameTable table/purchaseOrder",
				"expand renameFunction function/purchaseOrder_revision_fence"},
			drop:      "expand dropTrigger trigger/purchaseOrder/trg_order_revision_fence",
			functions: []string{"expand replaceFunction function/purchaseOrder_revision_fence"},
			create:    "contract createTrigger trigger/purchaseOrder/trg_purchaseOrder_revision_fence",
			rule:      "CONSTRAINT = 'purchaseOrder_revision_fence'"},
		{plan: "rename-fence-table", dialect: SQLite,
			renames: []string{"expand renameTable table/purchaseOrder"},
			drop:    "expand dropTrigger trigger/purchaseOrder/trg_order_revision_fence",
			create:  "contract createTrigger trigger/purchaseOrder/trg_purchaseOrder_revision_fence",
			rule:    `BEFORE UPDATE ON "purchaseOrder" FOR EACH ROW`},
	} {
		t.Run(tc.plan+"/"+string(tc.dialect), func(t *testing.T) {
			plan := planCaseNamed(t, tc.plan, tc.dialect).plan(t)
			lines := stepLines(plan)
			at := func(line string) int {
				i := slices.Index(lines, line)
				if i < 0 {
					t.Fatalf("the plan has no step %q:\n  %s", line, strings.Join(lines, "\n  "))
				}
				return i
			}
			drop := at(tc.drop)
			for _, rename := range tc.renames {
				if i := at(rename); i > drop {
					t.Errorf("%q is step %d, after the trigger is dropped in step %d", rename, i+1, drop+1)
				}
			}
			rule := plan.Steps[at(tc.create)]
			for _, function := range tc.functions {
				i := at(function)
				if i < drop {
					t.Errorf("%q is step %d, before the trigger is dropped in step %d", function, i+1, drop+1)
				}
				if plan.Steps[i].Op != string(opDropFunction) {
					rule = plan.Steps[i]
				}
			}
			if statements := strings.Join(rule.Statements, ";\n"); !strings.Contains(statements, tc.rule) {
				t.Errorf("step %d %s %s lacks %q:\n%s", rule.Index, rule.Op, rule.Subject, tc.rule, statements)
			}
			fences := 0
			for _, line := range lines {
				if strings.Contains(line, "Trigger trigger/") && strings.HasSuffix(line, "_fence") {
					fences++
				}
			}
			if fences != 2 {
				t.Errorf("the plan has %d steps of the fence's trigger, want its drop and its creation:\n  %s", fences, strings.Join(lines, "\n  "))
			}
		})
	}
}

// TestFencesOfAnEmptyDatabase: the fixture's model holds a trigger that
// refuses a write for each fence, and the plan from an empty database,
// which create.sql is, creates each in expand, with its table, and has no
// contract step (D118).
func TestFencesOfAnEmptyDatabase(t *testing.T) {
	schema := planCase{base: fencedFixture}.load(t, nil)
	declared := 0
	for _, td := range schema.Types {
		declared += len(td.Fences)
	}
	if declared < 4 {
		t.Fatalf("the fixture declares %d fences, want Parcel's, Delivery's two and Depot's", declared)
	}
	for _, dialect := range []Dialect{Postgres, SQLite} {
		model, err := BuildModel(schema, sqlgen.Options{SchemaName: "fixture-fenced-db"}, dialect)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, trigger := range model.Triggers {
			if !strings.HasSuffix(trigger.Name, "_fence") {
				continue
			}
			if !trigger.Refuses {
				t.Errorf("%s: the model's trigger %s of %s does not refuse a write", dialect, trigger.Name, trigger.Origin)
			}
			names = append(names, trigger.Name)
		}
		if len(names) != declared {
			t.Errorf("%s: the model has the fences %v, want the %d the fixture declares", dialect, names, declared)
		}
		for _, want := range []string{"trg_delivery_plan_version_fence", "trg_delivery_run_version_fence", "trg_depot_revision_fence", "trg_parcel_version_fence"} {
			if !slices.Contains(names, want) {
				t.Errorf("%s: the model has no trigger %s: %v", dialect, want, names)
			}
		}
		plan, err := Diff(nil, model, Options{})
		if err != nil {
			t.Fatal(err)
		}
		created := 0
		for _, step := range plan.Steps {
			if step.Phase != Expand {
				t.Errorf("%s: step %d %s %s is in %s", dialect, step.Index, step.Op, step.Subject, step.Phase)
			}
			if step.Op == string(opCreateTrigger) && strings.HasSuffix(step.Subject, "_fence") {
				created++
			}
		}
		if created != declared {
			t.Errorf("%s: the plan creates %d fences, want %d", dialect, created, declared)
		}
	}
}
