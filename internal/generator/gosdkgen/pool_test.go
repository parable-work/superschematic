package gosdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// poolService is the API that serves fixture-pool-db's Account's behavior
// operations, those of the Pool and Budget it implements, and runs its
// behavior worker (D104).
const poolService = "fixture-pool-api"

// loadPoolAPI loads fixture-pool-api and its database, and generates its
// API output.
func loadPoolAPI(t *testing.T) (*ir.Schema, *ir.Schema, *apigen.APIOutput) {
	t.Helper()
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(poolService), load("fixture-pool-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   poolService,
		ModulePath:   "example.com/schemas/api/" + poolService,
		TypesModule:  "example.com/schemas/types/go/" + poolService,
		Dependencies: map[string]*ir.Schema{"fixture-pool-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, apiOutput
}

// TestPoolSDKGolden pins every file of the Go SDK for fixture-pool-api:
// the operations of the Pool Account implements are methods of a struct of
// their own, the field of the namespace named for its member
// (AccountNamespace.Pool.MarkOut(ctx, id, input)), and pick, an operation
// of the table as a whole, takes no key
// (AccountNamespace.Pool.Pick(ctx, input)), at /api/account/pool/pick
// (D104). The event recheck's types reach no endpoint, so the SDK names
// none of them. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestPoolSDKGolden -update
func TestPoolSDKGolden(t *testing.T) {
	_, _, apiOutput := loadPoolAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", poolService)
	typesDir := filepath.Join(root, "types", "go", poolService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+poolService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", poolService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			if member.FieldName != "Pool" {
				continue
			}
			for _, ep := range member.Endpoints {
				got = append(got, ns.StructName+"."+member.FieldName+" *"+member.StructName+"."+ep.MethodName)
			}
		}
	}
	want := []string{
		"AccountNamespace.Pool *AccountPoolOperations.Pick",
		"AccountNamespace.Pool *AccountPoolOperations.MarkOut",
		"AccountNamespace.Pool *AccountPoolOperations.ReportRoom",
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("Pool's members =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	source, err := os.ReadFile(filepath.Join(sdkDir, "namespaces", "account.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, each := range []string{"func (n *AccountPoolOperations) Pick(\n\tctx context.Context,\n\tinput types.AccountPoolPickInput,\n)", `path := "/api/account/pool/pick"`} {
		if !strings.Contains(string(source), each) {
			t.Errorf("namespaces/account.go lacks %q", each)
		}
	}
	err = filepath.WalkDir(sdkDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range []string{"AccountPoolRecheckEvent", "AccountPoolRecheckPayload"} {
			if strings.Contains(string(data), name) {
				t.Errorf("%s names %s, which no endpoint references", strings.TrimPrefix(path, sdkDir), name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPoolSDKBuilds builds and vets the Go SDK of fixture-pool-api over the
// types modules of the API and its database, whose API types hold the event
// recheck's and its payload's, which a Workers method takes.
func TestPoolSDKBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, db, apiOutput := loadPoolAPI(t)
	paths := testpaths.Local(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{"fixture-pool-db", db, nil},
		{poolService, schema, map[string]*ir.Schema{"fixture-pool-db": db}},
	} {
		modules := map[string]string{}
		for dep := range tc.deps {
			modules[dep] = "example.com/schemas/types/go/" + dep
		}
		output, err := typegen.Generate(tc.schema, typegen.Options{
			SchemaName:        tc.name,
			ModulePath:        "example.com/schemas/types/go/" + tc.name,
			Dependencies:      tc.deps,
			DependencyModules: modules,
			Registry:          registry.New(naming.Default()),
			Clock:             nestedArraysClock,
		})
		if err != nil {
			t.Fatalf("typegen.Generate %s: %v", tc.name, err)
		}
		dir := filepath.Join(root, "types", "go", tc.name)
		if err := typegen.SetReplacePaths(output, paths, dir); err != nil {
			t.Fatal(err)
		}
		if err := typegen.WriteTypes(output, dir); err != nil {
			t.Fatal(err)
		}
		if tc.name == poolService {
			types, err := os.ReadFile(filepath.Join(dir, "types.go"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"type AccountPoolRecheckEvent struct", "type AccountPoolRecheckPayload struct"} {
				if !strings.Contains(string(types), want) {
					t.Errorf("the API's types lack %q", want)
				}
			}
		}
	}
	sdkDir := filepath.Join(root, "sdk", "go", poolService)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+poolService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "go", poolService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = sdkDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated SDK: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
