package registry

import (
	"embed"
	"path"
)

// coreBehaviorFiles holds the declarations of the behaviors the core
// declares, one JSON file each (D16 in docs/DECISIONS.md): every behavior
// @superschematic/engine implements, under a bare name. The engine carries
// a copy of each, which `superschematic behaviors --out` writes and CI
// checks (docs/extension-model.md section 3.16).
//
//go:embed behaviors/*.behavior.json
var coreBehaviorFiles embed.FS

// coreBehaviors are the core's behavior declarations, in the order New
// registers them.
var coreBehaviors = []string{"workflow", "comments", "revisions"}

// coreBehaviorSpecs returns the core's BehaviorSpecs. A declaration that
// does not read is a defect of the build, as a core decorator that does not
// register is.
func coreBehaviorSpecs() []BehaviorSpec {
	specs := make([]BehaviorSpec, 0, len(coreBehaviors))
	for _, name := range coreBehaviors {
		data, err := coreBehaviorFiles.ReadFile(path.Join("behaviors", name+".behavior.json"))
		if err != nil {
			panic("registry: core behavior " + name + ": " + err.Error())
		}
		specs = append(specs, BehaviorSpec{Declaration: data})
	}
	return specs
}
