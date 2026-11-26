package agents_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

const fixtures = "../tsreader/testdata/services"

const fixture = "fixture-agent-runs-db"

func fieldNames(td *ir.TypeDef) []string {
	var out []string
	for _, fd := range td.Fields {
		out = append(out, fd.Name)
	}
	return out
}

// TestAgentsFixtureLoads (D158): every part of fixture-agent-runs-db holds
// the fields the build adds, with the agents origin, beside the ones the
// class declares itself (the runs' Workflow status, the controls' budget
// limit and scope), which keep their own; the definitions are versioned;
// and an API over the database uses the agents feature, which needs a
// sandbox provider and a model provider.
func TestAgentsFixtureLoads(t *testing.T) {
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixtures, fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := map[string][]string{
		ir.AgentDefinitions: {"id", "name", "purpose", "instructions", "task", "role", "tools", "credentials", "model", "modelSlot", "sandboxSlot", "command", "limits", "inbound"},
		ir.AgentRuns:        {"id", "status", "agent", "definitionVersion", "trigger", "triggerTable", "triggerKey", "trust", "targets", "grants", "instance", "outcome", "reads", "writes", "messagesSent", "approvals", "untrusted", "cost", "credentialUses", "transcriptObject"},
		ir.AgentRequests:    {"step", "agent", "trigger", "subjectTable", "subjectKey", "party", "message", "grant", "run", "ask"},
		ir.AgentPolicies:    {"agent", "operation", "operationClass", "mode", "permission"},
		ir.AgentApprovals:   {"agent", "run", "operation", "params", "paramsHash", "summary", "permission", "state", "decidedBy", "decidedVia"},
		ir.AgentControls:    {"id", "budgetLimit", "total", "agent", "paused", "pausedReason", "warnAt"},
	}
	own := map[string][]string{
		ir.AgentRuns:      {"id", "status"},
		ir.AgentControls:  {"id", "budgetLimit", "total"},
		ir.AgentApprovals: {"summary"},
	}
	for part, fields := range want {
		td := db.AgentsTable(part)
		if td == nil {
			t.Errorf("no %s part", part)
			continue
		}
		if names := fieldNames(td); !containsAll(names, fields) {
			t.Errorf("%s (%s) has %v, want %v among them", td.Name, part, names, fields)
		}
		for _, fd := range td.Fields {
			mine := slices.Contains(own[part], fd.Name)
			if mine && fd.Origin == ir.OriginAgents {
				t.Errorf("%s.%s is the class's own, and has the agents origin", td.Name, fd.Name)
			}
			if !mine && slices.Contains(fields, fd.Name) && fd.Origin != ir.OriginAgents {
				t.Errorf("%s.%s is the build's, and has the origin %q", td.Name, fd.Name, fd.Origin)
			}
		}
	}
	if !db.AgentsTable(ir.AgentDefinitions).Versioned {
		t.Error("the definitions are not versioned")
	}
	if db.AgentsTable(ir.AgentRequests).Queue == nil {
		t.Error("the requests part is no queue")
	}
	if !db.UsesAgents() {
		t.Error("the fixture uses no agents")
	}
	used := reg.RuntimeFeaturesUsed(registry.RuntimeFeatureInput{API: &ir.Schema{Name: "api"}, Databases: map[string]*ir.Schema{db.Name: db}})
	var interfaces []string
	for _, use := range used {
		if use.Feature == "agents" {
			interfaces = append(interfaces, use.Interface)
		}
	}
	slices.Sort(interfaces)
	if !slices.Equal(interfaces, []string{ir.RuntimeModel, ir.RuntimeSandbox}) {
		t.Errorf("RuntimeFeaturesUsed = %v", used)
	}
}

func containsAll(names, want []string) bool {
	for _, name := range want {
		if !slices.Contains(names, name) {
			return false
		}
	}
	return true
}

// TestAgentsRefusals (D158): the verification pass refuses definitions that
// are not versioned, the requests part on no queue, a part declared twice,
// a schema that leaves out a part, and a field of the orchestration's
// declared as another scalar.
func TestAgentsRefusals(t *testing.T) {
	base, err := os.ReadFile(filepath.Join(fixtures, fixture, "src", "agents.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"definitions not versioned": {
			"@versioned\n@agents(\"definitions\")", "@agents(\"definitions\")",
			"mark the class @versioned",
		},
		"requests on no queue": {
			"@queue()\n@agents(\"requests\")", "@agents(\"requests\")",
			"mark the class @queue() too",
		},
		"no policies": {
			"@agents(\"policies\")\nexport abstract class AutonomyPolicy {}", "",
			`declares no @agents("policies")`,
		},
		"a part twice": {
			"@agents(\"policies\")\nexport abstract class AutonomyPolicy {}", "@agents(\"policies\")\nexport abstract class AutonomyPolicy {}\n@agents(\"policies\")\nexport abstract class OtherPolicy {}",
			`marks AutonomyPolicy and OtherPolicy`,
		},
		"a field of another scalar": {
			"export abstract class AutonomyPolicy {}", "export abstract class AutonomyPolicy {\n  mode: Generic.Int64;\n}",
			"mode is the orchestration's field, a string",
		},
		"no such part": {
			`@agents("controls")`, `@agents("switches")`,
			"is not assignable to parameter of type 'AgentsPart'",
		},
	} {
		t.Run(name, func(t *testing.T) {
			text := string(base)
			if !strings.Contains(text, tc.from) {
				t.Fatalf("the fixture has no %q", tc.from)
			}
			dir, err := os.MkdirTemp(fixtures, "tmp-agents-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			copyDir(t, filepath.Join(fixtures, fixture), dir)
			if err := os.WriteFile(filepath.Join(dir, "src", "agents.schema.ts"), []byte(strings.Replace(text, tc.from, tc.to, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = loader.LoadService(dir, loader.WithRegistry(registry.New(naming.Default())))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAgentOrchestrationExampleLoads: examples/agent-orchestration's schemas
// load (D158): work-db holds every part of the orchestration, work-api runs
// the dispatcher on a worker of its request queue and of section 7's run
// requests (D157), beside the notifier's,
// its operations carry their autonomy, and the API uses the agents feature
// (a sandbox and a model) and the notifications feature (a channel).
func TestAgentOrchestrationExampleLoads(t *testing.T) {
	services := filepath.Join("..", "..", "..", "examples", "agent-orchestration", "schemas", "services")
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(services, "work-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load work-db: %v", err)
	}
	for _, part := range ir.AgentParts {
		if db.AgentsTable(part) == nil {
			t.Errorf("work-db declares no %s", part)
		}
	}
	api, err := loader.LoadService(filepath.Join(services, "work-api"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load work-api: %v", err)
	}
	workers := map[string]*ir.Worker{}
	for _, w := range api.Workers {
		workers[w.Name] = w
	}
	if w := workers["Dispatch"]; w == nil || w.Queue != "AgentRunRequest" {
		t.Errorf("Dispatch is %+v", w)
	}
	if w := workers["InboundRuns"]; w == nil || w.Queue != "RunRequest" {
		t.Errorf("InboundRuns is %+v", w)
	}
	classes := map[string]string{}
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if op.Autonomy != nil {
				classes[op.Name] = op.Autonomy.Class
			}
		}
	}
	if classes["payInvoice"] != ir.AgentClassMoneyMovement || classes["getTask"] != ir.AgentClassRead {
		t.Errorf("the operations' autonomy is %v", classes)
	}
	used := reg.RuntimeFeaturesUsed(registry.RuntimeFeatureInput{API: api, Databases: map[string]*ir.Schema{db.Name: db}})
	var got []string
	for _, use := range used {
		got = append(got, use.Feature+":"+use.Interface)
	}
	slices.Sort(got)
	if want := []string{"agents:model", "agents:sandbox", "notifications:channel"}; !slices.Equal(got, want) {
		t.Errorf("work-api uses %v, want %v", got, want)
	}
}

// TestAutonomyRefusesMoneyWithoutAsking (D158): an operation that moves
// money and says it runs on its own is the load's refusal.
func TestAutonomyRefusesMoneyWithoutAsking(t *testing.T) {
	services := filepath.Join("..", "..", "..", "examples", "agent-orchestration", "schemas", "services")
	base, err := os.ReadFile(filepath.Join(services, "work-api", "src", "work.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	from := `@autonomy("moneyMovement")`
	if !strings.Contains(string(base), from) {
		t.Fatalf("the example has no %s", from)
	}
	dir, err := os.MkdirTemp(services, "tmp-work-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	copyDir(t, filepath.Join(services, "work-api"), dir)
	text := strings.Replace(string(base), from, `@autonomy("moneyMovement", "auto")`, 1)
	if err := os.WriteFile(filepath.Join(dir, "src", "work.schema.ts"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = loader.LoadService(dir, loader.WithRegistry(registry.New(naming.Default())))
	if err == nil || !strings.Contains(err.Error(), "an agent may only ask to move money") {
		t.Fatalf("load = %v, want the refusal of money that moves on its own", err)
	}
}
