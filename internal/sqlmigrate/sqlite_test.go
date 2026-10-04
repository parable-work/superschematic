package sqlmigrate

import (
	"bytes"
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
		{"@searchField", shop(func(s *ir.Schema) { fieldNamed(s, "Product", "title").SearchField = true }),
			"Product.title is a @searchField, which the sqlite dialect does not support"},
		{"projection", planCase{}.load(t, func(s *ir.Schema) { fieldNamed(s, "Product", "title").SearchField = false }),
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
// transaction with no recovery, only a rebuild or a dropped table turns
// foreign keys off, and no statement ends in a semicolon.
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
			if step.ForeignKeysOff != (step.Op == "copyTable" || step.Op == "dropTable") {
				t.Errorf("%s: step %d %s has foreignKeysOff %t", pc.name, step.Index, step.Op, step.ForeignKeysOff)
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
	})
}

// TestSQLiteRebuildBatches: each table is rebuilt at most once per phase,
// and a table rebuilt in a phase has no other step in it but its renames.
func TestSQLiteRebuildBatches(t *testing.T) {
	for _, pc := range sqlitePlanCases() {
		plan := pc.plan(t)
		rebuilt := map[string]bool{}
		for _, step := range plan.Steps {
			if step.Op != "copyTable" {
				continue
			}
			key := string(step.Phase) + " " + step.Subject
			if rebuilt[key] {
				t.Errorf("%s: %s is rebuilt twice in %s", pc.name, step.Subject, step.Phase)
			}
			rebuilt[key] = true
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
		got := sqliteDialect{}.convert(tc.from, tc.to)
		if got.kind != tc.kind || got.lossy != tc.lossy {
			t.Errorf("convert(%s, %s) = %+v, want kind %d lossy %t", tc.from, tc.to, got, tc.kind, tc.lossy)
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

// TestSQLiteDropCycle: SQLite cannot drop two tables that reference each
// other, and the plan says so.
func TestSQLiteDropCycle(t *testing.T) {
	table := func(name, ref string) *Table {
		return &Table{
			Name: name, Kind: TableEntity,
			Columns:     []*Column{{Name: "id", Type: "TEXT"}, {Name: ref + "_id", Type: "TEXT", Nullable: true}},
			PrimaryKey:  &Constraint{Name: name + "_pkey", Columns: []string{"id"}},
			ForeignKeys: []*ForeignKey{{Name: "fk_" + name + "_" + ref + "_id", Columns: []string{ref + "_id"}, RefTable: ref, RefColumns: []string{"id"}, OnDelete: "CASCADE"}},
		}
	}
	from := &Model{Version: ModelVersion, Dialect: SQLite, Service: "s", Tables: []*Table{table("a", "b"), table("b", "a")}}
	_, err := Diff(from, &Model{Version: ModelVersion, Dialect: SQLite, Service: "s"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "the sqlite dialect cannot drop foreign key fk_b_a_id to break the cycle") {
		t.Fatalf("Diff = %v, want the cycle refused", err)
	}
}
