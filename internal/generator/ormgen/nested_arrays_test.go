package ormgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestArraysOfArraysFields: every T[][] column of fixture-nested-arrays-db
// is a JSON field of Go type [][]T, and the schema switches on the encoder
// those columns write through. fixture-db has no T[][] and does not.
func TestArraysOfArraysFields(t *testing.T) {
	output := generateFixture(t, "fixture-nested-arrays-db")
	if !output.HasArraysOfArrays {
		t.Error("HasArraysOfArrays = false, want true")
	}
	if generateFixtureDB(t).HasArraysOfArrays {
		t.Error("fixture-db HasArraysOfArrays = true, want false")
	}
	if len(output.Repositories) != 1 {
		t.Fatalf("repositories = %d, want 1 (Board)", len(output.Repositories))
	}

	fields := map[string]Field{}
	for _, field := range output.Repositories[0].Fields {
		fields[field.Name] = field
	}
	for name, want := range map[string]struct {
		goType   string
		required bool
	}{
		"labels": {"[][]string", true},
		"states": {"[][]types.CellState", true},
		"walls":  {"[][]types.BoardPoint", true},
		"scores": {"[][]float64", false},
	} {
		field, ok := fields[name]
		if !ok {
			t.Errorf("Board has no %s field", name)
			continue
		}
		if !field.IsArrayOfArrays || !field.IsArray || field.ArrayDepth() != 2 || !field.IsJSONField {
			t.Errorf("%s not classified as a JSON array of arrays: %+v", name, field)
		}
		if got := field.ValueGoType(); got != want.goType {
			t.Errorf("%s ValueGoType = %q, want %q", name, got, want.goType)
		}
		if field.IsRequired != want.required || field.DerefValue {
			t.Errorf("%s IsRequired = %v, DerefValue = %v; want %v, false", name, field.IsRequired, field.DerefValue, want.required)
		}
	}
}

func TestFieldValueGoType(t *testing.T) {
	for _, tc := range []struct {
		field Field
		want  string
	}{
		{Field{GoType: "string"}, "string"},
		{Field{GoType: "string", IsArray: true}, "[]string"},
		{Field{GoType: "string", IsArray: true, IsArrayOfArrays: true}, "[][]string"},
		{Field{GoType: "types.Point", IsMap: true}, "map[string]types.Point"},
		{Field{GoType: "types.Point", IsMap: true, IsArray: true}, "map[string][]types.Point"},
	} {
		if got := tc.field.ValueGoType(); got != tc.want {
			t.Errorf("ValueGoType(%+v) = %q, want %q", tc.field, got, tc.want)
		}
	}
}

// TestArrayOfArraysOfTableTypeFails: a list of lists of a table type has no
// relational meaning and is refused with its own error, not the @hasMany
// hint a flat list of a table type gets.
func TestArrayOfArraysOfTableTypeFails(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString}
	schema.Types["A"] = &ir.TypeDef{
		Name: "A",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "grid", TypeRef: ir.TypeRef{Name: "B", IsArray: true, IsArrayOfArrays: true}, Required: true},
		},
	}
	schema.Types["B"] = &ir.TypeDef{
		Name: "B",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
		},
	}

	_, err := Generate(schema, Options{SchemaName: "synthetic"})
	if err == nil || !strings.Contains(err.Error(), "A.grid is an array of arrays of table type B, which cannot be a relation") {
		t.Fatalf("err = %v", err)
	}
}

// TestArrayOfArraysJSONUnionDecoder: a list of lists of a closed union
// decodes each innermost element through the union's wrapper and names both
// indexes when one fails.
func TestArrayOfArraysJSONUnionDecoder(t *testing.T) {
	createdKind := "created"
	db := ir.NewSchema("union-db", ir.SchemaKindDB)
	db.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString}
	db.Types["CreatedRevision"] = &ir.TypeDef{
		Name: "CreatedRevision",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &createdKind},
		},
	}
	db.Unions["RevisionRef"] = &ir.UnionDef{Name: "RevisionRef", Types: []string{"CreatedRevision"}}
	db.Types["Event"] = &ir.TypeDef{
		Name: "Event",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "batches", TypeRef: ir.TypeRef{Name: "RevisionRef", IsArray: true, IsArrayOfArrays: true}, Required: true},
		},
	}

	output, err := Generate(db, Options{
		SchemaName:  "union-db",
		ModulePath:  "example.com/schemas/orm/union-db",
		TypesModule: "example.com/schemas/types/go/union-db",
		Clock:       codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	repo := output.Repositories[0]
	if !repo.HasJSONUnionCollections {
		t.Error("HasJSONUnionCollections = false, want true")
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("WriteORM: %v", err)
	}
	var source []byte
	for _, name := range []string{"repository_event.go", "query.go"} {
		content, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		source = append(source, content...)
	}
	for _, want := range []string{
		"func decodeEventBatchesJSONUnion(raw []byte) ([][]types.RevisionRef, error) {",
		"var encoded [][]json.RawMessage",
		"decoded := make([][]types.RevisionRef, len(encoded))",
		"var wrapper types.RevisionRefWrapper",
		`fmt.Errorf("decode item [%d][%d]: %w", index, inner, err)`,
		"jsonValueBatches, err := marshalArrayOfArraysFieldValue(input.Batches)",
		"Batches *[][]types.RevisionRef",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("generated ORM missing %q", want)
		}
	}
}

// TestArrayOfArraysJSONCodec compiles the generated utils.go (stdlib only)
// and checks the T[][] encoder: a nil outer list is JSON null like any JSON
// field value, a nil inner list is written as [], ragged lists keep their
// shape, the caller's value is not modified, and the JSON decoder reads every
// case back.
func TestArrayOfArraysJSONCodec(t *testing.T) {
	output := generateFixture(t, "fixture-nested-arrays-db")

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read generated orm dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == "utils.go" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(outDir, entry.Name())); err != nil {
			t.Fatalf("remove %s: %v", entry.Name(), err)
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "go.mod"), []byte("module arraysofarraystest\n\ngo 1.26.4\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "codec_test.go"), []byte(arrayOfArraysCodecPackageTest), 0o644); err != nil {
		t.Fatalf("write codec_test.go: %v", err)
	}

	cmd := exec.Command("go", "test", ".", "-count=1", "-run", "TestArrayOfArrays")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=go1.26.4")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test array-of-arrays codec failed: %v\n%s", err, out)
	}
}

const arrayOfArraysCodecPackageTest = `package orm

import (
	"reflect"
	"testing"
)

type point struct {
	X float64 ` + "`json:\"x\"`" + `
	Y float64 ` + "`json:\"y\"`" + `
}

func encodeArrayOfArrays[T any](t *testing.T, value [][]T) []byte {
	t.Helper()
	encoded, err := marshalArrayOfArraysFieldValue(value)
	if err != nil {
		t.Fatalf("marshal %#v: %v", value, err)
	}
	payload, ok := encoded.([]byte)
	if !ok {
		t.Fatalf("marshal %#v = %T, want []byte", value, encoded)
	}
	return payload
}

func TestArrayOfArraysStrings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value [][]string
		json  string
		back  [][]string
	}{
		{"nil outer", nil, "null", nil},
		{"empty outer", [][]string{}, "[]", [][]string{}},
		{"empty inner", [][]string{{}}, "[[]]", [][]string{{}}},
		{"nil inner", [][]string{nil, {"a"}, nil}, ` + "`" + `[[],["a"],[]]` + "`" + `, [][]string{{}, {"a"}, {}}},
		{"ragged", [][]string{{"a", "b", "c"}, {"d"}, {}}, ` + "`" + `[["a","b","c"],["d"],[]]` + "`" + `, [][]string{{"a", "b", "c"}, {"d"}, {}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := encodeArrayOfArrays(t, tc.value)
			if string(payload) != tc.json {
				t.Fatalf("encoded %s, want %s", payload, tc.json)
			}
			var back [][]string
			if err := unmarshalJSONFieldValue(payload, &back); err != nil {
				t.Fatalf("decode %s: %v", payload, err)
			}
			if !reflect.DeepEqual(back, tc.back) {
				t.Fatalf("decoded %#v, want %#v", back, tc.back)
			}
		})
	}
}

func TestArrayOfArraysObjectsKeepInput(t *testing.T) {
	walls := [][]point{{{X: 1, Y: 2}, {X: 3, Y: 4}}, nil}
	payload := encodeArrayOfArrays(t, walls)
	if want := ` + "`" + `[[{"x":1,"y":2},{"x":3,"y":4}],[]]` + "`" + `; string(payload) != want {
		t.Fatalf("encoded %s, want %s", payload, want)
	}
	if walls[1] != nil {
		t.Fatal("encoding replaced the caller's nil inner list")
	}
	var back [][]point
	if err := unmarshalJSONFieldValue(payload, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := [][]point{{{X: 1, Y: 2}, {X: 3, Y: 4}}, {}}; !reflect.DeepEqual(back, want) {
		t.Fatalf("decoded %#v, want %#v", back, want)
	}
}
`
