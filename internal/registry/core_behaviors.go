package registry

import (
	"embed"
	"path"
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
