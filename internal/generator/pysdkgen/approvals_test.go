package pysdkgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// approvalsService is the API that serves fixture-approvals-db's Task's
// behavior operations, those of the Approvals it implements among them
// (D104); Constants and StateRules, which it implements too, have none.
const approvalsService = "fixture-approvals-api"

// TestApprovalsSDKGolden pins every file of the Python SDK for
// fixture-approvals-api: the operations of the Approvals Task implements
// are methods of a class of their own, the property approvals of the
// namespace, sdk.task.approvals.sign_off, whose input names the gated state
// and the version its signer read. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestApprovalsSDKGolden -update
func TestApprovalsSDKGolden(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(approvalsService), load("fixture-approvals-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   approvalsService,
		Dependencies: map[string]*ir.Schema{"fixture-approvals-db": db},
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
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", approvalsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.AccessorName+"."+member.FieldName+" "+member.ClassName+"."+ep.MethodName)
			}
		}
	}
	want := "task.approvals TaskApprovalsOperations.list_sign_offs; task.approvals TaskApprovalsOperations.sign_off; " +
		"task.approvals TaskApprovalsOperations.withdraw_sign_off; task.status TaskStatusOperations.transition"
	if strings.Join(got, "; ") != want {
		t.Errorf("members = %v, want %s", got, want)
	}
}
