package generator

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// rustBehaviorRun is a run of fixture-workflow-api (D103), its API served in
// Rust, over fixture-workflow-db, with paths and the core's registry: what
// generateRustAPI builds the crate from. edit changes the API's schema
// first.
func rustBehaviorRun(t *testing.T, paths naming.LocalPaths, edit func(*ir.Schema)) run {
	t.Helper()
	return rustBehaviorRunOf(t, "fixture-workflow-db", "fixture-workflow-api", paths, edit)
}

// rustBehaviorRunOf is a run of the fixture API apiName, served in Rust,
// over the fixture database dbName, as rustBehaviorRun is of
// fixture-workflow-api.
func rustBehaviorRunOf(t *testing.T, dbName, apiName string, paths naming.LocalPaths, edit func(*ir.Schema)) run {
	t.Helper()
	reg := CoreRegistry(naming.Default())
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for _, name := range []string{dbName, apiName} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	api, cfg := schemas[apiName], configs[apiName]
	if edit != nil {
		edit(api)
	}
	cfg.Outputs = map[string]any{
		"types": map[string]any{"rust": map[string]any{"enabled": true}},
		"api":   map[string]any{"enabled": true, "language": APILanguageRust},
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, reg)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRun(api, cfg, outputs, Options{
		// rustAPI writes nothing: the output root places the crate under
		// /repo, beside the checkout's crates.
		OutputRoot: "/repo/schemas/dist",
		Paths:      paths,
		Naming:     naming.Default(),
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		LoadDependency: func(name string) (*ir.Schema, error) {
			if schema, ok := schemas[name]; ok {
				return schema, nil
			}
			return nil, fmt.Errorf("no fixture %s", name)
		},
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			cfg, ok := configs[name]
			return cfg, ok
		},
		Stage: registry.StageAll,
	}, reg)
	return r
}

// rustBehaviorPaths are a checkout's paths under /repo, the HTTP runtime's
// and the two crates the behavior operations run through among them.
func rustBehaviorPaths() naming.LocalPaths {
	return naming.LocalPaths{
		HTTPRuntimeRust:    "/repo/runtime/http/rust",
		RulesAdapterRust:   "/repo/runtime/engine/rust/rules-adapter",
		RulesBehaviorsRust: "/repo/runtime/engine/rust/rules-behaviors",
	}
}

// TestRustBehaviorDeps: the Rust crate of an API with behavior operations
// (D103) takes, in its Deps, its database's layout document and the paths
// of superschematic-rules-adapter and superschematic-rules-behaviors from
// the naming file's [paths], relative to the crate. Both crates are
// unpublished, so a build whose naming file leaves either unset is
// refused, naming the server, the first table, field, behavior and set,
// the crate, the key and the
// checkout's directory; an API without behavior operations needs neither.
func TestRustBehaviorDeps(t *testing.T) {
	output, _, err := rustBehaviorRun(t, rustBehaviorPaths(), nil).rustAPI()
	if err != nil {
		t.Fatalf("rust api: %v", err)
	}
	b := output.Deps.Behaviors
	if b == nil {
		t.Fatal("the crate's Deps name no behaviors")
	}
	if output.Deps.Database != "fixture-workflow-db" || b.AdapterPath != "../../../../runtime/engine/rust/rules-adapter" || b.BehaviorsPath != "../../../../runtime/engine/rust/rules-behaviors" {
		t.Errorf("deps %q, adapter %q, behaviors %q; want fixture-workflow-db and the checkout's crates from the API crate", output.Deps.Database, b.AdapterPath, b.BehaviorsPath)
	}
	for _, want := range []string{`"Order": {`, `"Shipment": {`, `"table": "order"`, `"paymentStatus"`} {
		if !strings.Contains(b.Layout, want) {
			t.Errorf("the layout does not contain %s:\n%s", want, b.Layout)
		}
	}

	for key, unset := range map[string]func(*naming.LocalPaths){
		"rules_adapter_rust":   func(p *naming.LocalPaths) { p.RulesAdapterRust = "" },
		"rules_behaviors_rust": func(p *naming.LocalPaths) { p.RulesBehaviorsRust = "" },
	} {
		paths := rustBehaviorPaths()
		unset(&paths)
		_, _, err := rustBehaviorRun(t, paths, nil).rustAPI()
		crate, dir := "superschematic-rules-adapter", "runtime/engine/rust/rules-adapter"
		if key == "rules_behaviors_rust" {
			crate, dir = "superschematic-rules-behaviors", "runtime/engine/rust/rules-behaviors"
		}
		want := "generator: the Rust server of fixture-workflow-api runs the behavior operations of Order's status, a Workflow, which @behaviorOperations on OrderActions serves, among others (D103) through " + crate + ", an unpublished crate the naming file's [paths] " + key + " names, and it is unset; set it to a superschematic checkout's " + dir
		if err == nil || err.Error() != want {
			t.Errorf("[paths] %s unset: %v\nwant %s", key, err, want)
		}
	}

	withoutSets := func(schema *ir.Schema) {
		schema.OperationSets = slices.DeleteFunc(schema.OperationSets, (*ir.OperationSet).IsBehaviorOperations)
	}
	output, _, err = rustBehaviorRun(t, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, withoutSets).rustAPI()
	if err != nil {
		t.Fatalf("an API without behavior operations: %v", err)
	}
	if output.Deps.Behaviors != nil || output.HasBehaviorOperations() {
		t.Errorf("an API without behavior operations has behaviors %+v", output.Deps.Behaviors)
	}
}

// TestRustBehaviorDepsOfABehaviorWorker: the Rust crate of
// fixture-timers-api (D103), whose @behaviorWorker TicketTimers runs
// Ticket's deadlines and changes beside its @behaviorOperations set, takes
// its database's layout, Ticket's deadline queue in it, and its database's
// Rust types, which declare the queue's message; a refusal of a crate that
// runs a behavior worker alone names the table, what the worker runs and
// the worker.
func TestRustBehaviorDepsOfABehaviorWorker(t *testing.T) {
	output, _, err := rustBehaviorRunOf(t, "fixture-timers-db", "fixture-timers-api", rustBehaviorPaths(), nil).rustAPI()
	if err != nil {
		t.Fatalf("rust api: %v", err)
	}
	if len(output.BehaviorWorkers) != 1 || len(output.Workers) != 0 {
		t.Fatalf("workers %+v and behavior workers %+v, want TicketTimers a behavior worker alone", output.Workers, output.BehaviorWorkers)
	}
	b := output.Deps.Behaviors
	if b == nil || !strings.Contains(b.Layout, `"ticket_deadline_queue"`) {
		t.Fatalf("the crate's Deps name no layout of Ticket's deadline queue: %+v", b)
	}
	if output.Deps.DatabaseTypesCrate != "schemas-fixture-timers-db-types" {
		t.Errorf("the crate re-exports %q, want fixture-timers-db's Rust types", output.Deps.DatabaseTypesCrate)
	}
	schema := &ir.Schema{Workers: []*ir.Worker{{Name: "Plain", Queue: "Q"}, {Name: "TicketTimers", BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Ticket"}}}}
	if got, want := behaviorWorkerUse(schema, nil), "Ticket's deadlines by its @behaviorWorker TicketTimers"; got != want {
		t.Errorf("behaviorWorkerUse: %q, want %q", got, want)
	}
	reg := CoreRegistry(naming.Default())
	db, err := loader.LoadService(filepath.Join(tsFixtures, "fixture-timers-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := behaviorWorkerUse(schema, db), "Ticket's deadlines and changes by its @behaviorWorker TicketTimers"; got != want {
		t.Errorf("behaviorWorkerUse with the database: %q, want %q", got, want)
	}
	arming := &ir.Schema{Workers: []*ir.Worker{{Name: "TicketTimers", Queue: "TicketDeadline", Queues: []string{"TicketDeadline"}, BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Ticket"}}}}
	if got, want := behaviorWorkerUse(arming, nil), "Ticket's deadlines by its @behaviorWorker TicketTimers"; got != want {
		t.Errorf("behaviorWorkerUse of a worker of deadlines alone: %q, want %q", got, want)
	}
	reactions, err := loader.LoadService(filepath.Join(tsFixtures, "fixture-reactions-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	hearing := &ir.Schema{Workers: []*ir.Worker{{Name: "TaskReactions", BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Task"}}}}
	if got, want := behaviorWorkerUse(hearing, reactions), "Task's changes by its @behaviorWorker TaskReactions"; got != want {
		t.Errorf("behaviorWorkerUse of a worker of changes alone: %q, want %q", got, want)
	}
	if got := behaviorWorkerUse(&ir.Schema{}, db); got != "" {
		t.Errorf("behaviorWorkerUse of a schema with no behavior worker: %q", got)
	}
}
