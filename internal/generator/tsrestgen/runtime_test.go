package tsrestgen

import (
	"slices"
	"strings"
	"testing"
)

// TestDepsHoldTheRuntimePieces: an API whose database declares the
// notifier, the inbound path, search and the orchestration holds each in
// its Deps or its routes, over the repositories; a @worker of the
// notifier's dispatch queue is the notifier's and one of a @searchable
// declaration's queue search's, as the dispatcher's queues are the
// dispatcher's, and the Workers interface has a method for none of them;
// every other @worker stays the implementation's. DepsBase leaves out what
// withRuntime builds. Runtime pieces with no repositories are refused, and
// so is the inbound path with no notifier.
func TestDepsHoldTheRuntimePieces(t *testing.T) {
	output := &APIOutput{SchemaName: "work-api", HasEnvConfig: true, Workers: []WorkerInfo{
		{Name: "Dispatch", Method: "dispatch", Queue: "AgentRunRequest"},
		{Name: "Indexing", Method: "indexing", Queue: "SearchIndexing"},
		{Name: "Notify", Method: "notify", Queue: "NoticeWork"},
		{Name: "Released", Method: "released", Queue: "FileReleased"},
	}}
	deps := DepsInfo{
		Database: "work-db", ORM: "WorkDbOrm", DatabaseTypesPackage: "@schemas/work-db-types",
		Agents:    &DepsAgents{Requests: "AgentRunRequest", RunRequests: "RunRequest"},
		Providers: &DepsProviders{Slots: []DepsProviderSlot{{Slot: "MAIL", Interface: "channel"}, {Slot: "MODEL", Interface: "model"}, {Slot: "SMS", Interface: "channel"}}},
		Notifier:  &DepsNotifier{Dispatch: "NoticeWork"},
		Inbound:   &DepsInbound{Runs: "RunRequest"},
		Routes:    DepsRoutes{Search: true, SearchQueues: []string{"SearchIndexing"}, Grants: true},
	}
	if err := output.SetDeps(deps); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, w := range output.Workers {
		got[w.Name] = w.Runtime
	}
	want := map[string]string{"Dispatch": WorkerAgents, "Indexing": WorkerSearch, "Notify": WorkerNotifier, "Released": ""}
	for name, runtime := range want {
		if got[name] != runtime {
			t.Errorf("worker %s's runtime = %q, want %q", name, got[name], runtime)
		}
	}
	if queues := output.WorkerQueues(); !slices.Equal(queues, []string{"FileReleased"}) {
		t.Errorf("WorkerQueues = %v, want the implementation's FileReleased alone", queues)
	}
	if !output.HasRuntime() || !output.BuildsRuntime() || !output.HasProviders() {
		t.Errorf("HasRuntime %v, BuildsRuntime %v, HasProviders %v, want all", output.HasRuntime(), output.BuildsRuntime(), output.HasProviders())
	}
	if members := strings.Join(output.RuntimeMembers(), " | "); members != "'notifier' | 'inbound' | 'agents'" {
		t.Errorf("RuntimeMembers = %s", members)
	}
	if slots := output.Deps.Providers.ChannelSlots(); !slices.Equal(slots, []string{"MAIL", "SMS"}) {
		t.Errorf("ChannelSlots = %v, want MAIL and SMS", slots)
	}
	if queues := output.RuntimeQueueImports(); !slices.Equal(queues, []string{"agentRunRequestQueue", "noticeWorkQueue", "runRequestQueue"}) {
		t.Errorf("RuntimeQueueImports = %v", queues)
	}

	search := &APIOutput{SchemaName: "search-api"}
	if err := search.SetDeps(DepsInfo{Database: "work-db", ORM: "WorkDbOrm", DatabaseTypesPackage: "@schemas/work-db-types", Routes: DepsRoutes{Search: true}}); err != nil {
		t.Fatal(err)
	}
	if !search.HasRuntime() || search.BuildsRuntime() {
		t.Errorf("search alone: HasRuntime %v, BuildsRuntime %v, want a runtime.ts that builds nothing into Deps", search.HasRuntime(), search.BuildsRuntime())
	}

	for name, refused := range map[string]DepsInfo{
		"no repositories": {Database: "work-db", Notifier: &DepsNotifier{Dispatch: "NoticeWork"}},
		"sqlite":          {Database: "work-db", Dialect: SQLiteDialect, ORM: "WorkDbOrm", DatabaseTypesPackage: "@schemas/work-db-types", Routes: DepsRoutes{Calendar: true}},
		"no notifier":     {Database: "work-db", ORM: "WorkDbOrm", DatabaseTypesPackage: "@schemas/work-db-types", Inbound: &DepsInbound{Runs: "RunRequest"}},
	} {
		if err := (&APIOutput{SchemaName: "work-api"}).SetDeps(refused); err == nil {
			t.Errorf("SetDeps with %s = nil, want a refusal", name)
		}
	}
}
