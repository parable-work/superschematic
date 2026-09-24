package typegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// unionFieldsSchema declares a discriminated union (Choice, tagged by kind)
// and uses it in every field shape: an optional object field, required and
// optional maps and maps of lists, and optional input fields (InputField
// wrappers) that hold one value, a list and a map. The link field gives the module a
// scalar, so scalars.go and enums.go do not both declare ValidationError.
func unionFieldsSchema(t *testing.T) *ir.Schema {
	t.Helper()
	schema := ir.NewSchema("union-fields", ir.SchemaKindGeneral)
	schema.Scalars["Network.Url"] = &ir.ScalarDef{
		Name: "Network.Url", LanguagePrimitive: ir.LanguageString,
		TypeMappings: map[string]string{"go": "NetworkUrl"},
	}
	schema.Enums["ChoiceKind"] = &ir.EnumDef{Name: "ChoiceKind", Values: []ir.EnumValueDef{
		{Name: "Alpha", SerializedAs: "alpha"}, {Name: "Beta", SerializedAs: "beta"},
	}}
	for _, encoded := range []string{
		`{"name":"AlphaChoice","role":"EmbeddedStruct","fields":[{"name":"kind","typeRef":{"name":"ChoiceKind"},"required":true,"default":"alpha","internalMetadata":true},{"name":"value","typeRef":{"name":"string"},"required":true}]}`,
		`{"name":"BetaChoice","role":"EmbeddedStruct","fields":[{"name":"kind","typeRef":{"name":"ChoiceKind"},"required":true,"default":"beta","internalMetadata":true},{"name":"count","typeRef":{"name":"number"},"required":true}]}`,
		`{"name":"ChoiceRecord","role":"EmbeddedStruct","fields":[{"name":"link","typeRef":{"name":"Network.Url"}},{"name":"optionalChoice","typeRef":{"name":"Choice"}},{"name":"byName","typeRef":{"name":"Choice","isMap":true},"required":true},{"name":"listsByName","typeRef":{"name":"Choice","isMap":true,"isArray":true},"required":true},{"name":"optionalByName","typeRef":{"name":"Choice","isMap":true}},{"name":"optionalListsByName","typeRef":{"name":"Choice","isMap":true,"isArray":true}}]}`,
		`{"name":"NullableUnionInput","role":"APIInput","fields":[{"name":"scalar","typeRef":{"name":"Choice"}},{"name":"array","typeRef":{"name":"Choice","isArray":true}},{"name":"map","typeRef":{"name":"Choice","isMap":true}}]}`,
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

// TestGeneratedUnionFieldsDecode generates the module, drops a test into it
// and runs it: map and map-of-list union fields decode each value through
// the union wrapper, an optional union field is a nil interface when absent,
// and an input union keeps absent, null and value apart and re-encodes the
// payload it decoded.
func TestGeneratedUnionFieldsDecode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	paths := testpaths.Local(t)
	output, err := Generate(unionFieldsSchema(t), Options{SchemaName: "union-fields", ModulePath: "example.com/schemas/types/go/union-fields"})
	if err != nil {
		t.Fatal(err)
	}
	for _, typeInfo := range output.Types {
		if typeInfo.Name != "ChoiceRecord" {
			continue
		}
		// A union is an interface and already nilable: no field shape adds a
		// pointer to it.
		want := map[string]string{
			"optionalChoice":      "Choice",
			"byName":              "map[string]Choice",
			"listsByName":         "map[string][]Choice",
			"optionalByName":      "map[string]Choice",
			"optionalListsByName": "map[string][]Choice",
		}
		for _, field := range typeInfo.Fields {
			if goType, ok := want[field.Name]; ok && field.GoType != goType {
				t.Errorf("%s Go type = %q, want %q", field.Name, field.GoType, goType)
			}
		}
	}
	dir := filepath.Join(t.TempDir(), "union-fields")
	if err := SetReplacePaths(output, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestChoiceRecord(t *testing.T) {
	var record ChoiceRecord
	payload := []byte(` + "`" + `{"byName":{"a":{"kind":"alpha","value":"one"},"b":{"kind":"beta","count":2}},"listsByName":{"x":[{"kind":"beta","count":3}]}}` + "`" + `)
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record.OptionalChoice != nil {
		t.Fatalf("absent optional union = %#v, want nil", record.OptionalChoice)
	}
	if alpha, ok := record.ByName["a"].(AlphaChoice); !ok || alpha.Value != "one" {
		t.Fatalf("byName[a] = %#v", record.ByName["a"])
	}
	if beta, ok := record.ByName["b"].(BetaChoice); !ok || beta.Count != 2 {
		t.Fatalf("byName[b] = %#v", record.ByName["b"])
	}
	if items := record.ListsByName["x"]; len(items) != 1 {
		t.Fatalf("listsByName[x] = %#v", items)
	} else if beta, ok := items[0].(BetaChoice); !ok || beta.Count != 3 {
		t.Fatalf("listsByName[x][0] = %#v", items[0])
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"optionalChoice":{"kind":"beta","count":1},"byName":{},"listsByName":{}}` + "`" + `), &record); err != nil {
		t.Fatalf("decode optional: %v", err)
	}
	if _, ok := record.OptionalChoice.(BetaChoice); !ok {
		t.Fatalf("optionalChoice = %#v", record.OptionalChoice)
	}
	if record.OptionalByName != nil || record.OptionalListsByName != nil {
		t.Fatalf("absent optional union maps = %#v, %#v; want nil", record.OptionalByName, record.OptionalListsByName)
	}
	var optionalMaps ChoiceRecord
	if err := json.Unmarshal([]byte(` + "`" + `{"byName":{},"listsByName":{},"optionalByName":{"a":{"kind":"alpha","value":"two"}},"optionalListsByName":{"x":[{"kind":"beta","count":4}]}}` + "`" + `), &optionalMaps); err != nil {
		t.Fatalf("decode optional maps: %v", err)
	}
	if alpha, ok := optionalMaps.OptionalByName["a"].(AlphaChoice); !ok || alpha.Value != "two" {
		t.Fatalf("optionalByName[a] = %#v", optionalMaps.OptionalByName["a"])
	}
	if items := optionalMaps.OptionalListsByName["x"]; len(items) != 1 {
		t.Fatalf("optionalListsByName[x] = %#v", items)
	} else if beta, ok := items[0].(BetaChoice); !ok || beta.Count != 4 {
		t.Fatalf("optionalListsByName[x][0] = %#v", items[0])
	}
}

func TestNullableUnionInputTriState(t *testing.T) {
	var omitted NullableUnionInput
	if err := json.Unmarshal([]byte("{}"), &omitted); err != nil {
		t.Fatalf("decode omitted: %v", err)
	}
	if omitted.Scalar.Set || omitted.Array.Set || omitted.Map.Set {
		t.Fatal("omitted union inputs must stay unset")
	}
	var nulled NullableUnionInput
	if err := json.Unmarshal([]byte(` + "`" + `{"scalar":null,"array":null,"map":null}` + "`" + `), &nulled); err != nil {
		t.Fatalf("decode nulls: %v", err)
	}
	if !nulled.Scalar.Set || !nulled.Scalar.Null || !nulled.Array.Set || !nulled.Array.Null || !nulled.Map.Set || !nulled.Map.Null {
		t.Fatal("explicit null union inputs must keep the null state")
	}
	var valued NullableUnionInput
	payload := []byte(` + "`" + `{"scalar":{"kind":"alpha","value":"one"},"array":[{"kind":"beta","count":2}],"map":{"first":{"kind":"alpha","value":"three"}}}` + "`" + `)
	if err := json.Unmarshal(payload, &valued); err != nil {
		t.Fatalf("decode values: %v", err)
	}
	if !valued.Scalar.Set || valued.Scalar.Null || valued.Scalar.Value == nil {
		t.Fatal("scalar union value was not kept")
	}
	if !valued.Array.Set || valued.Array.Null || len(valued.Array.Value) != 1 {
		t.Fatal("array union value was not kept")
	}
	if !valued.Map.Set || valued.Map.Null || len(valued.Map.Value) != 1 || valued.Map.Value["first"] == nil {
		t.Fatal("map union value was not kept")
	}
	for _, text := range []string{"{}", ` + "`" + `{"scalar":null,"array":null,"map":null}` + "`" + `, ` + "`" + `{"array":[],"map":{}}` + "`" + `, string(payload)} {
		var value NullableUnionInput
		var expected any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(text), &expected); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var actual any
		if err := json.Unmarshal(encoded, &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("round trip of %s = %s", text, encoded)
		}
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "union_fields_test.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	command := exec.Command("go", "test", "-count=1", ".")
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated union field tests failed: %v\n%s", err, out)
	}
}
