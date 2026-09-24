package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// enumsWithoutScalarsSchema has an enum and a type that uses it, and no
// scalar: types.go needs scalars.go's ValidationErrors aliases, and enums.go
// needs ValidationError.
func enumsWithoutScalarsSchema() *ir.Schema {
	schema := ir.NewSchema("enums-only", ir.SchemaKindGeneral)
	schema.Enums["Status"] = &ir.EnumDef{Name: "Status", Values: []ir.EnumValueDef{
		{Name: "Open", SerializedAs: "open"}, {Name: "Closed", SerializedAs: "closed"},
	}}
	schema.Types["Ticket"] = &ir.TypeDef{Name: "Ticket", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "status", TypeRef: ir.TypeRef{Name: "Status"}, Required: true},
		{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	return schema
}

// TestEnumsAndTypesWithoutScalarsDeclareValidationErrorOnce: a module with
// enums and types but no scalars writes scalars.go for the aliases types.go
// uses, so enums.go must not declare ValidationError as well. The module
// builds.
func TestEnumsAndTypesWithoutScalarsDeclareValidationErrorOnce(t *testing.T) {
	output, err := Generate(enumsWithoutScalarsSchema(), Options{SchemaName: "enums-only", ModulePath: "example.com/schemas/types/go/enums-only"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "enums-only")
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	declaration := regexp.MustCompile(`(?m)^type ValidationError = `)
	declared := 0
	for _, name := range []string{"scalars.go", "enums.go", "types.go"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		declared += len(declaration.FindAll(data, -1))
	}
	if declared != 1 {
		t.Fatalf("ValidationError is declared %d times across scalars.go, enums.go and types.go, want 1", declared)
	}

	if testing.Short() {
		t.Skip("skipping the generated-module build in -short mode")
	}
	paths := testpaths.Local(t)
	buildDir := filepath.Join(t.TempDir(), "enums-only")
	if err := SetReplacePaths(output, paths, buildDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, buildDir); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = buildDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	vet := exec.Command("go", "vet", "./...")
	vet.Dir = buildDir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("generated module does not build: %v\n%s", err, out)
	}
}
