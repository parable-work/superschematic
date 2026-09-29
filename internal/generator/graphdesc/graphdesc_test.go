package graphdesc_test

import (
	"os"
	"path/filepath"
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
	// A @jsonField object type, a list of lists of an object type and a map
	// are stored as JSONB columns, and each is json.
	t.Run("JSONB columns", func(t *testing.T) {
		schema := loadFixture(t)
		schema.Types["Plating"] = &ir.TypeDef{Name: "Plating", Fields: []*ir.FieldDef{
			{Name: "garnish", TypeRef: ir.TypeRef{Name: "string"}},
		}}
		schema.Types["Tasting"].Fields = append(schema.Types["Tasting"].Fields,
			&ir.FieldDef{Name: "plating", TypeRef: ir.TypeRef{Name: "Plating"}, JsonField: true, Required: true},
			&ir.FieldDef{Name: "courses", TypeRef: ir.TypeRef{Name: "Plating", IsArray: true, IsArrayOfArrays: true}, Required: true},
			&ir.FieldDef{Name: "pairings", TypeRef: ir.TypeRef{Name: "string", IsMap: true}, Required: true},
		)
		checkAgainstDDL(t, schema)
		graphs, err := graphdesc.Graphs(schema)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"plating": graphdesc.ClassJSON, "courses": graphdesc.ClassJSON + "[][]", "pairings": graphdesc.ClassJSON}
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
	if d.Version != 2 {
		t.Fatalf("descriptor version %d, want 2", d.Version)
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
// the field and the SQL type the sql generator writes: a string scalar
// stored as POINT or as the BIGINT its name infers, a scalar holding JSON
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
		sql    string
	}{
		{"string scalar as POINT", func(*testing.T) *ir.ScalarDef {
			return scalar("Test.Location", `^-?\d+(\.\d+)?,-?\d+(\.\d+)?$`, "object", "POINT")
		}, "POINT"},
		{"string scalar as an inferred BIGINT", func(*testing.T) *ir.ScalarDef { return scalar("Test.Duration", "", "", "") }, "BIGINT"},
		{"JSON array scalar as TEXT", func(*testing.T) *ir.ScalarDef { return scalar("Test.Vector", "", ir.JSONSchemaArrayType, "TEXT") }, "TEXT"},
		{"JSON object scalar as TEXT", func(*testing.T) *ir.ScalarDef { return scalar("Test.StringMap", "", ir.JSONSchemaObjectType, "TEXT") }, "TEXT"},
		{"any JSON scalar as TEXT", func(*testing.T) *ir.ScalarDef { return scalar("Test.AnyJSON", "", ir.JSONSchemaAnyType, "TEXT") }, "TEXT"},
		{"Embedding.Vector from the catalog", func(t *testing.T) *ir.ScalarDef { return catalogScalar(t, "Embedding.Vector") }, "TEXT"},
		{"boolean scalar as TEXT", func(*testing.T) *ir.ScalarDef { return scalar("Test.Checked", "", "boolean", "") }, "TEXT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := withShelf(loadFixture(t), tc.scalar(t))
			if got := columnSQLType(t, schema, "utensil", "shelf"); got != tc.sql {
				t.Fatalf("the sql generator stores shelf as %s, want %s", got, tc.sql)
			}
			_, err := graphdesc.Graphs(schema)
			if err == nil || !strings.Contains(err.Error(), "Utensil.shelf") || !strings.Contains(err.Error(), "stored as "+tc.sql+",") {
				t.Fatalf("Graphs = %v, want a refusal naming Utensil.shelf and %s", err, tc.sql)
			}
		})
	}
}

// TestValueClassOfEachKindOfField checks the class each kind of field
// derives: a map, a @jsonField object type or input and a @jsonField
// relation are json, whole; a list of lists of an object type or a JSON
// scalar is json[][]; a to-one relation takes its target key's class;
// a scalar has the class that reads its SQL type, whatever its primitive;
// and an object type or input stored as TEXT is refused.
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
		},
	}
	for _, tc := range []struct {
		name string
		fd   *ir.FieldDef
		want string
	}{
		{"map", &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: "Serial", IsMap: true}}, graphdesc.ClassJSON},
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
