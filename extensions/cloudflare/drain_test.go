package cloudflare_test

import (
	"encoding/json"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"
)

// TestDrainModeChangesNothingOnWorkers (D145): a worker on
// cloudflare.drainer already drains its queue at each firing of its Cron
// Trigger and stops, so a worker its settings put in drain mode resolves to
// the nodes it had: the same Worker, bundle and trigger, and no wake, which
// the target does not provide yet. Its run records the mode all the same.
func TestDrainModeChangesNothingOnWorkers(t *testing.T) {
	reg := assemble(t)
	resolve := func(s *ir.Stack) *ir.ResolvedEnvironment {
		t.Helper()
		env, err := stack.Resolve(reg, stack.Input{Stack: s, Services: ledgerServices(), Environment: "Staging"})
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	pool := resolve(ledgerStack())
	s := ledgerStack()
	s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: worker("SyncPlaidItem"), Mode: ir.WorkerModeDrain})
	drain := resolve(s)
	if run := drain.Deployable("finances-api-sync-plaid-item").Worker; !run.Drains() || run.IdleSeconds != ir.DefaultWorkerIdleSeconds {
		t.Errorf("the worker runs %+v, want drain mode", run)
	}
	want, err := json.Marshal(pool.Resources)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(drain.Resources)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("drain mode changed the environment's nodes on cloudflare:\n%s\nwant\n%s", got, want)
	}
}
