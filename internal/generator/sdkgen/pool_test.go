package sdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// poolAPI is the API that serves fixture-pool-db's Account's behavior
// operations, those of the Pool and Budget it implements, and runs its
// behavior worker (D104).
const poolAPI = "fixture-pool-api"

// poolAPIGolden is fixture-pool-api's SDK, whose types package depends on
// fixture-pool-db's.
func poolAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadBehaviorAPI(t, poolAPI, "fixture-pool-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: poolAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-pool-db": db}},
			{name: "fixture-pool-db", schema: db},
		},
	}
}

// TestPoolSDKGolden pins every file of the TypeScript SDK for
// fixture-pool-api, its tool documents included: the operations of the Pool
// Account implements sit in a class of their own under its member, markOut
// and reportRoom taking the row's key, sdk.account.pool.markOut(id, input),
// and pick, an operation of the table as a whole, none,
// sdk.account.pool.pick(input), at /api/account/pool/pick; their tools are
// account.pool.markOut and its siblings. The event recheck's types reach no
// endpoint, so neither the SDK nor a tool names them.
// TestEveryGoldenSDKTypeChecks type-checks it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestPoolSDKGolden -update
func TestPoolSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadBehaviorAPI(t, poolAPI, "fixture-pool-db")
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", poolAPI))

	var members []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			if member.ClassName != "AccountPoolOperations" {
				continue
			}
			for _, ep := range member.Endpoints {
				members = append(members, ns.Name+" "+member.ClassName+"."+ep.MethodPath()+" "+ep.ToolName)
			}
		}
	}
	// A namespace's endpoints are in their routes' order, the table's
	// account/pool/pick before the rows' account/{id}/....
	want := "account AccountPoolOperations.pool.pick account.pool.pick; " +
		"account AccountPoolOperations.pool.markOut account.pool.markOut; " +
		"account AccountPoolOperations.pool.reportRoom account.pool.reportRoom"
	if got := strings.Join(members, "; "); got != want {
		t.Errorf("Pool's members =\n%s\nwant\n%s", got, want)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "account.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, each := range []string{
		"public readonly pool: AccountPoolOperations;", "export class AccountPoolOperations {",
		"public async markOut(", "`/api/account/${encodeURIComponent(id)}/pool/mark-out`",
		"public async pick(", "`/api/account/pool/pick`",
	} {
		if !strings.Contains(string(source), each) {
			t.Errorf("namespaces/account.ts lacks %q", each)
		}
	}
	if strings.Contains(string(source), "${encodeURIComponent(id)}/pool/pick") {
		t.Errorf("namespaces/account.ts routes pick on a row: it runs on the table as a whole")
	}
	err = filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range []string{"AccountPoolRecheckEvent", "AccountPoolRecheckPayload", "AccountRechecks"} {
			if strings.Contains(string(data), name) {
				t.Errorf("%s names %s, which no endpoint references", strings.TrimPrefix(path, outDir), name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
