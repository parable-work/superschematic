package sqlmigrate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestDiffRefuses checks the plans Diff refuses, each with an error that
// names what it cannot do.
func TestDiffRefuses(t *testing.T) {
	tests := []struct {
		name    string
		pc      planCase
		edit    func(from, to *Model)
		renames []Rename
		want    string
	}{
		{name: "rename of a table the previous version lacks", renames: []Rename{{From: "invoice", To: "bill"}},
			want: "--rename invoice=bill: the previous version has no table invoice"},
		{name: "rename of a table the new version still has", renames: []Rename{{From: "label", To: "tag"}},
			want: "--rename label=tag: the new version still has a table label"},
		{name: "rename to a table the new version lacks",
			pc:      planCase{after: func(s *ir.Schema) { delete(s.Types, "Label"); dropField(s, "Order", "labels") }},
			renames: []Rename{{From: "label", To: "tag"}},
			want:    "--rename label=tag: the new version has no table tag"},
		{name: "rename to a table the previous version has",
			pc:      planCase{after: func(s *ir.Schema) { delete(s.Types, "Label"); dropField(s, "Order", "labels") }},
			renames: []Rename{{From: "label", To: "product"}},
			want:    "--rename label=product: the previous version already has a table product"},
		{name: "rename of a column the previous version lacks", renames: []Rename{{From: "order.amount", To: "order.total"}},
			want: "--rename order.amount=order.total: the previous version has no column order.amount"},
		{name: "rename of a column the new version still has", renames: []Rename{{From: "order.note", To: "order.reference"}},
			want: "--rename order.note=order.reference: the new version still has a column order.note"},
		{name: "rename to a column the new version lacks",
			pc:      planCase{after: func(s *ir.Schema) { dropField(s, "Order", "note") }},
			renames: []Rename{{From: "order.note", To: "order.memo"}},
			want:    "--rename order.note=order.memo: the new version has no column order.memo"},
		{name: "rename to a column the previous version has",
			pc:      planCase{after: func(s *ir.Schema) { dropField(s, "Order", "note") }},
			renames: []Rename{{From: "order.note", To: "order.reference"}},
			want:    "--rename order.note=order.reference: the previous version already has a column order.reference"},
		{name: "rename of a column across tables",
			pc:      planCase{after: func(s *ir.Schema) { dropField(s, "Order", "note") }},
			renames: []Rename{{From: "order.note", To: "customer.name"}},
			want:    "--rename order.note=customer.name: table order is order in the new version"},
		{name: "rename of a table to a column", renames: []Rename{{From: "order", To: "order.total"}},
			want: "--rename order=order.total: a rename is of a table"},
		{name: "partitioning of an existing history table",
			pc: planCase{
				before: func(s *ir.Schema) { versioned(s, "Order", nil) },
				after:  func(s *ir.Schema) { versioned(s, "Order", &ir.VersionedConfig{PartitionBy: "month"}) },
			},
			want: "table order_history changes from PARTITION BY \"\" to \"RANGE (recorded_at)\", which a plan cannot express"},
		{name: "type with no cast",
			pc:   planCase{after: func(s *ir.Schema) { fieldNamed(s, "Customer", "referrerId").TypeRef = int64Ref }},
			want: "column customer.referrer_id (Customer.referrerId) changes from UUID to BIGINT, which postgres cannot convert"},
		{name: "scalar to list",
			pc:   planCase{after: func(s *ir.Schema) { fieldNamed(s, "Order", "note").TypeRef.IsArray = true }},
			want: "changes from TEXT to TEXT[], which postgres cannot convert"},
		{name: "primary key",
			edit: func(from, to *Model) { tablesByName(to)["order"].PrimaryKey.Columns = []string{"code"} },
			want: "the primary key of table order changes, which a plan cannot express"},
		{name: "dialect",
			edit: func(from, to *Model) { from.Dialect = SQLite },
			want: "the previous model is for sqlite and the new one for postgres"},
		{name: "service",
			edit: func(from, to *Model) { from.Service = "other-db" },
			want: "the previous model is of service other-db and the new one of shop-db"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			from, to := tc.pc.models(t)
			if tc.edit != nil {
				tc.edit(from, to)
			}
			_, err := Diff(from, to, Options{Renames: tc.renames})
			if err == nil {
				t.Fatalf("Diff succeeded, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestBuildModelWithoutTables(t *testing.T) {
	model, err := BuildModel(ir.NewSchema("x", ir.SchemaKindDB), sqlgen.Options{SchemaName: "x"}, Postgres)
	if err != nil {
		t.Fatal(err)
	}
	if model.Service != "x" || model.Dialect != Postgres || len(model.Tables) != 0 {
		t.Fatalf("model = %+v, want an empty postgres model of x", model)
	}
	plan, err := Diff(nil, model, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.From != "" || len(plan.Steps) != 0 || plan.Steps == nil {
		t.Fatalf("plan from empty to empty: from %q, %d steps", plan.From, len(plan.Steps))
	}
}

// TestDiffIsPure plans the same pair twice and from the same models twice.
func TestDiffIsPure(t *testing.T) {
	pc := planCases[0]
	for _, c := range planCases {
		if c.name == "rename-table-with-dependents" {
			pc = c
		}
	}
	first, second := pc.plan(t), pc.plan(t)
	if first.Hash != second.Hash {
		t.Fatalf("two plans of one pair differ: %s and %s", first.Hash, second.Hash)
	}
	if first.Renames[0] != "order=purchase" {
		t.Fatalf("renames = %v", first.Renames)
	}
}

func TestMakeObjectName(t *testing.T) {
	long := strings.Repeat("t", 70)
	tests := []struct {
		name1, name2, label string
		want                string
	}{
		{"order", "", "pkey", "order_pkey"},
		{"order", "code", "key", "order_code_key"},
		{long, "", "pkey", strings.Repeat("t", 58) + "_pkey"},
		// The longer name is cut first, until the two are as long.
		{strings.Repeat("a", 40), strings.Repeat("b", 40), "key", strings.Repeat("a", 29) + "_" + strings.Repeat("b", 29) + "_key"},
		{strings.Repeat("a", 60), "code", "key", strings.Repeat("a", 54) + "_code_key"},
		// A multi-byte character is never cut in half.
		{strings.Repeat("a", 57) + "é", "", "pkey", strings.Repeat("a", 57) + "_pkey"},
	}
	for _, tc := range tests {
		got := makeObjectName(tc.name1, tc.name2, tc.label)
		if got != tc.want {
			t.Errorf("makeObjectName(%q, %q, %q) = %q, want %q", tc.name1, tc.name2, tc.label, got, tc.want)
		}
		if len(got) > 63 {
			t.Errorf("makeObjectName gave %d bytes", len(got))
		}
	}
}

// TestConstraintNames checks the names the model gives create.sql's
// unnamed constraints where Postgres does more than join names: a name cut
// to 63 bytes, two cut names that collide, and a name another table's
// constraint already holds. TestConvergenceOnPostgres checks the same
// names against Postgres.
func TestConstraintNames(t *testing.T) {
	model, err := BuildModel(namesSchema(), sqlgen.Options{SchemaName: "names"}, Postgres)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, table := range model.Tables {
		names = append(names, table.PrimaryKey.Name)
		for _, u := range table.Uniques {
			names = append(names, u.Name)
		}
	}
	long := "this_type_name_is_long_enough_to_make_postgres_cut_its_key_name"
	column := "a_column_name_long_enough_that_it_is_cut_at_the_end_one"
	want := []string{
		"order_pkey", "order_item_code_key",
		"order_item_pkey", "order_item_code_key1",
		long[:58] + "_pkey",
		// The longer label of the second leaves one byte less for the
		// column.
		long[:29] + "_" + column[:28] + "_key1",
		long[:29] + "_" + column[:29] + "_key",
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("names:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
}

func TestConvert(t *testing.T) {
	tests := []struct {
		from, to string
		kind     conversionKind
		lossy    bool
	}{
		{"TEXT", "TEXT", convertSame, false},
		{"varchar(4096)", "VARCHAR(4096)", convertSame, false},
		{"VARCHAR(80)", "TEXT", convertCoercible, false},
		{"VARCHAR(80)", "VARCHAR(100)", convertCoercible, false},
		{"VARCHAR(80)", "VARCHAR(16)", convertRewrite, true},
		{"TEXT", "VARCHAR(80)", convertRewrite, true},
		{"TEXT", "CITEXT", convertRewrite, false},
		{"TEXT", "UUID", convertMayFail, false},
		{"UUID", "TEXT", convertRewrite, false},
		{"INTEGER", "BIGINT", convertRewrite, false},
		{"BIGINT", "INTEGER", convertMayFail, false},
		{"BIGINT", "DOUBLE PRECISION", convertRewrite, true},
		{"INTEGER", "DOUBLE PRECISION", convertRewrite, false},
		{"DOUBLE PRECISION", "BIGINT", convertMayFail, true},
		{"NUMERIC(10,2)", "NUMERIC", convertCoercible, false},
		{"NUMERIC(10,2)", "NUMERIC(12,2)", convertCoercible, false},
		{"NUMERIC(10,2)", "NUMERIC(10,1)", convertMayFail, true},
		{"DATE", "TIMESTAMPTZ", convertRewrite, false},
		{"TIMESTAMPTZ", "DATE", convertRewrite, true},
		{"TEXT[]", "UUID[]", convertMayFail, false},
		{"VARCHAR(80)[]", "TEXT[]", convertRewrite, false},
		{"TEXT", "TEXT[]", convertImpossible, false},
		{"UUID", "BIGINT", convertImpossible, false},
		{"JSONB", "UUID", convertImpossible, false},
	}
	for _, tc := range tests {
		got := pgConvert(tc.from, tc.to)
		if got.kind != tc.kind || got.lossy != tc.lossy {
			t.Errorf("convert(%s, %s) = %+v, want kind %d lossy %t", tc.from, tc.to, got, tc.kind, tc.lossy)
		}
	}
}

func TestRenameInView(t *testing.T) {
	r := newRenames()
	r.addTable("purchase", "order", "")
	r.addColumn("purchase", "total", "order", "amount", "")
	r.addColumn("customer", "name", "customer", "full_name", "")
	statement := "CREATE VIEW report.v WITH (security_barrier = true) AS\n" +
		"-- Projection column order is the published Arrow contract.\n" +
		"SELECT -- noqa: ST06\n" +
		"  base.total,\n" +
		"  customer.\"name\" AS customer_name\n" +
		"FROM purchase AS base\n" +
		"  INNER JOIN customer AS customer ON base.customer_id = customer.id\n" +
		"WHERE base.total = current_setting('app.total')::bigint\n" +
		"ORDER BY base.total ASC"
	want := "CREATE VIEW report.v WITH (security_barrier = true) AS\n" +
		"-- Projection column order is the published Arrow contract.\n" +
		"SELECT -- noqa: ST06\n" +
		"  base.amount AS total,\n" +
		"  customer.full_name AS customer_name\n" +
		"FROM \"order\" AS base\n" +
		"  INNER JOIN customer AS customer ON base.customer_id = customer.id\n" +
		"WHERE base.amount = current_setting('app.total')::bigint\n" +
		"ORDER BY base.amount ASC"
	if got := renameInView(statement, r); got != want {
		t.Errorf("renameInView:\n%s\nwant:\n%s", got, want)
	}
}

// TestDialectSeam plans through a dialect whose ALTER TABLE cannot change
// a column's type or drop a column, as SQLite's cannot change a type: the
// planner hands those changes, by table and phase, to rebuild, with the
// other changes of the table in that phase and the tables that reference
// it, and every other change to render, with no change to the diff.
func TestDialectSeam(t *testing.T) {
	pc := planCase{after: func(s *ir.Schema) {
		fieldNamed(s, "OrderLine", "sku").TypeRef = ir.TypeRef{Name: "Contact.PhoneNumber"}
		fieldNamed(s, "OrderLine", "quantity").TypeRef = ir.TypeRef{Name: "number"}
		dropField(s, "Order", "note")
		addField(s, "Order", &ir.FieldDef{Name: "discount", TypeRef: int64Ref})
	}}
	from, to := pc.models(t)
	fake := &rebuildingDialect{}
	saved := dialects[Postgres]
	dialects[Postgres] = fake
	defer func() { dialects[Postgres] = saved }()
	plan, err := Diff(from, to, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, step := range plan.Steps {
		ops = append(ops, string(step.Phase)+" "+step.Op+" "+step.Subject)
	}
	want := []string{
		"expand addColumn table/order/column/discount",
		"expand copyTable table/order_line",
		"contract copyTable table/order",
	}
	if strings.Join(ops, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(ops, "\n"), strings.Join(want, "\n"))
	}
	if len(fake.rebuilds) != 2 {
		t.Fatalf("rebuilds = %v", fake.rebuilds)
	}
	// The rebuild of order copies the tables that reference it too.
	if got := fake.rebuilds[0]; got != "order_line expand 2: quantity DOUBLE PRECISION sku VARCHAR(16) [order_line]" {
		t.Errorf("first rebuild = %q", got)
	}
	if got := fake.rebuilds[1]; got != "order contract 1: note dropped [order order_label order_line]" {
		t.Errorf("second rebuild = %q", got)
	}
	// The shared hazards of every change a rebuild makes ride on its step.
	classes := map[string][]HazardClass{}
	for _, step := range plan.Steps {
		for _, h := range step.Hazards {
			classes[step.Subject] = append(classes[step.Subject], h.Class)
		}
	}
	if got := classes["table/order_line"]; len(got) < 3 {
		t.Errorf("the order_line rebuild carries %v", got)
	}
	if got := classes["table/order"]; len(got) != 2 || got[0] != HazardDestructive || got[1] != HazardCopyTable {
		t.Errorf("the order rebuild carries %v, want destructive and copy-table", got)
	}
}

// rebuildingDialect is Postgres with a narrow ALTER TABLE: it rebuilds a
// table for a type change or a dropped column.
type rebuildingDialect struct {
	postgresDialect
	rebuilds []string
}

func (d *rebuildingDialect) canAlter(c *change) bool {
	return c.op != opAlterColumnType && c.op != opDropColumn && c.op != opDropNotNull
}

func (d *rebuildingDialect) rebuild(rb *tableRebuild) (rendered, error) {
	tables := map[string]*rebuiltTable{}
	var names []string
	for _, rt := range rb.tables {
		tables[rt.name] = rt
		names = append(names, rt.name)
	}
	var parts []string
	for _, c := range rb.changes {
		before, after := tables[c.table].before, tables[c.table].after
		switch c.op {
		case opAlterColumnType:
			for _, r := range c.alter.retypes {
				parts = append(parts, r.after.Name+" "+columnNamed(after, r.after.Name).Type)
			}
		case opDropColumn:
			if columnNamed(before, c.column.Name) != nil && columnNamed(after, c.column.Name) == nil {
				parts = append(parts, c.column.Name+" dropped")
			}
		}
	}
	d.rebuilds = append(d.rebuilds, fmt.Sprintf("%s %s %d: %s [%s]", rb.at.table, rb.at.phase, len(rb.changes), strings.Join(parts, " "), strings.Join(names, " ")))
	step := &Step{Op: "copyTable", Subject: tableSubject(rb.at.table), Statements: []string{"-- rebuild " + rb.at.table}, Transactional: true}
	step.Hazards = append(step.Hazards, &Hazard{
		ID: HazardID(HazardCopyTable, step.Subject, ""), Class: HazardCopyTable, Subject: step.Subject, Reason: "rebuilt",
	})
	return one(step), nil
}

// TestDropOrder drops tables that reference each other: each before the
// tables it references, and in a cycle the foreign key that points back
// first.
func TestDropOrder(t *testing.T) {
	table := func(name string, refs ...string) *Table {
		tb := &Table{Name: name, Kind: TableEntity, Columns: []*Column{{Name: "id", Type: "UUID"}},
			PrimaryKey: &Constraint{Name: name + "_pkey", Columns: []string{"id"}}}
		for _, ref := range refs {
			tb.Columns = append(tb.Columns, &Column{Name: ref + "_id", Type: "UUID", Nullable: true})
			tb.ForeignKeys = append(tb.ForeignKeys, &ForeignKey{
				Name: "fk_" + name + "_" + ref + "_id", Columns: []string{ref + "_id"}, RefTable: ref, RefColumns: []string{"id"}, OnDelete: "CASCADE",
			})
		}
		return tb
	}
	from := &Model{Version: ModelVersion, Dialect: Postgres, Service: "s", Tables: []*Table{
		table("a", "b"), table("b", "a"), table("c", "d"), table("d"),
	}}
	to := &Model{Version: ModelVersion, Dialect: Postgres, Service: "s"}
	plan, err := Diff(from, to, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, step := range plan.Steps {
		got = append(got, step.Statements...)
	}
	want := []string{
		"ALTER TABLE b DROP CONSTRAINT fk_b_a_id",
		"DROP TABLE c",
		"DROP TABLE d",
		"DROP TABLE a",
		"DROP TABLE b",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("statements:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestGraphContentWithoutDDL: columns that join or leave a graph member's
// content while they stay get one step with no SQL and a history hazard
// that names them. A column the plan adds or renames carries its own
// hazard, and a renamed column that stays in the content is no content
// change.
func TestGraphContentWithoutDDL(t *testing.T) {
	model := func(epoch int, content ...string) *Model {
		var columns []*Column
		for _, name := range []string{"id", "a", "b", "c", "old"} {
			columns = append(columns, &Column{Name: name, Origin: "Step." + name, Type: "TEXT"})
		}
		return &Model{
			Version: ModelVersion, Dialect: Postgres, Service: "s",
			Tables: []*Table{{Name: "step", Kind: TableEntity, Columns: columns,
				PrimaryKey: &Constraint{Name: "step_pkey", Columns: []string{"id"}}}},
			Graphs: []*Graph{{Name: "Recipe", SchemaEpoch: epoch, Members: []*GraphMember{{Table: "step", Content: content}}}},
		}
	}
	from := model(1, "a", "old")
	to := model(1, "b", "c", "d", "new")
	to.Tables[0].Columns[4].Name, to.Tables[0].Columns[4].Origin = "new", "Step.new"
	to.Tables[0].Columns = append(to.Tables[0].Columns, &Column{Name: "d", Origin: "Step.d", Type: "TEXT", Nullable: true})
	plan, err := Diff(from, to, Options{Renames: []Rename{{From: "step.old", To: "step.new"}}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, step := range plan.Steps {
		for _, h := range step.Hazards {
			if h.Class == HazardHistory {
				got = append(got, fmt.Sprintf("%s %s %d: %s", step.Op, step.Subject, len(step.Statements), h.Reason))
			}
		}
	}
	want := []string{
		"renameColumn table/step/column/new 1: Step.old is content of version graph Recipe: commits made before this change hash and merge rows of the old shape. The graph's schemaEpoch stays 1.",
		"addColumn table/step/column/d 1: Step.d is content of version graph Recipe: commits made before this change hash and merge rows of the old shape. The graph's schemaEpoch stays 1.",
		"changeGraphContent table/step 0: Step.b and Step.c join and Step.a leaves the content of step in version graph Recipe: commits made before this change hash and merge rows of the old shape. The graph's schemaEpoch stays 1.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("history hazards:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
