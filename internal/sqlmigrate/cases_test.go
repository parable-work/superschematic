package sqlmigrate

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// The plan cases: pairs of a previous and a new version of a schema, one
// change each. The previous version is a service directory as it loads,
// optionally changed by before; the new version is the same directory
// changed by after. plan_golden_test.go plans each pair and compares the
// plan with its golden copy, hazards_test.go checks their hazard classes,
// and converge_test.go applies them to Postgres.

const (
	// shopDir is the base most cases change.
	shopDir = "testdata/services/shop"
	// sqlgenFixtures holds the services sqlgen's goldens are built from.
	sqlgenFixtures = "../loader/tsreader/testdata/services"
)

type planCase struct {
	name string
	// base is the service directory both versions load: shopDir when empty.
	base string
	// fromEmpty plans from an empty database: Diff's nil model.
	fromEmpty bool
	// before changes the previous version; after the new one.
	before, after func(s *ir.Schema)
	renames       []Rename
	readersBefore []Read
	readersAfter  []Read
	// viewOwner is the new version's outputs.sql.viewOwner.
	viewOwner string
	// noConverge leaves the case out of the Postgres convergence test, and
	// noSeed applies it to tables with no rows, each with the reason.
	noConverge string
	noSeed     string
	// dialect is the dialect the case plans for: Postgres when empty.
	// noSQLite leaves a Postgres case out of the SQLite cases
	// (sqlitePlanCases), with the reason.
	dialect  Dialect
	noSQLite string
	// lists are rows of product a SQLite case seeds with lists in its
	// items column, each with the JSON text it holds after the plan.
	lists listRows
}

func (pc planCase) dialectOrDefault() Dialect {
	if pc.dialect == "" {
		return Postgres
	}
	return pc.dialect
}

// golden names the case's golden file, without .json: the case's name, and
// <name>.sqlite for SQLite.
func (pc planCase) golden() string {
	if pc.dialectOrDefault() == SQLite {
		return pc.name + ".sqlite"
	}
	return pc.name
}

func (pc planCase) load(t *testing.T, change func(*ir.Schema)) *ir.Schema {
	t.Helper()
	dir := pc.base
	if dir == "" {
		dir = shopDir
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	if change != nil {
		change(schema)
	}
	return schema
}

func (pc planCase) service() string {
	if pc.base == "" {
		return "shop-db"
	}
	return filepath.Base(pc.base)
}

// versions loads both versions' schemas and their sqlgen options.
func (pc planCase) versions(t *testing.T) (from, to *ir.Schema, fromOpts, toOpts sqlgen.Options) {
	t.Helper()
	from, to = pc.load(t, pc.before), pc.load(t, pc.after)
	fromOpts = sqlgen.Options{SchemaName: pc.service()}
	toOpts = sqlgen.Options{SchemaName: pc.service(), ViewOwner: pc.viewOwner}
	return from, to, fromOpts, toOpts
}

// models builds both versions' models.
func (pc planCase) models(t *testing.T) (from, to *Model) {
	t.Helper()
	fromSchema, toSchema, fromOpts, toOpts := pc.versions(t)
	var err error
	if from, err = BuildModel(fromSchema, fromOpts, pc.dialectOrDefault()); err != nil {
		t.Fatalf("model of the previous version: %v", err)
	}
	if to, err = BuildModel(toSchema, toOpts, pc.dialectOrDefault()); err != nil {
		t.Fatalf("model of the new version: %v", err)
	}
	return from, to
}

func (pc planCase) plan(t *testing.T) *Plan {
	t.Helper()
	from, to := pc.models(t)
	if pc.fromEmpty {
		from = nil
	}
	plan, err := Diff(from, to, Options{Renames: pc.renames, ReadersBefore: pc.readersBefore, ReadersAfter: pc.readersAfter})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	return plan
}

// Schema changes the cases make.

func typeNamed(s *ir.Schema, name string) *ir.TypeDef {
	td := s.Types[name]
	if td == nil {
		panic("no type " + name)
	}
	return td
}

func fieldNamed(s *ir.Schema, typeName, name string) *ir.FieldDef {
	for _, f := range typeNamed(s, typeName).Fields {
		if f.Name == name {
			return f
		}
	}
	panic("no field " + typeName + "." + name)
}

func addField(s *ir.Schema, typeName string, f *ir.FieldDef) {
	td := typeNamed(s, typeName)
	td.Fields = append(td.Fields, f)
}

func dropField(s *ir.Schema, typeName, name string) {
	td := typeNamed(s, typeName)
	for i, f := range td.Fields {
		if f.Name == name {
			td.Fields = append(td.Fields[:i], td.Fields[i+1:]...)
			return
		}
	}
	panic("no field " + typeName + "." + name)
}

// renameType renames a type and every reference to it.
func renameType(s *ir.Schema, from, to string) {
	td := typeNamed(s, from)
	delete(s.Types, from)
	td.Name = to
	s.Types[to] = td
	for _, other := range s.Types {
		for _, f := range other.Fields {
			if f.TypeRef.Name == from {
				f.TypeRef.Name = to
			}
			if f.Relation != nil && f.Relation.Type == from {
				f.Relation.Type = to
			}
		}
		if p := other.Projection; p != nil {
			if p.Source == from {
				p.Source = to
			}
			for _, j := range p.Joins {
				if j.Type == from {
					j.Type = to
				}
			}
		}
	}
}

func versioned(s *ir.Schema, typeName string, cfg *ir.VersionedConfig) {
	td := typeNamed(s, typeName)
	td.Versioned = true
	td.VersionedConfig = cfg
}

func intPtr(n int) *int { return &n }

var (
	stringRef = ir.TypeRef{Name: "string"}
	int64Ref  = ir.TypeRef{Name: "Generic.Int64"}
	uuidRef   = ir.TypeRef{Name: "Identity.UUID"}
	nameRef   = ir.TypeRef{Name: "Identity.Name"}
)

// planCases are the plan goldens, in the order D27's Testing section lists
// the changes.
var planCases = []planCase{
	{name: "from-empty", fromEmpty: true},

	// Columns.
	{name: "add-column", after: func(s *ir.Schema) {
		addField(s, "Order", &ir.FieldDef{Name: "discount", TypeRef: int64Ref})
	}},
	{name: "add-required-column", after: func(s *ir.Schema) {
		addField(s, "Order", &ir.FieldDef{Name: "channel", TypeRef: stringRef, Required: true})
	}, noSeed: "a required column without a default fails on a table with rows"},
	{name: "add-required-column-with-default", after: func(s *ir.Schema) {
		addField(s, "Order", &ir.FieldDef{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true})
	}},
	{name: "drop-column", after: func(s *ir.Schema) {
		dropField(s, "Order", "note")
	}, readersAfter: []Read{{Reader: "lagging-api", Via: "OrderView.note", Table: "order", Column: "note"}}},
	{name: "drop-required-column", after: func(s *ir.Schema) {
		dropField(s, "OrderLine", "sku")
	}},
	{name: "rename-column-as-drop-add", after: renameCustomerToBuyer,
		noSeed: "the added buyer_id is required and has no default"},
	{name: "rename-column", after: renameCustomerToBuyer,
		renames:       []Rename{{From: "order.customer_id", To: "order.buyer_id"}},
		readersBefore: []Read{{Reader: "shop-api", Via: "OrderView.customer", Table: "order", Column: "customer_id"}}},
	{name: "rename-unique-column", after: func(s *ir.Schema) {
		fieldNamed(s, "Customer", "email").Name = "contactEmail"
	}, renames: []Rename{{From: "customer.email", To: "customer.contact_email"}}},
	{name: "retype-column", after: func(s *ir.Schema) {
		// The projection reads the column, so it is dropped and created
		// again around the change, and publishes the new type.
		fieldNamed(s, "Order", "code").TypeRef = stringRef
		fieldNamed(s, "OrderSummary", "code").TypeRef = stringRef
	}, readersBefore: []Read{{Reader: "shop-api", Via: "OrderView.code", Table: "order", Column: "code"}}},
	{name: "retype-column-cast", after: func(s *ir.Schema) {
		fieldNamed(s, "Order", "reference").TypeRef = uuidRef
	}},
	{name: "retype-column-narrow", after: func(s *ir.Schema) {
		fieldNamed(s, "OrderLine", "sku").TypeRef = ir.TypeRef{Name: "Contact.PhoneNumber"}
	}},
	{name: "make-column-required", after: func(s *ir.Schema) {
		fieldNamed(s, "Order", "note").Required = true
	}},
	{name: "make-column-optional", after: func(s *ir.Schema) {
		fieldNamed(s, "Order", "total").Required = false
	}},

	// Tables.
	{name: "add-table", after: func(s *ir.Schema) {
		s.Types["Coupon"] = &ir.TypeDef{Name: "Coupon", Role: ir.RoleDBTable, Comment: "A discount code.", Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: uuidRef, Required: true, Key: true, AutoGenerated: true},
			{Name: "code", TypeRef: stringRef, Required: true, Unique: true},
			{Name: "percent", TypeRef: int64Ref, Required: true},
		}}
	}},
	{name: "drop-table", after: func(s *ir.Schema) { delete(s.Types, "Product") }},
	// Two tables that reference each other: Postgres drops the foreign key
	// that closes the cycle first, SQLite drops both in one step.
	{name: "drop-tables-in-cycle", before: addSupplierAndWarehouse},
	{name: "rename-table-as-drop-add", after: func(s *ir.Schema) { renameType(s, "Label", "Tag") }},
	{name: "rename-table", after: func(s *ir.Schema) { renameType(s, "Label", "Tag") },
		renames: []Rename{{From: "label", To: "tag"}}},
	{name: "rename-table-with-dependents", after: func(s *ir.Schema) { renameType(s, "Order", "Purchase") },
		renames:       []Rename{{From: "order", To: "purchase"}},
		readersBefore: []Read{{Reader: "shop-api", Via: "OrderView.total", Table: "order", Column: "total"}}},
	{name: "rename-versioned-table", noSQLite: "sqlite does not support @versioned",
		before: func(s *ir.Schema) { versioned(s, "Order", &ir.VersionedConfig{RetentionDays: intPtr(30)}) },
		after: func(s *ir.Schema) {
			versioned(s, "Order", &ir.VersionedConfig{RetentionDays: intPtr(30)})
			renameType(s, "Order", "Purchase")
		},
		renames: []Rename{{From: "order", To: "purchase"}}},

	// Indexes and unique fields.
	{name: "add-index", after: func(s *ir.Schema) {
		typeNamed(s, "OrderLine").Indexes = []ir.IndexDef{{Keys: []string{"sku"}}}
	}},
	{name: "add-unique-index", after: func(s *ir.Schema) {
		typeNamed(s, "OrderLine").Indexes = []ir.IndexDef{{Keys: []string{"sku"}, Unique: true, Name: "sku"}}
	}},
	{name: "drop-index", after: func(s *ir.Schema) { typeNamed(s, "Order").Indexes = nil }},
	{name: "add-unique", after: func(s *ir.Schema) { fieldNamed(s, "Order", "code").Unique = true }},
	{name: "drop-unique", after: func(s *ir.Schema) { fieldNamed(s, "Customer", "email").Unique = false }},

	// Relations and join tables.
	{name: "add-relation", after: func(s *ir.Schema) {
		addField(s, "OrderLine", &ir.FieldDef{Name: "product", TypeRef: ir.TypeRef{Name: "Product"}})
	}},
	{name: "relation-on-existing-column", after: func(s *ir.Schema) {
		fieldNamed(s, "Customer", "referrerId").Relation = &ir.RelationDef{Type: "Customer", OnDelete: "SET NULL"}
	}},
	{name: "change-on-delete", after: func(s *ir.Schema) {
		fieldNamed(s, "Order", "customer").Relation.OnDelete = "CASCADE"
	}},
	// Expand renames the foreign key with its column, so contract replaces
	// it under its new name.
	{name: "rename-column-and-change-on-delete", after: func(s *ir.Schema) {
		renameCustomerToBuyer(s)
		fieldNamed(s, "Order", "buyer").Relation.OnDelete = "CASCADE"
	}, renames: []Rename{{From: "order.customer_id", To: "order.buyer_id"}}},
	{name: "drop-relation", after: func(s *ir.Schema) {
		dropField(s, "Order", "customer")
		typeNamed(s, "Order").Indexes = nil
		fixSummaryWithoutCustomer(s)
	}},
	{name: "add-join-table", after: func(s *ir.Schema) {
		addField(s, "Product", &ir.FieldDef{Name: "labels", TypeRef: ir.TypeRef{Name: "Label", IsArray: true}, ManyToMany: true})
	}},
	{name: "drop-join-table", after: func(s *ir.Schema) { dropField(s, "Order", "labels") }},

	// @searchField.
	{name: "add-search-field", noSQLite: "sqlite does not support @searchField", after: func(s *ir.Schema) { fieldNamed(s, "Customer", "name").SearchField = true }},
	{name: "change-search-fields", noSQLite: "sqlite does not support @searchField", after: func(s *ir.Schema) {
		addField(s, "Product", &ir.FieldDef{Name: "blurb", TypeRef: stringRef, SearchField: true})
	}},
	{name: "drop-search-field", noSQLite: "sqlite does not support @searchField", after: func(s *ir.Schema) { fieldNamed(s, "Product", "title").SearchField = false }},
	{name: "retype-search-field", noSQLite: "sqlite does not support @searchField", after: func(s *ir.Schema) { fieldNamed(s, "Product", "title").TypeRef = stringRef }},

	// @versioned and @optimistic.
	{name: "versioned-on", noSQLite: "sqlite does not support @versioned", after: func(s *ir.Schema) { versioned(s, "Order", nil) }},
	{name: "versioned-on-with-exclude", noSQLite: "sqlite does not support @versioned", after: func(s *ir.Schema) {
		versioned(s, "Order", &ir.VersionedConfig{Exclude: []string{"note"}})
	}},
	{name: "versioned-off", noSQLite: "sqlite does not support @versioned", before: func(s *ir.Schema) { versioned(s, "Order", nil) }},
	{name: "versioned-retention", noSQLite: "sqlite does not support @versioned",
		before: func(s *ir.Schema) { versioned(s, "Order", nil) },
		after:  func(s *ir.Schema) { versioned(s, "Order", &ir.VersionedConfig{RetentionDays: intPtr(30)}) }},
	{name: "versioned-retention-change", noSQLite: "sqlite does not support @versioned",
		before: func(s *ir.Schema) { versioned(s, "Order", &ir.VersionedConfig{RetentionDays: intPtr(30)}) },
		after:  func(s *ir.Schema) { versioned(s, "Order", &ir.VersionedConfig{RetentionDays: intPtr(90)}) }},
	{name: "versioned-prune-keep", noSQLite: "sqlite does not support @versioned",
		before: func(s *ir.Schema) { versioned(s, "Order", &ir.VersionedConfig{RetentionDays: intPtr(30)}) },
		after: func(s *ir.Schema) {
			versioned(s, "Order", &ir.VersionedConfig{RetentionDays: intPtr(30), PruneKeepReferencedBy: []*ir.PruneReference{
				{Table: "order_line", KeyColumn: "order_id", VersionColumn: "quantity"},
			}})
		}},
	{name: "versioned-exclude", noSQLite: "sqlite does not support @versioned",
		before: func(s *ir.Schema) { versioned(s, "Order", nil) },
		after:  func(s *ir.Schema) { versioned(s, "Order", &ir.VersionedConfig{Exclude: []string{"note"}}) }},
	{name: "versioned-partitioned-on", noSQLite: "sqlite does not support @versioned", after: func(s *ir.Schema) {
		versioned(s, "Order", &ir.VersionedConfig{PartitionBy: "month"})
	}},
	{name: "versioned-partitioned-retention", noSQLite: "sqlite does not support @versioned",
		before: func(s *ir.Schema) { versioned(s, "Order", &ir.VersionedConfig{PartitionBy: "month"}) },
		after: func(s *ir.Schema) {
			versioned(s, "Order", &ir.VersionedConfig{PartitionBy: "month", RetentionDays: intPtr(30)})
		}},
	{name: "versioned-retype", noSQLite: "sqlite does not support @versioned",
		before: func(s *ir.Schema) { versioned(s, "Order", nil) },
		after: func(s *ir.Schema) {
			versioned(s, "Order", nil)
			fieldNamed(s, "Order", "total").TypeRef = ir.TypeRef{Name: "number"}
			fieldNamed(s, "OrderSummary", "total").TypeRef = ir.TypeRef{Name: "number"}
		}},
	{name: "optimistic-on", noSQLite: "sqlite does not support @optimistic", after: func(s *ir.Schema) { typeNamed(s, "Product").Optimistic = true }},
	{name: "optimistic-off", noSQLite: "sqlite does not support @optimistic", before: func(s *ir.Schema) { typeNamed(s, "Product").Optimistic = true }},
	{name: "optimistic-to-versioned", noSQLite: "sqlite does not support @optimistic or @versioned",
		before: func(s *ir.Schema) { typeNamed(s, "Product").Optimistic = true },
		after:  func(s *ir.Schema) { versioned(s, "Product", nil) }},
	{name: "versioned-to-optimistic", noSQLite: "sqlite does not support @optimistic or @versioned",
		before: func(s *ir.Schema) { versioned(s, "Product", nil) },
		after:  func(s *ir.Schema) { typeNamed(s, "Product").Optimistic = true }},

	// Projections.
	{name: "add-projection", noSQLite: "sqlite does not support projections", after: func(s *ir.Schema) {
		s.Types["CustomerDirectory"] = &ir.TypeDef{
			Name: "CustomerDirectory", Role: ir.RoleProjection, Comment: "Customers by name.",
			Projection: &ir.ProjectionDef{Pool: "report", Name: "customer_directory", Migration: "20260102000000", Source: "Customer"},
			Fields: []*ir.FieldDef{
				{Name: "name", TypeRef: nameRef, Required: true},
				{Name: "email", TypeRef: ir.TypeRef{Name: "Contact.Email"}, Required: true},
			},
		}
	}},
	{name: "drop-projection", noSQLite: "sqlite does not support projections", after: func(s *ir.Schema) { delete(s.Types, "OrderSummary") }},
	{name: "change-projection", noSQLite: "sqlite does not support projections", after: func(s *ir.Schema) {
		addField(s, "OrderSummary", &ir.FieldDef{
			Name: "placedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true,
		})
	}},
	{name: "projection-owner", noSQLite: "sqlite does not support projections", viewOwner: "report_owner", noConverge: "the role report_owner does not exist"},

	// Version graphs.
	{name: "graph-content-retype", noSQLite: "the version graph fixture is @versioned", base: filepath.Join(sqlgenFixtures, "fixture-version-graph-db"), after: func(s *ir.Schema) {
		fieldNamed(s, "Note", "body").TypeRef = ir.TypeRef{Name: "Generic.JSON"}
		typeNamed(s, "Recipe").VersionGraph.SchemaEpoch = 2
	}, noSeed: "the seeded text is not JSON"},
	{name: "graph-content-add", noSQLite: "the version graph fixture is @versioned", base: filepath.Join(sqlgenFixtures, "fixture-version-graph-db"), after: func(s *ir.Schema) {
		addField(s, "Cover", &ir.FieldDef{Name: "caption", TypeRef: stringRef})
	}},
	// A field leaves the content and no column changes: a step with no SQL.
	{name: "graph-content-exclude", noSQLite: "the version graph fixture is @versioned", base: filepath.Join(sqlgenFixtures, "fixture-version-graph-db"), after: func(s *ir.Schema) {
		fieldNamed(s, "Note", "body").ConflictUnit = ir.ConflictUnitExcluded
		typeNamed(s, "Recipe").VersionGraph.SchemaEpoch = 2
	}},
}

// addSupplierAndWarehouse adds two tables that reference each other: a
// supplier's main warehouse, and the supplier that runs a warehouse.
func addSupplierAndWarehouse(s *ir.Schema) {
	s.Types["Supplier"] = &ir.TypeDef{Name: "Supplier", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: uuidRef, Required: true, Key: true, AutoGenerated: true},
		{Name: "name", TypeRef: nameRef, Required: true},
		{Name: "mainWarehouse", TypeRef: ir.TypeRef{Name: "Warehouse"}, Relation: &ir.RelationDef{Type: "Warehouse", OnDelete: "SET NULL"}},
	}}
	s.Types["Warehouse"] = &ir.TypeDef{Name: "Warehouse", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: uuidRef, Required: true, Key: true, AutoGenerated: true},
		{Name: "city", TypeRef: stringRef, Required: true},
		{Name: "supplier", TypeRef: ir.TypeRef{Name: "Supplier"}, Relation: &ir.RelationDef{Type: "Supplier", OnDelete: "CASCADE"}},
	}}
}

// renameCustomerToBuyer renames Order.customer, which a foreign key, an
// index and the projection's join read.
func renameCustomerToBuyer(s *ir.Schema) {
	fieldNamed(s, "Order", "customer").Name = "buyer"
	typeNamed(s, "Order").Indexes[0].Keys = []string{"buyer", "placedAt"}
	typeNamed(s, "OrderSummary").Projection.Joins[0].On[0].Right = "base.buyer"
}

// fixSummaryWithoutCustomer reads the projection's customer name without
// the relation the case drops.
func fixSummaryWithoutCustomer(s *ir.Schema) {
	p := typeNamed(s, "OrderSummary")
	p.Projection.Joins = nil
	dropField(s, "OrderSummary", "customerName")
}

// namesSchema has the tables whose unnamed constraints Postgres names in
// the less common ways: a primary key name cut to 63 bytes, unique names
// cut so two collide (the second takes key1), and a unique name another
// table's constraint already holds.
func namesSchema() *ir.Schema {
	s := ir.NewSchema("names", ir.SchemaKindDB)
	text := ir.TypeRef{Name: "string"}
	s.Types["Order"] = &ir.TypeDef{Name: "Order", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "itemCode", TypeRef: text, Required: true, Unique: true},
	}}
	s.Types["OrderItem"] = &ir.TypeDef{Name: "OrderItem", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "code", TypeRef: text, Required: true, Unique: true},
	}}
	s.Types["ThisTypeNameIsLongEnoughToMakePostgresCutItsKeyName"] = &ir.TypeDef{
		Name: "ThisTypeNameIsLongEnoughToMakePostgresCutItsKeyName", Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "aColumnNameLongEnoughThatItIsCutAtTheEndOne", TypeRef: text, Unique: true},
			{Name: "aColumnNameLongEnoughThatItIsCutAtTheEndTwo", TypeRef: text, Unique: true},
		},
	}
	return s
}
