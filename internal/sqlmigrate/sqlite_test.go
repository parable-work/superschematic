package sqlmigrate

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSQLiteModelGoldens builds the SQLite model of each fixture SQLite
// supports and compares it with testdata/models/<service>.sqlite.json.
// Regenerate with:
// go test ./internal/sqlmigrate -run TestSQLiteModelGoldens -update
func TestSQLiteModelGoldens(t *testing.T) {
	fixtures := sqliteModelFixtures(t)
	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			model := sqliteModel(t, name, fixtures[name])
			canonical, err := model.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			again, err := sqliteModel(t, name, fixtures[name]).CanonicalJSON()
			if err != nil || !bytes.Equal(canonical, again) {
				t.Errorf("two builds of the model differ")
			}
			checkGolden(t, filepath.Join("testdata", "models", name+".sqlite.json"), indentedJSON(t, canonical))
		})
	}
}

// TestBuildModelSQLiteWithoutTables: a schema with no tables is an empty
// SQLite model, whose plan from an empty database has no steps.
func TestBuildModelSQLiteWithoutTables(t *testing.T) {
	model := sqliteModel(t, "x", ir.NewSchema("x", ir.SchemaKindDB))
	if model.Dialect != SQLite || len(model.Tables) != 0 {
		t.Fatalf("model = %+v, want an empty sqlite model", model)
	}
	plan, err := Diff(nil, model, Options{})
	if err != nil || len(plan.Steps) != 0 {
		t.Fatalf("plan from empty to empty: %v, %+v", err, plan)
	}
}

// refusedSchemas are schemas that each use one feature the SQLite dialect
// refuses, with text its error must contain.
func refusedSchemas(t *testing.T) []struct {
	feature string
	schema  *ir.Schema
	want    string
} {
	t.Helper()
	shop := func(change func(*ir.Schema)) *ir.Schema {
		return planCase{}.load(t, andThen(sqliteShop, change))
	}
	scalar := func(s *ir.Schema, sql string) ir.TypeRef {
		name := "Geo." + sql
		s.Scalars[name] = &ir.ScalarDef{Name: name, LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": sql}}
		return ir.TypeRef{Name: name}
	}
	typed := func(sql string) *ir.Schema {
		return shop(func(s *ir.Schema) {
			addField(s, "Label", &ir.FieldDef{Name: "place", TypeRef: scalar(s, sql)})
		})
	}
	return []struct {
		feature string
		schema  *ir.Schema
		want    string
	}{
		{"@versioned", shop(func(s *ir.Schema) { versioned(s, "Order", nil) }),
			"Order is @versioned, which the sqlite dialect does not support"},
		{"@optimistic", shop(func(s *ir.Schema) { typeNamed(s, "Product").Optimistic = true }),
			"Product is @optimistic, which the sqlite dialect does not support"},
		{"projection", planCase{}.load(t, nil),
			"OrderSummary is a projection (report.order_summary), which the sqlite dialect does not support"},
		{"GIN index", shop(func(s *ir.Schema) {
			addField(s, "Order", &ir.FieldDef{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}})
			typeNamed(s, "Order").Indexes = append(typeNamed(s, "Order").Indexes, ir.IndexDef{Keys: []string{"tags"}})
		}), "the index idx_order_tags of Order is a GIN index, which the sqlite dialect does not support"},
		{"GIST index", shop(func(s *ir.Schema) {
			addField(s, "Label", &ir.FieldDef{Name: "path", TypeRef: scalar(s, "LTREE")})
			typeNamed(s, "Label").Indexes = []ir.IndexDef{{Keys: []string{"path"}}}
		}), "the index idx_label_path of Label is a GIST index, which the sqlite dialect does not support"},
		{"LTREE", typed("LTREE"), "column label.place (Label.place) is LTREE, which the sqlite dialect has no storage for"},
		{"POINT", typed("POINT"), "column label.place (Label.place) is POINT, which the sqlite dialect has no storage for"},
		{"GEOGRAPHY", typed("GEOGRAPHY"), "column label.place (Label.place) is GEOGRAPHY, which the sqlite dialect has no storage for"},
		{"GEOMETRY", typed("GEOMETRY"), "column label.place (Label.place) is GEOMETRY, which the sqlite dialect has no storage for"},
	}
}

// TestSQLiteRefusals: BuildModel refuses each feature SQLite has no form
// of, naming the feature and the dialect.
func TestSQLiteRefusals(t *testing.T) {
	for _, tc := range refusedSchemas(t) {
		t.Run(tc.feature, func(t *testing.T) {
			_, err := BuildModel(tc.schema, sqlgen.Options{SchemaName: "shop-db"}, SQLite)
			if err == nil {
				t.Fatalf("BuildModel(sqlite) succeeded, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestSQLiteSteps checks every SQLite plan case: every step runs in a
// transaction with no recovery and with foreign keys on, as D1 runs it
// (D27, amended): no step turns them off, a rebuild defers their checks to
// its commit first, no statement is a PRAGMA but that one or a transaction
// statement, and no statement ends in a semicolon.
func TestSQLiteSteps(t *testing.T) {
	for _, pc := range sqlitePlanCases() {
		plan := pc.plan(t)
		if plan.Dialect != SQLite {
			t.Fatalf("%s: plan for %s", pc.name, plan.Dialect)
		}
		contract := false
		for _, step := range plan.Steps {
			if !step.Transactional || len(step.Recovery) > 0 {
				t.Errorf("%s: step %d %s does not run in a transaction alone", pc.name, step.Index, step.Op)
			}
			if step.ForeignKeysOff {
				t.Errorf("%s: step %d %s turns foreign keys off", pc.name, step.Index, step.Op)
			}
			if step.Op == "copyTable" && (len(step.Statements) == 0 || step.Statements[0] != sqliteDeferForeignKeys) {
				t.Errorf("%s: step %d copyTable does not start with %s", pc.name, step.Index, sqliteDeferForeignKeys)
			}
			for i, stmt := range step.Statements {
				word := strings.ToUpper(strings.Fields(stmt)[0])
				if (word == "PRAGMA" && (stmt != sqliteDeferForeignKeys || i > 0)) ||
					word == "BEGIN" || word == "COMMIT" || word == "END" || word == "ROLLBACK" || word == "SAVEPOINT" || word == "RELEASE" {
					t.Errorf("%s: step %d %s runs %s", pc.name, step.Index, step.Op, stmt)
				}
			}
			if step.Phase == Contract {
				contract = true
			} else if contract {
				t.Errorf("%s: expand step %d follows a contract step", pc.name, step.Index)
			}
			for _, stmt := range step.Statements {
				if strings.HasSuffix(strings.TrimSpace(stmt), ";") {
					t.Errorf("%s: step %d has a statement with a trailing semicolon", pc.name, step.Index)
				}
			}
		}
	}
}

// TestSQLiteHazardClasses checks the hazard classes and the phase of the
// SQLite steps, as TestHazardClasses does for Postgres.
func TestSQLiteHazardClasses(t *testing.T) {
	const copyTable = HazardCopyTable
	checkHazardClasses(t, sqlitePlanCases(), []hazardTest{
		{plan: "from-empty", op: "createTable", phase: Expand},
		{plan: "from-empty", op: "createIndex", phase: Expand},

		{plan: "add-column", op: "addColumn", phase: Expand},
		{plan: "add-required-column", op: "copyTable", phase: Expand,
			want: []HazardClass{blockingClass, compat, dataDependent, copyTable}, reason: "add channel NOT NULL without a default"},
		{plan: "add-required-column-with-default", op: "addColumn", phase: Expand},
		{plan: "drop-column", op: "dropColumn", phase: Contract, want: []HazardClass{destructive, blockingClass, apiBreaking}},
		{plan: "drop-required-column", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, copyTable},
			reason: "drop NOT NULL from sku"},
		{plan: "drop-required-column", op: "dropColumn", phase: Contract, want: []HazardClass{destructive, blockingClass}},
		{plan: "rename-column", op: "renameColumn", phase: Expand, want: []HazardClass{compat, apiBreaking}},
		{plan: "rename-column", op: "renameIndex", phase: Expand, want: []HazardClass{blockingClass}},
		{plan: "rename-unique-column", op: "renameConstraint", phase: Expand, want: []HazardClass{blockingClass}},
		{plan: "make-column-required", op: "copyTable", phase: Contract, want: []HazardClass{blockingClass, dataDependent, copyTable},
			reason: "make note NOT NULL"},
		{plan: "make-column-optional", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, copyTable}},

		{plan: "add-table", op: "createTable", phase: Expand},
		{plan: "drop-table", op: "dropTable", phase: Contract, want: []HazardClass{destructive}},
		{plan: "drop-tables-in-cycle", op: "dropTable", phase: Contract, want: []HazardClass{destructive}, reason: "Dropping table warehouse"},
		{plan: "rename-table", op: "renameTable", phase: Expand, want: []HazardClass{compat}},

		{plan: "add-index", op: "createIndex", phase: Expand, want: []HazardClass{blockingClass}},
		{plan: "add-unique-index", op: "createIndex", phase: Expand, want: []HazardClass{blockingClass, compat, dataDependent}},
		{plan: "drop-index", op: "dropIndex", phase: Contract},
		{plan: "add-unique", op: "addUnique", phase: Expand, want: []HazardClass{blockingClass, compat, dataDependent}},
		{plan: "drop-unique", op: "dropUnique", phase: Contract},

		{plan: "add-relation", op: "addColumn", phase: Expand},
		{plan: "relation-on-existing-column", op: "copyTable", phase: Contract,
			want: []HazardClass{blockingClass, dataDependent, copyTable}, reason: "add foreign key fk_customer_referrer_id"},
		{plan: "change-on-delete", op: "copyTable", phase: Contract, want: []HazardClass{blockingClass, copyTable},
			reason: "change foreign key fk_order_customer_id"},
		{plan: "drop-relation", op: "copyTable", phase: Contract, want: []HazardClass{destructive, blockingClass, copyTable}},
		{plan: "add-join-table", op: "createTable", phase: Expand},
		{plan: "drop-join-table", op: "dropTable", phase: Contract, want: []HazardClass{destructive}},

		{plan: "rebuild-retype", op: "copyTable", phase: Expand,
			want: []HazardClass{destructive, blockingClass, compat, copyTable}, reason: "change the type of quantity"},
		{plan: "rebuild-collation", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, compat, copyTable}},
		{plan: "rebuild-json-to-text", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, compat, copyTable},
			reason: "Order.details changes from a JSON value (TEXT) to a scalar (TEXT)"},
		{plan: "rebuild-set-default", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, copyTable},
			reason: "change the default of referrer_id"},
		{plan: "rebuild-drop-default", op: "copyTable", phase: Contract, want: []HazardClass{blockingClass, copyTable},
			reason: "drop the default of referrer_id"},
		{plan: "rebuild-add-column-volatile-default", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, copyTable},
			reason: "add shipped_at with a default that is not a constant"},
		{plan: "rebuild-add-uuid-column", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, copyTable}},
		{plan: "rebuild-batched", op: "copyTable", subject: "table/order", phase: Expand,
			want:   []HazardClass{destructive, blockingClass, compat, copyTable},
			reason: "cannot add shipped_at with a default that is not a constant, drop NOT NULL from code, drop NOT NULL from placed_at and change the type of total, so"},
		{plan: "rebuild-batched", op: "copyTable", subject: "table/order", phase: Contract,
			want: []HazardClass{destructive, blockingClass, dataDependent, copyTable}},
		{plan: "rebuild-renamed-referenced-table", op: "renameTable", phase: Expand, want: []HazardClass{compat}},
		{plan: "rebuild-renamed-referenced-table", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, copyTable}},
		{plan: "rebuild-foreign-keys", op: "addColumn", phase: Expand},
		{plan: "rebuild-foreign-keys", op: "copyTable", phase: Contract, want: []HazardClass{blockingClass, dataDependent, copyTable}},
		{plan: "rebuild-referenced-table", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, copyTable},
			reason: "rebuilds customer with the tables that reference it, directly or through another table: invoice, order, order_label, order_line, review, review_reply and visit."},
		{plan: "rebuild-referenced-table", op: "copyTable", phase: Contract, want: []HazardClass{blockingClass, copyTable},
			reason: "Copying customer, invoice, order, order_label, order_line, review, review_reply and visit holds"},
		{plan: "rebuild-self-referencing-table", op: "copyTable", subject: "table/category", phase: Expand, want: []HazardClass{blockingClass, copyTable},
			reason: "so the step rebuilds category: it copies the rows"},
		{plan: "rebuild-self-referencing-table", op: "copyTable", subject: "table/customer", phase: Expand, want: []HazardClass{blockingClass, copyTable}},
		{plan: "rebuild-with-referencing-changes", op: "copyTable", phase: Expand,
			want:   []HazardClass{destructive, blockingClass, compat, copyTable},
			reason: "cannot drop NOT NULL from created_at in customer, nor change the type of total in order, so the step rebuilds customer and order with"},
		{plan: "rebuild-and-drop-referencing-table", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, copyTable},
			reason: "directly or through another table: order, order_label, order_line and review."},
		{plan: "rebuild-and-drop-referencing-table", op: "copyTable", phase: Contract, want: []HazardClass{destructive, blockingClass, copyTable},
			reason: "The phase drops review, which references it too, so the step drops it without copying it."},
		{plan: "drop-tables-in-restrict-cycle", op: "dropTable", phase: Contract, want: []HazardClass{destructive}},

		{plan: "list-element-text-to-integer", op: "copyTable", phase: Expand,
			want:   []HazardClass{destructive, blockingClass, compat, copyTable},
			reason: "text that is not a number becomes 0 and a fraction is cut toward zero"},
		{plan: "list-element-integer-to-text", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, compat, copyTable},
			reason: "cannot convert the elements of items"},
		{plan: "list-element-integer-to-real", op: "copyTable", phase: Expand,
			want: []HazardClass{destructive, blockingClass, compat, copyTable}, reason: "an integer past 2^53 is rounded"},
		{plan: "list-element-integer-to-boolean", op: "copyTable", phase: Expand,
			want: []HazardClass{destructive, blockingClass, compat, copyTable}, reason: "every number other than 0 becomes true"},
		{plan: "list-element-boolean-to-text", op: "copyTable", phase: Expand, want: []HazardClass{blockingClass, compat, copyTable},
			reason: "Product.items changes from a list of BOOLEAN to a list of TEXT"},
		{plan: "list-element-and-drop-column", op: "copyTable", phase: Expand,
			want:   []HazardClass{destructive, blockingClass, compat, copyTable},
			reason: "cannot drop NOT NULL from price and convert the elements of items"},
		{plan: "list-element-and-drop-column", op: "dropColumn", phase: Contract, want: []HazardClass{destructive, blockingClass}},
	})
}

// TestSQLiteRebuildBatches: each table is copied or dropped by at most one
// rebuild per phase, and a table a rebuild copies or drops in a phase has
// no other step in it but its renames.
func TestSQLiteRebuildBatches(t *testing.T) {
	for _, pc := range sqlitePlanCases() {
		plan := pc.plan(t)
		rebuilt := map[string]bool{}
		for _, step := range plan.Steps {
			if step.Op != "copyTable" {
				continue
			}
			for _, stmt := range step.Statements {
				table, ok := strings.CutPrefix(stmt, "DROP TABLE ")
				if !ok {
					continue
				}
				key := string(step.Phase) + " table/" + strings.Trim(table, `"`)
				if rebuilt[key] {
					t.Errorf("%s: %s is rebuilt twice in %s", pc.name, table, step.Phase)
				}
				rebuilt[key] = true
			}
		}
		for _, step := range plan.Steps {
			if step.Op == "copyTable" || step.Op == "renameTable" || step.Op == "renameColumn" {
				continue
			}
			parts := strings.SplitN(step.Subject, "/", 3)
			if len(parts) >= 2 && parts[0] == "table" && rebuilt[string(step.Phase)+" table/"+parts[1]] {
				t.Errorf("%s: step %d %s %s runs beside the rebuild of its table", pc.name, step.Index, step.Op, step.Subject)
			}
		}
	}
	// The batched case rebuilds order once in each phase, with the column
	// and the index expand adds.
	for _, pc := range sqlitePlanCases() {
		if pc.name != "rebuild-batched" {
			continue
		}
		var ops []string
		for _, step := range pc.plan(t).Steps {
			ops = append(ops, string(step.Phase)+" "+step.Op+" "+step.Subject)
			if step.Op == "copyTable" && step.Phase == Expand {
				all := strings.Join(step.Statements, "\n")
				for _, want := range []string{`"discount" INTEGER`, `CREATE INDEX "idx_order_reference"`, `CAST("total" AS REAL)`} {
					if !strings.Contains(all, want) {
						t.Errorf("the expand rebuild lacks %s:\n%s", want, all)
					}
				}
			}
		}
		want := []string{"expand copyTable table/order", "contract copyTable table/order"}
		if !slices.Equal(ops, want) {
			t.Errorf("steps:\n%s\nwant:\n%s", strings.Join(ops, "\n"), strings.Join(want, "\n"))
		}
	}
}

// TestCreateSQL: SQLite's create.sql is the plan from an empty database,
// every statement of every step in order.
func TestCreateSQL(t *testing.T) {
	for name, schema := range sqliteModelFixtures(t) {
		model := sqliteModel(t, name, schema)
		script, err := CreateSQL(model)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := Diff(nil, model, Options{})
		if err != nil {
			t.Fatal(err)
		}
		var want strings.Builder
		want.WriteString("-- Generated SQLite DDL for schema: " + name + "\n")
		want.WriteString("-- This file is auto-generated. Do not edit manually.\n")
		want.WriteString("-- It is the migration plan from an empty database to model " + plan.To + ".\n")
		for _, step := range plan.Steps {
			want.WriteString("\n")
			for _, statement := range step.Statements {
				want.WriteString(statement + ";\n")
			}
		}
		if script != want.String() {
			t.Errorf("%s: create.sql is not the plan from an empty database:\n%s", name, script)
		}
	}
}

func TestSQLiteType(t *testing.T) {
	tests := map[string]string{
		"UUID": "TEXT", "TEXT": "TEXT", "VARCHAR(80)": "TEXT", "varchar(4096)": "TEXT", "CITEXT": "TEXT COLLATE NOCASE",
		"DATE": "TEXT", "TIME": "TEXT", "TIMESTAMPTZ": "TEXT", "TIMESTAMP": "TEXT", "INTERVAL": "TEXT", "INET": "TEXT",
		"SMALLINT": "INTEGER", "INTEGER": "INTEGER", "BIGINT": "INTEGER", "BOOLEAN": "INTEGER",
		"REAL": "REAL", "DOUBLE PRECISION": "REAL", "NUMERIC(12,2)": "NUMERIC", "NUMERIC": "NUMERIC",
		"JSONB": "TEXT", "JSON": "TEXT", "TEXT[]": "TEXT", "UUID[]": "TEXT", "BIGINT[]": "TEXT", "BYTEA": "BLOB",
	}
	for pg, want := range tests {
		if got, err := sqliteType(pg); err != nil || got != want {
			t.Errorf("sqliteType(%s) = %q, %v; want %q", pg, got, err, want)
		}
	}
	for _, pg := range []string{"LTREE", "POINT", "GEOGRAPHY", "GEOMETRY", "LTREE[]", "TSVECTOR"} {
		if got, err := sqliteType(pg); err == nil {
			t.Errorf("sqliteType(%s) = %q, want an error", pg, got)
		}
	}
}

func TestSQLiteDefault(t *testing.T) {
	tests := []struct{ def, pg, want string }{
		{"", "TEXT", ""},
		{"gen_random_uuid()", "UUID", sqliteUUIDDefault},
		{"CURRENT_TIMESTAMP", "TIMESTAMPTZ", "(strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))"},
		{"CURRENT_DATE", "DATE", "(strftime('%Y-%m-%d', 'now'))"},
		{"CURRENT_TIME", "TIME", "(strftime('%H:%M:%f', 'now'))"},
		{"'{}'", "TEXT[]", "'[]'"},
		{`'{"theme": "dark", "it''s": 1}'::jsonb`, "JSONB", `'{"theme": "dark", "it''s": 1}'`},
		{"1", "BIGINT", "1"},
		{"-2.5", "DOUBLE PRECISION", "-2.5"},
		{"'draft'", "TEXT", "'draft'"},
		{"true", "BOOLEAN", "true"},
	}
	for _, tc := range tests {
		got, err := sqliteDefault(tc.def, tc.pg)
		if err != nil || got != tc.want {
			t.Errorf("sqliteDefault(%s, %s) = %q, %v; want %q", tc.def, tc.pg, got, err, tc.want)
		}
	}
	if got, err := sqliteDefault("nextval('seq')", "BIGINT"); err == nil {
		t.Errorf("sqliteDefault(nextval) = %q, want an error", got)
	}
	for def, constant := range map[string]bool{
		"'[]'": true, "1": true, "-2.5": true, "NULL": true, "true": true,
		sqliteUUIDDefault: false, sqliteTimestampDefault: false, "CURRENT_TIMESTAMP": false,
	} {
		if sqliteConstant(def) != constant {
			t.Errorf("sqliteConstant(%s) = %t", def, !constant)
		}
	}
}

func TestSQLiteConvert(t *testing.T) {
	tests := []struct {
		from, to string
		kind     conversionKind
		lossy    bool
	}{
		{"TEXT", "TEXT", convertSame, false},
		{"TEXT", "TEXT COLLATE NOCASE", convertRewrite, false},
		{"TEXT COLLATE NOCASE", "TEXT", convertRewrite, false},
		{"INTEGER", "TEXT", convertRewrite, false},
		{"REAL", "TEXT", convertRewrite, false},
		{"BLOB", "TEXT", convertRewrite, true},
		{"TEXT", "BLOB", convertRewrite, false},
		{"INTEGER", "NUMERIC", convertRewrite, false},
		{"REAL", "NUMERIC", convertRewrite, false},
		{"INTEGER", "REAL", convertRewrite, true},
		{"REAL", "INTEGER", convertRewrite, true},
		{"NUMERIC", "INTEGER", convertRewrite, true},
		{"NUMERIC", "REAL", convertRewrite, true},
		{"TEXT", "INTEGER", convertRewrite, true},
		{"INTEGER", "BLOB", convertImpossible, false},
		{"BLOB", "REAL", convertImpossible, false},
	}
	for _, tc := range tests {
		got := sqliteConvert(tc.from, tc.to)
		if got.kind != tc.kind || got.lossy != tc.lossy {
			t.Errorf("convert(%s, %s) = %+v, want kind %d lossy %t", tc.from, tc.to, got, tc.kind, tc.lossy)
		}
	}
}

// TestSQLiteKindChanges: a list, a JSON value and text are all TEXT in
// SQLite, and the model tells them apart by what a column holds. A JSON
// value becomes a scalar as its text does, through a rebuild; every other
// change between them is refused, naming the column and both kinds.
func TestSQLiteKindChanges(t *testing.T) {
	model := func(typ, holds string) *Model {
		return &Model{Version: ModelVersion, Dialect: SQLite, Service: "s", Tables: []*Table{{
			Name: "sample", Kind: TableEntity,
			Columns: []*Column{
				{Name: "id", Type: "TEXT"},
				{Name: "v", Origin: "Sample.v", Type: typ, Nullable: true, Holds: holds},
			},
			PrimaryKey: &Constraint{Name: "sample_pkey", Columns: []string{"id"}},
		}}}
	}
	const (
		scalar = ""
		list   = holdsList
		json   = holdsJSON
	)
	tests := []struct {
		fromType, fromHolds, toType, toHolds string
		// want is the refusal, or, for a change the plan makes, the
		// rebuild's copy of v and its hazard classes.
		want string
	}{
		{"TEXT", scalar, "TEXT", list, "changes from a scalar (TEXT) to a list (TEXT holding a JSON array)"},
		{"INTEGER", scalar, "TEXT", list, "changes from a scalar (INTEGER) to a list (TEXT holding a JSON array)"},
		{"TEXT", list, "TEXT", scalar, "changes from a list (TEXT holding a JSON array) to a scalar (TEXT)"},
		{"TEXT", scalar, "TEXT", json, "changes from a scalar (TEXT) to a JSON value (TEXT)"},
		{"INTEGER", scalar, "TEXT", json, "changes from a scalar (INTEGER) to a JSON value (TEXT)"},
		{"TEXT", list, "TEXT", json, "changes from a list (TEXT holding a JSON array) to a JSON value (TEXT)"},
		{"TEXT", json, "TEXT", list, "changes from a JSON value (TEXT) to a list (TEXT holding a JSON array)"},
		{"TEXT", json, "TEXT", scalar, `"v" [blocking compat copy-table]`},
		{"TEXT", json, "TEXT COLLATE NOCASE", scalar, `"v" [blocking compat copy-table]`},
		{"TEXT", json, "INTEGER", scalar, `CAST("v" AS INTEGER) [destructive blocking compat copy-table]`},
	}
	for _, tc := range tests {
		name := fmt.Sprintf("%s %s to %s %s", tc.fromType, tc.fromHolds, tc.toType, tc.toHolds)
		plan, err := Diff(model(tc.fromType, tc.fromHolds), model(tc.toType, tc.toHolds), Options{})
		if strings.HasPrefix(tc.want, "changes") {
			want := "sqlmigrate: column sample.v (Sample.v) " + tc.want + ", which sqlite cannot convert; change the column by hand and adopt the new model"
			if err == nil || err.Error() != want {
				t.Errorf("%s: Diff = %v, want %s", name, err, want)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(plan.Steps) != 1 || plan.Steps[0].Op != "copyTable" {
			t.Errorf("%s: steps %+v, want one rebuild", name, plan.Steps)
			continue
		}
		var classes []string
		for _, h := range plan.Steps[0].Hazards {
			classes = append(classes, string(h.Class))
		}
		got := strings.TrimPrefix(plan.Steps[0].Statements[2], `INSERT INTO "_new_sample" ("id", "v")`+"\n"+`SELECT "id", `)
		got = strings.TrimSuffix(got, "\n"+`FROM "sample"`) + " [" + strings.Join(classes, " ") + "]"
		if got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}

	// The model of a schema tells the kinds apart: a field that becomes a
	// list is refused.
	pc := forSQLite(planCase{after: func(s *ir.Schema) {
		fieldNamed(s, "Order", "note").TypeRef = ir.TypeRef{Name: "string", IsArray: true}
	}})
	from, to := pc.models(t)
	_, err := Diff(from, to, Options{})
	want := "column order.note (Order.note) changes from a scalar (TEXT) to a list (TEXT holding a JSON array), which sqlite cannot convert"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Diff = %v, want %s", err, want)
	}
}

// TestSQLiteElement: the model records what each element of a list's JSON
// array holds, and nothing for a column that is not a list. A list of
// lists is JSONB, which holds a JSON value, not a list.
func TestSQLiteElement(t *testing.T) {
	tests := map[string]string{
		"TEXT[]": "TEXT", "UUID[]": "TEXT", "VARCHAR(80)[]": "TEXT", "CITEXT[]": "TEXT", "DATE[]": "TEXT",
		"TIMESTAMPTZ[]": "TEXT", "INET[]": "TEXT", "SMALLINT[]": "INTEGER", "BIGINT[]": "INTEGER",
		"BOOLEAN[]": "BOOLEAN", "DOUBLE PRECISION[]": "REAL", "NUMERIC(12,2)[]": "NUMERIC",
		"JSONB[]": "JSON", "BYTEA[]": "BLOB",
		"TEXT": "", "BIGINT": "", "BOOLEAN": "", "JSONB": "",
	}
	for pg, want := range tests {
		if got := sqliteElement(pg); got != want {
			t.Errorf("sqliteElement(%s) = %q, want %q", pg, got, want)
		}
	}
}

// TestSQLiteListConvert classifies a list's element changes as the same
// cast of a column is classified: SQLite's CAST never fails, so a change
// that cannot keep every element is lossy and says what it loses, never
// one that may fail.
func TestSQLiteListConvert(t *testing.T) {
	tests := []struct {
		from, to string
		kind     conversionKind
		lossy    bool
	}{
		{"TEXT", "TEXT", convertSame, false},
		{"", "INTEGER", convertSame, false},
		{"INTEGER", "", convertSame, false},
		{"TEXT", "INTEGER", convertRewrite, true},
		{"TEXT", "REAL", convertRewrite, true},
		{"TEXT", "NUMERIC", convertRewrite, true},
		{"TEXT", "BOOLEAN", convertRewrite, true},
		{"INTEGER", "TEXT", convertRewrite, false},
		{"REAL", "TEXT", convertRewrite, false},
		{"NUMERIC", "TEXT", convertRewrite, false},
		{"INTEGER", "REAL", convertRewrite, true},
		{"INTEGER", "NUMERIC", convertRewrite, false},
		{"REAL", "INTEGER", convertRewrite, true},
		{"REAL", "NUMERIC", convertRewrite, false},
		{"NUMERIC", "INTEGER", convertRewrite, true},
		{"NUMERIC", "REAL", convertRewrite, true},
		{"INTEGER", "BOOLEAN", convertRewrite, true},
		{"REAL", "BOOLEAN", convertRewrite, true},
		{"BOOLEAN", "TEXT", convertRewrite, false},
		{"BOOLEAN", "INTEGER", convertRewrite, false},
		{"BOOLEAN", "REAL", convertRewrite, false},
		{"BOOLEAN", "NUMERIC", convertRewrite, false},
		{"JSON", "TEXT", convertImpossible, false},
		{"TEXT", "JSON", convertImpossible, false},
		{"BOOLEAN", "JSON", convertImpossible, false},
		{"BLOB", "TEXT", convertImpossible, false},
		{"TEXT", "BLOB", convertImpossible, false},
		{"JSON", "BLOB", convertImpossible, false},
	}
	for _, tc := range tests {
		got := sqliteListConvert(tc.from, tc.to)
		if got.kind != tc.kind || got.lossy != tc.lossy {
			t.Errorf("sqliteListConvert(%s, %s) = %+v, want kind %d lossy %t", tc.from, tc.to, got, tc.kind, tc.lossy)
		}
		if got.lossy != (got.loss != "") || strings.Contains(got.loss, ":  ") || strings.HasSuffix(got.loss, ": .") {
			t.Errorf("sqliteListConvert(%s, %s) loses %q", tc.from, tc.to, got.loss)
		}
	}
}

// TestSQLiteListElementChanges: a list whose element changes is rebuilt,
// and the copy converts each element of its JSON array, in order, keeping
// a NULL list NULL; the step carries the conversion's hazards. A change to
// or from a JSON value or bytes is refused, naming the column and both
// elements. A list whose element stays, or that a model does not record,
// has no step.
func TestSQLiteListElementChanges(t *testing.T) {
	model := func(element string) *Model {
		return &Model{Version: ModelVersion, Dialect: SQLite, Service: "s", Tables: []*Table{{
			Name: "sample", Kind: TableEntity,
			Columns: []*Column{
				{Name: "id", Type: "TEXT"},
				{Name: "v", Origin: "Sample.v", Type: "TEXT", Nullable: true, Holds: holdsList, Element: element},
			},
			PrimaryKey: &Constraint{Name: "sample_pkey", Columns: []string{"id"}},
		}}}
	}
	const e = `"_element"."value"`
	list := func(element string) string {
		return `CASE WHEN "sample"."v" IS NOT NULL THEN (SELECT json_group_array(` + element +
			` ORDER BY "_element"."key") FROM json_each("sample"."v") AS "_element") END`
	}
	truth := "CASE WHEN " + e + " THEN 'true' WHEN NOT " + e + " THEN 'false' END"
	const (
		lossy = " [destructive blocking compat copy-table]"
		kept  = " [blocking compat copy-table]"
	)
	tests := []struct {
		from, to string
		// want is the refusal, or the rebuild's copy of v and its hazard
		// classes.
		want string
	}{
		{"TEXT", "INTEGER", list("CAST("+e+" AS INTEGER)") + lossy},
		{"TEXT", "REAL", list("CAST("+e+" AS REAL)") + lossy},
		{"TEXT", "NUMERIC", list("CAST("+e+" AS NUMERIC)") + lossy},
		{"INTEGER", "TEXT", list("CAST("+e+" AS TEXT)") + kept},
		{"REAL", "TEXT", list("CAST("+e+" AS TEXT)") + kept},
		{"INTEGER", "REAL", list("CAST("+e+" AS REAL)") + lossy},
		{"INTEGER", "NUMERIC", list("CAST("+e+" AS NUMERIC)") + kept},
		{"REAL", "INTEGER", list("CAST("+e+" AS INTEGER)") + lossy},
		{"NUMERIC", "REAL", list("CAST("+e+" AS REAL)") + lossy},
		{"INTEGER", "BOOLEAN", list("json("+truth+")") + lossy},
		{"TEXT", "BOOLEAN", list("json("+truth+")") + lossy},
		{"BOOLEAN", "INTEGER", list("CAST("+e+" AS INTEGER)") + kept},
		{"BOOLEAN", "REAL", list("CAST("+e+" AS REAL)") + kept},
		{"BOOLEAN", "TEXT", list(truth) + kept},
		{"JSON", "TEXT", "changes from a list of JSON to a list of TEXT"},
		{"TEXT", "JSON", "changes from a list of TEXT to a list of JSON"},
		{"BLOB", "TEXT", "changes from a list of BLOB to a list of TEXT"},
		{"INTEGER", "BLOB", "changes from a list of INTEGER to a list of BLOB"},
	}
	for _, tc := range tests {
		name := tc.from + " to " + tc.to
		plan, err := Diff(model(tc.from), model(tc.to), Options{})
		if strings.HasPrefix(tc.want, "changes") {
			want := "sqlmigrate: column sample.v (Sample.v) " + tc.want + ", which sqlite cannot convert; change the column by hand and adopt the new model"
			if err == nil || err.Error() != want {
				t.Errorf("%s: Diff = %v, want %s", name, err, want)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(plan.Steps) != 1 || plan.Steps[0].Op != "copyTable" {
			t.Errorf("%s: steps %+v, want one rebuild", name, plan.Steps)
			continue
		}
		step := plan.Steps[0]
		var classes []string
		for _, h := range step.Hazards {
			classes = append(classes, string(h.Class))
			if h.Class == HazardCopyTable && !strings.Contains(h.Reason, "cannot convert the elements of v,") {
				t.Errorf("%s: copy-table reason %q", name, h.Reason)
			}
		}
		got := strings.TrimPrefix(step.Statements[2], `INSERT INTO "_new_sample" ("id", "v")`+"\n"+`SELECT "id", `)
		got = strings.TrimSuffix(got, "\n"+`FROM "sample"`) + " [" + strings.Join(classes, " ") + "]"
		if got != tc.want {
			t.Errorf("%s:\n%s\nwant\n%s", name, got, tc.want)
		}
	}

	for _, pair := range [][2]string{{"TEXT", "TEXT"}, {"", "INTEGER"}, {"BOOLEAN", ""}} {
		plan, err := Diff(model(pair[0]), model(pair[1]), Options{})
		if err != nil || len(plan.Steps) != 0 {
			t.Errorf("%q to %q: %v, %d steps; want no step", pair[0], pair[1], err, len(plan.Steps))
		}
	}

	// The destructive hazard says what the conversion loses.
	for pair, want := range map[[2]string]string{
		{"TEXT", "INTEGER"}: "Sample.v changes from a list of TEXT to a list of INTEGER: SQLite converts each element with a cast that never fails, " +
			"so the conversion does not keep every element: text that is not a number becomes 0 and a fraction is cut toward zero.",
		{"TEXT", "BOOLEAN"}: "Sample.v changes from a list of TEXT to a list of BOOLEAN: SQLite converts each element with a cast that never fails, " +
			"so the conversion does not keep every element: text is true only where it reads as a number other than 0, so 'true' becomes false.",
		{"NUMERIC", "INTEGER"}: "Sample.v changes from a list of NUMERIC to a list of INTEGER: SQLite converts each element with a cast that never fails, " +
			"so the conversion does not keep every element: a fraction is cut toward zero.",
	} {
		plan, err := Diff(model(pair[0]), model(pair[1]), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if h := plan.Steps[0].Hazards[0]; h.Class != HazardDestructive || h.Reason != want {
			t.Errorf("%s to %s: %s hazard %q, want destructive %q", pair[0], pair[1], h.Class, h.Reason, want)
		}
	}
}

// TestSQLiteAddable: ALTER TABLE ADD COLUMN adds a column only as SQLite
// allows.
func TestSQLiteAddable(t *testing.T) {
	tests := []struct {
		col        Column
		references bool
		want       bool
	}{
		{Column{Type: "TEXT", Nullable: true}, false, true},
		{Column{Type: "TEXT"}, false, false},
		{Column{Type: "TEXT", Default: "'[]'"}, false, true},
		{Column{Type: "TEXT", Default: "NULL"}, false, false},
		{Column{Type: "TEXT", Nullable: true, Default: sqliteUUIDDefault}, false, false},
		{Column{Type: "TEXT", Default: sqliteTimestampDefault}, false, false},
		{Column{Type: "TEXT", Nullable: true}, true, true},
		{Column{Type: "TEXT", Nullable: true, Default: "'x'"}, true, false},
		{Column{Type: "TEXT"}, true, false},
	}
	for _, tc := range tests {
		if got := sqliteAddable(&tc.col, tc.references); got != tc.want {
			t.Errorf("sqliteAddable(%+v, references %t) = %t", tc.col, tc.references, got)
		}
	}
}

// TestSQLiteDropCycle: SQLite drops the tables of a reference cycle in one
// step, with foreign keys on and their checks deferred to the commit, after
// the tables that reference them and before the tables they reference,
// each table with its own hazard. A table that is in the cycle only
// through another (z) is in the step too.
func TestSQLiteDropCycle(t *testing.T) {
	table := func(name string, refs ...string) *Table {
		tb := &Table{
			Name: name, Kind: TableEntity, Origin: strings.ToUpper(name),
			Columns:    []*Column{{Name: "id", Type: "TEXT"}},
			PrimaryKey: &Constraint{Name: name + "_pkey", Columns: []string{"id"}},
		}
		for _, ref := range refs {
			tb.Columns = append(tb.Columns, &Column{Name: ref + "_id", Type: "TEXT", Nullable: true})
			tb.ForeignKeys = append(tb.ForeignKeys, &ForeignKey{
				Name: "fk_" + name + "_" + ref + "_id", Columns: []string{ref + "_id"}, RefTable: ref, RefColumns: []string{"id"}, OnDelete: "CASCADE",
			})
		}
		return tb
	}
	// a and b reference each other, and a reaches b through z too; c
	// references b, and a references d.
	from := &Model{Version: ModelVersion, Dialect: SQLite, Service: "s", Tables: []*Table{
		table("a", "b", "z", "d"), table("b", "a"), table("c", "b"), table("d"), table("z", "b"),
	}}
	plan, err := Diff(from, &Model{Version: ModelVersion, Dialect: SQLite, Service: "s"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, step := range plan.Steps {
		var ids []string
		for _, h := range step.Hazards {
			ids = append(ids, h.ID)
		}
		got = append(got, fmt.Sprintf("%s %s %t: %s [%s]", step.Op, step.Subject, step.ForeignKeysOff,
			strings.Join(step.Statements, "; "), strings.Join(ids, " ")))
	}
	want := []string{
		`dropTable table/c false: DROP TABLE "c" [destructive:table/c]`,
		`dropTable table/a false: PRAGMA defer_foreign_keys = ON; DROP TABLE "a"; DROP TABLE "b"; DROP TABLE "z" [destructive:table/a destructive:table/b destructive:table/z]`,
		`dropTable table/d false: DROP TABLE "d" [destructive:table/d]`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// refTable is a SQLite table with a text key and a column <ref>_id for
// each foreign key in refs, "<ref> <ON DELETE action>", nullable unless the
// action ends in "NOT NULL".
func refTable(name string, refs ...string) *Table {
	tb := &Table{
		Name: name, Kind: TableEntity, Origin: strings.ToUpper(name),
		Columns: []*Column{
			{Name: "id", Type: "TEXT"},
			{Name: "label", Type: "TEXT", Nullable: true},
		},
		PrimaryKey: &Constraint{Name: name + "_pkey", Columns: []string{"id"}},
	}
	for _, r := range refs {
		ref, action, _ := strings.Cut(r, " ")
		action, notNull := strings.CutSuffix(action, " NOT NULL")
		tb.Columns = append(tb.Columns, &Column{Name: ref + "_id", Type: "TEXT", Nullable: !notNull})
		tb.ForeignKeys = append(tb.ForeignKeys, &ForeignKey{
			Name: "fk_" + name + "_" + ref + "_id", Columns: []string{ref + "_id"}, RefTable: ref, RefColumns: []string{"id"}, OnDelete: action,
		})
	}
	return tb
}

// sqliteModelOf is a SQLite model of tables.
func sqliteModelOf(tables ...*Table) *Model {
	return &Model{Version: ModelVersion, Dialect: SQLite, Service: "s", Tables: tables}
}

// TestSQLiteRestrict: a step that drops a table in a reference cycle, or a
// table that references itself, defers the foreign key checks to its
// commit first. SQLite then runs no RESTRICT action and checks a RESTRICT
// key as it checks a NO ACTION one, at the commit, when the step has
// dropped every row the key protects; with the checks not deferred, the
// drop fails at once. So a RESTRICT key there plans as any other, NOT NULL
// or not, whether a drop or a rebuild meets it. Tables dropped one after
// the other need nothing. sqliteconverge applies such steps to rows that
// reference one another, and sees them fail without the deferral
// (TestConvergenceOnSQLite).
func TestSQLiteRestrict(t *testing.T) {
	// statements plans from one model to another and returns each step's
	// statements, each cut at its first parenthesis.
	statements := func(from, to *Model) string {
		t.Helper()
		plan, err := Diff(from, to, Options{})
		if err != nil {
			return err.Error()
		}
		var steps []string
		for _, step := range plan.Steps {
			var cut []string
			for _, stmt := range step.Statements {
				cut = append(cut, strings.SplitN(stmt, " (", 2)[0])
			}
			steps = append(steps, step.Op+": "+strings.Join(cut, "; "))
		}
		return strings.Join(steps, "\n")
	}
	// relabel makes label NOT NULL in the named tables, a change SQLite
	// rebuilds a table for.
	relabel := func(m *Model, names ...string) *Model {
		out := *m
		out.Tables = nil
		for _, t := range m.Tables {
			c := *t
			if slices.Contains(names, t.Name) {
				c.Columns = []*Column{t.Columns[0], {Name: "label", Type: "TEXT"}}
				c.Columns = append(c.Columns, t.Columns[2:]...)
			}
			out.Tables = append(out.Tables, &c)
		}
		return &out
	}
	empty := sqliteModelOf()
	self := func(action string) *Model { return sqliteModelOf(refTable("node", "node "+action)) }
	cycle := func(action string) *Model {
		return sqliteModelOf(refTable("p"), refTable("a", "p CASCADE", "b RESTRICT"), refTable("b", "a "+action))
	}
	const (
		dropNode    = `dropTable: PRAGMA defer_foreign_keys = ON; DROP TABLE "node"`
		dropCycle   = `dropTable: PRAGMA defer_foreign_keys = ON; DROP TABLE "a"; DROP TABLE "b"` + "\n" + `dropTable: DROP TABLE "p"`
		rebuildNode = `copyTable: PRAGMA defer_foreign_keys = ON; CREATE TABLE "_new_node"; INSERT INTO "_new_node"; ` +
			`DROP TABLE "node"; ALTER TABLE "_new_node" RENAME TO "node"`
		rebuildP = `copyTable: PRAGMA defer_foreign_keys = ON; CREATE TABLE "_new_p"; CREATE TABLE "_new_a"; CREATE TABLE "_new_b"; ` +
			`INSERT INTO "_new_p"; INSERT INTO "_new_a"; INSERT INTO "_new_b"; DROP TABLE "a"; DROP TABLE "b"; DROP TABLE "p"; ` +
			`ALTER TABLE "_new_p" RENAME TO "p"; ALTER TABLE "_new_a" RENAME TO "a"; ALTER TABLE "_new_b" RENAME TO "b"`
	)
	tests := []struct {
		name     string
		from, to *Model
		want     string
	}{
		{"a table that references itself, dropped", self("RESTRICT"), empty, dropNode},
		{"a table that references itself NOT NULL, dropped", self("RESTRICT NOT NULL"), empty, dropNode},
		{"a table that references itself, rebuilt", self("RESTRICT"), relabel(self("RESTRICT"), "node"), rebuildNode},
		{"a table that references itself NOT NULL, rebuilt", self("RESTRICT NOT NULL"), relabel(self("RESTRICT NOT NULL"), "node"), rebuildNode},
		{"a cycle, dropped", cycle("RESTRICT"), empty, dropCycle},
		{"a cycle NOT NULL, dropped", cycle("RESTRICT NOT NULL"), empty, dropCycle},
		// p is rebuilt, so a and b, which reference it, are rebuilt with it.
		{"a cycle NOT NULL that references a rebuilt table", cycle("RESTRICT NOT NULL"), relabel(cycle("RESTRICT NOT NULL"), "p"), rebuildP},
		{"a cycle of other actions, dropped",
			sqliteModelOf(refTable("a", "b CASCADE NOT NULL", "a NO ACTION NOT NULL"), refTable("b", "a SET NULL")), empty,
			`dropTable: PRAGMA defer_foreign_keys = ON; DROP TABLE "a"; DROP TABLE "b"`},
		{"tables dropped one after the other",
			sqliteModelOf(refTable("a", "b RESTRICT NOT NULL"), refTable("b")), empty,
			"dropTable: DROP TABLE \"a\"\ndropTable: DROP TABLE \"b\""},
	}
	for _, tc := range tests {
		if got := statements(tc.from, tc.to); got != tc.want {
			t.Errorf("%s:\n%s\nwant:\n%s", tc.name, got, tc.want)
		}
	}
}

// TestSQLiteRebuildClosure: a rebuild copies every table that references the
// rebuilt one, directly or through another table, and the rebuilt tables
// that reference one another share a step; a table that references none
// of them keeps its own. Each copy's foreign keys name the new tables until
// the renames, the copies run referenced tables first and the drops
// referencing ones first, and the hazards name every table copied.
func TestSQLiteRebuildClosure(t *testing.T) {
	from := sqliteModelOf(refTable("p"), refTable("child", "p CASCADE NOT NULL"), refTable("grandchild", "child CASCADE"),
		refTable("pinned", "p RESTRICT NOT NULL"), refTable("q"), refTable("other", "q NO ACTION"))
	to := sqliteModelOf(refTable("p"), refTable("child", "p CASCADE NOT NULL"), refTable("grandchild", "child CASCADE"),
		refTable("pinned", "p RESTRICT NOT NULL"), refTable("q"), refTable("other", "q NO ACTION"))
	to.Tables[0].Columns[1] = &Column{Name: "label", Type: "TEXT"}
	to.Tables[4].Columns[1] = &Column{Name: "label", Type: "TEXT"}
	plan, err := Diff(from, to, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 2 || plan.Steps[0].Subject != "table/p" || plan.Steps[1].Subject != "table/q" {
		t.Fatalf("steps:\n%s", plan.SQL())
	}
	step := plan.Steps[0]
	var got []string
	for _, stmt := range step.Statements {
		got = append(got, strings.SplitN(stmt, " (", 2)[0])
	}
	want := []string{
		"PRAGMA defer_foreign_keys = ON",
		`CREATE TABLE "_new_p"`, `CREATE TABLE "_new_child"`, `CREATE TABLE "_new_grandchild"`, `CREATE TABLE "_new_pinned"`,
		`INSERT INTO "_new_p"`, `INSERT INTO "_new_child"`, `INSERT INTO "_new_grandchild"`, `INSERT INTO "_new_pinned"`,
		`DROP TABLE "grandchild"`, `DROP TABLE "child"`, `DROP TABLE "pinned"`, `DROP TABLE "p"`,
		`ALTER TABLE "_new_p" RENAME TO "p"`, `ALTER TABLE "_new_child" RENAME TO "child"`,
		`ALTER TABLE "_new_grandchild" RENAME TO "grandchild"`, `ALTER TABLE "_new_pinned" RENAME TO "pinned"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("step %s:\n%s\nwant:\n%s", step.Subject, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, key := range []string{`REFERENCES "_new_p" ("id") ON DELETE CASCADE`, `REFERENCES "_new_child" ("id") ON DELETE CASCADE`, `REFERENCES "_new_p" ("id") ON DELETE RESTRICT`} {
		if !strings.Contains(strings.Join(step.Statements, "\n"), key) {
			t.Errorf("no copy declares %s", key)
		}
	}
	reasons := map[HazardClass]string{}
	for _, h := range step.Hazards {
		reasons[h.Class] = h.Reason
	}
	if want := "SQLite's ALTER TABLE cannot make label NOT NULL, so the step rebuilds p with the tables that reference it, directly or through another table: " +
		"child, grandchild and pinned. It copies the rows of each table it keeps into a new table, drops the old tables and renames the new ones."; reasons[HazardCopyTable] != want {
		t.Errorf("copy-table reason %q", reasons[HazardCopyTable])
	}
	if want := "Copying child, grandchild, p and pinned holds the database's write lock for time that grows with the tables."; reasons[HazardBlocking] != want {
		t.Errorf("blocking reason %q", reasons[HazardBlocking])
	}
	// q's rebuild copies other, and no table of p's.
	if all := strings.Join(plan.Steps[1].Statements, "\n"); !strings.Contains(all, `CREATE TABLE "_new_other"`) || strings.Contains(all, `"_new_p"`) {
		t.Errorf("q's rebuild:\n%s", all)
	}
}
