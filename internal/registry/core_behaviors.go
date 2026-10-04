package registry

import (
	"embed"
	"path"
)

// coreBehaviorFiles holds the declarations of the behaviors the core
// declares, one JSON file each (D16 in docs/DECISIONS.md), under bare
// names: every behavior @superschematic/engine implements, and every one
// the optional @superschematic/engine-workqueue package does. Each
// package carries a copy of the declarations it implements, which
// `superschematic behaviors --package <name> --out` writes and CI checks
// (docs/extension-model.md section 3.16).
//
//go:embed behaviors/*.behavior.json
var coreBehaviorFiles embed.FS

// The npm packages that implement the core's behaviors.
const (
	// EnginePackage is @superschematic/engine, which registers its
	// behaviors when an engine opens.
	EnginePackage = "@superschematic/engine"
	// WorkQueuePackage is @superschematic/engine-workqueue, whose
	// behaviors a deployment registers with the engine to run them.
	WorkQueuePackage = "@superschematic/engine-workqueue"
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
	{"lease", WorkQueuePackage},
	{"assignment", WorkQueuePackage},
	{"queue", WorkQueuePackage},
	{"presence", WorkQueuePackage},
	{"blueprint", WorkQueuePackage},
	{"budget", WorkQueuePackage},
	{"retries", WorkQueuePackage},
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
