package tsrestgen

import (
	"strings"
	"testing"
)

// TestDepsHoldTheAgentDispatcher (D183): an API whose database uses the
// agent orchestration holds the dispatcher in its Deps, over the
// repositories; a @worker of the requests queue or of the run requests
// queue is the dispatcher's, which the Workers interface has no method
// for, and every other @worker stays the implementation's. Deps with the
// dispatcher and no repositories is refused.
func TestDepsHoldTheAgentDispatcher(t *testing.T) {
	output := &APIOutput{SchemaName: "work-api", Workers: []WorkerInfo{
		{Name: "Dispatch", Method: "dispatch", Queue: "AgentRunRequest"},
		{Name: "InboundRuns", Method: "inboundRuns", Queue: "RunRequest"},
		{Name: "Notify", Method: "notify", Queue: "NoticeWork"},
	}}
	deps := DepsInfo{Database: "work-db", ORM: "WorkDbOrm", DatabaseTypesPackage: "@schemas/work-db-types", Agents: &DepsAgents{Requests: "AgentRunRequest", RunRequests: "RunRequest"}}
	if err := output.SetDeps(deps); err != nil {
		t.Fatal(err)
	}
	if !output.HasAgents() || output.Deps.Agents.RequestsConst() != "agentRunRequestQueue" {
		t.Fatalf("Deps.Agents = %+v", output.Deps.Agents)
	}
	names := func(workers []WorkerInfo) string {
		var out []string
		for _, w := range workers {
			out = append(out, w.Name)
		}
		return strings.Join(out, " ")
	}
	if got := names(output.OrdinaryWorkers()); got != "Notify" {
		t.Errorf("OrdinaryWorkers = %s, want Notify", got)
	}
	if got := names(output.DispatcherWorkers()); got != "Dispatch InboundRuns" {
		t.Errorf("DispatcherWorkers = %s, want Dispatch InboundRuns", got)
	}
	if got := output.WorkerQueues(); len(got) != 1 || got[0] != "NoticeWork" {
		t.Errorf("WorkerQueues = %v, want the implementation's NoticeWork alone", got)
	}

	refused := &APIOutput{SchemaName: "work-api"}
	deps.ORM = ""
	if err := refused.SetDeps(deps); err == nil || !strings.Contains(err.Error(), "holds the agent dispatcher (D183)") {
		t.Fatalf("SetDeps without repositories = %v", err)
	}
}
