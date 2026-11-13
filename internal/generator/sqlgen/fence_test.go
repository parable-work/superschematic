package sqlgen

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// fenceSchema is two tables: Owner, and typeName, which refers to an
// Owner and whose integer field fieldName is a fence over the fields of
// covers, or over the row when there are none.
func fenceSchema(typeName, fieldName string, covers ...string) *ir.Schema {
	s := ir.NewSchema("fences-db", ir.SchemaKindDB)
	s.Types["Owner"] = &ir.TypeDef{Name: "Owner", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
	}}
	s.Types[typeName] = &ir.TypeDef{Name: typeName, Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "order", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "owner", TypeRef: ir.TypeRef{Name: "Owner"}, Relation: &ir.RelationDef{Type: "Owner"}},
		{Name: fieldName, TypeRef: ir.TypeRef{Name: "number"}, Required: true},
	}, Fences: []*ir.Fence{{Field: fieldName, Covers: covers}}}
	return s
}

// TestFencesOfTheFixture: each @fenced declaration of a table gets a
// trigger, by table and column, named after both, that fires before every
// update of a row and refuses a write; its covers are the columns of the
// fields the declaration lists, a relation's its key's; and a table
// without a fence, Courier, gets none (D118).
func TestFencesOfTheFixture(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-fenced-db"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-fenced-db"})
	if err != nil {
		t.Fatal(err)
	}
	covers := map[string]string{}
	var names []string
	for _, f := range out.Fences {
		if f.FunctionName != f.Table+"_"+f.Column+"_fence" || f.TriggerName != "trg_"+f.FunctionName {
			t.Errorf("the fence of %s.%s is the function %s and the trigger %s", f.Table, f.Column, f.FunctionName, f.TriggerName)
		}
		if decl := schema.Types[f.Origin].FenceOf(f.Field); decl == nil || len(decl.Covers) != len(f.Covers) || decl.CoversRow() != f.CoversRow() {
			t.Errorf("the fence %s is of %s.%s, which declares %+v", f.TriggerName, f.Origin, f.Field, decl)
		}
		trigger := f.Trigger()
		if !trigger.Refuses || trigger.Timing != "BEFORE UPDATE" || trigger.Function != f.FunctionName || trigger.Table != f.Table {
			t.Errorf("the trigger of %s is %+v", f.TriggerName, trigger)
		}
		if sqlite := f.SQLiteTrigger(sqlutil.QuoteIdentifier); !sqlite.Refuses || sqlite.Name != f.TriggerName || sqlite.Table != f.Table {
			t.Errorf("the SQLite trigger of %s is %+v", f.TriggerName, sqlite)
		}
		var columns []string
		for _, c := range f.Covers {
			columns = append(columns, c.Name)
		}
		covers[f.TriggerName] = strings.Join(columns, ",")
		names = append(names, f.TriggerName)
		if f.Table == "courier" {
			t.Errorf("courier, which declares no fence, has the trigger %s", f.TriggerName)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("the fences are not by table and column: %v", names)
	}
	for name, want := range map[string]string{
		"trg_delivery_plan_version_fence": "address,courier_id",
		"trg_delivery_run_version_fence":  "attempts,proof",
		"trg_depot_revision_fence":        "name,deleted_at",
		"trg_parcel_version_fence":        "",
	} {
		if got, ok := covers[name]; !ok || got != want {
			t.Errorf("the fence %s covers %q (there: %v), want %q", name, got, ok, want)
		}
	}
}

// TestFenceRule: the two dialects write one rule. A fence with covers
// compares them as one row on Postgres and column by column with IS NOT on
// SQLite: a write that changes one sets the fence to the stored value plus
// one, and any other keeps it or adds one. A fence without covers holds
// every update to the stored value plus one. Postgres raises
// check_violation with the fence's constraint and the stored, the expected
// and the written value, and SQLite fails with a fixed message (D118).
func TestFenceRule(t *testing.T) {
	quoted := func(name string) string { return `"` + name + `"` }
	f := Fence{
		Origin: "Delivery", Field: "planVersion", Table: "delivery", QuotedTable: "delivery",
		Column: "plan_version", QuotedColumn: "plan_version",
		FunctionName: "delivery_plan_version_fence", TriggerName: "trg_delivery_plan_version_fence",
		Covers: []FenceColumn{{Name: "address", QuotedName: "address"}, {Name: "courier_id", QuotedName: "courier_id"}},
	}
	wantPG := "CREATE OR REPLACE FUNCTION delivery_plan_version_fence() RETURNS trigger AS $$\n" +
		"BEGIN\n" +
		"  -- The @fenced counter Delivery.planVersion (D118):\n" +
		"  -- an update that changes a column it covers sets it to the stored value\n" +
		"  -- plus one, and any other keeps it or adds one.\n" +
		"  IF ROW(NEW.address, NEW.courier_id) IS DISTINCT FROM ROW(OLD.address, OLD.courier_id) THEN\n" +
		"    IF NEW.plan_version IS DISTINCT FROM OLD.plan_version + 1 THEN\n" +
		"      RAISE EXCEPTION 'delivery.plan_version must advance from % to %, not to %', OLD.plan_version, OLD.plan_version + 1, NEW.plan_version\n" +
		"        USING ERRCODE = 'check_violation', CONSTRAINT = 'delivery_plan_version_fence';\n" +
		"    END IF;\n" +
		"  ELSIF NEW.plan_version IS DISTINCT FROM OLD.plan_version THEN\n" +
		"    IF NEW.plan_version IS DISTINCT FROM OLD.plan_version + 1 THEN\n" +
		"      RAISE EXCEPTION 'delivery.plan_version must stay at % or advance to %, not move to %', OLD.plan_version, OLD.plan_version + 1, NEW.plan_version\n" +
		"        USING ERRCODE = 'check_violation', CONSTRAINT = 'delivery_plan_version_fence';\n" +
		"    END IF;\n" +
		"  END IF;\n" +
		"  RETURN NEW;\n" +
		"END;\n" +
		"$$ LANGUAGE plpgsql"
	if got := f.FunctionSQL(); got != wantPG {
		t.Errorf("the function is\n%s\nwant\n%s", got, wantPG)
	}
	trigger, err := f.Trigger().SQL()
	if want := "CREATE TRIGGER trg_delivery_plan_version_fence\n  BEFORE UPDATE ON delivery\n  FOR EACH ROW EXECUTE FUNCTION delivery_plan_version_fence()"; err != nil || trigger != want {
		t.Errorf("the trigger is\n%s (%v)\nwant\n%s", trigger, err, want)
	}
	wantSQLite := `CREATE TRIGGER "trg_delivery_plan_version_fence" BEFORE UPDATE ON "delivery" FOR EACH ROW` + "\n" +
		`WHEN NEW."plan_version" IS NOT OLD."plan_version" + 1 AND (` + "\n" +
		`  NEW."plan_version" IS NOT OLD."plan_version"` + "\n" +
		`  OR NEW."address" IS NOT OLD."address"` + "\n" +
		`  OR NEW."courier_id" IS NOT OLD."courier_id")` + "\n" +
		"BEGIN\n  SELECT RAISE(ABORT, 'delivery.plan_version is fenced: an update that changes a column it covers must add one to it, and any other must keep it or add one');\nEND"
	if got := f.SQLiteTrigger(quoted).Definition; got != wantSQLite {
		t.Errorf("the SQLite trigger is\n%s\nwant\n%s", got, wantSQLite)
	}

	// A fence without covers counts every update of the row.
	row := Fence{
		Origin: "Parcel", Field: "version", Table: "parcel", QuotedTable: "parcel", Column: "version", QuotedColumn: "version",
		FunctionName: "parcel_version_fence", TriggerName: "trg_parcel_version_fence",
	}
	wantPG = "CREATE OR REPLACE FUNCTION parcel_version_fence() RETURNS trigger AS $$\n" +
		"BEGIN\n" +
		"  -- The @fenced counter Parcel.version (D118):\n" +
		"  -- every update of a row sets it to the stored value plus one.\n" +
		"  IF NEW.version IS DISTINCT FROM OLD.version + 1 THEN\n" +
		"    RAISE EXCEPTION 'parcel.version must advance from % to %, not to %', OLD.version, OLD.version + 1, NEW.version\n" +
		"      USING ERRCODE = 'check_violation', CONSTRAINT = 'parcel_version_fence';\n" +
		"  END IF;\n" +
		"  RETURN NEW;\n" +
		"END;\n" +
		"$$ LANGUAGE plpgsql"
	if got := row.FunctionSQL(); got != wantPG {
		t.Errorf("the function of a fence without covers is\n%s\nwant\n%s", got, wantPG)
	}
	wantSQLite = `CREATE TRIGGER "trg_parcel_version_fence" BEFORE UPDATE ON "parcel" FOR EACH ROW` + "\n" +
		`WHEN NEW."version" IS NOT OLD."version" + 1` + "\n" +
		"BEGIN\n  SELECT RAISE(ABORT, 'parcel.version is fenced: an update of the row must add one to it');\nEND"
	if got := row.SQLiteTrigger(quoted).Definition; got != wantSQLite {
		t.Errorf("the SQLite trigger of a fence without covers is\n%s\nwant\n%s", got, wantSQLite)
	}
}

// TestFenceInTheScripts: create.sql creates each fence's function, then
// its trigger, and drop.sql drops the trigger, then the function, before
// the tables (D118).
func TestFenceInTheScripts(t *testing.T) {
	out, err := Generate(fenceSchema("Task", "revision", "title"), Options{SchemaName: "fences-db"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteDDL(out, dir); err != nil {
		t.Fatal(err)
	}
	script := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	create := script("create.sql")
	function, trigger := strings.Index(create, "CREATE OR REPLACE FUNCTION task_revision_fence()"), strings.Index(create, "CREATE TRIGGER trg_task_revision_fence\n  BEFORE UPDATE ON task\n")
	if table := strings.Index(create, "CREATE TABLE task ("); table < 0 || function < table || trigger < function {
		t.Errorf("create.sql creates the table at %d, the function at %d and the trigger at %d:\n%s", table, function, trigger, create)
	}
	drop := script("drop.sql")
	trigger, function = strings.Index(drop, "DROP TRIGGER IF EXISTS trg_task_revision_fence ON task;"), strings.Index(drop, "DROP FUNCTION IF EXISTS task_revision_fence();")
	if table := strings.Index(drop, "DROP TABLE IF EXISTS task CASCADE;"); trigger < 0 || function < trigger || table < function {
		t.Errorf("drop.sql drops the trigger at %d, the function at %d and the table at %d:\n%s", trigger, function, table, drop)
	}
}

// TestFenceQuotesNames: a column named by a keyword is quoted wherever the
// rule names it, a relation is covered as its key's column, and a name
// that holds a quote, a percent sign or a dollar quote is written so that
// the function's message and constraint hold it as it is (D118).
func TestFenceQuotesNames(t *testing.T) {
	s := fenceSchema("Task", "count", "order", "owner")
	fieldNamed := func(name string) *ir.FieldDef {
		for _, f := range s.Types["Task"].Fields {
			if f.Name == name {
				return f
			}
		}
		t.Fatalf("no field %s", name)
		return nil
	}
	fieldNamed("count").SQLName = "it's 100%"
	fieldNamed("title").SQLName = "a$$b"
	s.Types["Task"].Fences[0].Covers = append(s.Types["Task"].Fences[0].Covers, "title")
	out, err := Generate(s, Options{SchemaName: "fences-db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Fences) != 1 {
		t.Fatalf("Fences = %+v", out.Fences)
	}
	f := out.Fences[0]
	pg := f.FunctionSQL()
	for _, want := range []string{
		`CREATE OR REPLACE FUNCTION "task_it's 100%_fence"() RETURNS trigger AS $guard$` + "\nBEGIN\n",
		`  IF ROW(NEW."order", NEW.owner_id, NEW."a$$b") IS DISTINCT FROM ROW(OLD."order", OLD.owner_id, OLD."a$$b") THEN`,
		`    IF NEW."it's 100%" IS DISTINCT FROM OLD."it's 100%" + 1 THEN`,
		`RAISE EXCEPTION 'task.it''s 100%% must advance from % to %, not to %', OLD."it's 100%", OLD."it's 100%" + 1, NEW."it's 100%"`,
		`RAISE EXCEPTION 'task.it''s 100%% must stay at % or advance to %, not move to %', `,
		`USING ERRCODE = 'check_violation', CONSTRAINT = 'task_it''s 100%_fence';`,
		"END;\n$guard$ LANGUAGE plpgsql",
	} {
		if !strings.Contains(pg, want) {
			t.Errorf("the function lacks %q:\n%s", want, pg)
		}
	}
	if trigger, err := f.Trigger().SQL(); err != nil || !strings.Contains(trigger, `CREATE TRIGGER "trg_task_it's 100%_fence"`+"\n  BEFORE UPDATE ON task\n") {
		t.Errorf("the trigger is %q (%v)", trigger, err)
	}
	sqlite := f.SQLiteTrigger(func(name string) string { return `"` + name + `"` }).Definition
	for _, want := range []string{
		`WHEN NEW."it's 100%" IS NOT OLD."it's 100%" + 1 AND (`,
		`  OR NEW."order" IS NOT OLD."order"` + "\n" + `  OR NEW."owner_id" IS NOT OLD."owner_id"` + "\n" + `  OR NEW."a$$b" IS NOT OLD."a$$b")`,
		`SELECT RAISE(ABORT, 'task.it''s 100% is fenced: `,
	} {
		if !strings.Contains(sqlite, want) {
			t.Errorf("the SQLite trigger lacks %q:\n%s", want, sqlite)
		}
	}
}

// TestFenceNameLimit: a fence's trigger is named after its table and its
// column, and one over PostgreSQL's 63 bytes fails the build, naming the
// type, the field and the table; one of 63 bytes builds (D118).
func TestFenceNameLimit(t *testing.T) {
	// trg_ and _fence take 10 bytes, and the underscore between the table
	// and the column one more: 52 are left for the two names.
	fits := fenceSchema("Shipment"+strings.Repeat("x", 36), "revision")
	out, err := Generate(fits, Options{SchemaName: "fences-db"})
	if err != nil {
		t.Fatalf("a fence whose trigger's name is 63 bytes: %v", err)
	}
	if name := out.Fences[0].TriggerName; len(name) != sqlutil.PostgresIdentifierLimit {
		t.Fatalf("the trigger %s is %d bytes, want %d", name, len(name), sqlutil.PostgresIdentifierLimit)
	}

	typeName := "Shipment" + strings.Repeat("x", 37)
	_, err = Generate(fenceSchema(typeName, "revision"), Options{SchemaName: "fences-db"})
	if err == nil {
		t.Fatal("a fence whose trigger's name is 64 bytes built")
	}
	table := strings.ToLower(typeName)
	for _, want := range []string{
		"@fenced on " + typeName + ".revision",
		"table " + table,
		"trg_" + table + "_revision_fence, 64 bytes, over PostgreSQL's 63-byte limit",
		"(D118)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
}

// TestFenceNeedsItsColumns: a fence whose field, or whose covered field,
// stores no column fails the build, naming the type and the field, should
// one pass verification; and a fence on a class that is no table, here a
// @jsonField one and an API type, gets no trigger and fails nothing.
func TestFenceNeedsItsColumns(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*ir.Fence)
		want   string
	}{
		"fence":   {func(f *ir.Fence) { f.Field = "missing" }, "@fenced on Task.missing: table task has no column of field missing"},
		"covered": {func(f *ir.Fence) { f.Covers = []string{"title", "missing"} }, "@fenced on Task.revision: covers lists missing, and table task has no column of it"},
	} {
		s := fenceSchema("Task", "revision", "title")
		tc.change(s.Types["Task"].Fences[0])
		if _, err := Generate(s, Options{SchemaName: "fences-db"}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the build fails with %v, want %q", name, err, tc.want)
		}
	}

	s := fenceSchema("Task", "revision", "title")
	for name, change := range map[string]func(td *ir.TypeDef){
		"Payload": func(td *ir.TypeDef) { td.JsonField = true },
		"View":    func(td *ir.TypeDef) { td.Role = ir.RoleAPIView },
	} {
		td := *s.Types["Task"]
		td.Name = name
		change(&td)
		s.Types[name] = &td
	}
	out, err := Generate(s, Options{SchemaName: "fences-db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Fences) != 1 || out.Fences[0].Origin != "Task" {
		t.Errorf("Fences = %+v, want Task's alone", out.Fences)
	}
}
