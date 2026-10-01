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

// optionalJSONSchema is fixture-db (for Generic.JSON) plus types with an
// optional single Generic.JSON field: an output type, one that also has a
// union field (UnmarshalJSON's other branch), a @strictJSON one, and an
// input type that holds one as a field, nests the output type and pairs
// with Note through ToNote.
func optionalJSONSchema(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{
		`{"name":"Note","role":"EmbeddedStruct","jsonField":true,"fields":[
			{"name":"body","typeRef":{"name":"Generic.JSON"},"required":true},
			{"name":"extra","typeRef":{"name":"Generic.JSON"}},
			{"name":"tags","typeRef":{"name":"string","isArray":true}}
		]}`,
		`{"name":"Plain","role":"EmbeddedStruct","fields":[{"name":"label","typeRef":{"name":"string"},"required":true}]}`,
		`{"name":"Choosy","role":"EmbeddedStruct","fields":[
			{"name":"extra","typeRef":{"name":"Generic.JSON"}},
			{"name":"choice","typeRef":{"name":"Choice"}}
		]}`,
		`{"name":"StrictNote","role":"EmbeddedStruct","strictJSON":true,"fields":[
			{"name":"extra","typeRef":{"name":"Generic.JSON"}}
		]}`,
		`{"name":"NoteInput","role":"APIInput","fields":[
			{"name":"body","typeRef":{"name":"Generic.JSON"},"required":true},
			{"name":"extra","typeRef":{"name":"Generic.JSON"}},
			{"name":"tags","typeRef":{"name":"string","isArray":true}}
		]}`,
		`{"name":"WrapperInput","role":"APIInput","fields":[
			{"name":"note","typeRef":{"name":"Note"}}
		]}`,
	} {
		var model ir.TypeDef
		if err := json.Unmarshal([]byte(encoded), &model); err != nil {
			t.Fatal(err)
		}
		schema.Types[model.Name] = &model
	}
	schema.Unions["Choice"] = &ir.UnionDef{Name: "Choice", Types: []string{"Plain"}}
	return schema
}

// TestGeneratedOptionalGenericJSONKeepsNull runs a test inside the
// generated module: an optional Generic.JSON field takes null as a value,
// apart from absent. encoding/json leaves the field's pointer nil for null,
// as for an absent key; UnmarshalJSON then sets it to the JSON null token,
// MarshalJSON writes it back as null and leaves out a nil one, and ToNote
// carries an input's null over. A null elsewhere in the payload is not the
// field's.
func TestGeneratedOptionalGenericJSONKeepsNull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	paths := testpaths.Local(t)
	output, err := Generate(optionalJSONSchema(t), Options{SchemaName: "optional-json", ModulePath: "example.com/schemas/types/go/optional-json"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "optional-json")
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

func decode(t *testing.T, payload string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(payload), target); err != nil {
		t.Fatalf("decode %s: %v", payload, err)
	}
}

func members(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// extraOf is "absent" for a nil field, else the field's JSON text.
func extraOf(extra *GenericJSON) string {
	if extra == nil {
		return "absent"
	}
	return string(*extra)
}

func TestAnOptionalGenericJSONKeepsNullApartFromAbsent(t *testing.T) {
	for _, tc := range []struct{ payload, want string }{
		{` + "`" + `{"body": 1}` + "`" + `, "absent"},
		{` + "`" + `{"body": 1, "extra": null}` + "`" + `, "null"},
		{` + "`" + `{"body": 1, "extra": {"a": null}}` + "`" + `, ` + "`" + `{"a": null}` + "`" + `},
		{` + "`" + `{"body": {"extra": null}, "tags": [], "x": null}` + "`" + `, "absent"},
		{` + "`" + `{"body": 1, "EXTRA": null}` + "`" + `, "null"},
		{` + "`" + `{"body": 1, "extra": null, "extra": 2}` + "`" + `, "2"},
		{` + "`" + `{"body": 1, "extra": 2, "extra": null}` + "`" + `, "null"},
		{` + "`" + `{"body": 1, "ex\u0074ra": null}` + "`" + `, "null"},
	} {
		var note Note
		decode(t, tc.payload, &note)
		if got := extraOf(note.Extra); got != tc.want {
			t.Errorf("%s: extra = %s, want %s", tc.payload, got, tc.want)
		}
		raw, present := members(t, note)["extra"]
		if want := tc.want != "absent"; present != want {
			t.Errorf("%s: re-encoded extra present = %v, want %v", tc.payload, present, want)
		} else if present && !jsonEqual(raw, *note.Extra) {
			t.Errorf("%s: re-encoded extra = %s", tc.payload, raw)
		}
	}

	var choosy Choosy
	decode(t, ` + "`" + `{"extra": null, "choice": {"label": "a"}}` + "`" + `, &choosy)
	if got := extraOf(choosy.Extra); got != "null" {
		t.Errorf("a type with a union field: extra = %s, want null", got)
	}
	var strict StrictNote
	decode(t, ` + "`" + `{"extra": null}` + "`" + `, &strict)
	if got := extraOf(strict.Extra); got != "null" {
		t.Errorf("a @strictJSON type: extra = %s, want null", got)
	}

	// An input type's own field is an InputField, set to null; an object it
	// holds keeps its own field's null.
	var input NoteInput
	decode(t, ` + "`" + `{"body": 1, "extra": null}` + "`" + `, &input)
	if !input.Extra.IsNull() {
		t.Errorf("an input type's null extra is not set to null: %+v", input.Extra)
	}
	if got := extraOf(input.ToNote().Extra); got != "null" {
		t.Errorf("ToNote: extra = %s, want null", got)
	}
	var unset NoteInput
	decode(t, ` + "`" + `{"body": 1}` + "`" + `, &unset)
	if got := extraOf(unset.ToNote().Extra); got != "absent" {
		t.Errorf("ToNote of an absent extra: extra = %s, want absent", got)
	}
	var wrapper WrapperInput
	decode(t, ` + "`" + `{"note": {"body": 1, "extra": null}}` + "`" + `, &wrapper)
	if got := extraOf(wrapper.Note.Value.Extra); got != "null" {
		t.Errorf("a nested object: extra = %s, want null", got)
	}

	// A required one is still refused when null.
	var required Note
	decode(t, ` + "`" + `{"body": null}` + "`" + `, &required)
	if errs := required.Validate(); !errs.HasErrors() {
		t.Error("a null required Generic.JSON passed Validate")
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "optional_json_test.go"), []byte(code), 0o600); err != nil {
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
	command := exec.Command("go", "test", "-count=1", "-run", "^TestAnOptionalGenericJSONKeepsNullApartFromAbsent$", ".")
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated optional Generic.JSON test failed: %v\n%s", err, out)
	}
}
