package typegen

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// keepFields declares each optional shape go.optional_zero = "keep"
// changes, and two it leaves alone: a number defaulting to 0 (scale) and a
// string without a default (label).
func keepFields() []*ir.FieldDef {
	five, zero, bearer, yes, fifty := "5", "0", "Bearer", "true", "50"
	return []*ir.FieldDef{
		{Name: "threshold", TypeRef: ir.TypeRef{Name: "number"}},
		{Name: "retries", TypeRef: ir.TypeRef{Name: "number"}, Default: &five},
		{Name: "scale", TypeRef: ir.TypeRef{Name: "number"}, Default: &zero},
		{Name: "label", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "prefix", TypeRef: ir.TypeRef{Name: "string"}, Default: &bearer},
		{Name: "enabled", TypeRef: ir.TypeRef{Name: "boolean"}, Default: &yes},
		{Name: "budget", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Default: &fifty},
		{Name: "count", TypeRef: ir.TypeRef{Name: "Generic.Int64"}},
	}
}

func keepOutput(t *testing.T, n naming.Naming) *ModuleOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Types["Limits"] = &ir.TypeDef{Name: "Limits", Role: ir.RoleEmbeddedStruct, JsonField: true, Fields: keepFields()}
	schema.Types["LimitsInput"] = &ir.TypeDef{Name: "LimitsInput", Role: ir.RoleAPIInput, Fields: keepFields()}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-general",
		ModulePath: "example.com/schemas/types/go/optional-zero",
		Naming:     n,
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

// TestOptionalZeroFieldTypes pins the Go field of each shape under both
// values of go.optional_zero. "omit" is the core's rule and does not
// change.
func TestOptionalZeroFieldTypes(t *testing.T) {
	for value, shapes := range map[string]map[string]string{
		naming.GoOptionalOmit: {
			"Threshold": "float64", "Retries": "float64", "Scale": "float64", "Label": "string",
			"Prefix": "string", "Enabled": "bool", "Budget": "GenericInt64", "Count": "*GenericInt64",
		},
		naming.GoOptionalKeep: {
			"Threshold": "*float64", "Retries": "*float64", "Scale": "float64", "Label": "string",
			"Prefix": "*string", "Enabled": "*bool", "Budget": "*GenericInt64", "Count": "*GenericInt64",
		},
	} {
		dir := t.TempDir()
		if err := WriteTypes(keepOutput(t, naming.Naming{Go: naming.GoConfig{OptionalZero: value}}), dir); err != nil {
			t.Fatal(err)
		}
		written, err := os.ReadFile(filepath.Join(dir, "types.go"))
		if err != nil {
			t.Fatal(err)
		}
		structBody := regexp.MustCompile(`(?s)type Limits struct \{(.*?)\n\}`).FindSubmatch(written)
		if structBody == nil {
			t.Fatalf("%s: types.go has no Limits struct", value)
		}
		for goName, goType := range shapes {
			line := regexp.MustCompile(`(?m)^\s*` + goName + `\s+` + regexp.QuoteMeta(goType) + `\s+` + "`")
			if !line.Match(structBody[1]) {
				t.Errorf("optional_zero = %q: Limits.%s is not %s:\n%s", value, goName, goType, structBody[1])
			}
		}
	}
}

// TestOptionalZeroKeepsEveryStatedValue: with go.optional_zero = "keep" a
// stated 0, "" and false survive a read and a write, an absent key reads
// as the default, and an explicit null as no value, also where the field
// has a default. An input twin converts to the same wire value.
func TestOptionalZeroKeepsEveryStatedValue(t *testing.T) {
	output := keepOutput(t, naming.Naming{Go: naming.GoConfig{OptionalZero: naming.GoOptionalKeep}})
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

func TestOptionalZero(t *testing.T) {
	for payload, want := range map[string]string{
		// scale and label are values whose zero is their default, so
		// leaving out their 0 and "" loses nothing.
		` + "`" + `{"threshold":0,"retries":0,"scale":0,"label":"","prefix":"","enabled":false,"budget":0,"count":0}` + "`" + `: ` + "`" + `{"threshold":0,"retries":0,"prefix":"","enabled":false,"budget":0,"count":0}` + "`" + `,
		// An absent key reads as the default.
		` + "`" + `{}` + "`" + `: ` + "`" + `{"retries":5,"prefix":"Bearer","enabled":true,"budget":50}` + "`" + `,
		// An explicit null is no value, also on a field with a default.
		` + "`" + `{"threshold":null,"retries":null,"prefix":null,"enabled":null,"budget":null,"count":null}` + "`" + `: ` + "`" + `{}` + "`" + `,
		` + "`" + `{"threshold":2.5,"retries":3,"scale":1.5,"label":"x","prefix":"SSWS","enabled":true,"budget":7,"count":8}` + "`" + `: ` + "`" + `{"threshold":2.5,"retries":3,"scale":1.5,"label":"x","prefix":"SSWS","enabled":true,"budget":7,"count":8}` + "`" + `,
	} {
		var output Limits
		if err := json.Unmarshal([]byte(payload), &output); err != nil {
			t.Fatal(err)
		}
		if got := wire(t, &output); got != want {
			t.Fatalf("output %s marshaled as %s, want %s", payload, got, want)
		}
	}
	for payload, want := range map[string]string{
		` + "`" + `{"threshold":0,"retries":0,"prefix":"","enabled":false,"budget":0,"count":0}` + "`" + `: ` + "`" + `{"threshold":0,"retries":0,"prefix":"","enabled":false,"budget":0,"count":0}` + "`" + `,
		` + "`" + `{"threshold":2.5,"retries":3,"prefix":"SSWS","enabled":true,"budget":7,"count":8}` + "`" + `: ` + "`" + `{"threshold":2.5,"retries":3,"prefix":"SSWS","enabled":true,"budget":7,"count":8}` + "`" + `,
	} {
		var input LimitsInput
		if err := json.Unmarshal([]byte(payload), &input); err != nil {
			t.Fatal(err)
		}
		if got := wire(t, input.ToLimits()); got != want {
			t.Fatalf("input %s converted to %s, want %s", payload, got, want)
		}
	}
	if built := NewLimits(); built.Retries == nil || *built.Retries != 5 || built.Prefix == nil || *built.Prefix != "Bearer" {
		t.Fatalf("NewLimits() = %s, want the defaults", wire(t, built))
	}
}
`
	runGeneratedModuleTest(t, output, "optional_zero", code)
}
