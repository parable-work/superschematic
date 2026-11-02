package sdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	ir "github.com/parable-work/superschematic/ir"
)

// workflowAPI is the API whose @behaviorOperations sets the loader fills
// from fixture-workflow-db's tables (D103). The servers run those
// operations through the behaviors' core; the SDK calls them under their
// field, sdk.order.status.transition.
const workflowAPI = "fixture-workflow-api"

// loadWorkflowAPI loads fixture-workflow-api and its database, and
// generates its API output and its types' parseable names.
func loadWorkflowAPI(t *testing.T) (*ir.Schema, *ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	return loadBehaviorAPI(t, workflowAPI, "fixture-workflow-db")
}

// loadBehaviorAPI loads the API api and its database dbName, and generates
// its API output and its types' parseable names.
func loadBehaviorAPI(t *testing.T, api, dbName string) (*ir.Schema, *ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema := loadFixtureSchema(t, api)
	db := loadFixtureSchema(t, dbName)
	deps := map[string]*ir.Schema{dbName: db}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   api,
		ModulePath:   "example.com/schemas/api/" + api,
		TypesModule:  "example.com/schemas/types/go/" + api,
		Dependencies: deps,
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{
		SchemaName:         api,
		Dependencies:       deps,
		DependencyPackages: map[string]string{dbName: naming.Default().NpmTypesPackage(dbName)},
		Clock:              nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, db, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// workflowAPIGolden is fixture-workflow-api's SDK, whose types package
// depends on fixture-workflow-db's.
func workflowAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadWorkflowAPI(t)
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: workflowAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-workflow-db": db}},
			{name: "fixture-workflow-db", schema: db},
		},
	}
}

// TestBehaviorOperationsSDKGolden pins every file of the TypeScript SDK for
// fixture-workflow-api, its tool documents included: each behavior use's
// operations sit in a class of their own under the use's member,
// sdk.order.status.transition(id, input), and its tool is
// order.status.transition. TestEveryGoldenSDKTypeChecks type-checks it.
// Regenerate with:
// go test ./internal/generator/sdkgen -run TestBehaviorOperationsSDKGolden -update
func TestBehaviorOperationsSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadWorkflowAPI(t)
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", workflowAPI))

	members := map[string][]string{}
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			if ep.Member != "" {
				t.Errorf("%s.%s is a behavior operation among the namespace's own methods", ns.Name, ep.Name)
			}
		}
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				members[ns.Name] = append(members[ns.Name], member.ClassName+"."+ep.MethodPath()+" "+ep.ToolName)
			}
		}
	}
	want := map[string][]string{
		"order":    {"OrderStatusOperations.status.transition order.status.transition"},
		"shipment": {"ShipmentPaymentStatusOperations.paymentStatus.transition shipment.paymentStatus.transition", "ShipmentStatusOperations.status.transition shipment.status.transition"},
	}
	for ns, got := range want {
		if strings.Join(members[ns], "; ") != strings.Join(got, "; ") {
			t.Errorf("namespace %s's members = %v, want %v", ns, members[ns], got)
		}
	}

	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "order.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"public readonly status: OrderStatusOperations;",
		"this.status = new OrderStatusOperations(client);",
		"export class OrderStatusOperations {",
		"public async transition(",
		"`/api/order/${encodeURIComponent(id)}/status/transition`",
		"public async getOrder(",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("namespaces/order.ts lacks %q", want)
		}
	}
	if strings.Contains(string(source), "statusTransition") {
		t.Error("namespaces/order.ts holds the flat statusTransition, which no SDK has")
	}
	tools, err := os.ReadFile(filepath.Join(outDir, "tools", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"export interface OrderStatusTransitionParams", "sdk.order.status.transition(", "methodName: 'status.transition'"} {
		if !strings.Contains(string(tools), want) {
			t.Errorf("tools/index.ts lacks %q", want)
		}
	}
}

// timersAPI is the API that serves fixture-timers-db's Ticket's behavior
// operations, Timers' under timers among them (D103).
const timersAPI = "fixture-timers-api"

// timersAPIGolden is fixture-timers-api's SDK, whose types package depends
// on fixture-timers-db's.
func timersAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadBehaviorAPI(t, timersAPI, "fixture-timers-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: timersAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-timers-db": db}},
			{name: "fixture-timers-db", schema: db},
		},
	}
}

// TestTimersSDKGolden pins every file of the TypeScript SDK for
// fixture-timers-api, its tool documents included: the operations of the
// Timers Ticket implements sit in a class of their own under the use's
// member, sdk.ticket.timers.fireTimer(id, { deadline }), whose deadline is
// the enum TicketTimersDeadline, and its tool is ticket.timers.fireTimer.
// TestEveryGoldenSDKTypeChecks type-checks it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestTimersSDKGolden -update
func TestTimersSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadBehaviorAPI(t, timersAPI, "fixture-timers-db")
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", timersAPI))

	var members []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				members = append(members, ns.Name+" "+member.ClassName+"."+ep.MethodPath()+" "+ep.ToolName)
			}
		}
	}
	want := []string{
		"ticket TicketStatusOperations.status.transition ticket.status.transition",
		"ticket TicketTimersOperations.timers.fireTimer ticket.timers.fireTimer",
		"ticket TicketTimersOperations.timers.resetTimer ticket.timers.resetTimer",
		"ticket TicketTimersOperations.timers.snoozeTimer ticket.timers.snoozeTimer",
	}
	if strings.Join(members, "; ") != strings.Join(want, "; ") {
		t.Errorf("members = %v, want %v", members, want)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "ticket.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"public readonly timers: TicketTimersOperations;",
		"export class TicketTimersOperations {",
		"public async fireTimer(",
		"`/api/ticket/${encodeURIComponent(id)}/timers/fire-timer`",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("namespaces/ticket.ts lacks %q", want)
		}
	}
}
