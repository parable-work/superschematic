package pysdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// poolService is the API that serves fixture-pool-db's Account's behavior
// operations, those of the Pool and Budget it implements, and runs its
// behavior worker (D104).
const poolService = "fixture-pool-api"

// TestPoolSDKGolden pins every file of the Python SDK for fixture-pool-api:
// the operations of the Pool Account implements are methods of a class of
// their own, the property of the namespace named for its member,
// sdk.account.pool.mark_out(id, input), and pick, an operation of the table
// as a whole, takes no key, sdk.account.pool.pick(input), at
// /api/account/pool/pick (D104). The event recheck's types reach no
// endpoint, so the SDK names none of them. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestPoolSDKGolden -update
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
		Dependencies: map[string]*ir.Schema{"fixture-pool-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", poolService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			if member.FieldName != "pool" {
				continue
			}
			for _, ep := range member.Endpoints {
				got = append(got, ns.AccessorName+"."+member.FieldName+" "+member.ClassName+"."+ep.MethodName)
			}
		}
	}
	want := []string{
		"account.pool AccountPoolOperations.pick",
		"account.pool AccountPoolOperations.mark_out",
		"account.pool AccountPoolOperations.report_room",
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("Pool's members =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	err = filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range []string{"AccountPoolRecheckEvent", "AccountPoolRecheckPayload"} {
			if strings.Contains(string(data), name) {
				t.Errorf("%s names %s, which no endpoint references", strings.TrimPrefix(path, outDir), name)
			}
		}
		if strings.HasSuffix(path, filepath.Join("namespaces", "account.py")) && !strings.Contains(string(data), `"/api/account/pool/pick"`) {
			t.Errorf("namespaces/account.py routes no pick at /api/account/pool/pick")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
