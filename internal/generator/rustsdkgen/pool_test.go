package rustsdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// poolService is the API that serves fixture-pool-db's Account's behavior
// operations, those of the Pool and Budget it implements, and runs its
// behavior worker (D104).
const poolService = "fixture-pool-api"

// TestPoolSDKGolden pins every file of the Rust SDK for fixture-pool-api,
// its tool documents included: the operations of the Pool Account
// implements are methods of a struct of their own, the field of the
// namespace named for its member, account.pool.mark_out(id, input, None),
// and pick, an operation of the table as a whole, takes no key,
// account.pool.pick(input, None), at /api/account/pool/pick (D104); their
// tools are named account.pool.markOut and account.pool.pick with the
// methods pool.mark_out and pool.pick. The event recheck's types reach no
// endpoint, so no tool names them. Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestPoolSDKGolden -update
func TestPoolSDKGolden(t *testing.T) {
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
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", poolService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(poolService), naming.Default().RustTypesCrate(poolService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", poolService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", poolService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			if member.FieldName != "pool" {
				continue
			}
			for _, ep := range member.Endpoints {
				got = append(got, ns.FieldName+"."+member.FieldName+" "+member.StructName+"::"+ep.MethodName)
			}
		}
	}
	want := []string{
		"account.pool AccountPoolOperations::pick",
		"account.pool AccountPoolOperations::mark_out",
		"account.pool AccountPoolOperations::report_room",
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("Pool's members =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	source, err := os.ReadFile(filepath.Join(sdkDir, "src", "namespaces", "account.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, each := range []string{"pub async fn pick(", `"/api/account/pool/pick"`} {
		if !strings.Contains(string(source), each) {
			t.Errorf("src/namespaces/account.rs lacks %q", each)
		}
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name+" "+tool.MethodName)
	}
	for _, each := range []string{"account.pool.markOut pool.mark_out", "account.pool.reportRoom pool.report_room", "account.pool.pick pool.pick"} {
		if !strings.Contains(strings.Join(names, "; "), each) {
			t.Errorf("tools = %v, want %s among them", names, each)
		}
	}
	if strings.Contains(strings.Join(names, "; "), "RecheckEvent") || len(names) != 12 {
		t.Errorf("tools = %v, want one per operation, 12", names)
	}
}
