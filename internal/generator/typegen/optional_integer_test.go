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

// countFields declares an optional integer scalar without a default
// (budget), one with a minimum (floor) and one with a default (batch).
func countFields() []*ir.FieldDef {
	one, fifty := 1.0, "50"
	return []*ir.FieldDef{
		{Name: "budget", TypeRef: ir.TypeRef{Name: "Generic.Int64"}},
		{Name: "floor", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, ValidateMin: &one},
		{Name: "batch", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Default: &fifty},
	}
}

func countsOutput(t *testing.T) *ModuleOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Types["Counts"] = &ir.TypeDef{Name: "Counts", Role: ir.RoleEmbeddedStruct, JsonField: true, Fields: countFields()}
	schema.Types["CountsInput"] = &ir.TypeDef{Name: "CountsInput", Role: ir.RoleAPIInput, Fields: countFields()}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-general",
		ModulePath: "example.com/schemas/types/go/optional-integer",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

// TestOptionalIntegerFieldTypes pins the Go field of each optional integer
// scalar shape: a pointer without a default and the value type with one.
func TestOptionalIntegerFieldTypes(t *testing.T) {
	dir := t.TempDir()
	if err := WriteTypes(countsOutput(t), dir); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	for goName, shape := range map[string][2]string{
		"Budget": {`*GenericInt64`, "`json:\"budget,omitempty\"`"},
		"Floor":  {`*GenericInt64`, "`json:\"floor,omitempty\"`"},
		"Batch":  {`GenericInt64`, "`json:\"batch,omitempty\"`"},
	} {
		line := regexp.MustCompile(`(?m)^\s*` + goName + `\s+` + regexp.QuoteMeta(shape[0]) + `\s+` + regexp.QuoteMeta(shape[1]) + `$`)
		if !line.Match(written) {
			t.Errorf("types.go lacks %s %s %s", goName, shape[0], shape[1])
		}
	}
}

// TestOptionalIntegerKeepsExplicitZero: an optional integer scalar without a
// default is a pointer in a Go output type, so an explicit 0 survives a read
// and a write, and an absent value stays nil. omitempty on the value type
// dropped the 0, so a reader that takes absence as "inherit" or as a
// non-zero default acted on the wrong value. A 0 below a declared minimum is
// validated instead of being skipped as unset, and a fractional value is
// refused at decode.
func TestOptionalIntegerKeepsExplicitZero(t *testing.T) {
	output := countsOutput(t)
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

func TestOptionalInteger(t *testing.T) {
	for payload, want := range map[string]string{
		// batch is always set: its default is not what this test pins.
		` + "`" + `{"budget":0,"floor":1,"batch":7}` + "`" + `: ` + "`" + `{"budget":0,"floor":1,"batch":7}` + "`" + `,
		` + "`" + `{"budget":3,"batch":7}` + "`" + `:           ` + "`" + `{"budget":3,"batch":7}` + "`" + `,
		// An absent budget stays absent.
		` + "`" + `{"batch":7}` + "`" + `: ` + "`" + `{"batch":7}` + "`" + `,
	} {
		var output Counts
		if err := json.Unmarshal([]byte(payload), &output); err != nil {
			t.Fatal(err)
		}
		if got := wire(t, &output); got != want {
			t.Fatalf("output %s marshaled as %s, want %s", payload, got, want)
		}
		var input CountsInput
		if err := json.Unmarshal([]byte(payload), &input); err != nil {
			t.Fatal(err)
		}
		if got := wire(t, input.ToCounts()); got != want {
			t.Fatalf("input %s converted to %s, want %s", payload, got, want)
		}
	}
	var absent Counts
	if err := json.Unmarshal([]byte(` + "`" + `{}` + "`" + `), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.Budget != nil {
		t.Fatalf("absent budget decoded as %v, want nil", *absent.Budget)
	}
	if errs := absent.Validate(); errs["floor"] != nil {
		t.Fatalf("absent floor failed validation: %v", errs)
	}
	zero := GenericInt64(0)
	if errs := (&Counts{Floor: &zero}).Validate(); errs["floor"] == nil {
		t.Fatal("floor 0 passed validation against its minimum of 1")
	}
	var fractional Counts
	if err := json.Unmarshal([]byte(` + "`" + `{"budget":2.5}` + "`" + `), &fractional); err == nil {
		t.Fatalf("budget 2.5 decoded as %v, want an error", *fractional.Budget)
	}
}
`
	runGeneratedModuleTest(t, output, "optional_integer", code)
}
