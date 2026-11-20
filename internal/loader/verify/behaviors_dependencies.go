package verify

import (
	"encoding/json"
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// dependenciesBehavior is the name of the core's Dependencies (D104's step
// 4).
const dependenciesBehavior = "Dependencies"

// checkDependenciesConfig holds the config of a Dependencies use the table
// td implements to the tables of its database (D104's step 4), each
// problem `type <T>: behavior Dependencies config: <reason>` and the first
// reported: the table composes one Workflow, whose transitions the gate
// holds, and each table schemas names (the table itself when it names
// none) is a table of the database that composes one Workflow, whose
// rows may block. These read the IR; the gated states against td's
// Workflow are the core's define check's, at the first command. A config
// the configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkDependenciesConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Schemas []string `json:"schemas"`
	}
	if len(ref.Config) > 0 && json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
	}
	switch workflows := workflowUses(td); len(workflows) {
	case 0:
		fail("it gates the type's Workflow, which the type does not list")
		return
	case 1:
	default:
		fail("it gates the type's Workflow, and the type has several (%s): Dependencies gates a type's one Workflow", strings.Join(workflows, ", "))
		return
	}
	for _, name := range config.Schemas {
		if name == td.Name {
			continue
		}
		blocker := schema.Types[name]
		if blocker == nil || blocker.Role != ir.RoleDBTable || blocker.JsonField || blocker.IsEngineType() {
			fail("schemas names %s, which is not a table of %s", name, schema.Name)
			return
		}
		switch workflows := workflowUses(blocker); len(workflows) {
		case 0:
			fail("schemas names %s, which does not compose Workflow, so its rows cannot block", name)
			return
		case 1:
		default:
			fail("schemas names %s, which has several Workflows (%s): a blocker is read by its table's one Workflow", name, strings.Join(workflows, ", "))
			return
		}
	}
}
