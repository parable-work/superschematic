package tsrestgen

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/ir"
)

// TestRateLimitOperationIsTheGoServers: the operation part of a shared
// rate limit's key (D75) is `<namespace>.<name>`, which the TypeScript
// runtime joins from each operation's table entry (operationSpecs). For
// every operation of fixture-api that is the spelling the Go server passes
// to WithStore, apigen's RateLimitOperation, for an operation named in
// camelCase, as an operation's method is.
func TestRateLimitOperationIsTheGoServers(t *testing.T) {
	apiSchema, dbSchema := loadFixtureAPI(t)
	api := extractEndpoints(t, apiSchema, dbSchema)
	output, err := Generate(apiSchema, api, Options{
		SchemaName:   "fixture-api",
		Dependencies: map[string]*ir.Schema{"fixture-db": dbSchema},
		Clock:        fixedClock,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	ts := map[string]EndpointInfo{}
	for _, ep := range output.Endpoints {
		ts[ep.Method+" "+ep.Path] = ep
	}
	if len(api.Endpoints) == 0 {
		t.Fatal("fixture-api has no endpoints")
	}
	for _, ep := range api.Endpoints {
		got, ok := ts[strings.ToUpper(ep.Method)+" "+ep.Path]
		if !ok {
			t.Errorf("no TypeScript entry for %s %s", ep.Method, ep.Path)
			continue
		}
		if op, want := got.Namespace+"."+got.Name, ep.RateLimitOperation(); op != want {
			t.Errorf("the TypeScript server's operation is %q, the Go server's %q", op, want)
		}
	}
}
