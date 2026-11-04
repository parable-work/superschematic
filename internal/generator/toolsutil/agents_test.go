package toolsutil_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// TestToolSchemasGoldenAgents pins the tool schemas of fixture-agents-api
// (D104): every tool of a Job operation takes preconditions, an object
// whose member Lease is an object of a token of at least 0, as any input
// member, beside the operation's parameters and the row's key; a tool of
// Session's or Account's takes none; Session's beat takes the key alone;
// and recordAttempt's failure and observe's meter are their enums.
// Regenerate with:
// go test ./internal/generator/toolsutil -run TestToolSchemasGoldenAgents -update
func TestToolSchemasGoldenAgents(t *testing.T) {
	const fixture = "fixture-agents-api"
	quiet := loader.WithWarnings(func(*verify.Diagnostic) {})
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), quiet)
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-agents-db"), quiet)
	if err != nil {
		t.Fatalf("load fixture-agents-db: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   fixture,
		Dependencies: map[string]*ir.Schema{"fixture-agents-db": db},
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	tools := buildToolSchemas(t, output)
	byName := map[string]toolSchema{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	if len(tools) != 22 {
		t.Errorf("tools = %d, want 22", len(tools))
	}
	for name, tool := range byName {
		preconditions, ok := tool.Parameters.Properties["preconditions"]
		job := len(name) > 4 && name[:4] == "job."
		switch {
		case job && !ok:
			t.Errorf("%s takes no preconditions: %v", name, keysOf(tool.Parameters.Properties))
		case !job && ok:
			t.Errorf("%s takes preconditions, and no use of its table takes one", name)
		case job:
			lease, ok := preconditions.Properties["Lease"]
			token := lease.Properties["token"]
			if !ok || token.Minimum == nil || *token.Minimum != 0 || !slices.Contains(lease.Required, "token") || slices.Contains(tool.Parameters.Required, "preconditions") {
				t.Errorf("%s's preconditions = %+v, want an optional object whose Lease is a required token of at least 0", name, preconditions)
			}
		}
	}
	if beat := byName["session.presenceBeat"]; !slices.Equal(keysOf(beat.Parameters.Properties), []string{"id"}) {
		t.Errorf("session.presenceBeat takes %v, want the key alone", keysOf(beat.Parameters.Properties))
	}
	if failure := byName["job.retriesRecordAttempt"].Parameters.Properties["failure"]; !slices.Equal(failure.Enum, []string{"build", "rejected", "timeout"}) {
		t.Errorf("job.retriesRecordAttempt's failure = %+v, want its classes", failure)
	}
	if meter := byName["account.budgetObserve"].Parameters.Properties["meter"]; !slices.Equal(meter.Enum, []string{"questions", "tokens"}) {
		t.Errorf("account.budgetObserve's meter = %+v, want its meters", meter)
	}
	compareToolSchemasGolden(t, fixture, tools)
}

// keysOf are a map's keys, sorted.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
