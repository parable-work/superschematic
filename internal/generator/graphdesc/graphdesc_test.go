package graphdesc_test

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

const fixture = "../../loader/tsreader/testdata/services/fixture-version-graph-db"

func loadFixture(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.FromSlash(fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	return schema
}

// sqlClasses are the value classes whose rule reads what Postgres renders
// for each SQL type the sql generator writes for a single value. A list is
// a native array of its element's SQL type, or JSONB.
var sqlClasses = map[string][]string{
	"UUID":             {graphdesc.ClassUUID},
	"TEXT":             {graphdesc.ClassString, graphdesc.ClassEnum},
	"CITEXT":           {graphdesc.ClassString},
	"INET":             {graphdesc.ClassString},
	"BOOLEAN":          {graphdesc.ClassBoolean},
	"BIGINT":           {graphdesc.ClassInteger},
	"DOUBLE PRECISION": {graphdesc.ClassNumber},
	"TIMESTAMPTZ":      {graphdesc.ClassDateTime},
	"DATE":             {graphdesc.ClassDate},
	"TIME":             {graphdesc.ClassTime},
	"INTERVAL":         {graphdesc.ClassDuration},
	"JSONB":            {graphdesc.ClassJSON},
}

// TestDescriptorNamesTheGeneratedTables checks the descriptor of the
// fixture's graph against the DDL the sql generator writes for the same
// schema: the root, ref, commit and patch tables and the root's key exist;
// each kind's table and history table exist; the kind's columns are exactly
// its table's columns; and each column's class reads the SQL type it is
// stored as.
func TestDescriptorNamesTheGeneratedTables(t *testing.T) {
	t.Run("fixture", func(t *testing.T) { checkAgainstDDL(t, loadFixture(t)) })
	// A list relation has no column of its own: a join table holds it.
	t.Run("list relation", func(t *testing.T) {
		schema := loadFixture(t)
		schema.Types["Tasting"].Fields = append(schema.Types["Tasting"].Fields, &ir.FieldDef{
			Name: "pairings", TypeRef: ir.TypeRef{Name: "Utensil", IsArray: true}, ManyToMany: true,
		})
		checkAgainstDDL(t, schema)
	})
	// A @jsonField object type, a list of lists of an object type, a map and
	// a map of a table are stored as JSONB columns, and each is json: a map
	// of a table holds the rows, not a relation's key.
	t.Run("JSONB columns", func(t *testing.T) {
		schema := loadFixture(t)
		schema.Types["Plating"] = &ir.TypeDef{Name: "Plating", Fields: []*ir.FieldDef{
			{Name: "garnish", TypeRef: ir.TypeRef{Name: "string"}},
		}}
		schema.Types["Tasting"].Fields = append(schema.Types["Tasting"].Fields,
			&ir.FieldDef{Name: "plating", TypeRef: ir.TypeRef{Name: "Plating"}, JsonField: true, Required: true},
			&ir.FieldDef{Name: "courses", TypeRef: ir.TypeRef{Name: "Plating", IsArray: true, IsArrayOfArrays: true}, Required: true},
			&ir.FieldDef{Name: "pairings", TypeRef: ir.TypeRef{Name: "string", IsMap: true}, Required: true},
			&ir.FieldDef{Name: "stations", TypeRef: ir.TypeRef{Name: "Utensil", IsMap: true}, Required: true},
		)
		checkAgainstDDL(t, schema)
		graphs, err := graphdesc.Graphs(schema)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"plating": graphdesc.ClassJSON, "courses": graphdesc.ClassJSON + "[][]", "pairings": graphdesc.ClassJSON, "stations": graphdesc.ClassJSON}
		for _, kind := range graphs[0].Descriptor.Kinds {
			if kind.Kind != "tasting" {
				continue
			}
			for column, class := range want {
				if got := columnSQLType(t, schema, kind.Table, column); got != "JSONB" {
					t.Errorf("the sql generator stores tasting.%s as %s, want JSONB", column, got)
				}
				if kind.Columns[column] != class {
					t.Errorf("tasting.%s has class %q, want %q", column, kind.Columns[column], class)
				}
			}
		}
	})
	// A scalar without a sql type mapping is stored as the type its traits
	// infer, and its class reads that type.
	for _, tc := range []struct {
		name   string
		scalar *ir.ScalarDef
		sql    string
		class  string
	}{
		{"UUID from a uuid format", &ir.ScalarDef{Name: "Test.Owner", LanguagePrimitive: ir.LanguageString, Primitive: "String", Format: "uuid"}, "UUID", graphdesc.ClassUUID},
		{"TIMESTAMPTZ from a date-time format", &ir.ScalarDef{Name: "Test.Stamp", LanguagePrimitive: ir.LanguageString, Primitive: "String", Format: "date-time"}, "TIMESTAMPTZ", graphdesc.ClassDateTime},
		{"BIGINT from a milliseconds name", &ir.ScalarDef{Name: "Test.Milliseconds", LanguagePrimitive: ir.LanguageNumber, Primitive: "Float"}, "BIGINT", graphdesc.ClassInteger},
		{"JSONB from a json name", &ir.ScalarDef{Name: "Test.Json", LanguagePrimitive: ir.LanguageString, Primitive: "String"}, "JSONB", graphdesc.ClassJSON},
		{"CITEXT", &ir.ScalarDef{Name: "Test.Handle", LanguagePrimitive: ir.LanguageString, Primitive: "String", TypeMappings: map[string]string{"sql": "CITEXT"}}, "CITEXT", graphdesc.ClassString},
		{"INET", &ir.ScalarDef{Name: "Test.Address", LanguagePrimitive: ir.LanguageString, Primitive: "String", TypeMappings: map[string]string{"sql": "INET"}}, "INET", graphdesc.ClassString},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := withShelf(loadFixture(t), tc.scalar)
			if got := columnSQLType(t, schema, "utensil", "shelf"); got != tc.sql {
				t.Fatalf("the sql generator stores %s as %s, want %s", tc.scalar.Name, got, tc.sql)
			}
			checkAgainstDDL(t, schema)
			graphs, err := graphdesc.Graphs(schema)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range graphs[0].Descriptor.Kinds {
				if kind.Kind == "utensil" && kind.Columns["shelf"] != tc.class {
					t.Errorf("utensil.shelf has class %q, want %q", kind.Columns["shelf"], tc.class)
				}
			}
		})
	}
}

// withShelf adds scalar to schema and a required field shelf of it to the
// fixture's Utensil.
func withShelf(schema *ir.Schema, scalar *ir.ScalarDef) *ir.Schema {
	schema.Scalars[scalar.Name] = scalar
	schema.Types["Utensil"].Fields = append(schema.Types["Utensil"].Fields, &ir.FieldDef{
		Name: "shelf", TypeRef: ir.TypeRef{Name: scalar.Name}, Required: true,
	})
	return schema
}

// columnSQLType is the SQL type the sql generator writes for a column.
func columnSQLType(t *testing.T, schema *ir.Schema, table, column string) string {
	t.Helper()
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: "fixture-version-graph-db"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range ddl.Tables {
		for _, col := range tbl.Columns {
			if tbl.Name == table && col.Name == column {
				return col.Type
			}
		}
	}
	t.Fatalf("the DDL has no column %s.%s", table, column)
	return ""
}

// catalogScalar is the scalar catalog's definition of name, as the loader
// hydrates it.
func catalogScalar(t *testing.T, name string) *ir.ScalarDef {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "catalog-scalar")
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, text := range map[string]string{
		"schema.config.json":                  `{"name": "catalog-scalar", "kind": "General", "outputs": {}}`,
		filepath.Join("src", "s.schema.json"): `{"scalars": {"` + name + `": {"name": "` + name + `", "languagePrimitive": "string"}}}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	return schema.Scalars[name]
}

func checkAgainstDDL(t *testing.T, schema *ir.Schema) {
	t.Helper()
	graphs, err := graphdesc.Graphs(schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(graphs) != 1 {
		t.Fatalf("the fixture declares one graph, got %d", len(graphs))
	}
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: "fixture-version-graph-db"})
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string]sqlgen.Table{}
	for _, table := range ddl.Tables {
		tables[table.Name] = table
	}
	histories := map[string]bool{}
	for _, history := range ddl.HistoryTables {
		histories[history.Name] = true
	}

	d := graphs[0].Descriptor
	if d.Version != 3 {
		t.Fatalf("descriptor version %d, want 3", d.Version)
	}
	root, ok := tables[d.Root.Table]
	if !ok || root.PrimaryKey != d.Root.Key {
		t.Errorf("root %+v: no table with that primary key", d.Root)
	}
	for _, name := range []string{d.RefTable, d.CommitTable, d.PatchTable, d.ReleaseTable, d.SnapshotTable} {
		if _, ok := tables[name]; !ok {
			t.Errorf("the descriptor names table %s, which the DDL does not create", name)
		}
	}
	for _, kind := range d.Kinds {
		table, ok := tables[kind.Table]
		if !ok {
			t.Errorf("kind %s: no table %s", kind.Kind, kind.Table)
			continue
		}
		if !histories[kind.HistoryTable] {
			t.Errorf("kind %s: no history table %s", kind.Kind, kind.HistoryTable)
		}
		var described, created []string
		for column := range kind.Columns {
			described = append(described, column)
		}
		sqlTypes := map[string]string{}
		for _, column := range table.Columns {
			created = append(created, column.Name)
			sqlTypes[column.Name] = column.Type
		}
		sort.Strings(described)
		sort.Strings(created)
		if strings.Join(described, ",") != strings.Join(created, ",") {
			t.Errorf("kind %s: descriptor columns %v, table columns %v", kind.Kind, described, created)
			continue
		}
		for column, class := range kind.Columns {
			sqlType := sqlTypes[column]
			element := strings.TrimSuffix(strings.TrimSuffix(class, "[]"), "[]")
			switch {
			case strings.HasSuffix(class, "[][]"):
				if sqlType != "JSONB" {
					t.Errorf("kind %s column %s: %s is stored as %s, want JSONB", kind.Kind, column, class, sqlType)
				}
			case strings.HasSuffix(class, "[]"):
				native := strings.HasSuffix(sqlType, "[]") && contains(sqlClasses[strings.TrimSuffix(sqlType, "[]")], element)
				if !native && sqlType != "JSONB" {
					t.Errorf("kind %s column %s: %s is stored as %s", kind.Kind, column, class, sqlType)
				}
			default:
				if !contains(sqlClasses[sqlType], class) {
					t.Errorf("kind %s column %s: %s is stored as %s", kind.Kind, column, class, sqlType)
				}
			}
		}
	}
}

// TestHistoryIsWhatTheTriggersKeep checks each kind's history against the
// history table the sql generator writes for the same schema: its retention
// is the prune function's, its exclusions are the columns the capture
// function leaves out of every image, and its actor is the column a
// delete's image names its actor in. It checks every graph the fixture
// corpus declares, then the fixture's own facts, and variants of it that
// give a member deleted_by or exclude its actor column.
func TestHistoryIsWhatTheTriggersKeep(t *testing.T) {
	t.Run("fixture corpus", func(t *testing.T) {
		dirs, err := filepath.Glob(filepath.Join(filepath.Dir(filepath.FromSlash(fixture)), "fixture-*"))
		if err != nil {
			t.Fatal(err)
		}
		described := 0
		for _, dir := range dirs {
			// A fixture the loader refuses on purpose declares no graph to
			// check; its own tests load it.
			schema, err := loader.LoadService(dir)
			if err != nil {
				continue
			}
			described += checkHistory(t, schema)
		}
		if described == 0 {
			t.Fatal("no fixture of the corpus declares a version graph")
		}
	})
	t.Run("fixture", func(t *testing.T) {
		schema := loadFixture(t)
		checkHistory(t, schema)
		expectHistory(t, schema, map[string]graphdesc.History{
			"step":       {RetentionDays: 365, Exclude: []string{"scratch"}, Actor: "updated_by"},
			"ingredient": {RetentionDays: 365, Exclude: []string{}},
			"note":       {Exclude: []string{}},
		})
		// The graph's own versioned tables are not kinds, and the sql
		// generator gives them an actor by the same rule: a ref has
		// deleted_by and updated_by, a release pointer updated_by alone.
		graphs, err := graphdesc.Graphs(schema)
		if err != nil {
			t.Fatal(err)
		}
		actors := historyActors(t, schema)
		d := graphs[0].Descriptor
		for table, want := range map[string]string{d.RefTable: "deleted_by", d.ReleaseTable: "updated_by"} {
			if actors[table] != want {
				t.Errorf("the sql generator names %q as the actor of %s's delete images, want %q", actors[table], table, want)
			}
		}
	})
	t.Run("deleted_by before updated_by", func(t *testing.T) {
		schema := loadFixture(t)
		step := schema.Types["Step"]
		step.Fields = append(step.Fields, &ir.FieldDef{Name: "deletedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}})
		checkHistory(t, schema)
		expectHistory(t, schema, map[string]graphdesc.History{
			"step": {RetentionDays: 365, Exclude: []string{"scratch"}, Actor: "deleted_by"},
		})
	})
	t.Run("an excluded updated_by names no actor", func(t *testing.T) {
		schema := loadFixture(t)
		cfg := schema.Types["Step"].VersionedConfig
		cfg.Exclude = append(cfg.Exclude, "updatedBy")
		checkHistory(t, schema)
		expectHistory(t, schema, map[string]graphdesc.History{
			"step": {RetentionDays: 365, Exclude: []string{"scratch", "updated_by"}},
		})
	})
	// An excluded deleted_by does not hand the actor to updated_by: the
	// delete's image names none.
	t.Run("an excluded deleted_by names no actor", func(t *testing.T) {
		schema := loadFixture(t)
		step := schema.Types["Step"]
		step.Fields = append(step.Fields, &ir.FieldDef{
			Name: "deletedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, ConflictUnit: ir.ConflictUnitExcluded,
		})
		step.VersionedConfig.Exclude = append(step.VersionedConfig.Exclude, "deletedBy")
		checkHistory(t, schema)
		expectHistory(t, schema, map[string]graphdesc.History{
			"step": {RetentionDays: 365, Exclude: []string{"scratch", "deleted_by"}},
		})
	})
}

// checkHistory compares every kind's history in schema's graphs with the
// history table the sql generator writes for the kind's table, and returns
// how many kinds it compared.
func checkHistory(t *testing.T, schema *ir.Schema) int {
	t.Helper()
	graphs, err := graphdesc.Graphs(schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(graphs) == 0 {
		return 0
	}
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	histories := map[string]sqlgen.HistoryTable{}
	for _, history := range ddl.HistoryTables {
		histories[history.SourceTableName] = history
	}
	compared := 0
	for _, g := range graphs {
		for _, kind := range g.Descriptor.Kinds {
			history, ok := histories[kind.Table]
			if !ok {
				t.Errorf("%s kind %s: the sql generator writes no history for %s", g.Name, kind.Kind, kind.Table)
				continue
			}
			compared++
			got := kind.History
			if got.RetentionDays != history.RetentionDays {
				t.Errorf("%s kind %s: retentionDays %d, the prune function keeps %d", g.Name, kind.Kind, got.RetentionDays, history.RetentionDays)
			}
			if got.Exclude == nil {
				t.Errorf("%s kind %s: exclude is nil, which the descriptor would write as null", g.Name, kind.Kind)
			}
			if !slices.Equal(got.Exclude, history.ExcludedColumns) {
				t.Errorf("%s kind %s: exclude %q, the capture function leaves out %q", g.Name, kind.Kind, got.Exclude, history.ExcludedColumns)
			}
			if got.Actor != history.ActorColumn {
				t.Errorf("%s kind %s: actor %q, a delete's image names %q", g.Name, kind.Kind, got.Actor, history.ActorColumn)
			}
		}
	}
	return compared
}

// expectHistory checks the history of each kind want names.
func expectHistory(t *testing.T, schema *ir.Schema, want map[string]graphdesc.History) {
	t.Helper()
	graphs, err := graphdesc.Graphs(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range graphs[0].Descriptor.Kinds {
		w, ok := want[kind.Kind]
		if !ok {
			continue
		}
		delete(want, kind.Kind)
		got := kind.History
		if got.RetentionDays != w.RetentionDays || !slices.Equal(got.Exclude, w.Exclude) || got.Actor != w.Actor {
			t.Errorf("kind %s: history %+v, want %+v", kind.Kind, got, w)
		}
	}
	for kind := range want {
		t.Errorf("the graph has no kind %s", kind)
	}
}

// historyActors maps each table the sql generator writes history for to the
// column its delete images name their actor in.
func historyActors(t *testing.T, schema *ir.Schema) map[string]string {
	t.Helper()
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	actors := map[string]string{}
	for _, history := range ddl.HistoryTables {
		actors[history.SourceTableName] = history.ActorColumn
	}
	return actors
}

// TestEveryElementClassIsDerived checks that the fixture's graph, which
// declares a member with a field of each kind of value, gives every element
// class to some column, and lists and lists of lists too.
func TestEveryElementClassIsDerived(t *testing.T) {
	graphs, err := graphdesc.Graphs(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, kind := range graphs[0].Descriptor.Kinds {
		for _, class := range kind.Columns {
			seen[class] = true
		}
	}
	for _, class := range []string{
		graphdesc.ClassString, graphdesc.ClassInteger, graphdesc.ClassNumber, graphdesc.ClassBoolean,
		graphdesc.ClassUUID, graphdesc.ClassDateTime, graphdesc.ClassDate, graphdesc.ClassTime,
		graphdesc.ClassDuration, graphdesc.ClassEnum, graphdesc.ClassJSON,
		graphdesc.ClassString + "[]", graphdesc.ClassUUID + "[]", graphdesc.ClassInteger + "[][]",
	} {
		if !seen[class] {
			t.Errorf("no column of the fixture's graph has class %s", class)
		}
	}
}

// TestValueClassRefusesAStorageWithoutARule checks that a member column
// whose value is stored in a way no rule reads fails the descriptor, naming
// the field, what its value holds and the SQL type the sql generator
// writes: the catalog's Geo.Location, a JSON object stored as POINT, a
// string scalar stored as the BIGINT its name infers, a scalar holding JSON
// (an array, an object or any value) stored as TEXT rather than JSONB,
// the catalog's Embedding.Vector, which has no sql type mapping and is
// stored as TEXT, and a boolean stored as TEXT.
func TestValueClassRefusesAStorageWithoutARule(t *testing.T) {
	scalar := func(name, pattern, jsonSchema, sql string) *ir.ScalarDef {
		def := &ir.ScalarDef{Name: name, LanguagePrimitive: ir.LanguageString, Primitive: "String", Pattern: pattern, TypeMappings: map[string]string{}}
		if jsonSchema != "" {
			def.TypeMappings["json_schema"] = jsonSchema
		}
		if sql != "" {
			def.TypeMappings["sql"] = sql
		}
		return def
	}
	for _, tc := range []struct {
		name   string
		scalar func(t *testing.T) *ir.ScalarDef
		holds  string
		sql    string
	}{
		{"Geo.Location from the catalog", func(t *testing.T) *ir.ScalarDef { return catalogScalar(t, "Geo.Location") }, "object", "POINT"},
		{"string scalar as an inferred BIGINT", func(*testing.T) *ir.ScalarDef { return scalar("Test.Duration", "", "", "") }, "string", "BIGINT"},
		{"JSON array scalar as TEXT", func(*testing.T) *ir.ScalarDef { return scalar("Test.Vector", "", ir.JSONSchemaArrayType, "TEXT") }, "array", "TEXT"},
		{"JSON object scalar as TEXT", func(*testing.T) *ir.ScalarDef { return scalar("Test.StringMap", "", ir.JSONSchemaObjectType, "TEXT") }, "object", "TEXT"},
		{"any JSON scalar as TEXT", func(*testing.T) *ir.ScalarDef { return scalar("Test.AnyJSON", "", ir.JSONSchemaAnyType, "TEXT") }, "value", "TEXT"},
		{"Embedding.Vector from the catalog", func(t *testing.T) *ir.ScalarDef { return catalogScalar(t, "Embedding.Vector") }, "array", "TEXT"},
		{"boolean scalar as TEXT", func(*testing.T) *ir.ScalarDef { return scalar("Test.Checked", "", "boolean", "") }, "boolean", "TEXT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := withShelf(loadFixture(t), tc.scalar(t))
			if got := columnSQLType(t, schema, "utensil", "shelf"); got != tc.sql {
				t.Fatalf("the sql generator stores shelf as %s, want %s", got, tc.sql)
			}
			_, err := graphdesc.Graphs(schema)
			want := "holds a JSON " + tc.holds + " but is stored as " + tc.sql + ","
			if err == nil || !strings.Contains(err.Error(), "Utensil.shelf") || !strings.Contains(err.Error(), want) {
				t.Fatalf("Graphs = %v, want a refusal naming Utensil.shelf and saying it %s", err, want)
			}
		})
	}
}

// TestValueClassOfEachKindOfField checks the class each kind of field
// derives: a map, a map of a table, a @jsonField object type or input and a
// @jsonField relation are json, whole; a list of lists of an object type or
// a JSON scalar is json[][]; a to-one relation takes its target key's class;
// a scalar has the class that reads its SQL type, whatever its primitive,
// INT among them; and an object type, an input or an object scalar stored as
// TEXT is refused.
func TestValueClassOfEachKindOfField(t *testing.T) {
	schema := &ir.Schema{
		Types: map[string]*ir.TypeDef{
			"Shelf": {Name: "Shelf", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Serial"}, Key: true},
			}},
			"Point": {Name: "Point", Fields: []*ir.FieldDef{{Name: "x", TypeRef: ir.TypeRef{Name: "number"}}}},
		},
		Inputs: map[string]*ir.TypeDef{
			"PointInput": {Name: "PointInput", Fields: []*ir.FieldDef{{Name: "x", TypeRef: ir.TypeRef{Name: "number"}}}},
		},
		Scalars: map[string]*ir.ScalarDef{
			"Serial":  {Name: "Serial", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int", TypeMappings: map[string]string{"sql": "BIGINT", "json_schema": "integer"}},
			"Weight":  {Name: "Weight", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": "number"}},
			"Checked": {Name: "Checked", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": "boolean", "sql": "BOOLEAN"}},
			"Ratio":   {Name: "Ratio", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int", TypeMappings: map[string]string{"json_schema": "number"}},
			"Handle":  {Name: "Handle", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "CITEXT"}},
			"Address": {Name: "Address", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "INET"}},
			"Code":    {Name: "Code", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "VARCHAR(12)"}},
			"Blob":    {Name: "Blob", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": ir.JSONSchemaAnyType, "sql": "TEXT"}},
			"Price":   {Name: "Price", LanguagePrimitive: ir.LanguageNumber, TypeMappings: map[string]string{"json_schema": "number", "sql": "NUMERIC(10,2)"}},
			"Rate":    {Name: "Rate", LanguagePrimitive: ir.LanguageNumber, TypeMappings: map[string]string{"json_schema": "number", "sql": "DECIMAL"}},
			"Count":   {Name: "Count", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int", TypeMappings: map[string]string{"json_schema": "integer", "sql": "INTEGER"}},
			"Rank":    {Name: "Rank", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int", TypeMappings: map[string]string{"json_schema": "integer", "sql": "SMALLINT"}},
			"Gauge":   {Name: "Gauge", LanguagePrimitive: ir.LanguageNumber, TypeMappings: map[string]string{"json_schema": "number", "sql": "REAL"}},
			"Tally":   {Name: "Tally", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int", TypeMappings: map[string]string{"sql": "INT"}},
			"Payload": {Name: "Payload", LanguagePrimitive: ir.LanguageObject, Primitive: "Object", TypeMappings: map[string]string{"sql": "TEXT"}},
		},
	}
	for _, tc := range []struct {
		name string
		fd   *ir.FieldDef
		want string
	}{
		{"map", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Serial", IsMap: true}}, graphdesc.ClassJSON},
		{"map of a table", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Shelf", IsMap: true}}, graphdesc.ClassJSON},
		{"jsonField object type", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Point"}, JsonField: true}, graphdesc.ClassJSON},
		{"jsonField object type list", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Point", IsArray: true}, JsonField: true}, graphdesc.ClassJSON + "[]"},
		{"jsonField input", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "PointInput"}, JsonField: true}, graphdesc.ClassJSON},
		{"jsonField relation", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Shelf"}, JsonField: true}, graphdesc.ClassJSON},
		{"to-one relation", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Shelf"}}, graphdesc.ClassInteger},
		{"json_schema number over a string primitive", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Weight"}}, graphdesc.ClassNumber},
		{"json_schema number over an Int primitive", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Ratio"}}, graphdesc.ClassNumber},
		{"json_schema boolean stored as BOOLEAN", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Checked"}}, graphdesc.ClassBoolean},
		{"CITEXT", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Handle"}}, graphdesc.ClassString},
		{"INET list", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Address", IsArray: true}}, graphdesc.ClassString + "[]"},
		{"VARCHAR", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Code"}}, graphdesc.ClassString},
		{"object type list of lists", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Point", IsArray: true, IsArrayOfArrays: true}}, graphdesc.ClassJSON + "[][]"},
		{"JSON scalar list of lists", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Blob", IsArray: true, IsArrayOfArrays: true}}, graphdesc.ClassJSON + "[][]"},
		{"NUMERIC(10,2)", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Price"}}, graphdesc.ClassNumber},
		{"DECIMAL", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Rate"}}, graphdesc.ClassNumber},
		{"INTEGER", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Count"}}, graphdesc.ClassInteger},
		{"SMALLINT", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Rank"}}, graphdesc.ClassInteger},
		{"REAL", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Gauge"}}, graphdesc.ClassNumber},
		{"INT", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Tally"}}, graphdesc.ClassInteger},
	} {
		got, err := graphdesc.ValueClass(schema, tc.fd)
		if err != nil || got != tc.want {
			t.Errorf("%s: ValueClass = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		fd   *ir.FieldDef
		sql  string
	}{
		{"object type", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Point"}}, "TEXT"},
		{"object type list", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Point", IsArray: true}}, "TEXT[]"},
		{"input", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "PointInput"}}, "TEXT"},
		{"object scalar", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Payload"}}, "TEXT"},
	} {
		got, err := graphdesc.ValueClass(schema, tc.fd)
		if err == nil || !strings.Contains(err.Error(), "stored as "+tc.sql+",") {
			t.Errorf("%s: ValueClass = %q, %v; want a refusal naming %s", tc.name, got, err, tc.sql)
		}
	}
}

// TestValueClassReadsTheInferredSQLType checks scalars without a sql type
// mapping, which the sql generator stores as the type their traits or
// primitive infer (sqlutil.ColumnType): each class is the one that reads
// that type.
func TestValueClassReadsTheInferredSQLType(t *testing.T) {
	schema := &ir.Schema{Scalars: map[string]*ir.ScalarDef{
		"Count":             {Name: "Count", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int"},
		"Ratio":             {Name: "Ratio", LanguagePrimitive: ir.LanguageNumber, Primitive: "Float"},
		"Flag":              {Name: "Flag", LanguagePrimitive: ir.LanguageBoolean},
		"Payload":           {Name: "Payload", LanguagePrimitive: ir.LanguageObject},
		"Label":             {Name: "Label", LanguagePrimitive: ir.LanguageString},
		"Test.Uuid":         {Name: "Test.Uuid", LanguagePrimitive: ir.LanguageString},
		"Test.DateTime":     {Name: "Test.DateTime", LanguagePrimitive: ir.LanguageString},
		"Test.Int":          {Name: "Test.Int", LanguagePrimitive: ir.LanguageNumber},
		"Test.Milliseconds": {Name: "Test.Milliseconds", LanguagePrimitive: ir.LanguageNumber},
		"Test.Json":         {Name: "Test.Json", LanguagePrimitive: ir.LanguageString},
	}}
	sqlTypes := sqlutil.ScalarSQLTypes(schema)
	for name, want := range map[string]struct{ sql, class string }{
		"Count":             {"DOUBLE PRECISION", graphdesc.ClassNumber},
		"Ratio":             {"DOUBLE PRECISION", graphdesc.ClassNumber},
		"Flag":              {"BOOLEAN", graphdesc.ClassBoolean},
		"Payload":           {"JSONB", graphdesc.ClassJSON},
		"Label":             {"TEXT", graphdesc.ClassString},
		"Test.Uuid":         {"UUID", graphdesc.ClassUUID},
		"Test.DateTime":     {"TIMESTAMPTZ", graphdesc.ClassDateTime},
		"Test.Int":          {"BIGINT", graphdesc.ClassInteger},
		"Test.Milliseconds": {"BIGINT", graphdesc.ClassInteger},
		"Test.Json":         {"JSONB", graphdesc.ClassJSON},
	} {
		fd := &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: name, IsArray: true}}
		if got := sqlutil.ColumnType(fd, sqlTypes); got != want.sql+"[]" {
			t.Errorf("ColumnType(%s[]) = %q, want %q", name, got, want.sql+"[]")
		}
		got, err := graphdesc.ValueClass(schema, fd)
		if err != nil || got != want.class+"[]" {
			t.Errorf("ValueClass(%s[]) = %q, %v; want %q", name, got, err, want.class+"[]")
		}
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// TestScalarClassIsASingleFieldsClass checks ScalarClass against ValueClass
// for a single field of each scalar of two schemas, one whose scalars map
// their SQL types and one whose scalars infer them: the class is the
// field's, and "" where ValueClass refuses the field.
func TestScalarClassIsASingleFieldsClass(t *testing.T) {
	for _, schema := range []*ir.Schema{
		{Scalars: map[string]*ir.ScalarDef{
			"Weight":  {Name: "Weight", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": "number"}},
			"Checked": {Name: "Checked", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": "boolean", "sql": "BOOLEAN"}},
			"Handle":  {Name: "Handle", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "CITEXT"}},
			"Blob":    {Name: "Blob", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": ir.JSONSchemaAnyType, "sql": "TEXT"}},
			"Count":   {Name: "Count", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int", TypeMappings: map[string]string{"json_schema": "integer", "sql": "INTEGER"}},
			"Payload": {Name: "Payload", LanguagePrimitive: ir.LanguageObject, Primitive: "Object", TypeMappings: map[string]string{"sql": "TEXT"}},
			"Point":   {Name: "Point", LanguagePrimitive: ir.LanguageString, Pattern: `^\d+,\d+$`, TypeMappings: map[string]string{"json_schema": "object", "sql": "POINT"}},
		}},
		{Scalars: map[string]*ir.ScalarDef{
			"Ratio":         {Name: "Ratio", LanguagePrimitive: ir.LanguageNumber, Primitive: "Float"},
			"Flag":          {Name: "Flag", LanguagePrimitive: ir.LanguageBoolean},
			"Payload":       {Name: "Payload", LanguagePrimitive: ir.LanguageObject},
			"Test.Uuid":     {Name: "Test.Uuid", LanguagePrimitive: ir.LanguageString},
			"Test.DateTime": {Name: "Test.DateTime", LanguagePrimitive: ir.LanguageString},
			"Test.Int":      {Name: "Test.Int", LanguagePrimitive: ir.LanguageNumber},
		}},
	} {
		names := []string{"string", "number", "boolean"}
		for name := range schema.Scalars {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			got, err := graphdesc.ScalarClass(schema, name)
			if err != nil {
				t.Fatalf("ScalarClass(%s): %v", name, err)
			}
			want, refused := graphdesc.ValueClass(schema, &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: name}})
			if refused != nil {
				want = ""
			}
			if got != want {
				t.Errorf("ScalarClass(%s) = %q, a single field of it has %q (%v)", name, got, want, refused)
			}
		}
	}
	if _, err := graphdesc.ScalarClass(&ir.Schema{}, "Nope"); err == nil {
		t.Error("ScalarClass of a name the schema lacks gives no error")
	}
}
