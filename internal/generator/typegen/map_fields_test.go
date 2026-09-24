package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// runGeneratedModuleTest writes output as a module wired against the real
// superscalar binding, adds code as a test file inside it, and runs it.
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
