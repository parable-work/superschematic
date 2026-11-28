package servergen_test

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// goWiringGoldenRoot holds the entrypoints of a Go server whose API's
// database has the runtime's wiring (D190, D192, D187, D180).
const goWiringGoldenRoot = "testdata/golden/wiring/go"

// goWiringInput is goBehaviorInput with the API's database hosting the
// notifier, the dispatcher, the inbound path, a calendar, search and
// grants, and a @worker of the notifier's queue.
func goWiringInput(repoRoot string) servergen.Input {
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
		RulesGo:       filepath.Join(repoRoot, "runtime", "rules", "go"),
	}
	in := goBehaviorInput(repoRoot, paths)
	o := in.APIs[0].Output
	o.Deps.Wiring = &apigen.DepsWiring{
		Notifier: "NoticeWork", Agents: "AgentRunRequest", Inbound: true, RunRequests: "RunRequest",
		Calendar: true, Search: true, SearchQueues: []string{"SearchIndexing"}, Grants: true,
	}
	o.Workers = append(o.Workers, apigen.WorkerInfo{Name: "Notify", Queue: "NoticeWork", Concurrency: 4, Runtime: apigen.RuntimeNotifier})
	return in
}

// TestGoServerWiresTheRuntimeGolden: the server of a public API whose
// database has the runtime's wiring asks the implementation's NewWiring
// for its options, builds Deps' notifier, dispatcher and inbound path with
// the API package's Wire, wraps the implementation's AuthMiddleware in the
// API package's Authenticator, which its Config takes, and serves the API
// package's Routes beside its router; and the worker of the notifier's
// queue builds the same Deps and runs NotifyQueue, calling no NewWorkers.
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoServerWiresTheRuntimeGolden -update
func TestGoServerWiresTheRuntimeGolden(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	in := goWiringInput(repoRoot)
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	worker := goWiringInput(repoRoot)
	worker.Server = "workflow-notify"
	worker.Dir = filepath.Join(filepath.Dir(in.Dir), worker.Server)
	worker.Worker = &servergen.WorkerInput{Name: "Notify"}
	w, err := servergen.Plan(worker)
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.Write(w, worker.Dir); err != nil {
		t.Fatal(err)
	}
	compareGoldens(t, goWiringGoldenRoot, map[string]string{
		"server/" + servergen.MainFile: filepath.Join(in.Dir, servergen.MainFile),
		"worker/" + servergen.MainFile: filepath.Join(worker.Dir, servergen.MainFile),
	})
}
