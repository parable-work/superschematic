package ormgen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// optionalZeroFields are the Tenant columns go.optional_zero = "keep"
// makes pointers: a number and a boolean without a default, a number, a
// string and a boolean whose default is not the zero value.
func optionalZeroFields() []*ir.FieldDef {
	five, bearer, yes := "5", "Bearer", "true"
	return []*ir.FieldDef{
		{Name: "threshold", TypeRef: ir.TypeRef{Name: "number"}},
		{Name: "retries", TypeRef: ir.TypeRef{Name: "number"}, Default: &five},
		{Name: "tokenPrefix", TypeRef: ir.TypeRef{Name: "string"}, Default: &bearer},
		{Name: "respect", TypeRef: ir.TypeRef{Name: "boolean"}},
		{Name: "enabled", TypeRef: ir.TypeRef{Name: "boolean"}, Default: &yes},
	}
}

// TestOptionalZeroFieldsMatchTheTypes: with go.optional_zero = "keep" the
// ORM reads each such column into the pointer the types module declares,
// and writes it from it: Field.IsNullableBool and IsNullableScalar follow
// codegen.GoOptionalShapes, and a pointer string is not scanned through
// sql.NullString. The types and the ORM of fixture-db, with those columns
// and the compile-coverage additions, build and vet together.
func TestOptionalZeroFieldsMatchTheTypes(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	extendFixtureForCompileCoverage(schema)
	schema.Types["Tenant"].Fields = append(schema.Types["Tenant"].Fields, optionalZeroFields()...)
	keep := naming.Naming{Go: naming.GoConfig{OptionalZero: naming.GoOptionalKeep}}
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/fixture-db"

	ormOutput, err := Generate(schema, Options{
		SchemaName:  "fixture-db",
		ModulePath:  "example.com/schemas/orm/fixture-db",
		TypesModule: typesModule,
		Naming:      keep,
		Clock:       fixedClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range ormOutput.Repositories {
		if repo.TypeName != "Tenant" {
			continue
		}
		for _, field := range repo.Fields {
			want, ok := map[string][2]bool{
				"threshold": {true, false}, "retries": {true, false}, "tokenPrefix": {true, false},
				"respect": {true, false}, "enabled": {true, false},
				// Required with a default: a value under either rule.
				"seatCount": {false, false}, "isActive": {false, false},
			}[field.Name]
			if ok && (field.IsNullableBool != want[0] || field.IsNullableScalar != want[1]) {
				t.Errorf("%s: IsNullableBool %t, IsNullableScalar %t; want %t, %t", field.Name, field.IsNullableBool, field.IsNullableScalar, want[0], want[1])
			}
		}
	}

	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	tempRoot := t.TempDir()
	typesDir := filepath.Join(tempRoot, "types", "go", "fixture-db")
	ormDir := filepath.Join(tempRoot, "orm", "fixture-db")
	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: "fixture-db",
		ModulePath: typesModule,
		Naming:     keep,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := SetReplacePaths(ormOutput, paths, ormDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteORM(ormOutput, ormDir); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{typesDir, ormDir} {
		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = dir
		if out, err := tidy.CombinedOutput(); err != nil {
			t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
		}
		for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}} {
			run := exec.Command("go", args...)
			run.Dir = dir
			if out, err := run.CombinedOutput(); err != nil {
				t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
			}
		}
	}
}
