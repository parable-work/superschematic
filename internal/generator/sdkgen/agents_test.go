package sdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// agentsAPI is the API that serves fixture-agents-db's Job's, Session's and
// Account's behavior operations, those of the Lease, Retries, Presence and
// Budget they implement among them (D104).
const agentsAPI = "fixture-agents-api"

// agentsAPIGolden is fixture-agents-api's SDK, whose types package depends
// on fixture-agents-db's.
func agentsAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadBehaviorAPI(t, agentsAPI, "fixture-agents-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: agentsAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-agents-db": db}},
			{name: "fixture-agents-db", schema: db},
		},
	}
}

// TestAgentsSDKGolden pins every file of the TypeScript SDK for
// fixture-agents-api, its tool documents included: the operations of each
// use Job, Session and Account implement sit in a class of their own under
// the use's member, sdk.job.lease.heartbeat(id, { preconditions: { Lease:
// { token } } }) and sdk.account.budget.observe(id, { meter, used }), and
// Lease's expireHolder, an operation of the table as a whole, takes no key,
// sdk.job.lease.expireHolder({ holder }) (D104); their tools are
// job.lease.heartbeat, job.lease.expireHolder and account.budget.observe.
// TestEveryGoldenSDKTypeChecks type-checks it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestAgentsSDKGolden -update
func TestAgentsSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadBehaviorAPI(t, agentsAPI, "fixture-agents-db")
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", agentsAPI))

	var members []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				members = append(members, ns.Name+" "+member.ClassName+"."+ep.MethodPath()+" "+ep.ToolName)
			}
		}
	}
	for _, want := range []string{
		"job JobLeaseOperations.lease.heartbeat job.lease.heartbeat",
		"job JobLeaseOperations.lease.expireHolder job.lease.expireHolder",
		"job JobRetriesOperations.retries.recordAttempt job.retries.recordAttempt",
		"job JobStatusOperations.status.transition job.status.transition",
		"session SessionPresenceOperations.presence.beat session.presence.beat",
		"account AccountBudgetOperations.budget.observe account.budget.observe",
	} {
		if !strings.Contains(strings.Join(members, "; "), want) {
			t.Errorf("members = %v, want %s among them", members, want)
		}
	}
	if len(members) != 23 {
		t.Errorf("members = %d (%v), want 23", len(members), members)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "job.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"public readonly lease: JobLeaseOperations;",
		"export class JobLeaseOperations {",
		"public async heartbeat(",
		"`/api/job/${encodeURIComponent(id)}/lease/heartbeat`",
		"public async expireHolder(",
		"`/api/job/lease/expire-holder`",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("namespaces/job.ts lacks %q", want)
		}
	}
}
