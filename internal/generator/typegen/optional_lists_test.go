package typegen

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// optionalListSchema is fixture-maps with list fields added to MapContainer
// and an input type that carries the same lists: an optional and a required
// list of a generated type, an optional list of lists, and an optional map
// that is not a list.
func optionalListSchema(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-maps"))
	if err != nil {
		t.Fatal(err)
	}
	fields := func() []*ir.FieldDef {
		return []*ir.FieldDef{
			{Name: "arguments", TypeRef: ir.TypeRef{Name: "MapValue", IsArray: true}},
			{Name: "requiredArguments", TypeRef: ir.TypeRef{Name: "MapValue", IsArray: true}, Required: true},
			{Name: "grid", TypeRef: ir.TypeRef{Name: "string", IsArray: true, IsArrayOfArrays: true}},
			{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}},
		}
	}
	schema.Types["MapContainer"].Fields = append(schema.Types["MapContainer"].Fields, fields()...)
	schema.Types["ListInput"] = &ir.TypeDef{Name: "ListInput", Role: ir.RoleAPIInput, Fields: fields()}
	return schema
}

// TestOptionalListTags pins the JSON tag of each optional field shape: an
// optional list or list of lists is omitzero, so only a nil slice is left
// out; an optional map stays omitempty; an optional input field is an
// omitzero InputField; a required field has no option.
func TestOptionalListTags(t *testing.T) {
	output, err := Generate(optionalListSchema(t), Options{
		SchemaName: "fixture-maps",
		ModulePath: "example.com/schemas/types/go/optional-lists",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(dir, "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Arguments\\s+\\[\\]MapValue\\s+`json:\"arguments,omitzero\"`",
		"RequiredArguments\\s+\\[\\]MapValue\\s+`json:\"requiredArguments\"`",
		"Grid\\s+\\[\\]\\[\\]string\\s+`json:\"grid,omitzero\"`",
		"Labels\\s+map\\[string\\]\\*string\\s+`json:\"labels,omitempty\"`",
		"Arguments\\s+InputField\\[\\[\\]MapValue\\]\\s+`json:\"arguments,omitzero\"`",
		"Grid\\s+InputField\\[\\[\\]\\[\\]string\\]\\s+`json:\"grid,omitzero\"`",
		"Labels\\s+InputField\\[map\\[string\\]\\*string\\]\\s+`json:\"labels,omitzero\"`",
	} {
		if !regexp.MustCompile(want).Match(source) {
			t.Errorf("types.go has no field matching %s", want)
		}
	}
}

// TestOptionalListMarshal builds the module and pins the wire behavior of
// an optional list: nil is left out, an explicit [] is written and survives
// a decode and re-encode through JSON and through a map, null decodes back
// to nil, and a required list still encodes nil as []. An optional list of
// lists behaves the same, and a nil inner list encodes as [].
func TestOptionalListMarshal(t *testing.T) {
	output, err := Generate(optionalListSchema(t), Options{
		SchemaName: "fixture-maps",
		ModulePath: "example.com/schemas/types/go/optional-lists",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"encoding/json"
	"testing"
)

func encode(t *testing.T, value *MapContainer) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestOptionalLists(t *testing.T) {
	value := &MapContainer{}
	fields := encode(t, value)
	for _, name := range []string{"arguments", "grid", "labels"} {
		if _, ok := fields[name]; ok {
			t.Errorf("unset optional %s was written: %s", name, fields[name])
		}
	}
	if string(fields["requiredArguments"]) != "[]" {
		t.Errorf("nil required list encoded as %s, want []", fields["requiredArguments"])
	}
	if value.Arguments != nil || value.Grid != nil {
		t.Error("encoding filled an unset optional list")
	}

	value = &MapContainer{Arguments: []MapValue{}, Grid: [][]string{}}
	fields = encode(t, value)
	if string(fields["arguments"]) != "[]" || string(fields["grid"]) != "[]" {
		t.Errorf("explicit empty lists encoded as arguments=%s grid=%s", fields["arguments"], fields["grid"])
	}

	value = &MapContainer{Grid: [][]string{nil, {"a"}}}
	if got := string(encode(t, value)["grid"]); got != "[[],[\"a\"]]" {
		t.Errorf("nil inner list encoded as %s", got)
	}

	decoded := &MapContainer{}
	if err := json.Unmarshal([]byte("{\"strings\":{},\"nested\":{},\"requiredArguments\":[],\"arguments\":[],\"grid\":[]}"), decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Arguments == nil || decoded.Grid == nil {
		t.Fatal("explicit [] decoded as nil")
	}
	fields = encode(t, decoded)
	if string(fields["arguments"]) != "[]" || string(fields["grid"]) != "[]" {
		t.Errorf("explicit [] lost on a JSON round trip: arguments=%s grid=%s", fields["arguments"], fields["grid"])
	}
	asMap, err := decoded.ToMap()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := asMap["arguments"]; !ok {
		t.Error("explicit [] missing from ToMap")
	}
	fromMap := &MapContainer{}
	if err := fromMap.FromMap(asMap); err != nil {
		t.Fatal(err)
	}
	if fromMap.Arguments == nil {
		t.Error("explicit [] lost on a map round trip")
	}

	decoded = &MapContainer{}
	if err := json.Unmarshal([]byte("{\"arguments\":null,\"grid\":null}"), decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Arguments != nil || decoded.Grid != nil {
		t.Fatal("null decoded as a non-nil list")
	}
	fields = encode(t, decoded)
	if _, ok := fields["arguments"]; ok {
		t.Error("null optional list was written after a round trip")
	}
	if _, ok := fields["grid"]; ok {
		t.Error("null optional list of lists was written after a round trip")
	}
}
`
	runGeneratedModuleTest(t, output, "optional_lists", code)
}
