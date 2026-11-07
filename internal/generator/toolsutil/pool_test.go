package toolsutil_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// TestToolSchemasGoldenPool pins the tool schemas of fixture-pool-api
// (D104): Account's namespace holds the members Pool and Budget, the
// behaviors it implements, pick, an operation of the table as a whole, a
// method of Pool's member as any, and its tools are by name; markOut and
// reportRoom take the row's key beside their parameters, and pick its
// parameters alone, match, prefer and exclude, no key, whose match is an
// object of the table's own fields; no tool takes preconditions, since no
// use takes one, and none is the event recheck's, which only the behavior
// worker hands to the project's Workers method. Regenerate with:
// go test ./internal/generator/toolsutil -run TestToolSchemasGoldenPool -update
func TestToolSchemasGoldenPool(t *testing.T) {
	const fixture = "fixture-pool-api"
	quiet := loader.WithWarnings(func(*verify.Diagnostic) {})
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), quiet)
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-pool-db"), quiet)
	if err != nil {
		t.Fatalf("load fixture-pool-db: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   fixture,
		Dependencies: map[string]*ir.Schema{"fixture-pool-db": db},
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var endpoints []apigen.EndpointInfo
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo != nil && codegen.ToCamelCase(bo.Type) == "account" {
			endpoints = append(endpoints, ep)
		}
	}
	members, err := toolsutil.BehaviorMembers("account", endpoints)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, member := range members {
		var methods []string
		for _, ep := range member.Endpoints {
			methods = append(methods, toolsutil.EndpointMethodName(ep))
		}
		got = append(got, member.Use+" "+member.Behavior+" "+map[bool]string{true: "true", false: "false"}[member.Implemented()]+" "+slices.Sorted(slices.Values(methods))[0])
	}
	if want := []string{"Pool Pool true pool.markOut", "Budget Budget true budget.checkReserve"}; !slices.Equal(got, want) {
		t.Errorf("account's members = %v, want %v", got, want)
	}
	tools := buildToolSchemas(t, output)
	byName := map[string]toolSchema{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	if len(tools) != 12 {
		t.Errorf("tools = %d, want 12", len(tools))
	}
	for name, tool := range byName {
		if _, ok := tool.Parameters.Properties["preconditions"]; ok {
			t.Errorf("%s takes preconditions, and no use of its table takes one", name)
		}
	}
	for name, want := range map[string][]string{
		"account.poolPick":       {"exclude", "match", "prefer"},
		"account.poolMarkOut":    {"id", "reason", "until"},
		"account.poolReportRoom": {"id", "near", "reason", "resetsAt", "room"},
	} {
		if got := keysOf(byName[name].Parameters.Properties); !slices.Equal(got, want) {
			t.Errorf("%s takes %v, want %v", name, got, want)
		}
	}
	if match := byName["account.poolPick"].Parameters.Properties["match"]; !slices.Equal(keysOf(match.Properties), []string{"kind", "name", "owner", "rank", "vendor"}) {
		t.Errorf("account.poolPick's match = %+v, want the table's own fields", match)
	}
	compareToolSchemasGolden(t, fixture, tools)
}
