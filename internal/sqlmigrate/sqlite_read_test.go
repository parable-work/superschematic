package sqlmigrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
)

// inspectTestdata holds files built by hand, each as the statements that
// build it and the inspection the migration runner's tests hold it to
// (runtime/migrate/go's inspect_test.go).
const inspectTestdata = "../../runtime/migrate/testdata/inspect"

// byHandDir is the schema that adopts by-hand.sql.
const byHandDir = "testdata/services/byhand"

func readInspection(t *testing.T, name string) *Inspection {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(inspectTestdata, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := ReadInspection(data)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func byHandModel(t *testing.T) *Model {
	t.Helper()
	model, err := BuildModel(planCase{base: byHandDir}.load(t, nil), sqlgen.Options{SchemaName: "byhand-db"}, SQLite)
	if err != nil {
		t.Fatal(err)
	}
	return model
}

// byHandDrops name the trigger and the view of by-hand.sql.
var byHandDrops = []Drop{{Kind: DropView, Name: "order_totals"}, {Kind: DropTrigger, Name: "order_audit"}}

func tableOf(t *testing.T, m *Model, name string) *Table {
	t.Helper()
	table := tablesByName(m)[name]
	if table == nil {
		t.Fatalf("the model has no table %s", name)
	}
	return table
}

// TestReadInspection: the compiler reads the inspection the runner
// writes, and refuses another version and a member it does not know, which
// a runner of another release writes.
func TestReadInspection(t *testing.T) {
	in := readInspection(t, "by-hand")
	if len(in.Tables) != 3 || len(in.Views) != 1 || len(in.Triggers) != 1 {
		t.Fatalf("inspection = %+v", in)
	}
	for doc, want := range map[string]string{
		`{"version": 2, "tables": [], "views": [], "triggers": []}`:             "inspects in version 2, and this compiler reads version 1",
		`{"version": 1, "tables": [], "views": [], "triggers": [], "extra": 1}`: "use the migration runner of this release",
	} {
		if _, err := ReadInspection([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", doc, err, want)
		}
	}
}

// TestReadDatabaseByHand (D116): a file built by hand reads into a model
// in the model's forms, with what only the compiler knows taken from the
// schema; its trigger and its view stop the read until a drop names each;
// and the plan from it drops them first, rebuilds the table whose
// definition holds a UNIQUE, and changes exactly what the file lacks or
// holds over the schema. The goldens are testdata/database/by-hand.*.json;
// regenerate with go test ./internal/sqlmigrate -run TestReadDatabaseByHand -update
func TestReadDatabaseByHand(t *testing.T) {
	schema, in := byHandModel(t), readInspection(t, "by-hand")

	_, err := ReadDatabase(in, schema, nil, nil)
	if err == nil {
		t.Fatal("a file with a trigger and a view no model holds reads")
	}
	for _, want := range []string{
		"the file holds 2 objects the model has no form of",
		"\n  trigger order_audit on order\n", "\n  view order_totals\n",
		"\n  --drop trigger:order_audit", "\n  --drop view:order_totals",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
	_, err = ReadDatabase(in, schema, nil, byHandDrops[:1])
	if err == nil || !strings.Contains(err.Error(), "the file holds 1 object the model has no form of") || strings.Contains(err.Error(), "--drop view:order_totals") {
		t.Errorf("with the view named: %v", err)
	}
	for _, tc := range []struct {
		drop Drop
		want string
	}{
		{Drop{Kind: DropView, Name: "missing"}, "--drop view:missing: the file holds no view missing"},
		{Drop{Kind: DropTrigger, Name: "order_totals"}, "--drop trigger:order_totals: the file holds no trigger order_totals"},
		{Drop{Kind: DropIndex, Name: "order_by_total"}, "--drop index:order_by_total: the index is the schema's own, and the plan decides what becomes of it"},
		{byHandDrops[0], "--drop view:order_totals is given twice"},
	} {
		if _, err := ReadDatabase(in, schema, nil, append(append([]Drop(nil), byHandDrops...), tc.drop)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.drop, err, tc.want)
		}
	}

	db, err := ReadDatabase(in, schema, nil, byHandDrops)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := db.Model.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, filepath.Join("testdata", "database", "by-hand.model.json"), indentedJSON(t, canonical))

	customer, order, audit := tableOf(t, db.Model, "customer"), tableOf(t, db.Model, "order"), tableOf(t, db.Model, "audit_log")
	var names []string
	for _, col := range customer.Columns {
		names = append(names, col.Name)
	}
	if want := []string{"name", "id", "email", "active", "nickname"}; !reflect.DeepEqual(names, want) {
		t.Errorf("customer's columns = %v, want the file's order %v", names, want)
	}
	if email := columnNamed(customer, "email"); email.Type != "TEXT" || email.Origin != "Customer.email" {
		t.Errorf("customer.email = %+v, want VARCHAR(255) read as TEXT, with the schema's origin", email)
	}
	if active := columnNamed(customer, "active"); active.Default != "1" {
		t.Errorf("customer.active defaults to %s, want TRUE read as the schema's 1", active.Default)
	}
	if want := []string{"UNIQUE (email) in the table's definition"}; !reflect.DeepEqual(customer.Unmodeled, want) {
		t.Errorf("customer.unmodeled = %v, want %v", customer.Unmodeled, want)
	}
	if len(customer.Uniques) != 1 || customer.Uniques[0].Name != "sqlite_autoindex_customer_2" {
		t.Errorf("customer.uniques = %+v, want the UNIQUE under the name SQLite gave its index", customer.Uniques)
	}
	if tags := columnNamed(order, "tags"); tags.Holds != holdsList || tags.Element != "TEXT" {
		t.Errorf("order.tags = %+v, want the list the schema says its text holds", tags)
	}
	if len(order.Checks) != 1 || order.Checks[0].Name != "ck_order_state" || !reflect.DeepEqual(order.Checks[0].In, []string{"open", "shipped"}) {
		t.Errorf("order.checks = %+v", order.Checks)
	}
	if len(order.ForeignKeys) != 0 || len(order.Indexes) != 1 || order.Indexes[0].Name != "order_by_total" {
		t.Errorf("order has foreign keys %+v and indexes %+v", order.ForeignKeys, order.Indexes)
	}
	if audit.Kind != TableEntity || audit.Origin != "" || columnNamed(audit, "id").Nullable || !reflect.DeepEqual(audit.Unmodeled, []string{"AUTOINCREMENT on id"}) {
		t.Errorf("audit_log = %+v", audit)
	}

	plan, err := DiffDatabase(db, schema, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if from, _ := db.Model.Hash(); plan.From != from {
		t.Errorf("the plan starts from %s, and the file's model is %s", plan.From, from)
	}
	sealed := *plan
	if err := sealed.Seal(); err != nil || sealed.Hash != plan.Hash {
		t.Errorf("the plan is not sealed")
	}
	var steps []string
	for i, step := range plan.Steps {
		if step.Index != i+1 || !step.Transactional {
			t.Errorf("step %d has index %d, transactional %t", i+1, step.Index, step.Transactional)
		}
		steps = append(steps, string(step.Phase)+" "+step.Op+" "+step.Subject)
	}
	want := []string{
		"expand dropTrigger trigger/order/order_audit",
		"expand dropView view/order_totals",
		"expand copyTable table/customer",
		"expand copyTable table/order",
		"contract copyTable table/order",
		"contract dropColumn table/customer/column/nickname",
		"contract dropTable table/audit_log",
	}
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("steps:\n  %s\nwant:\n  %s", strings.Join(steps, "\n  "), strings.Join(want, "\n  "))
	}
	checkExpandedHash(t, plan, true)
	plan.ToModel, plan.ExpandedModel = nil, nil
	canonical, err = plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, filepath.Join("testdata", "database", "by-hand.plan.json"), indentedJSON(t, canonical))

	// Each drop is destructive and compat, and gated as any hazard is.
	ids := map[string]bool{}
	for _, hazard := range plan.Hazards() {
		ids[hazard.ID] = true
	}
	for _, id := range []string{
		"destructive:trigger/order/order_audit", "compat:trigger/order/order_audit",
		"destructive:view/order_totals", "compat:view/order_totals",
		"copy-table:table/customer", "destructive:table/customer/column/nickname", "destructive:table/audit_log",
	} {
		if !ids[id] {
			t.Errorf("the plan has no hazard %s", id)
		}
	}
	stopped := plan.Unallowed([]HazardClass{HazardDestructive}, []string{"destructive:table/audit_log", "destructive:table/customer/column/nickname"})
	if len(stopped) != 2 || stopped[0].ID != "destructive:trigger/order/order_audit" || stopped[1].ID != "destructive:view/order_totals" {
		t.Errorf("--fail-on destructive stops on %+v", stopped)
	}
	// The rebuild's reason names what it drops of the table's definition.
	for _, step := range plan.Steps {
		if step.Subject != "table/customer" || step.Op != "copyTable" {
			continue
		}
		for _, hazard := range step.Hazards {
			if hazard.Class == HazardCopyTable && !strings.Contains(hazard.Reason, "drop what the table's definition holds and the model has no form of (UNIQUE (email) in the table's definition)") {
				t.Errorf("the rebuild's reason: %s", hazard.Reason)
			}
			if hazard.Class == HazardCompat || hazard.Class == HazardDataDependent && strings.Contains(hazard.Reason, "unique") {
				t.Errorf("customer.email was unique already: %s", hazard.Reason)
			}
		}
	}
}

// formsSchema is a model that declares part of what forms.sql builds:
// child and parent, child's bump trigger, and raw SQL that creates the
// search table and its trigger, an index and a view.
func formsSchema() *Model {
	number := func(f float64) *float64 { return &f }
	return &Model{
		Version: ModelVersion, Dialect: SQLite, Service: "forms-db",
		Tables: []*Table{
			{
				Name: "child", Kind: TableEntity, Origin: "Child",
				Columns: []*Column{
					{Name: "id", Origin: "Child.id", Type: "TEXT"},
					{Name: "parent_id", Origin: "Child.parent", Type: "INTEGER", Nullable: true},
					{Name: "amount", Origin: "Child.amount", Type: "NUMERIC", Nullable: true, Default: "0"},
					{Name: "title", Origin: "Child.title", Type: "TEXT", Default: "'untitled'"},
					{Name: "kind", Origin: "Child.kind", Type: "TEXT", Nullable: true},
					{Name: "flag", Origin: "Child.flag", Type: "INTEGER", Nullable: true, Default: "0"},
					{Name: "payload", Origin: "Child.payload", Type: "TEXT", Nullable: true, Holds: holdsJSON},
					{Name: "body", Origin: "Child.body", Type: "TEXT", Nullable: true, Holds: holdsJSON},
					{Name: "search_text", Type: "TEXT", Nullable: true, Generated: `COALESCE("title", '') || ' ' || COALESCE("body", '')`},
				},
				PrimaryKey:  &Constraint{Name: "child_pkey", Columns: []string{"id"}},
				ForeignKeys: []*ForeignKey{{Name: "fk_child_parent", Columns: []string{"parent_id"}, RefTable: "parent", RefColumns: []string{"id"}, OnDelete: "SET NULL"}},
				Checks:      []*Check{{Name: "ck_child_amount_range", Column: "amount", Min: number(0), Max: number(100.5)}},
				Indexes: []*Index{
					{Name: "idx_child_parent", Columns: []string{"parent_id"}, Method: "BTREE"},
					{Name: "uq_child_title", Columns: []string{"title"}, Method: "BTREE", Unique: true, Where: "kind = 'a'", Expression: "LOWER(title)", InTransaction: true},
				},
			},
			{
				Name: "parent", Kind: TableEntity, Origin: "Parent",
				Columns: []*Column{
					{Name: "id", Origin: "Parent.id", Type: "INTEGER"},
					{Name: "code", Origin: "Parent.code", Type: "TEXT COLLATE NOCASE"},
					{Name: "label", Origin: "Parent.label", Type: "TEXT", Nullable: true},
				},
				PrimaryKey: &Constraint{Name: "parent_pkey", Columns: []string{"id"}},
				Uniques:    []*Constraint{{Name: "parent_code_label_key", Columns: []string{"code", "label"}}},
			},
		},
		Triggers: []*Trigger{{
			Name: "trg_child_bump_version", Table: "child", Origin: "Child",
			Definition: `CREATE TRIGGER "trg_child_bump_version" AFTER UPDATE ON "child" FOR EACH ROW WHEN NEW.amount = OLD.amount BEGIN UPDATE "child" SET amount = OLD.amount + 1 WHERE rowid = NEW.rowid; END`,
		}},
		Graphs: []*Graph{
			{Name: "family", SchemaEpoch: 3, Members: []*GraphMember{
				{Table: "child", Content: []string{"body", "title"}},
				{Table: "parent", Content: []string{"code"}},
			}},
			// The file holds no table of this graph's.
			{Name: "elsewhere", SchemaEpoch: 1, Members: []*GraphMember{{Table: "absent", Content: []string{"name"}}}},
		},
		RawSQL: []*RawSQL{
			{ID: "search", Origin: "Child", Position: "tables", Checksum: "1", Statements: []string{
				"CREATE VIRTUAL TABLE IF NOT EXISTS child_fts USING fts5(title, body)",
				"CREATE TRIGGER IF NOT EXISTS child_fts_insert AFTER INSERT ON child BEGIN INSERT INTO child_fts (rowid, title, body) VALUES (NEW.rowid, NEW.title, NEW.body); END",
			}},
			{ID: "body", Origin: "Child", Position: "indexes", Checksum: "2", Statements: []string{"create index if not exists main.child_body on child (body)"}},
			// The file's view of this name selects another list.
			{ID: "titles", Origin: "Child", Position: "indexes", Checksum: "3", Statements: []string{"CREATE VIEW IF NOT EXISTS child_titles AS SELECT id FROM child"}},
			// A piece that creates nothing shows no sign of having run.
			{ID: "seed", Origin: "Child", Position: "indexes", Checksum: "4", Statements: []string{"INSERT OR IGNORE INTO kv (k, v) VALUES ('a', 'b')"}},
		},
	}
}

// TestReadDatabaseForms (D116): each form a file built by hand may take
// reads as the model form the read's rules give it. The file is forms.sql,
// as the migration runner inspects it.
func TestReadDatabaseForms(t *testing.T) {
	schema, in := formsSchema(), readInspection(t, "forms")

	// What the schema's raw SQL creates is the raw SQL's: only the index
	// and the virtual table nothing declares stop the read.
	_, err := ReadDatabase(in, schema, nil, nil)
	if err == nil {
		t.Fatal("a file with objects no model holds reads")
	}
	for _, want := range []string{
		"the file holds 2 objects the model has no form of",
		"\n  index child_kind_title on child\n", "\n  virtual table stray_fts\n",
		"\n  --drop index:child_kind_title", "\n  --drop virtual-table:stray_fts",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
	for _, own := range []string{"child_fts", "child_fts_insert", "child_titles", "trg_child_bump_version", "child_body"} {
		if strings.Contains(err.Error(), own+"\n") || strings.HasSuffix(err.Error(), own) {
			t.Errorf("the refusal names %s, which the schema declares:\n%v", own, err)
		}
	}
	if _, err := ReadDatabase(in, schema, nil, []Drop{{Kind: DropVirtualTable, Name: "child_fts"}}); err == nil ||
		!strings.Contains(err.Error(), "--drop virtual-table:child_fts: the virtual table is the schema's own") {
		t.Errorf("dropping what the raw SQL creates: %v", err)
	}

	db, err := ReadDatabase(in, schema, nil, []Drop{{Kind: DropVirtualTable, Name: "stray_fts"}, {Kind: DropIndex, Name: "child_kind_title"}})
	if err != nil {
		t.Fatal(err)
	}
	var drops []string
	for _, step := range db.Drops {
		drops = append(drops, step.Op+" "+step.Subject+": "+strings.Join(step.Statements, "; "))
	}
	if want := []string{
		`dropIndex table/child/index/child_kind_title: DROP INDEX "child_kind_title"`,
		`dropTable table/stray_fts: DROP TABLE "stray_fts"`,
	}; !reflect.DeepEqual(drops, want) {
		t.Errorf("drops = %q, want %q", drops, want)
	}

	var tables []string
	for _, table := range db.Model.Tables {
		tables = append(tables, table.Name)
	}
	if want := []string{"child", "kv", "parent", "seq", "strict_log"}; !reflect.DeepEqual(tables, want) {
		t.Errorf("tables = %v, want %v: no virtual table, and none of its shadow tables", tables, want)
	}

	child := tableOf(t, db.Model, "child")
	columns := map[string]Column{}
	for _, col := range child.Columns {
		columns[col.Name] = *col
	}
	for name, want := range map[string]Column{
		// A PRIMARY KEY column of a rowid table takes NULL unless it is the rowid.
		"id":        {Name: "id", Origin: "Child.id", Type: "TEXT", Nullable: true},
		"parent_id": {Name: "parent_id", Origin: "Child.parent", Type: "INTEGER", Nullable: true},
		"other_id":  {Name: "other_id", Type: "INTEGER", Nullable: true},
		"amount":    {Name: "amount", Origin: "Child.amount", Type: "NUMERIC", Nullable: true, Default: "0"},
		"title":     {Name: "title", Origin: "Child.title", Type: "TEXT", Default: "'untitled'"},
		"kind":      {Name: "kind", Origin: "Child.kind", Type: "TEXT", Nullable: true},
		// BOOLEAN has NUMERIC affinity; FALSE is the schema's 0.
		"flag": {Name: "flag", Origin: "Child.flag", Type: "NUMERIC", Nullable: true, Default: "0"},
		"made": {Name: "made", Type: "TEXT", Nullable: true, Default: "(CURRENT_TIMESTAMP)"},
		// A column with no type has BLOB affinity, so it holds no JSON text.
		"payload": {Name: "payload", Origin: "Child.payload", Type: "BLOB", Nullable: true},
		"ratio":   {Name: "ratio", Type: "REAL", Nullable: true},
		"body":    {Name: "body", Origin: "Child.body", Type: "TEXT", Nullable: true, Holds: holdsJSON},
		// A STORED column reads as the model's VIRTUAL one, in the schema's spelling.
		"search_text": {Name: "search_text", Type: "TEXT", Nullable: true, Generated: `COALESCE("title", '') || ' ' || COALESCE("body", '')`},
	} {
		if got, ok := columns[name]; !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("child.%s = %+v, want %+v", name, got, want)
		}
	}
	if len(columns) != 12 {
		t.Errorf("child has %d columns", len(columns))
	}
	if want := []string{
		"DEFERRABLE INITIALLY DEFERRED on a foreign key of other_id",
		"CHECK (amount < ratio)",
		"CHECK (kind IS NOT NULL OR flag)",
		"the foreign key over (other_id) to missing, whose key the file does not hold",
		"ON UPDATE CASCADE on the foreign key over (parent_id)",
	}; !reflect.DeepEqual(child.Unmodeled, want) {
		t.Errorf("child.unmodeled:\n  %s\nwant:\n  %s", strings.Join(child.Unmodeled, "\n  "), strings.Join(want, "\n  "))
	}
	one, eighty := 1, 80
	zero, most := 0.0, 100.5
	if want := []*Check{
		{Name: "ck_child_amount_range", Column: "amount", Min: &zero, Max: &most},
		{Name: "ck_child_kind", Column: "kind", In: []string{"a", "b'c"}},
		{Name: "ck_child_title", Column: "title", MinLength: &one, MaxLength: &eighty},
	}; !reflect.DeepEqual(child.Checks, want) {
		t.Errorf("child.checks = %s", describe(child.Checks))
	}
	// A key that names no columns references its table's primary key, and
	// takes the schema's name for the same key.
	if want := []*ForeignKey{{Name: "fk_child_parent", Columns: []string{"parent_id"}, RefTable: "parent", RefColumns: []string{"id"}, OnDelete: "SET NULL"}}; !reflect.DeepEqual(child.ForeignKeys, want) {
		t.Errorf("child.foreignKeys = %s", describe(child.ForeignKeys))
	}
	if want := []*Index{
		// A descending key is its column.
		{Name: "child_by_parent", Columns: []string{"parent_id"}, Method: "BTREE"},
		// The schema's index, by its predicate and expression, in the schema's spelling.
		{Name: "child_title_lower", Columns: []string{"title"}, Method: "BTREE", Unique: true, Where: "kind = 'a'", Expression: "LOWER(title)", InTransaction: true},
		// One key in another collation is an expression.
		{Name: "child_title_nocase", Columns: []string{"title"}, Method: "BTREE", Expression: "title COLLATE NOCASE"},
	}; !reflect.DeepEqual(child.Indexes, want) {
		t.Errorf("child.indexes = %s", describe(child.Indexes))
	}

	parent := tableOf(t, db.Model, "parent")
	if id := columnNamed(parent, "id"); id.Nullable || id.Type != "INTEGER" {
		t.Errorf("parent.id = %+v, want the rowid, which is never NULL", id)
	}
	if code := columnNamed(parent, "code"); code.Type != sqliteNoCase {
		t.Errorf("parent.code is %s", code.Type)
	}
	if want := []string{"COLLATE RTRIM on label", "UNIQUE (code, label) in the table's definition"}; !reflect.DeepEqual(parent.Unmodeled, want) {
		t.Errorf("parent.unmodeled = %q", parent.Unmodeled)
	}
	if want := []*Constraint{{Name: "sqlite_autoindex_parent_1", Columns: []string{"code", "label"}}}; !reflect.DeepEqual(parent.Uniques, want) {
		t.Errorf("parent.uniques = %s", describe(parent.Uniques))
	}
	for name, want := range map[string][]string{
		"strict_log": {"STRICT"},
		"kv":         {"WITHOUT ROWID"},
		"seq":        {"AUTOINCREMENT on id", "ON CONFLICT REPLACE on v", "UNIQUE (v) in the table's definition"},
	} {
		table := tableOf(t, db.Model, name)
		if !reflect.DeepEqual(table.Unmodeled, want) || len(table.Uniques) != 0 || table.Origin != "" {
			t.Errorf("%s: unmodeled %q, uniques %s, origin %q", name, table.Unmodeled, describe(table.Uniques), table.Origin)
		}
	}

	if len(db.Model.Triggers) != 1 || !reflect.DeepEqual(db.Model.Triggers[0], schema.Triggers[0]) {
		t.Errorf("triggers = %s, want the schema's bump trigger alone", describe(db.Model.Triggers))
	}
	// A version graph whose member tables the file all holds is carried,
	// with the columns the schema does not know counted as content.
	if want := []*Graph{{Name: "family", SchemaEpoch: 3, Members: []*GraphMember{
		{Table: "child", Content: []string{"body", "made", "other_id", "ratio", "title"}},
		{Table: "parent", Content: []string{"code"}},
	}}}; !reflect.DeepEqual(db.Model.Graphs, want) {
		t.Errorf("graphs = %s", describe(db.Model.Graphs))
	}
	var ran []string
	for _, piece := range db.Model.RawSQL {
		ran = append(ran, piece.ID)
	}
	if want := []string{"search", "body"}; !reflect.DeepEqual(ran, want) {
		t.Errorf("raw SQL read as run = %v, want %v", ran, want)
	}

	// The plan rebuilds each table that holds what the model has no form
	// of, runs the raw SQL the file does not show to have run, and drops
	// the tables the schema does not declare.
	// payload has no type in the file, so BLOB affinity, which nothing
	// converts to the JSON text the schema holds there: the plan says so.
	if _, err := DiffDatabase(db, schema, Options{}); err == nil || !strings.Contains(err.Error(), "column child.payload (Child.payload) changes from a scalar (BLOB) to a JSON value (TEXT), which sqlite cannot convert") {
		t.Fatalf("a plan that turns an untyped column into JSON: %v", err)
	}
	columnNamed(tableOf(t, schema, "child"), "payload").Type = "BLOB"
	columnNamed(tableOf(t, schema, "child"), "payload").Holds = ""
	plan, err := DiffDatabase(db, schema, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]bool{}
	for _, step := range plan.Steps {
		ops[string(step.Phase)+" "+step.Op+" "+step.Subject] = true
	}
	for _, want := range []string{
		"expand dropIndex table/child/index/child_kind_title", "expand dropTable table/stray_fts",
		"expand copyTable table/child", "expand runRawSQL rawSql/titles", "expand runRawSQL rawSql/seed",
		"contract dropTable table/kv", "contract dropTable table/seq", "contract dropTable table/strict_log",
	} {
		if !ops[want] {
			t.Errorf("the plan has no step %q", want)
		}
	}
	for op := range ops {
		if strings.Contains(op, "rawSql/search") || strings.Contains(op, "rawSql/body") {
			t.Errorf("the plan runs raw SQL the file holds: %s", op)
		}
	}
}

// describe prints a value's JSON, for a failure's message.
func describe(v any) string {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(out)
}

// compactJSONOf is a value's JSON on one line.
func compactJSONOf(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(out)
}

// TestReadDatabaseRenames: a table and a column --rename names take what
// only the compiler knows from the schema's table and column of the new
// names, so the plan renames them and converts nothing.
func TestReadDatabaseRenames(t *testing.T) {
	schema := byHandModel(t)
	in := readInspection(t, "by-hand")
	for i := range in.Tables {
		table := &in.Tables[i]
		if table.Name != "order" {
			continue
		}
		table.Name = "purchase"
		table.SQL = strings.Replace(table.SQL, `"order"`, "purchase", 1)
		for j := range table.Columns {
			if table.Columns[j].Name == "tags" {
				table.Columns[j].Name = "labels"
			}
		}
		table.SQL = strings.Replace(table.SQL, "tags TEXT", "labels TEXT", 1)
	}
	in.Views, in.Triggers = nil, nil
	renames := []Rename{{From: "purchase", To: "order"}, {From: "purchase.labels", To: "order.tags"}}

	db, err := ReadDatabase(in, schema, renames, nil)
	if err != nil {
		t.Fatal(err)
	}
	purchase := tableOf(t, db.Model, "purchase")
	if labels := columnNamed(purchase, "labels"); purchase.Origin != "Order" || labels.Origin != "Order.tags" || labels.Holds != holdsList {
		t.Errorf("purchase = origin %q, labels %+v", purchase.Origin, labels)
	}
	plan, err := DiffDatabase(db, schema, Options{Renames: renames})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) < 2 || plan.Steps[0].Op != string(opRenameTable) || plan.Steps[1].Op != string(opRenameColumn) {
		t.Errorf("the plan does not start with the renames: %s", describe(plan.Steps))
	}

	// Without the renames the file's table is none the schema declares.
	db, err = ReadDatabase(in, schema, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if purchase := tableOf(t, db.Model, "purchase"); purchase.Origin != "" || columnNamed(purchase, "labels").Holds != "" {
		t.Errorf("purchase read without its rename = %s", describe(purchase))
	}
}

// TestReadDatabaseRefusesANameInAnotherCase: SQLite takes Customer and
// customer as one name, and a plan would create the one beside the other.
func TestReadDatabaseRefusesANameInAnotherCase(t *testing.T) {
	schema := byHandModel(t)
	in := readInspection(t, "by-hand")
	in.Views, in.Triggers = nil, nil
	for i := range in.Tables {
		table := &in.Tables[i]
		switch table.Name {
		case "customer":
			table.Name = "Customer"
		case "order":
			table.Columns[2].Name = "Total"
		}
	}
	_, err := ReadDatabase(in, schema, nil, nil)
	for _, want := range []string{
		"the file's table Customer is the schema's customer but for its case",
		"the file's column order.Total is the schema's order.total but for its case",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v, want %q", err, want)
		}
	}
}

// TestSQLiteDeclaredAffinity: a declared type reads as its affinity, by
// SQLite's five rules in their order.
func TestSQLiteDeclaredAffinity(t *testing.T) {
	for declared, want := range map[string]string{
		"INT": "INTEGER", "BIGINT": "INTEGER", "unsigned big int": "INTEGER", "POINT": "INTEGER", "CHARINT": "INTEGER",
		"VARCHAR(255)": "TEXT", "nchar(55)": "TEXT", "CLOB": "TEXT", "text": "TEXT",
		"BLOB": "BLOB", "": "BLOB",
		"REAL": "REAL", "DOUBLE PRECISION": "REAL", "FLOAT": "REAL",
		"NUMERIC": "NUMERIC", "DECIMAL(10,5)": "NUMERIC", "BOOLEAN": "NUMERIC", "DATETIME": "NUMERIC", "JSON": "NUMERIC", "STRING": "NUMERIC",
	} {
		if got := sqliteDeclaredAffinity(declared); got != want {
			t.Errorf("sqliteDeclaredAffinity(%q) = %s, want %s", declared, got, want)
		}
	}
}

// TestParseSQLiteCheck: a predicate in one of the schema's forms reads as
// its rule, however it is spaced, cased or quoted; any other reads as
// none.
func TestParseSQLiteCheck(t *testing.T) {
	columns := []string{"state", "rating", "title"}
	for expr, want := range map[string]string{
		`"state" IN ('a', 'b')`:                           `{"name":"","column":"state","in":["a","b"]}`,
		"(STATE in('it''s'))":                             `{"name":"","column":"state","in":["it's"]}`,
		`"rating" >= 1 AND "rating" <= 5`:                 `{"name":"","column":"rating","min":1,"max":5}`,
		"rating>=-1.5":                                    `{"name":"","column":"rating","min":-1.5}`,
		"(rating <= 5) and (rating >= 0)":                 `{"name":"","column":"rating","min":0,"max":5}`,
		`length("title") >= 1 AND length("title") <= 100`: `{"name":"","column":"title","minLength":1,"maxLength":100}`,
		"/* short */ LENGTH(title) <= 9 -- chars":         `{"name":"","column":"title","maxLength":9}`,
	} {
		check, ok := parseSQLiteCheck(expr, columns)
		if got := compactJSONOf(check); !ok || got != want {
			t.Errorf("parseSQLiteCheck(%s) = %s, %t, want %s", expr, got, ok, want)
		}
	}
	for _, expr := range []string{
		"rating > 1", "rating >= 1 OR rating <= 5", "rating >= 1 AND title <= 5", "rating >= 1 AND rating >= 2",
		"state IN (1, 2)", "state IN ('a') AND rating >= 1", "other >= 1", "rating BETWEEN 1 AND 5", "length(title) >= 1.5",
		"rating >= 1 + 1", "state NOT IN ('a')", "",
	} {
		if check, ok := parseSQLiteCheck(expr, columns); ok {
			t.Errorf("parseSQLiteCheck(%s) = %s, want none", expr, compactJSONOf(check))
		}
	}
}

// TestParseSQLiteTable: the clauses only a table's statement holds are
// read where they stand, and not inside a default, a type or a string.
func TestParseSQLiteTable(t *testing.T) {
	def, err := parseSQLiteTable(`CREATE TABLE "t" (
		[a] VARCHAR(10) COLLATE nocase DEFAULT ('CHECK (x) COLLATE RTRIM'), -- a comment, CHECK (y)
		` + "`b`" + ` INT CONSTRAINT positive CHECK (b > 0) DEFAULT (CAST('1' AS INT)),
		c TEXT AS (a || 'AS (z)') STORED,
		d INTEGER DEFAULT CURRENT_TIMESTAMP NOT NULL ON CONFLICT IGNORE,
		"check" TEXT REFERENCES p (id) NOT DEFERRABLE INITIALLY DEFERRED,
		e TEXT REFERENCES p (id) DEFERRABLE INITIALLY DEFERRED,
		CONSTRAINT both CHECK (a <> b /* , */),
		PRIMARY KEY (a, b) ON CONFLICT ROLLBACK,
		FOREIGN KEY (a) REFERENCES p (id) DEFERRABLE INITIALLY DEFERRED
	) WITHOUT ROWID`)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]sqliteColumnClauses{
		"a": {collation: "NOCASE"}, "b": {}, "c": {generated: "a || 'AS (z)'"}, "d": {}, "check": {}, "e": {},
	} {
		if got := def.columns[name]; got == nil || *got != want {
			t.Errorf("column %s = %+v, want %+v", name, got, want)
		}
	}
	if len(def.columns) != 6 {
		t.Errorf("the definition has %d columns", len(def.columns))
	}
	if want := []sqliteCheckSQL{{column: "b", expr: "b > 0"}, {expr: "a <> b /* , */"}}; !reflect.DeepEqual(def.checks, want) {
		t.Errorf("checks = %+v", def.checks)
	}
	if want := []string{
		"ON CONFLICT IGNORE on d", "DEFERRABLE INITIALLY DEFERRED on a foreign key of e",
		"ON CONFLICT ROLLBACK", "DEFERRABLE INITIALLY DEFERRED on a foreign key",
	}; !reflect.DeepEqual(def.unmodeled, want) {
		t.Errorf("unmodeled = %q", def.unmodeled)
	}
	if _, err := parseSQLiteTable("CREATE VIRTUAL TABLE t USING fts5"); err == nil {
		t.Error("a statement with no column list reads")
	}
}

// TestParseSQLiteIndex: the key list and the predicate are read as
// written.
func TestParseSQLiteIndex(t *testing.T) {
	for sql, want := range map[string][2]string{
		`CREATE UNIQUE INDEX "i" ON "t" ((lower(label)))`:           {"(lower(label))", ""},
		`CREATE INDEX i ON t (a, b DESC) WHERE a IS NOT NULL`:       {"a, b DESC", "a IS NOT NULL"},
		"CREATE INDEX [on] ON t(\n  a\n)\nwhere (b = ')' or c) ;\n": {"a", "(b = ')' or c)"},
	} {
		keys, where, err := parseSQLiteIndex(sql)
		if err != nil || keys != want[0] || where != want[1] {
			t.Errorf("parseSQLiteIndex(%s) = %q, %q, %v", sql, keys, where, err)
		}
	}
	for key, want := range map[string]string{"(lower(label))": "lower(label)", "((a + b)) DESC": "(a + b)", "title COLLATE NOCASE asc": "title COLLATE NOCASE", "(a) || (b)": "(a) || (b)"} {
		if got := keyExpression(key); got != want {
			t.Errorf("keyExpression(%s) = %s, want %s", key, got, want)
		}
	}
}

// TestCreatedBy: the object a raw SQL statement creates is named as the
// file names it, whatever the statement adds that sqlite_schema leaves out.
func TestCreatedBy(t *testing.T) {
	for statement, want := range map[string]string{
		"CREATE TABLE IF NOT EXISTS main.audit (id INTEGER)":      "table audit",
		`create unique index if not exists "By Name" on t (name)`: "index By Name",
		"CREATE VIRTUAL TABLE notes_fts USING fts5(body);":        "virtual table notes_fts",
		"CREATE TEMP VIEW v AS SELECT 1":                          "view v",
		"CREATE TRIGGER t AFTER INSERT ON n BEGIN SELECT 1; END":  "trigger t",
		"DROP VIEW IF EXISTS v":                                   "",
		"INSERT INTO t VALUES ('CREATE TABLE x (y)')":             "",
		"CREATE": "",
	} {
		kind, name, _, ok := createdBy(statement)
		if got := strings.TrimSpace(kind + " " + name); got != want || ok != (want != "") {
			t.Errorf("createdBy(%s) = %q, %t, want %q", statement, got, ok, want)
		}
	}
	_, _, body, _ := createdBy("CREATE INDEX IF NOT EXISTS main.i ON t (a);")
	if !sameTokens(body, lexSQLite(`create index "i" on T(a)`)) {
		t.Error("a statement is not the one sqlite_schema keeps of it")
	}
}

// TestParseDrop: a --drop value names a kind and a name.
func TestParseDrop(t *testing.T) {
	for value, want := range map[string]Drop{
		"trigger:order_audit": {Kind: DropTrigger, Name: "order_audit"},
		"view:a:b":            {Kind: DropView, Name: "a:b"},
		"index:i":             {Kind: DropIndex, Name: "i"},
		"virtual-table:fts":   {Kind: DropVirtualTable, Name: "fts"},
	} {
		if got, err := ParseDrop(value); err != nil || got != want || got.String() != value {
			t.Errorf("ParseDrop(%s) = %+v, %v", value, got, err)
		}
	}
	for _, value := range []string{"order_audit", "table:order", "view:", ":x", "Trigger:x"} {
		if _, err := ParseDrop(value); err == nil || !strings.Contains(err.Error(), "want <kind>:<name>, the kind one of trigger, view, index and virtual-table") {
			t.Errorf("ParseDrop(%s): %v", value, err)
		}
	}
}
