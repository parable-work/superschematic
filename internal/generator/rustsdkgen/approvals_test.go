package rustsdkgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// approvalsService is the API that serves fixture-approvals-db's Task's
// behavior operations, those of the Approvals it implements among them
// (D104); Constants and StateRules, which it implements too, have none.
const approvalsService = "fixture-approvals-api"

// TestApprovalsSDKGolden pins every file of the Rust SDK for
// fixture-approvals-api, its tool documents included: the operations of the
// Approvals Task implements are methods of a struct of their own, the field
// approvals of the namespace, task.approvals.sign_off, whose tools are named
// task.approvals.signOff with the method approvals.sign_off. Regenerate
// with:
// go test ./internal/generator/rustsdkgen -run TestApprovalsSDKGolden -update
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
		ModulePath:   "example.com/schemas/api/" + approvalsService,
		TypesModule:  "example.com/schemas/types/go/" + approvalsService,
		Dependencies: map[string]*ir.Schema{"fixture-approvals-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", approvalsService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(approvalsService), naming.Default().RustTypesCrate(approvalsService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", approvalsService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", approvalsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.FieldName+"."+member.FieldName+" "+member.StructName+"::"+ep.MethodName)
			}
		}
	}
	want := "task.approvals TaskApprovalsOperations::list_sign_offs; task.approvals TaskApprovalsOperations::sign_off; " +
		"task.approvals TaskApprovalsOperations::withdraw_sign_off; task.status TaskStatusOperations::transition"
	if strings.Join(got, "; ") != want {
		t.Errorf("members = %v, want %s", got, want)
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name+" "+tool.MethodName)
	}
	if want := "task.approvals.listSignOffs approvals.list_sign_offs; task.approvals.signOff approvals.sign_off; task.approvals.withdrawSignOff approvals.withdraw_sign_off; task.status.transition status.transition; task-admin.reassignTask reassign_task"; strings.Join(names, "; ") != want {
		t.Errorf("tools = %v, want %s", names, want)
	}
}
