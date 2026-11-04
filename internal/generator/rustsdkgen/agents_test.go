package rustsdkgen

import (
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

// agentsService is the API that serves fixture-agents-db's Job's, Session's
// and Account's behavior operations, those of the Lease, Retries, Presence
// and Budget they implement among them (D104).
const agentsService = "fixture-agents-api"

// TestAgentsSDKGolden pins every file of the Rust SDK for
// fixture-agents-api, its tool documents included: the operations of each
// use Job, Session and Account implement are methods of a struct of their
// own, the field of the namespace named for the use's member,
// job.lease.heartbeat and account.budget.observe, whose tools are named
// job.lease.heartbeat with the method lease.heartbeat. Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestAgentsSDKGolden -update
func TestAgentsSDKGolden(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(agentsService), load("fixture-agents-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   agentsService,
		ModulePath:   "example.com/schemas/api/" + agentsService,
		TypesModule:  "example.com/schemas/types/go/" + agentsService,
		Dependencies: map[string]*ir.Schema{"fixture-agents-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", agentsService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(agentsService), naming.Default().RustTypesCrate(agentsService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", agentsService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", agentsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.FieldName+"."+member.FieldName+" "+member.StructName+"::"+ep.MethodName)
			}
		}
	}
	for _, want := range []string{
		"job.lease JobLeaseOperations::heartbeat",
		"job.retries JobRetriesOperations::record_attempt",
		"job.status JobStatusOperations::transition",
		"session.presence SessionPresenceOperations::beat",
		"account.budget AccountBudgetOperations::observe",
	} {
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Errorf("members = %v, want %s among them", got, want)
		}
	}
	if len(got) != 22 {
		t.Errorf("members = %d (%v), want 22", len(got), got)
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name+" "+tool.MethodName)
	}
	for _, want := range []string{"job.lease.heartbeat lease.heartbeat", "account.budget.recordUsageFor budget.record_usage_for", "session.presence.beat presence.beat"} {
		if !strings.Contains(strings.Join(names, "; "), want) {
			t.Errorf("tools = %v, want %s among them", names, want)
		}
	}
}
