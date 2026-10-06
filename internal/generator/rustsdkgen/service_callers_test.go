package rustsdkgen

import (
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestToolsLeaveOutServiceOnlyOperations: the Rust SDK keeps a method for a
// @requireService operation, which a service calls, and lists no tool for
// it, as the TypeScript and Go SDKs do.
func TestToolsLeaveOutServiceOnlyOperations(t *testing.T) {
	const service = "fixture-service-auth-api"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, service))
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: service,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(service), naming.Default().RustTypesCrate(service), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	methods := 0
	for _, ns := range sdkOutput.Namespaces {
		methods += len(ns.Endpoints)
	}
	if methods != 8 {
		t.Errorf("SDK has %d methods, want 8: every operation", methods)
	}

	toolsOutput, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatalf("GenerateTools: %v", err)
	}
	var tools []string
	for _, tool := range toolsOutput.Tools {
		tools = append(tools, tool.Name)
	}
	sort.Strings(tools)
	want := []string{
		"ledger.listReservations",
		"stock.getReservation", "stock.releaseReservation",
		"sync.syncMyStock", "sync.syncStatus",
	}
	if !slices.Equal(tools, want) {
		t.Errorf("tools = %v, want %v", tools, want)
	}
}
