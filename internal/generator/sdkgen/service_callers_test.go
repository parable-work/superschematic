package sdkgen

import (
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestToolsLeaveOutServiceOnlyOperations: a @requireService operation,
// its own clause or its set's, keeps its SDK method, which a service calls,
// but is no tool, since no end user's agent can call it. An @allowService
// operation is a tool, and so is an @publicRoute one in a set with a
// clause. The Go SDK's tools come from this list too.
func TestToolsLeaveOutServiceOnlyOperations(t *testing.T) {
	const service = "fixture-service-auth-api"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, service))
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: service,
		Clock:      mcpClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: service})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, tsgen.ParseableTypeNames(tsOutput), mcpClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var methods []string
	for _, ns := range sdkOutput.Namespaces {
		for _, endpoint := range ns.Endpoints {
			methods = append(methods, ns.Name+"."+endpoint.Name)
		}
	}
	sort.Strings(methods)
	wantMethods := []string{
		"ledger.listReservations",
		"stock.getReservation", "stock.reindexStock", "stock.releaseReservation", "stock.reserveStock",
		"sync.syncMyStock", "sync.syncStatus", "sync.syncStock",
	}
	if !slices.Equal(methods, wantMethods) {
		t.Errorf("SDK methods = %v, want %v", methods, wantMethods)
	}

	toolsOutput, err := GenerateTools(sdkOutput, apiOutput, mcpClock)
	if err != nil {
		t.Fatalf("GenerateTools: %v", err)
	}
	var tools []string
	for _, tool := range toolsOutput.Tools {
		tools = append(tools, tool.Name)
	}
	sort.Strings(tools)
	wantTools := []string{
		"ledger.listReservations",
		"stock.getReservation", "stock.releaseReservation",
		"sync.syncMyStock", "sync.syncStatus",
	}
	if !slices.Equal(tools, wantTools) {
		t.Errorf("tools = %v, want %v", tools, wantTools)
	}
}
