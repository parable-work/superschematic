package verify

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkBehaviors holds every type's behaviors to their declarations in the
// registry (D16): each behavior is registered and listed once, its config
// passes its config schema, what it requires is on the type and what it
// conflicts with is not, and no two behaviors add an operation of the same
// name. The fields a behavior adds cannot collide: an instance keeps them
// under the behavior's name, apart from the type's own fields (D16,
// amended: a behavior's fields sit under its name). A declaration's own
// field and operation names are checked when it registers.
func checkBehaviors(schema *ir.Schema, reg *registry.Registry, r *Result) {
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			if td := types[name]; td != nil && len(td.Behaviors) > 0 {
				checkTypeBehaviors(td, reg, r)
			}
		}
	}
}

func checkTypeBehaviors(td *ir.TypeDef, reg *registry.Registry, r *Result) {
	listed := make([]string, 0, len(td.Behaviors))
	var declared []registry.Behavior
	for _, ref := range td.Behaviors {
		if slices.Contains(listed, ref.Name) {
			r.errorf(td.Owner, "type %s lists behavior %s twice", td.Name, ref.Name)
			continue
		}
		listed = append(listed, ref.Name)
		behavior, ok := reg.Behavior(ref.Name)
		if !ok {
			known := "none are registered"
			if names := reg.BehaviorNames(); len(names) > 0 {
				known = "registered: " + strings.Join(names, ", ")
			}
			r.errorf(td.Owner, "type %s: behavior %q is not a registered behavior (%s)", td.Name, ref.Name, known)
			continue
		}
		if err := behavior.ValidateConfig(ref.Config); err != nil {
			r.errorf(td.Owner, "type %s: %s", td.Name, err)
		}
		declared = append(declared, behavior)
	}

	opOwner := map[string]string{}
	for _, behavior := range declared {
		for _, required := range behavior.Requires {
			if !slices.Contains(listed, required) {
				r.errorf(td.Owner, "type %s: behavior %s requires behavior %s, which the type does not list", td.Name, behavior.Name, required)
			}
		}
		for _, conflict := range behavior.Conflicts {
			if slices.Contains(listed, conflict) {
				r.errorf(td.Owner, "type %s: behavior %s conflicts with behavior %s, which the type also lists", td.Name, behavior.Name, conflict)
			}
		}
		for _, op := range behavior.Operations {
			if prev, taken := opOwner[op.Name]; taken {
				r.errorf(td.Owner, "type %s: behaviors %s and %s both add operation %s", td.Name, prev, behavior.Name, op.Name)
				continue
			}
			opOwner[op.Name] = behavior.Name
		}
	}
}
