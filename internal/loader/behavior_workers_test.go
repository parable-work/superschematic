package loader

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// timersAPIDataService writes an API in the JSON form, config its
// schema.config.json and body its src/tickets.schema.json, and returns its
// directory with the option that reads db, by name, as a build's dependency
// loader does.
func timersAPIDataService(t *testing.T, config, body string, db *ir.Schema) (string, Option) {
	t.Helper()
	load := WithDependencyLoader(func(name string) (*ir.Schema, error) {
		if db == nil || name != db.Name {
			return nil, errors.New("no such service")
		}
		return db, nil
	})
	api := filepath.Join(t.TempDir(), "fixture-timers-api")
	for rel, contents := range map[string]string{"schema.config.json": config, "src/tickets.schema.json": body} {
		path := filepath.Join(api, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return api, load
}

// timersDB loads fixture-timers-db afresh, for a case to edit.
func timersDB(t *testing.T) *ir.Schema {
	t.Helper()
	db, err := LoadService(filepath.FromSlash(timersDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

const timersAPIConfig = `{"name": "fixture-timers-api", "kind": "API", "dependencies": [{"name": "fixture-timers-db", "kind": "DB"}], "outputs": {}}`

// TestBehaviorWorkerFromTheDataForms: a JSON API's worker with
// behaviorWorker and no queue loads as the TypeScript form's does, an
// ordinary worker of its table's deadline queue and change queue,
// deadlines first (D103).
func TestBehaviorWorkerFromTheDataForms(t *testing.T) {
	dir, load := timersAPIDataService(t, timersAPIConfig, `{
  "imports": [{"package": "@schemas/fixture-timers-db", "types": ["Ticket"]}],
  "workers": [{"name": "TicketTimers", "behaviorWorker": {"type": "Ticket", "permissions": ["tickets.escalate"]}, "concurrency": 2}]
}`, timersDB(t))
	schema, err := LoadService(dir, load)
	if err != nil {
		t.Fatal(err)
	}
	worker := schema.Worker("TicketTimers")
	if worker == nil || worker.Queue != "TicketDeadline" || strings.Join(worker.Queues, ",") != "TicketDeadline,TicketChange" || worker.Origin != ir.OriginBehavior || worker.Concurrency != 2 || len(worker.BehaviorWorker.Permissions) != 1 {
		t.Errorf("TicketTimers = %+v", worker)
	}
}

// TestBehaviorWorkerRefusals (D103): a behavior worker is refused, in
// these words, outside an API, with a queue of its own, on an API with no
// one database, over a table the API does not import from it, that it
// does not declare, that is no table, an engine type or a table whose
// behaviors arm no deadlines and hear no writes, and beside a second
// worker of its table.
func TestBehaviorWorkerRefusals(t *testing.T) {
	const imports = `"imports": [{"package": "@schemas/fixture-timers-db", "types": ["Ticket"]}]`
	worker := func(name string) string {
		return `{"name": "` + name + `", "behaviorWorker": {"type": "Ticket"}}`
	}
	for _, tc := range []struct {
		name, config, body string
		db                 func(*ir.Schema)
		want               string
	}{
		{"a queue of its own", timersAPIConfig, `{` + imports + `, "workers": [{"name": "TicketTimers", "queue": "Other", "behaviorWorker": {"type": "Ticket"}}]}`, nil,
			"@behaviorWorker TicketTimers names queue Other: a behavior worker runs its table's queues, which the build adds (D103)"},
		{"no database", `{"name": "fixture-timers-api", "kind": "API", "outputs": {}}`, `{` + imports + `, "workers": [` + worker("TicketTimers") + `]}`, nil,
			"TicketTimers: @behaviorWorker(Ticket) runs Ticket's behaviors on the API's database, and fixture-timers-api connects to none: set its authDb, or give it one DB dependency, the DB service that declares Ticket"},
		{"several databases", `{"name": "fixture-timers-api", "kind": "API", "dependencies": [{"name": "fixture-timers-db", "kind": "DB"}, {"name": "other-db", "kind": "DB"}], "outputs": {}}`,
			`{` + imports + `, "workers": [` + worker("TicketTimers") + `]}`, nil,
			"TicketTimers: @behaviorWorker(Ticket) runs Ticket's behaviors on the API's database, and fixture-timers-api names 2 DB dependencies and no authDb: set its authDb to the one that declares Ticket"},
		{"not imported", timersAPIConfig, `{"workers": [` + worker("TicketTimers") + `]}`, nil,
			"TicketTimers: @behaviorWorker names Ticket, which fixture-timers-api does not import; import it from fixture-timers-db, its database"},
		{"imported from elsewhere", timersAPIConfig, `{"imports": [{"package": "@schemas/other-db", "types": ["Ticket"]}], "workers": [` + worker("TicketTimers") + `]}`, nil,
			"TicketTimers: @behaviorWorker names Ticket, which fixture-timers-api imports from other-db, not from fixture-timers-db, its database (its authDb, or its one DB dependency)"},
		{"not declared", timersAPIConfig, `{` + imports + `, "workers": [` + worker("TicketTimers") + `]}`, func(db *ir.Schema) { delete(db.Types, "Ticket") },
			"TicketTimers: @behaviorWorker names Ticket, which fixture-timers-db does not declare"},
		{"not a table", timersAPIConfig, `{` + imports + `, "workers": [` + worker("TicketTimers") + `]}`, func(db *ir.Schema) { db.Types["Ticket"].Role = ir.RoleEmbeddedStruct },
			"TicketTimers: @behaviorWorker names Ticket, which is not a table of fixture-timers-db: a behavior worker runs a table's behaviors (D103)"},
		{"an engine type", timersAPIConfig, `{` + imports + `, "workers": [` + worker("TicketTimers") + `]}`, func(db *ir.Schema) { db.Types["Ticket"].Behaviors = []ir.BehaviorRef{{Name: "Comments"}} },
			"TicketTimers: @behaviorWorker names Ticket, an engine type of fixture-timers-db (D58), whose engine runs its timed work; a behavior worker runs a table's behaviors (D103)"},
		{"no deadlines and no changes", timersAPIConfig, `{` + imports + `, "workers": [` + worker("TicketTimers") + `]}`, func(db *ir.Schema) {
			db.Types["Ticket"].Behaviors = db.Types["Ticket"].FieldBehaviors()
			delete(db.Types, "TicketDeadline")
			delete(db.Types, "TicketChange")
		}, "TicketTimers: @behaviorWorker names Ticket, whose behaviors arm no deadlines, hear no writes and emit no events, so the worker has nothing to run (D103, D104)"},
		{"a second worker", timersAPIConfig, `{` + imports + `, "workers": [` + worker("TicketTimers") + `, ` + worker("OtherTimers") + `]}`, nil,
			"OtherTimers: @behaviorWorker names Ticket, which TicketTimers names too: an API runs a table's behaviors from one worker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := timersDB(t)
			if tc.db != nil {
				tc.db(db)
			}
			dir, load := timersAPIDataService(t, tc.config, tc.body, db)
			_, err := LoadService(dir, load)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error holding %q", err, tc.want)
			}
		})
	}

	// Outside an API, the pass refuses one in every form.
	schema := ir.NewSchema("fixture-timers-db", ir.SchemaKindDB)
	schema.Workers = []*ir.Worker{{Name: "TicketTimers", BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Ticket"}}}
	res := verify.CheckBehaviorWorkers(schema, verify.Input{})
	if err := res.Err(); err == nil || !strings.Contains(err.Error(), "TicketTimers: @behaviorWorker is only allowed in an API schema (this schema has kind DB)") {
		t.Errorf("CheckBehaviorWorkers outside an API = %v", err)
	}
}

// TestDeadlinesNoWorkerRunsAreAWarning (D103): an API that serves a
// table's behavior operations, whose behaviors arm deadlines and hear its
// writes, and runs no @behaviorWorker of it loads, with a warning that
// names the set, the table, the behaviors that keep each queue and the
// database; a table whose queues nothing runs is the author's to choose,
// and their messages wait.
func TestDeadlinesNoWorkerRunsAreAWarning(t *testing.T) {
	dir, load := timersAPIDataService(t, timersAPIConfig, `{
  "imports": [{"package": "@schemas/fixture-timers-db", "types": ["Ticket"]}],
  "operationSets": [{"name": "TicketActions", "operations": [], "behaviorOperations": {"type": "Ticket"}, "permissions": ["tickets.write"]}]
}`, timersDB(t))
	var warnings []string
	schema, err := LoadService(dir, load, WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) }))
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.OperationSets[0].Operations) != 4 {
		t.Errorf("TicketActions has %d operations, want 4", len(schema.OperationSets[0].Operations))
	}
	want := "TicketActions: Ticket's behaviors keep deadlines (Timers) and changes (Timers) for a worker to run, and fixture-timers-api declares no @behaviorWorker(Ticket): nothing runs them until a worker does; declare one on this API or another over fixture-timers-db (D103)"
	data, _ := json.Marshal(warnings)
	if len(warnings) != 1 || warnings[0] != want {
		t.Errorf("warnings = %s, want %q", data, want)
	}
}

// reactionsDB loads fixture-reactions-db afresh, for a case to edit.
func reactionsDB(t *testing.T) *ir.Schema {
	t.Helper()
	db, err := LoadService(filepath.FromSlash(reactionsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

const reactionsAPIConfig = `{"name": "fixture-reactions-api", "kind": "API", "dependencies": [{"name": "fixture-reactions-db", "kind": "DB"}], "outputs": {}}`

// TestChangesNoWorkerRunsAreAWarning (D103): the warning names a table's
// change queue by the behaviors of its own that hear its writes and the
// tables that hear them through a relation, and a table heard by another
// alone by those; a worker of a table with a change queue and no deadlines
// runs that queue alone.
func TestChangesNoWorkerRunsAreAWarning(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		db         func(*ir.Schema)
		want       string
	}{
		{"own and heard by", `{
  "imports": [{"package": "@schemas/fixture-reactions-db", "types": ["Task"]}],
  "operationSets": [{"name": "TaskActions", "operations": [], "behaviorOperations": {"type": "Task"}}]
}`, nil, "TaskActions: Task's behaviors keep changes (Reactions; heard by Project) for a worker to run, and fixture-reactions-api declares no @behaviorWorker(Task): nothing runs them until a worker does; declare one on this API or another over fixture-reactions-db (D103)"},
		{"heard by alone", `{
  "imports": [{"package": "@schemas/fixture-reactions-db", "types": ["Task"]}],
  "operationSets": [{"name": "TaskActions", "operations": [], "behaviorOperations": {"type": "Task"}}]
}`, func(db *ir.Schema) { db.Types["TaskChange"].BehaviorQueue.Behaviors = nil },
			"TaskActions: Task's behaviors keep changes (heard by Project) for a worker to run, and fixture-reactions-api declares no @behaviorWorker(Task): nothing runs them until a worker does; declare one on this API or another over fixture-reactions-db (D103)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := reactionsDB(t)
			if tc.db != nil {
				tc.db(db)
			}
			dir, load := timersAPIDataService(t, reactionsAPIConfig, tc.body, db)
			var warnings []string
			if _, err := LoadService(dir, load, WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(warnings)
			if len(warnings) != 1 || warnings[0] != tc.want {
				t.Errorf("warnings = %s, want %q", data, tc.want)
			}
		})
	}

	dir, load := timersAPIDataService(t, reactionsAPIConfig, `{
  "imports": [{"package": "@schemas/fixture-reactions-db", "types": ["Task"]}],
  "workers": [{"name": "TaskReactions", "behaviorWorker": {"type": "Task"}}]
}`, reactionsDB(t))
	schema, err := LoadService(dir, load)
	if err != nil {
		t.Fatal(err)
	}
	if worker := schema.Worker("TaskReactions"); worker == nil || worker.Queue != "TaskChange" || strings.Join(worker.Queues, ",") != "TaskChange" {
		t.Errorf("TaskReactions = %+v, want a worker of TaskChange alone", worker)
	}
}

// TestListenedTableNoWorkerRunsIsAWarning (D103): an API that serves a
// table whose Reactions hear another table's writes through a relation of
// it, and runs that table's worker but no @behaviorWorker of the other,
// whose change queue carries those writes, loads with a warning naming the
// set, both tables, the link and the database; with a worker of the other
// table, it loads with none.
func TestListenedTableNoWorkerRunsIsAWarning(t *testing.T) {
	for _, tc := range []struct {
		name, workers string
		want          []string
	}{
		{"no worker of the heard table", `[{"name": "ProjectReactions", "behaviorWorker": {"type": "Project"}}]`, []string{
			"ProjectActions: Project's Reactions hear Task's writes through Task.project, which Task's change queue carries, and fixture-reactions-api declares no @behaviorWorker(Task): Project's rules over Task do not run until a worker of Task does; declare one on this API or another over fixture-reactions-db (D103)",
		}},
		{"a worker of each", `[{"name": "ProjectReactions", "behaviorWorker": {"type": "Project"}}, {"name": "TaskReactions", "behaviorWorker": {"type": "Task"}}]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, load := timersAPIDataService(t, reactionsAPIConfig, `{
  "imports": [{"package": "@schemas/fixture-reactions-db", "types": ["Project", "Task"]}],
  "operationSets": [{"name": "ProjectActions", "operations": [], "behaviorOperations": {"type": "Project"}}],
  "workers": `+tc.workers+`
}`, reactionsDB(t))
			var warnings []string
			if _, err := LoadService(dir, load, WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(warnings)
			if strings.Join(warnings, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("warnings = %s, want %q", data, tc.want)
			}
		})
	}
}
