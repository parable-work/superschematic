package tsrestgen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// largeRouterOperations is how many times the large router's API declares
// each of two of fixture-api's operations again. TypeScript's control-flow analysis
// gave up on buildRouter (TS2563) past roughly 330 operations while each
// one read its three per-operation options through an optional chain.
const largeRouterOperations = 420

// largeFixtureAPI is fixture-api with getTenant and its
// @manualRouteRegistration operation, customHandler, each declared again
// largeRouterOperations times under its own name and path.
func largeFixtureAPI(t *testing.T) apiFixture {
	t.Helper()
	apiSchema, dbSchema := loadFixtureAPI(t)
	for _, name := range []string{"getTenant", "customHandler"} {
		var set *ir.OperationSet
		var original *ir.FieldDef
		for _, candidate := range apiSchema.OperationSets {
			for _, op := range candidate.Operations {
				if op.Name == name {
					set, original = candidate, op
				}
			}
		}
		if original == nil {
			t.Fatalf("fixture-api declares no %s", name)
		}
		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		for i := 0; i < largeRouterOperations; i++ {
			op := &ir.FieldDef{}
			if err := json.Unmarshal(encoded, op); err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
			op.Name = fmt.Sprintf("%sAgain%d", name, i)
			if original.RestPath != "" {
				op.RestPath = fmt.Sprintf("%s/again-%d", original.RestPath, i)
			}
			set.Operations = append(set.Operations, op)
		}
	}
	return apiFixture{
		name:      "fixture-api",
		schema:    apiSchema,
		deps:      map[string]*ir.Schema{"fixture-db": dbSchema},
		endpoints: extractEndpoints(t, apiSchema, dbSchema),
	}
}

// TestALargeRouterReadsItsPerOperationOptionsThroughConstants pins the
// form that keeps buildRouter within TypeScript's control-flow analysis:
// the per-operation option maps (bodyLimits, rateLimits, timeouts and
// manualRoutes) are read once into constants, and no operation reads one
// through an optional chain on options.
func TestALargeRouterReadsItsPerOperationOptionsThroughConstants(t *testing.T) {
	fixture := largeFixtureAPI(t)
	output, err := Generate(fixture.schema, fixture.endpoints, Options{
		SchemaName:   fixture.name,
		Dependencies: fixture.deps,
		Clock:        fixedClock,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatalf("WriteAPI: %v", err)
	}
	text, err := os.ReadFile(filepath.Join(dir, "router.ts"))
	if err != nil {
		t.Fatalf("read router.ts: %v", err)
	}
	router := string(text)
	for _, chain := range []string{"options.bodyLimits?.", "options.rateLimits?.", "options.timeouts?.", "options.manualRoutes?."} {
		if strings.Contains(router, chain) {
			t.Errorf("router.ts reads %s for an operation: each optional chain deepens the flow analysis of every later reference in buildRouter", chain)
		}
	}
	for _, read := range []string{
		"bodyLimitBytes: bodyLimits.getTenantAgain0,",
		"rateLimitPerMinute: rateLimits.getTenantAgain0,",
		"timeoutSeconds: timeouts.getTenantAgain0,",
		"manualRoutes.customHandlerAgain0,",
	} {
		if !strings.Contains(router, read) {
			t.Errorf("router.ts lacks %q", read)
		}
	}
}

// TestALargeGeneratedAPICompiles type-checks an API of more than 800
// operations: buildRouter mounts every one in a single function, which
// TypeScript must still analyse (no TS2563), so each implementation keeps
// its types.
func TestALargeGeneratedAPICompiles(t *testing.T) {
	materializeAPI(t, largeFixtureAPI(t)).typeCheck(t)
}
