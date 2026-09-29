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

// retryFields declares an optional boolean without a default (respect), one
// defaulting to true (enabled) and one defaulting to false (quiet).
func retryFields() []*ir.FieldDef {
	yes, no := "true", "false"
	return []*ir.FieldDef{
		{Name: "respect", TypeRef: ir.TypeRef{Name: "boolean"}},
		{Name: "enabled", TypeRef: ir.TypeRef{Name: "boolean"}, Default: &yes},
		{Name: "quiet", TypeRef: ir.TypeRef{Name: "boolean"}, Default: &no},
	}
}

func retryOutput(t *testing.T) *ModuleOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-maps"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Types["Retry"] = &ir.TypeDef{Name: "Retry", Role: ir.RoleEmbeddedStruct, JsonField: true, Fields: retryFields()}
	schema.Types["RetryInput"] = &ir.TypeDef{Name: "RetryInput", Role: ir.RoleAPIInput, Fields: retryFields()}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-maps",
		ModulePath: "example.com/schemas/types/go/optional-bool",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

// TestOptionalBoolFieldTypes pins the Go field of each optional boolean
// shape: *bool without a default, a bool that always serializes with a true
// default, and an omitempty bool with a false default.
func TestOptionalBoolFieldTypes(t *testing.T) {
	output := retryOutput(t)
	var fields []FieldInfo
	for _, typeInfo := range output.Types {
		if typeInfo.Name == "Retry" {
			fields = typeInfo.Fields
		}
	}
	want := map[string][2]string{
		"respect": {`*bool`, "`json:\"respect,omitempty\"`"},
		"enabled": {`bool`, "`json:\"enabled\"`"},
		"quiet":   {`bool`, "`json:\"quiet,omitempty\"`"},
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(written)
	for _, field := range fields {
		shape := want[field.Name]
		line := regexp.MustCompile(`(?m)^\s*` + field.GoName + `\s+` + regexp.QuoteMeta(shape[0]) + `\s+` + regexp.QuoteMeta(shape[1]) + `$`)
		if !line.MatchString(source) {
			t.Errorf("types.go lacks %s %s %s", field.GoName, shape[0], shape[1])
		}
	}
	if len(fields) != len(want) {
		t.Fatalf("Retry has %d fields, want %d", len(fields), len(want))
	}
}

// TestOptionalBoolKeepsExplicitFalse: an explicit false survives marshaling
// in every optional boolean shape, and the input twin converts to the same
// wire value. omitempty on a plain bool dropped the false, so a reader that
// takes an absent key as unset, or as a true default, read the wrong value.
func TestOptionalBoolKeepsExplicitFalse(t *testing.T) {
	output := retryOutput(t)
	code := `package types

import (
	"encoding/json"
	"testing"
)

func wire(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestOptionalBool(t *testing.T) {
	for payload, want := range map[string]string{
		// quiet=false equals its default, so leaving it out loses nothing.
		` + "`" + `{"respect":false,"enabled":false,"quiet":false}` + "`" + `: ` + "`" + `{"respect":false,"enabled":false}` + "`" + `,
		` + "`" + `{"respect":true,"enabled":true,"quiet":true}` + "`" + `:    ` + "`" + `{"respect":true,"enabled":true,"quiet":true}` + "`" + `,
		// An absent respect stays absent; an absent enabled decodes to its default.
		` + "`" + `{}` + "`" + `: ` + "`" + `{"enabled":true}` + "`" + `,
	} {
		var output Retry
		if err := json.Unmarshal([]byte(payload), &output); err != nil {
			t.Fatal(err)
		}
		if got := wire(t, &output); got != want {
			t.Fatalf("output %s marshaled as %s, want %s", payload, got, want)
		}
		var input RetryInput
		if err := json.Unmarshal([]byte(payload), &input); err != nil {
			t.Fatal(err)
		}
		if got := wire(t, input.ToRetry()); got != want {
			t.Fatalf("input %s converted to %s, want %s", payload, got, want)
		}
	}
	var absent Retry
	if err := json.Unmarshal([]byte(` + "`" + `{}` + "`" + `), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.Respect != nil {
		t.Fatalf("absent respect decoded as %v, want nil", *absent.Respect)
	}
	var null RetryInput
	if err := json.Unmarshal([]byte(` + "`" + `{"respect":null,"enabled":null}` + "`" + `), &null); err != nil {
		t.Fatal(err)
	}
	if got := wire(t, null.ToRetry()); got != ` + "`" + `{"enabled":true}` + "`" + ` {
		t.Fatalf("null input converted to %s, want {\"enabled\":true}", got)
	}
}
`
	runGeneratedModuleTest(t, output, "optional_bool", code)
}
