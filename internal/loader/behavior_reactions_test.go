package loader

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D103's outbox and Reactions: a database whose Project's
// Reactions hears its Tasks through task.project and whose Task's
// Reactions moves its project, and an API that serves both tables'
// behavior operations and runs both tables' change queues.
const (
	reactionsDBFixture  = "tsreader/testdata/services/fixture-reactions-db"
	reactionsAPIFixture = "tsreader/testdata/services/fixture-reactions-api"
)

// A table whose writes are heard gets its change queue: a D80 queue keyed
// by the row's key and serialized, marked with the table's own behaviors
// that hear its writes and the tables that hear them through a relation of
// it (Task's, heard by Project). Reactions adds no field, no sub-table and
// no deadline queue, and its API's workers each run their table's change
// queue alone (D103). The expanded IR of both fixtures is held to its
// golden.
func TestReactionsFixturesExpand(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(reactionsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		table   string
		heardBy []string
	}{{"Project", nil}, {"Task", []string{"Project"}}} {
		td := db.Types[want.table]
		if !td.Optimistic || len(td.ImplementsBehaviors()) != 1 || td.ImplementsBehaviors()[0].Name != "Reactions" || td.IsEngineType() {
			t.Errorf("%s = optimistic %v, implements %v, engine %v", want.table, td.Optimistic, td.ImplementsBehaviors(), td.IsEngineType())
		}
		queue := db.BehaviorQueueOf(want.table, ir.BehaviorQueueChanges)
		if queue == nil || queue.Name != want.table+"Change" || queue.Role != ir.RoleEmbeddedStruct || queue.Origin != ir.OriginBehavior {
			t.Fatalf("%s's change queue = %+v", want.table, queue)
		}
		marker := &ir.BehaviorQueue{Row: want.table, Kind: ir.BehaviorQueueChanges, Behaviors: []string{"Reactions"}, HeardBy: want.heardBy}
		if !queue.BehaviorQueue.Same(marker) {
			t.Errorf("%s's change queue marker = %+v, want %+v", want.table, queue.BehaviorQueue, marker)
		}
		if q := queue.Queue; q == nil || q.Key != "rowKey" || !q.Serialize || q.OnDuplicate != "" || q.Retain != "0s" || len(queue.Indexes) != 0 {
			t.Errorf("%s's change queue = %+v, indexes %v; want keyed by rowKey, serialized, appended, retained 0s", want.table, q, queue.Indexes)
		}
		var fields []string
		for _, fd := range queue.Fields {
			fields = append(fields, fd.Name+":"+fd.TypeRef.Name)
		}
		if wantFields := []string{"rowKey:Identity.UUID", "write:string", "at:Generic.Int64", "before:Generic.JSON", "after:Generic.JSON"}; !slices.Equal(fields, wantFields) {
			t.Errorf("%s's change queue fields = %v, want %v", want.table, fields, wantFields)
		}
		if db.BehaviorQueueOf(want.table, ir.BehaviorQueueDeadlines) != nil {
			t.Errorf("%s has a deadline queue, and Reactions arms none", want.table)
		}
	}
	compareExpanded(t, db, "fixture-reactions-db.expanded.json")

	api, err := LoadService(filepath.FromSlash(reactionsAPIFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		worker, queue, deployable string
		concurrency               int
	}{
		{"ProjectReactions", "ProjectChange", "fixture-reactions-api-project-reactions", 0},
		{"TaskReactions", "TaskChange", "fixture-reactions-api-task-reactions", 2},
	} {
		worker := api.Worker(want.worker)
		if worker == nil || worker.Queue != want.queue || !slices.Equal(worker.Queues, []string{want.queue}) || worker.Origin != ir.OriginBehavior || worker.Concurrency != want.concurrency {
			t.Errorf("%s = %+v", want.worker, worker)
			continue
		}
		if got := ir.WorkerDeployableName(api.Name, worker.Name); got != want.deployable {
			t.Errorf("%s's deployable = %s", want.worker, got)
		}
	}
	var names []string
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			names = append(names, set.Name+" "+op.Name+" "+op.RestPath+" "+op.MCP.Handle)
		}
	}
	want := []string{
		"ProjectActions statusTransition project/{id}/status/transition project_status_transition",
		"TaskActions statusTransition task/{id}/status/transition task_status_transition",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("the operations =\n%v\nwant\n%v (Reactions has none)", names, want)
	}
	compareExpanded(t, api, "fixture-reactions-api.expanded.json")
}
