package behaviorops_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/behaviorops"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D104's second step: Task implements Constants, whose
// permission is tasks.admin, and StateRules, which names none.
const (
	approvalsDB  = "../tsreader/testdata/services/fixture-approvals-db"
	approvalsAPI = "../tsreader/testdata/services/fixture-approvals-api"
)

// WritePermissions (D104): the permissions the core checks on an
// operation's writes are Constants' and StateRules' permissions of each
// table it updates through a @source view of it: reassignTask, a PATCH of
// Task through TaskAssignment, writes under tasks.admin, which Constants
// names; a GET, a create, a behavior operation, an operation of a plain
// input and a dependency the deps lack write under none. Each permission is
// listed once, sorted, StateRules' beside Constants', and Lease's, Retries'
// and Budget's beside them.
func TestWritePermissions(t *testing.T) {
	db, err := loader.LoadService(filepath.FromSlash(approvalsDB))
	if err != nil {
		t.Fatal(err)
	}
	api, err := loader.LoadService(filepath.FromSlash(approvalsAPI))
	if err != nil {
		t.Fatal(err)
	}
	deps := map[string]*ir.Schema{db.Name: db}
	operation := func(set, name string) *ir.FieldDef {
		for _, s := range api.OperationSets {
			if s.Name != set {
				continue
			}
			for _, op := range s.Operations {
				if op.Name == name {
					return op
				}
			}
		}
		t.Fatalf("%s has no operation %s.%s", api.Name, set, name)
		return nil
	}
	reassign := operation("TaskAdmin", "reassignTask")
	if got := behaviorops.WritePermissions(api, reassign, deps); !slices.Equal(got, []string{"tasks.admin"}) {
		t.Errorf("reassignTask writes under %v, want tasks.admin", got)
	}
	if got := behaviorops.WritePermissions(api, reassign, nil); got != nil {
		t.Errorf("reassignTask without its database writes under %v, want none", got)
	}
	if got := behaviorops.WritePermissions(api, operation("TaskActions", "approvalsSignOff"), deps); got != nil {
		t.Errorf("a behavior operation writes under %v, want none: its own permissions are its behavior's", got)
	}
	for _, method := range []string{"GET", "POST"} {
		op := *reassign
		op.HTTPMethod = method
		op.Arguments = op.Arguments[1:]
		if got := behaviorops.WritePermissions(api, &op, deps); got != nil {
			t.Errorf("a %s that takes no key writes under %v, want none", method, got)
		}
	}
	// A POST that takes the key updates a row; StateRules' permission joins
	// Constants', sorted, each once.
	post := *reassign
	post.HTTPMethod = "POST"
	task := *db.Types["Task"]
	task.Behaviors = slices.Clone(task.Behaviors)
	for i, ref := range task.Behaviors {
		if ref.Name == "StateRules" {
			var config map[string]any
			if err := json.Unmarshal(ref.Config, &config); err != nil {
				t.Fatal(err)
			}
			config["permission"] = "tasks.edit"
			task.Behaviors[i].Config, _ = json.Marshal(config)
		}
	}
	edited := *db
	edited.Types = map[string]*ir.TypeDef{"Task": &task}
	if got := behaviorops.WritePermissions(api, &post, map[string]*ir.Schema{db.Name: &edited}); !slices.Equal(got, []string{"tasks.admin", "tasks.edit"}) {
		t.Errorf("a POST that takes the key writes under %v, want tasks.admin and tasks.edit", got)
	}
	if !behaviorops.UpdatesARow(&post, &task) || behaviorops.UpdatesARow(&ir.FieldDef{HTTPMethod: "POST"}, &task) {
		t.Error("UpdatesARow: a POST that takes the key updates a row, one that takes none creates one")
	}
	// Lease's overridePermission, Retries' limitsPermission and Budget's
	// limitPermission, each checked on an update through the core, join
	// them (D104); a member of another name does not.
	task.Behaviors = append(slices.Clone(task.Behaviors),
		ir.BehaviorRef{Name: "Lease", Implements: true, Config: json.RawMessage(`{"overridePermission": "tasks.override", "acquirePermission": "tasks.lease"}`)},
		ir.BehaviorRef{Name: "Retries", Implements: true, Config: json.RawMessage(`{"limitsPermission": "tasks.caps", "permission": "tasks.attempt"}`)},
		ir.BehaviorRef{Name: "Budget", Implements: true, Config: json.RawMessage(`{"limitPermission": "tasks.budget", "observePermission": "tasks.observe"}`)},
	)
	if got, want := behaviorops.WritePermissions(api, &post, map[string]*ir.Schema{db.Name: &edited}), []string{"tasks.admin", "tasks.budget", "tasks.caps", "tasks.edit", "tasks.override"}; !slices.Equal(got, want) {
		t.Errorf("a POST of a table that implements Lease, Retries and Budget writes under %v, want %v", got, want)
	}
}
