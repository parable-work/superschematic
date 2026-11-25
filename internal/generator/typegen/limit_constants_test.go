package typegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// limitSchemas loads fixture-db and fixture-api, which imports Tenant from
// it, and gives Tenant's fields Validate<> limits: name's maxLength is
// looser than its scalar's and its minLength tighter, email's is the only
// one, and seatCount has a fractional maximum.
func limitSchemas(t *testing.T) (db, api *ir.Schema) {
	t.Helper()
	db, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	api, err = loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatal(err)
	}
	intp := func(v int) *int { return &v }
	floatp := func(v float64) *float64 { return &v }
	for _, field := range db.Types["Tenant"].Fields {
		switch field.Name {
		case "name":
			field.ValidateMaxLength, field.ValidateMinLength = intp(100000), intp(3)
		case "email":
			field.ValidateMaxLength = intp(64)
		case "seatCount":
			field.ValidateMin, field.ValidateMax = floatp(1), floatp(2.5)
		}
	}
	// fixture-api names Tenant only in @source; an import of it gives the
	// alias.
	api.Imports = append(api.Imports, ir.Import{Package: "@schemas/fixture-db", Types: []string{"Tenant"}})
	return db, api
}

func generateLimits(t *testing.T, schema *ir.Schema, name string, deps map[string]*ir.Schema, n naming.Naming) *ModuleOutput {
	t.Helper()
	depModules := map[string]string{}
	for dep := range deps {
		depModules[dep] = "example.com/schemas/types/go/" + dep
	}
	output, err := Generate(schema, Options{
		SchemaName:        name,
		ModulePath:        "example.com/schemas/types/go/" + name,
		Dependencies:      deps,
		DependencyModules: depModules,
		Naming:            n,
		Clock:             codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

// TestLimitConstantsAreOffByDefault: without go.limit_constants the
// output has no limit constant.
func TestLimitConstantsAreOffByDefault(t *testing.T) {
	db, api := limitSchemas(t)
	for _, output := range []*ModuleOutput{
		generateLimits(t, db, "fixture-db", nil, naming.Naming{}),
		generateLimits(t, api, "fixture-api", map[string]*ir.Schema{"fixture-db": db}, naming.Naming{}),
	} {
		for _, typeInfo := range output.Types {
			if typeInfo.HasLimitConsts() {
				t.Errorf("%s has limit constants with the option off", typeInfo.Name)
			}
		}
		for _, imported := range output.ImportedTypes {
			if len(imported.LimitConsts) > 0 {
				t.Errorf("%s aliases limit constants with the option off", imported.Name)
			}
		}
	}
}

// TestLimitConstantsHoldTheBoundsValidateChecks: with
// go.limit_constants = "on", each declared limit is a constant at its
// effective bound, the importing module aliases it, and both modules
// compile. A test in the owning module checks each constant against
// Validate: a value at the bound passes and one past it fails.
func TestLimitConstantsHoldTheBoundsValidateChecks(t *testing.T) {
	db, api := limitSchemas(t)
	scalar := db.Scalars["Identity.Name"]
	if scalar == nil || scalar.MaxLength == 0 {
		t.Fatalf("Identity.Name has no maxLength to merge with: %+v", scalar)
	}
	on := naming.Naming{Go: naming.GoConfig{LimitConstants: naming.GoOn}}
	dbOut := generateLimits(t, db, "fixture-db", nil, on)
	apiOut := generateLimits(t, api, "fixture-api", map[string]*ir.Schema{"fixture-db": db}, on)

	want := map[string]string{
		"TenantNameMinLength":  "3",
		"TenantNameMaxLength":  fmt.Sprint(scalar.MaxLength),
		"TenantEmailMaxLength": "64",
		"TenantSeatCountMin":   "1",
		"TenantSeatCountMax":   "2.5",
	}
	got := map[string]string{}
	for _, typeInfo := range dbOut.Types {
		for _, field := range typeInfo.Fields {
			for _, limitConst := range field.LimitConsts {
				got[limitConst.Name] = limitConst.Literal
			}
		}
	}
	for name, literal := range want {
		if got[name] != literal {
			t.Errorf("%s = %q, want %q (all: %v)", name, got[name], literal, got)
		}
	}
	var aliased []string
	for _, imported := range apiOut.ImportedTypes {
		if imported.Name == "Tenant" {
			aliased = imported.LimitConsts
		}
	}
	if strings.Join(aliased, " ") != "TenantNameMinLength TenantNameMaxLength TenantEmailMaxLength TenantSeatCountMin TenantSeatCountMax" {
		t.Errorf("fixture-api aliases %v", aliased)
	}

	if testing.Short() {
		t.Skip("skipping generated-module run in -short mode")
	}
	paths := testpaths.Local(t)
	root := filepath.Join(t.TempDir(), "types", "go")
	for name, output := range map[string]*ModuleOutput{"fixture-db": dbOut, "fixture-api": apiOut} {
		dir := filepath.Join(root, name)
		if err := SetReplacePaths(output, paths, dir); err != nil {
			t.Fatal(err)
		}
		if err := WriteTypes(output, dir); err != nil {
			t.Fatal(err)
		}
	}
	tests := map[string]string{"fixture-db": limitsOwnerTest, "fixture-api": limitsAliasTest}
	for _, name := range []string{"fixture-db", "fixture-api"} {
		dir := filepath.Join(root, name)
		code := strings.Replace(tests[name], "PACKAGE", map[string]string{"fixture-db": dbOut.PackageName, "fixture-api": apiOut.PackageName}[name], 1)
		if err := os.WriteFile(filepath.Join(dir, "limits_test.go"), []byte(code), 0o644); err != nil {
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
			t.Fatalf("generated %s module: %v\n%s", name, err, out)
		}
	}
}

// limitsOwnerTest runs in the module that owns Tenant.
const limitsOwnerTest = `package PACKAGE

import (
	"strings"
	"testing"
)

func nameErrs(name string) bool {
	tenant := Tenant{Name: IdentityName(name)}
	return tenant.Validate()["name"] != nil
}

func TestTheConstantsAreTheBounds(t *testing.T) {
	if nameErrs(strings.Repeat("a", TenantNameMinLength)) || !nameErrs(strings.Repeat("a", TenantNameMinLength-1)) {
		t.Errorf("TenantNameMinLength %d is not the bound Validate checks", TenantNameMinLength)
	}
	if nameErrs(strings.Repeat("a", TenantNameMaxLength)) || !nameErrs(strings.Repeat("a", TenantNameMaxLength+1)) {
		t.Errorf("TenantNameMaxLength %d is not the bound Validate checks", TenantNameMaxLength)
	}
	var length int = TenantEmailMaxLength
	var count float64 = TenantSeatCountMax
	if length != 64 || count != 2.5 || TenantSeatCountMin != 1 {
		t.Errorf("email %d, seat count %v to %v", length, TenantSeatCountMin, count)
	}
}
`

// limitsAliasTest runs in the module that imports Tenant.
const limitsAliasTest = `package PACKAGE

import "testing"

func TestTheAliasesAreTheOwnersConstants(t *testing.T) {
	if TenantEmailMaxLength != 64 || TenantSeatCountMax != 2.5 || TenantNameMinLength != 3 {
		t.Errorf("aliases %d %v %d", TenantEmailMaxLength, TenantSeatCountMax, TenantNameMinLength)
	}
}
`
