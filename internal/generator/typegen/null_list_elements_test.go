package typegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// nullElementsSchema is fixture-db (for its scalars and enum) plus types
// with a list field of every element kind the generated UnmarshalJSON
// checks: string, number, boolean, a string scalar, a UUID scalar, an enum,
// an object, Generic.JSON and a union, as T[] and T[][], required and
// optional, in an output type, an input type (InputField wrappers) and a
// @strictJSON type. The maps of lists are there to show they are not
// checked. Batch, whose object elements have no list, is what the
// benchmarks decode.
func nullElementsSchema(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Enums["ChoiceKind"] = &ir.EnumDef{Name: "ChoiceKind", Values: []ir.EnumValueDef{
		{Name: "Alpha", SerializedAs: "alpha"}, {Name: "Beta", SerializedAs: "beta"},
	}}
	for _, encoded := range []string{
		`{"name":"AlphaChoice","role":"EmbeddedStruct","fields":[{"name":"kind","typeRef":{"name":"ChoiceKind"},"required":true,"default":"alpha","internalMetadata":true},{"name":"value","typeRef":{"name":"string"},"required":true}]}`,
		`{"name":"BetaChoice","role":"EmbeddedStruct","fields":[{"name":"kind","typeRef":{"name":"ChoiceKind"},"required":true,"default":"beta","internalMetadata":true},{"name":"count","typeRef":{"name":"number"},"required":true}]}`,
		`{"name":"Point","role":"EmbeddedStruct","fields":[{"name":"x","typeRef":{"name":"number"}},{"name":"tags","typeRef":{"name":"string","isArray":true}}]}`,
		`{"name":"Lists","role":"EmbeddedStruct","fields":[
			{"name":"note","typeRef":{"name":"string"}},
			{"name":"strs","typeRef":{"name":"string","isArray":true},"required":true},
			{"name":"optStrs","typeRef":{"name":"string","isArray":true}},
			{"name":"nums","typeRef":{"name":"number","isArray":true}},
			{"name":"flags","typeRef":{"name":"boolean","isArray":true}},
			{"name":"names","typeRef":{"name":"Identity.Name","isArray":true}},
			{"name":"ids","typeRef":{"name":"Identity.UUID","isArray":true}},
			{"name":"statuses","typeRef":{"name":"TenantStatus","isArray":true}},
			{"name":"points","typeRef":{"name":"Point","isArray":true}},
			{"name":"payloads","typeRef":{"name":"Generic.JSON","isArray":true}},
			{"name":"choices","typeRef":{"name":"Choice","isArray":true}},
			{"name":"grid","typeRef":{"name":"string","isArray":true,"isArrayOfArrays":true},"required":true},
			{"name":"numGrid","typeRef":{"name":"number","isArray":true,"isArrayOfArrays":true}},
			{"name":"pointGrid","typeRef":{"name":"Point","isArray":true,"isArrayOfArrays":true}},
			{"name":"choiceGrid","typeRef":{"name":"Choice","isArray":true,"isArrayOfArrays":true}},
			{"name":"strsByKey","typeRef":{"name":"string","isArray":true,"isMap":true}},
			{"name":"reqStrsByKey","typeRef":{"name":"string","isArray":true,"isMap":true},"required":true}
		]}`,
		`{"name":"ListsInput","role":"APIInput","fields":[
			{"name":"strs","typeRef":{"name":"string","isArray":true},"required":true},
			{"name":"optStrs","typeRef":{"name":"string","isArray":true}},
			{"name":"grid","typeRef":{"name":"string","isArray":true,"isArrayOfArrays":true}},
			{"name":"choices","typeRef":{"name":"Choice","isArray":true}}
		]}`,
		`{"name":"Sample","role":"EmbeddedStruct","fields":[{"name":"x","typeRef":{"name":"number"}},{"name":"label","typeRef":{"name":"string"}}]}`,
		`{"name":"Batch","role":"EmbeddedStruct","fields":[
			{"name":"note","typeRef":{"name":"string"}},
			{"name":"strs","typeRef":{"name":"string","isArray":true}},
			{"name":"nums","typeRef":{"name":"number","isArray":true}},
			{"name":"samples","typeRef":{"name":"Sample","isArray":true}},
			{"name":"grid","typeRef":{"name":"string","isArray":true,"isArrayOfArrays":true}}
		]}`,
		`{"name":"StrictLists","role":"EmbeddedStruct","strictJSON":true,"fields":[
			{"name":"strs","typeRef":{"name":"string","isArray":true},"required":true},
			{"name":"optStrs","typeRef":{"name":"string","isArray":true}}
		]}`,
	} {
		var model ir.TypeDef
		if err := json.Unmarshal([]byte(encoded), &model); err != nil {
			t.Fatal(err)
		}
		schema.Types[model.Name] = &model
	}
	schema.Unions["Choice"] = &ir.UnionDef{Name: "Choice", Types: []string{"AlphaChoice", "BetaChoice"}}
	return schema
}

// TestListFieldsSkipMaps pins which fields UnmarshalJSON checks: every list
// and list of lists, and no map of lists.
func TestListFieldsSkipMaps(t *testing.T) {
	output, err := Generate(nullElementsSchema(t), Options{SchemaName: "null-elements", ModulePath: "example.com/schemas/types/go/null-elements"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, typeInfo := range output.Types {
		if typeInfo.Name != "Lists" {
			continue
		}
		for _, field := range typeInfo.ListFields() {
			got = append(got, field.Name)
		}
	}
	want := []string{"strs", "optStrs", "nums", "flags", "names", "ids", "statuses", "points", "payloads", "choices", "grid", "numGrid", "pointGrid", "choiceGrid"}
	if len(got) != len(want) {
		t.Fatalf("Lists list fields = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Lists list fields = %v, want %v", got, want)
		}
	}
	if !output.HasListFields() {
		t.Fatal("HasListFields = false for a module with list fields")
	}
}

// TestGeneratedDecoderRejectsNullListElements generates the module, drops
// testdata/null_list_elements/decode_test.go into it and runs it: decoding
// refuses a null element of every list field, and of every innermost list of
// a list of lists, with an error that names the type, the field and the
// index; a null list, a null inner list, a null map-of-list element and
// "null" inside a string or a nested value still decode. A randomized test
// checks the scanner against a decode-based reference, and the benchmarks
// run once. Set SUPERSCHEMATIC_BENCHTIME (for example 1s) to run them for
// that long and print the numbers.
func TestGeneratedDecoderRejectsNullListElements(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	paths := testpaths.Local(t)
	output, err := Generate(nullElementsSchema(t), Options{SchemaName: "null-elements", ModulePath: "example.com/schemas/types/go/null-elements"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "null-elements")
	if err := SetReplacePaths(output, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(filepath.Join("testdata", "null_list_elements", "decode_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "decode_test.go"), code, 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	vet := exec.Command("go", "vet", ".")
	vet.Dir = dir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("generated module does not vet: %v\n%s", err, out)
	}
	benchtime := os.Getenv("SUPERSCHEMATIC_BENCHTIME")
	if benchtime == "" {
		benchtime = "1x"
	}
	run := exec.Command("go", "test", "-count=1", "-bench=.", "-benchmem", "-benchtime="+benchtime, ".")
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("generated module test failed: %v\n%s", err, out)
	}
	if os.Getenv("SUPERSCHEMATIC_BENCHTIME") != "" {
		t.Logf("%s", out)
	}
}
