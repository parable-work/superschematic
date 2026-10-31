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
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// workflowService is the API whose @behaviorOperations sets the loader
// fills from fixture-workflow-db's tables (D103).
const workflowService = "fixture-workflow-api"

// loadWorkflowAPI loads fixture-workflow-api and its database, and
// generates its API output.
func loadWorkflowAPI(t *testing.T) (*ir.Schema, *ir.Schema, *apigen.APIOutput) {
	t.Helper()
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(workflowService), load("fixture-workflow-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   workflowService,
		ModulePath:   "example.com/schemas/api/" + workflowService,
		TypesModule:  "example.com/schemas/types/go/" + workflowService,
		Dependencies: map[string]*ir.Schema{"fixture-workflow-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, apiOutput
}

// TestBehaviorOperationsSDKGolden pins every file of the Go SDK for
// fixture-workflow-api: each behavior use's operations are methods of a
// struct of their own, a field of the namespace named for the use,
// OrderNamespace.Status.Transition, and the namespace has no flat method
// for them. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestBehaviorOperationsSDKGolden -update
func TestBehaviorOperationsSDKGolden(t *testing.T) {
	_, _, apiOutput := loadWorkflowAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", workflowService)
	typesDir := filepath.Join(root, "types", "go", workflowService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+workflowService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", workflowService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			if strings.HasSuffix(ep.MethodName, "Transition") {
				t.Errorf("%s holds the flat method %s", ns.StructName, ep.MethodName)
			}
		}
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.StructName+"."+member.FieldName+" *"+member.StructName+"."+ep.MethodName)
			}
		}
	}
	want := "OrderNamespace.Status *OrderStatusOperations.Transition; ShipmentNamespace.PaymentStatus *ShipmentPaymentStatusOperations.Transition; ShipmentNamespace.Status *ShipmentStatusOperations.Transition"
	if strings.Join(got, "; ") != want {
		t.Errorf("members = %v, want %s", got, want)
	}
}

// TestBehaviorOperationsSDKBuildsAndCalls builds the Go SDK of
// fixture-workflow-api over the types modules of the API and its database,
// and runs a test in it against an httptest server: Status.Transition posts
// the input to the route under the key and returns the result, typed.
func TestBehaviorOperationsSDKBuildsAndCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, db, apiOutput := loadWorkflowAPI(t)
	paths := testpaths.Local(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{"fixture-workflow-db", db, nil},
		{workflowService, schema, map[string]*ir.Schema{"fixture-workflow-db": db}},
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
	}
	sdkDir := filepath.Join(root, "sdk", "go", workflowService)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+workflowService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "go", workflowService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sdkDir, "behavior_operations_test.go"), []byte(behaviorOperationsSDKTest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"build", "./..."},
		{"vet", "./..."},
		{"test", "-count=1", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = sdkDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated SDK: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// behaviorOperationsSDKTest runs in the generated SDK module against an
// httptest server that answers a transition's result in the envelope.
const behaviorOperationsSDKTest = `package sdk_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-workflow-api"
	types "example.com/schemas/types/go/fixture-workflow-api"
)

const id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"

func TestStatusTransition(t *testing.T) {
	var method, path string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		data := ` + "`" + `{"from":"draft","to":"paid"}` + "`" + `
		if r.URL.Path != "/api/order/"+id+"/status/transition" {
			data = ` + "`" + `{"from":"paid","to":"refunded"}` + "`" + `
		}
		_, _ = io.WriteString(w, ` + "`" + `{"data":` + "`" + `+data+` + "`" + `,"meta":{"requestId":"req-1"}}` + "`" + `)
	}))
	defer server.Close()
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.OrderNamespace.Status.Transition(context.Background(), id, types.OrderStatusTransitionInput{To: types.OrderStatusState_Paid})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/api/order/"+id+"/status/transition" || body["to"] != "paid" {
		t.Errorf("request = %s %s %v", method, path, body)
	}
	if result.From != types.OrderStatusState_Draft || result.To != types.OrderStatusState_Paid {
		t.Errorf("result = %+v", result)
	}
	if _, err := client.ShipmentNamespace.PaymentStatus.Transition(context.Background(), id, types.ShipmentPaymentStatusTransitionInput{To: types.ShipmentPaymentStatusState_Refunded}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/shipment/"+id+"/payment-status/transition" {
		t.Errorf("paymentStatus.transition posted to %s", path)
	}
}
`
