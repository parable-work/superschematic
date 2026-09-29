package graphdesc_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
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
	for _, name := range []string{d.RefTable, d.CommitTable, d.PatchTable} {
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
// whose scalar is stored in a way no rule reads fails the descriptor.
func TestValueClassRefusesAStorageWithoutARule(t *testing.T) {
	schema := loadFixture(t)
	schema.Scalars["Geo.Location"] = &ir.ScalarDef{
		Name:              "Geo.Location",
		LanguagePrimitive: ir.LanguageString,
		Primitive:         "String",
		Pattern:           `^-?\d+(\.\d+)?,-?\d+(\.\d+)?$`,
		TypeMappings:      map[string]string{"sql": "POINT", "json_schema": "object"},
	}
	schema.Types["Utensil"].Fields = append(schema.Types["Utensil"].Fields, &ir.FieldDef{
		Name: "shelf", TypeRef: ir.TypeRef{Name: "Geo.Location"}, Required: true,
	})
	_, err := graphdesc.Graphs(schema)
	if err == nil || !strings.Contains(err.Error(), "Utensil.shelf") || !strings.Contains(err.Error(), "POINT") {
		t.Fatalf("Graphs = %v, want a refusal naming Utensil.shelf and POINT", err)
	}
}

// TestValueClassWithoutAJSONSchemaMapping checks the classes of scalars
// whose json_schema type mapping is missing, which their primitive decides.
func TestValueClassWithoutAJSONSchemaMapping(t *testing.T) {
	schema := &ir.Schema{Scalars: map[string]*ir.ScalarDef{
		"Count":   {Name: "Count", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int"},
		"Ratio":   {Name: "Ratio", LanguagePrimitive: ir.LanguageNumber, Primitive: "Float"},
		"Flag":    {Name: "Flag", LanguagePrimitive: ir.LanguageBoolean},
		"Payload": {Name: "Payload", LanguagePrimitive: ir.LanguageObject},
		"Label":   {Name: "Label", LanguagePrimitive: ir.LanguageString},
	}}
	for name, want := range map[string]string{
		"Count": graphdesc.ClassInteger, "Ratio": graphdesc.ClassNumber, "Flag": graphdesc.ClassBoolean,
		"Payload": graphdesc.ClassJSON, "Label": graphdesc.ClassString,
	} {
		got, err := graphdesc.ValueClass(schema, &ir.FieldDef{Name: "f", TypeRef: ir.TypeRef{Name: name, IsArray: true}})
		if err != nil || got != want+"[]" {
			t.Errorf("ValueClass(%s[]) = %q, %v; want %q", name, got, err, want+"[]")
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
