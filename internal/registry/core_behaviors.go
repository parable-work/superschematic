package registry

import (
	"embed"
	"encoding/json"
	"path"
	"slices"
)

// coreBehaviorFiles holds the declarations of the behaviors the core
// declares, one JSON file each (D16 in docs/DECISIONS.md), under bare
// names: every behavior @superschematic/engine ships, every one the
// optional @superschematic/engine-workqueue package does, and the commerce
// behaviors the optional @superschematic/engine-commerce package does
// (D70). Each package's Rust crate (D58: runtime/engine/rust/behaviors,
// runtime/engine-workqueue/rust, runtime/engine-commerce/rust)
// carries a copy of the declarations it implements, which `superschematic
// behaviors --package <name> --out` writes and CI checks
// (docs/extension-model.md section 3.16).
//
//go:embed behaviors/*.behavior.json
var coreBehaviorFiles embed.FS

// The npm packages that ship the core's behaviors, which name them in a
// behavior's spec and the engine document (enginedoc).
const (
	// EnginePackage is @superschematic/engine, whose engine registers its
	// behaviors when it opens.
	EnginePackage = "@superschematic/engine"
	// WorkQueuePackage is @superschematic/engine-workqueue, whose
	// behaviors (in the engine's addon, by name) a deployment registers
	// with the engine to run them.
	WorkQueuePackage = "@superschematic/engine-workqueue"
	// CommercePackage is @superschematic/engine-commerce, whose behaviors
	// (D70: Stock, Calendar, Holds and Payments) a deployment registers
	// with the engine to run them, as it registers the work queue's.
	CommercePackage = "@superschematic/engine-commerce"
)

// coreBehaviors are the core's behavior declarations, by file name, with
// the package that implements each, in the order New registers them.
var coreBehaviors = []struct{ file, pkg string }{
	{"workflow", EnginePackage},
	{"comments", EnginePackage},
	{"revisions", EnginePackage},
	{"dependencies", EnginePackage},
	{"links", EnginePackage},
	{"rollups", EnginePackage},
	{"search", EnginePackage},
	{"reactions", EnginePackage},
	{"constants", EnginePackage},
	{"variants", EnginePackage},
	{"branches", EnginePackage},
	{"staterules", EnginePackage},
	{"approvals", EnginePackage},
	{"numbering", EnginePackage},
	{"timers", EnginePackage},
	{"derived", EnginePackage},
	{"provenance", EnginePackage},
	{"lease", WorkQueuePackage},
	{"assignment", WorkQueuePackage},
	{"queue", WorkQueuePackage},
	{"presence", WorkQueuePackage},
	{"blueprint", WorkQueuePackage},
	{"budget", WorkQueuePackage},
	{"retries", WorkQueuePackage},
	{"stock", CommercePackage},
	{"calendar", CommercePackage},
	{"holds", CommercePackage},
	{"payments", CommercePackage},
}

// coreBehaviorSpecs returns the core's BehaviorSpecs. A declaration that
// does not read is a defect of the build, as a core decorator that does not
// register is.
func coreBehaviorSpecs() []BehaviorSpec {
	specs := make([]BehaviorSpec, 0, len(coreBehaviors))
	for _, behavior := range coreBehaviors {
		data, err := coreBehaviorFiles.ReadFile(path.Join("behaviors", behavior.file+".behavior.json"))
		if err != nil {
			panic("registry: core behavior " + behavior.file + ": " + err.Error())
		}
		specs = append(specs, BehaviorSpec{Package: behavior.pkg, Declaration: data})
	}
	return specs
}

// RulesBehaviors are the behaviors the behaviors' core runs (D103): those
// superschematic-rules-behaviors registers (its NAMES), in its order, the
// engine's order of the first six and then the work queue's order of the
// four D104's step 2 ported in its second batch (Lease, Presence, Budget,
// Retries), so a build-time table may compose them. Of these, a table
// implements one that names no value field (implements Timers<T, C>, and
// Constants, StateRules and Approvals since D104's step 2, and Lease,
// Presence, Budget and Retries since its second batch) and carries one that
// does on a field (status: Workflow<C>). A test holds the list to the
// behaviors each transcript of runtime/engine/testdata/rules records.
var RulesBehaviors = []string{"Workflow", "Reactions", "Constants", "StateRules", "Approvals", "Timers", "Lease", "Presence", "Budget", "Retries"}

// RunsOnTheCore reports whether the behaviors' core runs the behavior name
// (RulesBehaviors).
func RunsOnTheCore(name string) bool {
	return slices.Contains(RulesBehaviors, name)
}

// BehaviorListen is a table whose writes a use of another table hears
// through a relation of that table (D103): Reactions' allTerminal over
// Task's project hears Task through task.project.
type BehaviorListen struct {
	Type string // the table whose writes are heard: Task
	Link string // its to-one relation to the listening table: project
}

// BehaviorListens are the tables a use of the behavior name with config
// listens to, in rule order, each once: Reactions' allTerminal and
// anyTerminal rules ({ schema, link }); none for any other behavior or a
// config that does not read. Per-behavior code, as Timers' check is, until
// step 5 derives it from the behavior's class (D103).
func BehaviorListens(name string, config json.RawMessage) []BehaviorListen {
	if name != "Reactions" || len(config) == 0 {
		return nil
	}
	type terminal struct {
		Schema string `json:"schema"`
		Link   string `json:"link"`
	}
	var parsed struct {
		Rules []struct {
			When struct {
				AllTerminal *terminal `json:"allTerminal"`
				AnyTerminal *terminal `json:"anyTerminal"`
			} `json:"when"`
		} `json:"rules"`
	}
	if json.Unmarshal(config, &parsed) != nil {
		return nil
	}
	var out []BehaviorListen
	for _, rule := range parsed.Rules {
		for _, heard := range []*terminal{rule.When.AllTerminal, rule.When.AnyTerminal} {
			if heard == nil || heard.Schema == "" || heard.Link == "" {
				continue
			}
			listen := BehaviorListen{Type: heard.Schema, Link: heard.Link}
			if !slices.Contains(out, listen) {
				out = append(out, listen)
			}
		}
	}
	return out
}
