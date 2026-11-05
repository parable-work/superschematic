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

// TestToolSchemasGoldenSeats pins the tool schemas of fixture-seats-api
// (D104): Problem's namespace holds the members seats, the Seats it
// implements, and status, and Sandbox's observed, the Reconcile on its
// field, and status, each a field use, in the order the API lists their
// endpoints; every seat a tool takes is the
// enum of the config's seats and observe's state the enum of the states of
// the Workflow its status names; listSeats' seat is optional, takeSeat's
// required; and no tool takes preconditions, since no use takes one.
// Regenerate with:
// go test ./internal/generator/toolsutil -run TestToolSchemasGoldenSeats -update
func TestToolSchemasGoldenSeats(t *testing.T) {
	const fixture = "fixture-seats-api"
	quiet := loader.WithWarnings(func(*verify.Diagnostic) {})
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), quiet)
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-seats-db"), quiet)
	if err != nil {
		t.Fatalf("load fixture-seats-db: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   fixture,
		Dependencies: map[string]*ir.Schema{"fixture-seats-db": db},
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for namespace, want := range map[string][]string{
		"problem": {"Seats Seats true", "status Workflow false"},
		"sandbox": {"observed Reconcile false", "status Workflow false"},
	} {
		var endpoints []apigen.EndpointInfo
		for _, ep := range output.Endpoints {
			if bo := ep.BehaviorOperation(); bo != nil && codegen.ToCamelCase(bo.Type) == namespace {
				endpoints = append(endpoints, ep)
			}
		}
		members, err := toolsutil.BehaviorMembers(namespace, endpoints)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, member := range members {
			got = append(got, member.Use+" "+member.Behavior+" "+map[bool]string{true: "true", false: "false"}[member.Implemented()])
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s's members = %v, want %v", namespace, got, want)
		}
	}
	tools := buildToolSchemas(t, output)
	byName := map[string]toolSchema{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	if len(tools) != 6 {
		t.Errorf("tools = %d (%v), want 6", len(tools), keysOf(byName))
	}
	for name, tool := range byName {
		if _, ok := tool.Parameters.Properties["preconditions"]; ok {
			t.Errorf("%s takes preconditions, and no use of its table takes one", name)
		}
	}
	seats := []string{"accountable", "handles", "responsible"}
	for _, name := range []string{"problem.seatsTakeSeat", "problem.seatsLeaveSeat", "problem.seatsListSeats"} {
		if seat := byName[name].Parameters.Properties["seat"]; !slices.Equal(seat.Enum, seats) {
			t.Errorf("%s's seat = %+v, want the config's seats", name, seat)
		}
	}
	if take, list := byName["problem.seatsTakeSeat"].Parameters.Required, byName["problem.seatsListSeats"].Parameters.Required; !slices.Contains(take, "seat") || slices.Contains(list, "seat") {
		t.Errorf("takeSeat requires %v and listSeats %v, want seat in the first alone", take, list)
	}
	if state := byName["sandbox.observedObserve"].Parameters.Properties["state"]; !slices.Equal(state.Enum, []string{"pending", "starting", "running", "stopping", "stopped", "failed", "unreachable"}) {
		t.Errorf("sandbox.observedObserve's state = %+v, want the Workflow's states", state)
	}
	compareToolSchemasGolden(t, fixture, tools)
}
