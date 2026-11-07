package pysdkgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// agentsService is the API that serves fixture-agents-db's Job's, Session's
// and Account's behavior operations, those of the Lease, Retries, Presence
// and Budget they implement among them (D104).
const agentsService = "fixture-agents-api"

// TestAgentsSDKGolden pins every file of the Python SDK for
// fixture-agents-api: the operations of each use Job, Session and Account
// implement are methods of a class of their own, the property of the
// namespace named for the use's member, sdk.job.lease.heartbeat and
// sdk.account.budget.observe, whose inputs present JobPreconditions and
// name an AccountBudgetMeter, and Lease's expireHolder, an operation of the
// table as a whole, takes no key, sdk.job.lease.expire_holder(input)
// (D104). Regenerate with:
// go test ./internal/generator/pysdkgen -run TestAgentsSDKGolden -update
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
		Dependencies: map[string]*ir.Schema{"fixture-agents-db": db},
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
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", agentsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.AccessorName+"."+member.FieldName+" "+member.ClassName+"."+ep.MethodName)
			}
		}
	}
	for _, want := range []string{
		"job.lease JobLeaseOperations.heartbeat",
		"job.lease JobLeaseOperations.expire_holder",
		"job.retries JobRetriesOperations.record_attempt",
		"job.status JobStatusOperations.transition",
		"session.presence SessionPresenceOperations.beat",
		"account.budget AccountBudgetOperations.observe",
	} {
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Errorf("members = %v, want %s among them", got, want)
		}
	}
	if len(got) != 23 {
		t.Errorf("members = %d (%v), want 23", len(got), got)
	}
}
