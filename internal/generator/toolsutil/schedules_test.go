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

// TestToolSchemasGoldenSchedules pins the tool schemas of
// fixture-schedules-api (D104): Problem's namespace holds the members
// schedules, the Schedules it implements, and status, a field use, in the
// order the API lists their endpoints, and its tools are by name;
// setSchedule takes a name and one of
// after, every and cron, a cron line's timeZone among them, and reportRun's
// outcome is the enum of the config's outcomes; no tool takes
// preconditions, since no use takes one, and none is the event run's, which
// only the behavior worker hands to the project's Workers method.
// Regenerate with:
// go test ./internal/generator/toolsutil -run TestToolSchemasGoldenSchedules -update
func TestToolSchemasGoldenSchedules(t *testing.T) {
	const fixture = "fixture-schedules-api"
	quiet := loader.WithWarnings(func(*verify.Diagnostic) {})
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), quiet)
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-schedules-db"), quiet)
	if err != nil {
		t.Fatalf("load fixture-schedules-db: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   fixture,
		Dependencies: map[string]*ir.Schema{"fixture-schedules-db": db},
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var endpoints []apigen.EndpointInfo
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo != nil && codegen.ToCamelCase(bo.Type) == "problem" {
			endpoints = append(endpoints, ep)
		}
	}
	members, err := toolsutil.BehaviorMembers("problem", endpoints)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, member := range members {
		got = append(got, member.Use+" "+member.Behavior+" "+map[bool]string{true: "true", false: "false"}[member.Implemented()])
	}
	if want := []string{"Schedules Schedules true", "status Workflow false"}; !slices.Equal(got, want) {
		t.Errorf("problem's members = %v, want %v", got, want)
	}
	tools := buildToolSchemas(t, output)
	byName := map[string]toolSchema{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if want := []string{"problem.schedulesCancelSchedule", "problem.schedulesListSchedules", "problem.schedulesPauseSchedule", "problem.schedulesReportRun",
		"problem.schedulesResumeSchedule", "problem.schedulesSetSchedule", "problem.statusTransition"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	for name, tool := range byName {
		if _, ok := tool.Parameters.Properties["preconditions"]; ok {
			t.Errorf("%s takes preconditions, and no use of its table takes one", name)
		}
	}
	set := byName["problem.schedulesSetSchedule"].Parameters
	for _, property := range []string{"name", "after", "every", "cron", "timeZone"} {
		if _, ok := set.Properties[property]; !ok {
			t.Errorf("setSchedule takes no %s: %+v", property, set.Properties)
		}
	}
	if !slices.Contains(set.Required, "name") || slices.Contains(set.Required, "after") {
		t.Errorf("setSchedule requires %v, want name alone of its own", set.Required)
	}
	if outcome := byName["problem.schedulesReportRun"].Parameters.Properties["outcome"]; !slices.Equal(outcome.Enum, []string{"told", "resumed", "held", "missed"}) {
		t.Errorf("reportRun's outcome = %+v, want the config's outcomes", outcome)
	}
	compareToolSchemasGolden(t, fixture, tools)
}
