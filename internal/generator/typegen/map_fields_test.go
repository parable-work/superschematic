package typegen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// runGeneratedModuleTest writes output as a module wired against the real
// superscalar binding, adds code as a test file inside it, vets the module
// and runs the test.
func runGeneratedModuleTest(t *testing.T, output *ModuleOutput, name, code string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping generated-module run in -short mode")
	}
	paths := testpaths.Local(t)
	dir := filepath.Join(t.TempDir(), name)
	if err := SetReplacePaths(output, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+"_test.go"), []byte(code), 0o644); err != nil {
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
		t.Fatalf("generated %s module does not vet: %v\n%s", name, err, out)
	}
	run := exec.Command("go", "test", "-count=1", ".")
	run.Dir = dir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("generated %s test failed: %v\n%s", name, err, out)
	}
}

// TestScalarMapValidationChecksEntries pins that a map of a scalar validates
// each entry on its own, reported under name[key]: a required map is
// required and its entries must be valid, an optional map skips a null
// entry, an input map is checked only when present and not null, and a
// field rule (minLength, pattern) applies to every entry.
func TestScalarMapValidationChecksEntries(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	minLength := 2
	fields := []*ir.FieldDef{
		{Name: "panes", TypeRef: ir.TypeRef{Name: "Identity.UUID", IsMap: true}, Required: true},
		{Name: "optionalPanes", TypeRef: ir.TypeRef{Name: "Identity.UUID", IsMap: true}},
		{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}, Required: true, ValidateMinLength: &minLength},
		{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsMap: true}, ValidatePattern: "^[a-z]+$"},
	}
	schema.Types["ScalarMap"] = &ir.TypeDef{Name: "ScalarMap", Role: ir.RoleEmbeddedStruct, Fields: fields}
	schema.Types["ScalarMapInput"] = &ir.TypeDef{Name: "ScalarMapInput", Role: ir.RoleAPIInput, Fields: fields}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-db",
		ModulePath: "example.com/schemas/types/go/scalar-map",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func mentions(errors ValidationErrors, key string) bool {
	return errors.HasErrors() && strings.Contains(fmt.Sprint(errors), key)
}

func TestMapEntries(t *testing.T) {
	value := ScalarMap{}
	if !mentions(value.Validate(), "panes") {
		t.Fatal("a missing required map was accepted")
	}
	value.Panes = map[string]IdentityUUID{}
	value.Labels = map[string]string{}
	if errors := value.Validate(); errors.HasErrors() {
		t.Fatalf("present empty maps were refused: %v", errors)
	}
	id, err := ParseIdentityUUID("60000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	value.Panes["detail"] = id
	value.OptionalPanes = map[string]*IdentityUUID{"detail": &id, "absent": nil}
	value.Labels["region"] = "eu"
	tag := "gold"
	value.Tags = map[string]*string{"tier": &tag, "absent": nil}
	if errors := value.Validate(); errors.HasErrors() {
		t.Fatalf("valid maps were refused: %v", errors)
	}

	value.Panes["bad"] = IdentityUUID{}
	if errors := value.Validate(); !mentions(errors, "panes[bad]") {
		t.Fatalf("an invalid required entry was not reported under its key: %v", errors)
	}
	delete(value.Panes, "bad")
	zero := IdentityUUID{}
	value.OptionalPanes["bad"] = &zero
	if errors := value.Validate(); !mentions(errors, "optionalPanes[bad]") {
		t.Fatalf("an invalid optional entry was not reported under its key: %v", errors)
	}
	delete(value.OptionalPanes, "bad")
	value.Labels["short"] = "x"
	if errors := value.Validate(); !mentions(errors, "labels[short]") {
		t.Fatalf("minLength was not applied to each entry: %v", errors)
	}
	delete(value.Labels, "short")
	upper := "Gold"
	value.Tags["upper"] = &upper
	if errors := value.Validate(); !mentions(errors, "tags[upper]") {
		t.Fatalf("pattern was not applied to each entry: %v", errors)
	}

	var input ScalarMapInput
	if err := json.Unmarshal([]byte(` + "`" + `{"panes":{},"labels":{},"optionalPanes":null}` + "`" + `), &input); err != nil {
		t.Fatal(err)
	}
	if errors := input.Validate(); errors.HasErrors() {
		t.Fatalf("a null optional input map was refused: %v", errors)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"panes":{},"labels":{},"optionalPanes":{"bad":"00000000-0000-0000-0000-000000000000"}}` + "`" + `), &input); err != nil {
		t.Fatal(err)
	}
	if errors := input.Validate(); !mentions(errors, "optionalPanes[bad]") {
		t.Fatalf("an invalid input entry was not reported under its key: %v", errors)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"panes":{"bad":"not-a-uuid"},"labels":{}}` + "`" + `), &value); err == nil {
		t.Fatal("a malformed UUID decoded")
	}
}
`
	runGeneratedModuleTest(t, output, "scalar_map", code)
}

// TestNullableObjectMapMaskPreservesNull pins MaskSecrets on an optional map
// of a generated type: a null entry stays a null entry and a present entry
// is a masked copy.
func TestNullableObjectMapMaskPreservesNull(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-maps"))
	if err != nil {
		t.Fatal(err)
	}
	container := schema.Types["MapContainer"]
	container.Fields = append(container.Fields, &ir.FieldDef{
		Name: "receipts", TypeRef: ir.TypeRef{Name: "MapValue", IsMap: true},
	})
	output, err := Generate(schema, Options{
		SchemaName: "fixture-maps",
		ModulePath: "example.com/schemas/types/go/fixture-maps",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	const code = `package types

import "testing"

func TestNullableMap(t *testing.T) {
	original := &MapContainer{Receipts: map[string]*MapValue{"none": nil, "present": {Count: 4}}}
	masked := original.MaskSecrets()
	if value, exists := masked.Receipts["none"]; !exists || value != nil {
		t.Fatal("the null entry was lost")
	}
	if masked.Receipts["present"] == original.Receipts["present"] {
		t.Fatal("the present entry was not copied")
	}
	if masked.Receipts["present"].Count != 4 {
		t.Fatal("the present entry changed")
	}
}
`
	runGeneratedModuleTest(t, output, "nullable_map", code)
}

// enumMapFields are a required and an optional map of an enum.
func enumMapFields() []*ir.FieldDef {
	return []*ir.FieldDef{
		{Name: "statuses", TypeRef: ir.TypeRef{Name: "TenantStatus", IsMap: true}, Required: true},
		{Name: "optionalStatuses", TypeRef: ir.TypeRef{Name: "TenantStatus", IsMap: true}},
	}
}

// TestEnumMapFieldsRoundTrip pins a map of an enum on an output type and on
// its input twin: the module builds and vets, a payload decodes and
// re-encodes unchanged, an optional map keeps a null entry, an unknown
// value is reported under name[key], a present-but-null input map is not
// checked, and the input converts to its output type.
func TestEnumMapFieldsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Types["EnumMap"] = &ir.TypeDef{Name: "EnumMap", Role: ir.RoleEmbeddedStruct, JsonField: true, Fields: enumMapFields()}
	schema.Types["EnumMapInput"] = &ir.TypeDef{Name: "EnumMapInput", Role: ir.RoleAPIInput, Fields: enumMapFields()}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-db",
		ModulePath: "example.com/schemas/types/go/enum-map",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func mentions(errors ValidationErrors, key string) bool {
	return errors.HasErrors() && strings.Contains(fmt.Sprint(errors), key)
}

func sameJSON(t *testing.T, value any, want string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got, expected any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("encoded %s, want %s", encoded, want)
	}
}

func TestEnumMaps(t *testing.T) {
	const payload = ` + "`" + `{"statuses":{"a":"active"},"optionalStatuses":{"b":"suspended","none":null}}` + "`" + `
	var value EnumMap
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		t.Fatal(err)
	}
	if value.Statuses["a"] != TenantStatus_Active {
		t.Fatalf("statuses[a] = %q", value.Statuses["a"])
	}
	if entry := value.OptionalStatuses["b"]; entry == nil || *entry != TenantStatus_Suspended {
		t.Fatalf("optionalStatuses[b] = %v", entry)
	}
	if entry, exists := value.OptionalStatuses["none"]; !exists || entry != nil {
		t.Fatal("the null entry of the optional map was lost")
	}
	if errors := value.Validate(); errors.HasErrors() {
		t.Fatalf("valid enum maps were refused: %v", errors)
	}
	sameJSON(t, &value, payload)
	if masked := value.MaskSecrets(); !reflect.DeepEqual(masked, &value) {
		t.Fatalf("MaskSecrets changed the maps: %#v", masked)
	}

	value.Statuses["bad"] = TenantStatus("retired")
	if errors := value.Validate(); !mentions(errors, "statuses[bad]") {
		t.Fatalf("an unknown value in the required map was not reported under its key: %v", errors)
	}
	delete(value.Statuses, "bad")
	retired := TenantStatus("retired")
	value.OptionalStatuses["bad"] = &retired
	if errors := value.Validate(); !mentions(errors, "optionalStatuses[bad]") {
		t.Fatalf("an unknown value in the optional map was not reported under its key: %v", errors)
	}
	if errors := (&EnumMap{}).Validate(); !mentions(errors, "statuses") {
		t.Fatalf("a missing required map was accepted: %v", errors)
	}

	var input EnumMapInput
	const nullInput = ` + "`" + `{"statuses":{},"optionalStatuses":null}` + "`" + `
	if err := json.Unmarshal([]byte(nullInput), &input); err != nil {
		t.Fatal(err)
	}
	if errors := input.Validate(); errors.HasErrors() {
		t.Fatalf("a null optional input map was refused: %v", errors)
	}
	sameJSON(t, &input, nullInput)
	input = EnumMapInput{}
	if err := json.Unmarshal([]byte(payload), &input); err != nil {
		t.Fatal(err)
	}
	if errors := input.Validate(); errors.HasErrors() {
		t.Fatalf("valid input enum maps were refused: %v", errors)
	}
	sameJSON(t, &input, payload)
	if masked := input.MaskSecrets(); !reflect.DeepEqual(masked, &input) {
		t.Fatalf("MaskSecrets changed the input maps: %#v", masked)
	}
	converted := input.ToEnumMap()
	if converted.Statuses["a"] != TenantStatus_Active || converted.OptionalStatuses["b"] == nil || *converted.OptionalStatuses["b"] != TenantStatus_Suspended {
		t.Fatalf("ToEnumMap lost entries: %#v", converted)
	}
	input.OptionalStatuses.Value["bad"] = &retired
	if errors := input.Validate(); !mentions(errors, "optionalStatuses[bad]") {
		t.Fatalf("an unknown input value was not reported under its key: %v", errors)
	}
}
`
	runGeneratedModuleTest(t, output, "enum_map", code)
}

// validatedScalarMapScalars are one validated scalar per Go representation:
// a pattern-checked string, a UUID, a timestamp, a duration, an integer, a
// bounded float and a URL.
var validatedScalarMapScalars = []string{
	"Identity.Slug", "Identity.UUID", "Temporal.DateTime", "Temporal.Duration",
	"Generic.Int64", "Generic.Probability", "Network.Url",
}

// invalidScalarMapEntries are JSON values each scalar must refuse, at decode
// or at Validate.
var invalidScalarMapEntries = map[string]string{
	"Identity.Slug":       `"Not A Slug"`,
	"Identity.UUID":       `"00000000-0000-0000-0000-000000000000"`,
	"Generic.Probability": `1.5`,
	"Network.Url":         `"not a url"`,
}

// loadScalarMapService writes a data-form General service with a type that
// holds a required and an optional map of each named scalar, and its input
// twin, and loads it so the scalars are hydrated from the catalog.
func loadScalarMapService(t *testing.T, names []string) *ir.Schema {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "scalar-maps")
	catalog := registry.CoreScalars()
	scalarDefs := map[string]any{}
	var fields []any
	for _, name := range names {
		meta, ok := catalog.Scalar(name)
		if !ok {
			t.Fatalf("the catalog has no %s", name)
		}
		scalarDefs[name] = map[string]any{"name": name, "languagePrimitive": languagePrimitive(meta.Primitive)}
		symbol := codegen.BuildScalarTokens(name).Symbol
		fields = append(fields,
			map[string]any{"name": strings.ToLower(symbol[:1]) + symbol[1:], "typeRef": map[string]any{"name": name, "isMap": true}, "required": true},
			map[string]any{"name": "optional" + symbol, "typeRef": map[string]any{"name": name, "isMap": true}},
		)
	}
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    "scalar-maps",
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "scalar-maps.schema.json"): map[string]any{
			"scalars": scalarDefs,
			"types": map[string]any{
				"ScalarMaps":      map[string]any{"name": "ScalarMaps", "role": "EmbeddedStruct", "jsonField": true, "fields": fields},
				"ScalarMapsInput": map[string]any{"name": "ScalarMapsInput", "role": "APIInput", "fields": fields},
			},
		},
	}
	for rel, doc := range files {
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load scalar-maps: %v", err)
	}
	return schema
}

// scalarMapJSON renders a catalog example as a JSON value.
func scalarMapJSON(t *testing.T, name string) string {
	t.Helper()
	meta, _ := registry.CoreScalars().Scalar(name)
	if len(meta.Examples) == 0 {
		t.Fatalf("%s has no catalog example", name)
	}
	example := meta.Examples[0]
	switch languagePrimitive(meta.Primitive) {
	case "number", "boolean":
		return example
	}
	quoted, err := json.Marshal(example)
	if err != nil {
		t.Fatal(err)
	}
	return string(quoted)
}

// TestValidatedScalarMapFieldsRoundTrip pins maps of validated scalars, one
// per Go representation, on an output type and its input twin: the module
// builds and vets, a payload of catalog examples decodes, validates and
// re-encodes stably, an optional map keeps a null entry, an invalid entry is
// refused under name[key], and the input converts to its output type.
func TestValidatedScalarMapFieldsRoundTrip(t *testing.T) {
	schema := loadScalarMapService(t, validatedScalarMapScalars)
	output, err := Generate(schema, Options{
		SchemaName: "scalar-maps",
		ModulePath: "example.com/schemas/types/go/scalar-maps",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	var valid, nulls, invalid []string
	for _, name := range validatedScalarMapScalars {
		symbol := codegen.BuildScalarTokens(name).Symbol
		required := strings.ToLower(symbol[:1]) + symbol[1:]
		example := scalarMapJSON(t, name)
		valid = append(valid,
			fmt.Sprintf("%q:{\"a\":%s}", required, example),
			fmt.Sprintf("%q:{\"a\":%s,\"none\":null}", "optional"+symbol, example))
		nulls = append(nulls, fmt.Sprintf("%q:{}", required), fmt.Sprintf("%q:null", "optional"+symbol))
		if bad, ok := invalidScalarMapEntries[name]; ok {
			invalid = append(invalid,
				fmt.Sprintf("{%q, %q, `{%q:{\"bad\":%s}}`}", required, required+"[bad]", required, bad),
				fmt.Sprintf("{%q, %q, `{%q:{\"bad\":%s}}`}", "optional"+symbol, "optional"+symbol+"[bad]", "optional"+symbol, bad))
		}
	}
	code := fmt.Sprintf(`package types

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const validPayload = %q

const nullPayload = %q

var invalidEntries = []struct{ field, key, patch string }{
	%s,
}

func mentions(errors ValidationErrors, key string) bool {
	return errors.HasErrors() && strings.Contains(fmt.Sprint(errors), key)
}

// stable decodes payload into a fresh T, validates it, and checks that
// encoding it and decoding the result encodes the same bytes again.
func stable[T any, P interface {
	*T
	Validate() ValidationErrors
}](t *testing.T, payload string) P {
	t.Helper()
	var value T
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		t.Fatalf("decode %%T: %%v", value, err)
	}
	if errors := P(&value).Validate(); errors.HasErrors() {
		t.Fatalf("valid %%T refused: %%v", value, errors)
	}
	first, err := json.Marshal(&value)
	if err != nil {
		t.Fatal(err)
	}
	var again T
	if err := json.Unmarshal(first, &again); err != nil {
		t.Fatalf("decode re-encoded %%T: %%v", value, err)
	}
	second, err := json.Marshal(&again)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("%%T re-encodes unstably:\n%%s\n%%s", value, first, second)
	}
	return &value
}

// keepsNullEntries checks that every optional map field kept its null entry.
func keepsNullEntries(t *testing.T, value any) {
	t.Helper()
	record := reflect.ValueOf(value).Elem()
	for i := 0; i < record.NumField(); i++ {
		name := record.Type().Field(i).Name
		if !strings.HasPrefix(name, "Optional") {
			continue
		}
		field := record.Field(i)
		if field.Kind() == reflect.Struct {
			field = field.FieldByName("Value")
		}
		entry := field.MapIndex(reflect.ValueOf("none"))
		if !entry.IsValid() || !entry.IsNil() {
			t.Errorf("%%s lost its null entry", name)
		}
	}
}

func TestScalarMaps(t *testing.T) {
	value := stable[ScalarMaps](t, validPayload)
	keepsNullEntries(t, value)
	if masked := value.MaskSecrets(); !reflect.DeepEqual(masked, value) {
		t.Fatalf("MaskSecrets changed the maps: %%#v", masked)
	}
	input := stable[ScalarMapsInput](t, validPayload)
	keepsNullEntries(t, input)
	if converted := input.ToScalarMaps(); !reflect.DeepEqual(converted, value) {
		t.Fatalf("ToScalarMaps = %%#v, want %%#v", converted, value)
	}
	stable[ScalarMapsInput](t, nullPayload)
	if errors := (&ScalarMaps{}).Validate(); !errors.HasErrors() {
		t.Fatal("missing required maps were accepted")
	}

	for _, entry := range invalidEntries {
		for _, target := range []interface{ Validate() ValidationErrors }{&ScalarMaps{}, &ScalarMapsInput{}} {
			if err := json.Unmarshal([]byte(validPayload), target); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(entry.patch), target); err != nil {
				continue
			}
			if errors := target.Validate(); !mentions(errors, entry.key) {
				t.Errorf("%%T: an invalid %%s entry was not refused under %%s: %%v", target, entry.field, entry.key, errors)
			}
		}
	}
}
`, "{"+strings.Join(valid, ",")+"}", "{"+strings.Join(nulls, ",")+"}", strings.Join(invalid, ",\n\t"))
	runGeneratedModuleTest(t, output, "scalar_maps", code)
}
