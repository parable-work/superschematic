package registry

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// agentDecorators returns the decorators of the agent orchestration
// (D158): @agents("<part>") on each DB class that is one of its tables,
// from @superschematic/db, and @autonomy(class, mode?) on an API
// operation, from @superschematic/api, which says what the operation is to
// an agent's autonomy policy. An operation that moves money is ask and
// nothing else: Apply refuses another mode, so the load refuses it.
func agentDecorators() []DecoratorSpec {
	return []DecoratorSpec{
		{
			Name: "agents", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if n.Type.Agents != nil {
					return fmt.Errorf("a class takes one @agents")
				}
				shape := fmt.Sprintf("@agents(part), part one of %s", strings.Join(ir.AgentParts, ", "))
				if len(args) != 1 {
					return fmt.Errorf("@agents on %s takes one argument: %s", n.Type.Name, shape)
				}
				part, ok := args[0].(string)
				if !ok || !slices.Contains(ir.AgentParts, part) {
					return ArgErrorf(0, "@agents on %s: %v is no part of the agent orchestration: %s", n.Type.Name, args[0], shape)
				}
				n.Type.Agents = &ir.AgentsDef{Part: part}
				return nil
			},
		},
		{
			Name: "autonomy", Packages: []string{pkgAPI}, Target: TargetOperation,
			Apply: func(n Node, args []any, _ Site) error {
				if n.Field.Autonomy != nil {
					return fmt.Errorf("operation %s has more than one @autonomy", n.Field.Name)
				}
				autonomy, err := autonomyOf(n.Field.Name, args)
				if err != nil {
					return err
				}
				n.Field.Autonomy = autonomy
				return nil
			},
		},
	}
}

// autonomyOf reads @autonomy(class, mode?) on the operation named op: the
// class a policy row may name it by, and its mode when no row names it.
func autonomyOf(op string, args []any) (*ir.OperationAutonomy, error) {
	shape := fmt.Sprintf("@autonomy(class, mode?), class one of %s and mode one of %s", strings.Join(ir.AgentClasses, ", "), strings.Join(ir.AutonomyModes, ", "))
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("@autonomy on %s takes a class and an optional mode: %s", op, shape)
	}
	out := &ir.OperationAutonomy{}
	for i, arg := range args {
		text, ok := arg.(string)
		if !ok || text == "" {
			return nil, ArgErrorf(i, "@autonomy on %s: %v is no string: %s", op, arg, shape)
		}
		if i == 0 {
			out.Class = text
		} else {
			out.Mode = text
		}
	}
	if err := ir.ValidateOperationAutonomy(out); err != nil {
		return nil, ArgErrorf(0, "@autonomy on %s: %s", op, err)
	}
	return out, nil
}

// agentFeatures are the agent orchestration as runtime features (D159):
// an API whose databases declare an @agents class runs agent runs, each in
// a sandbox instance whose turns a model completes, so the stack refuses
// an environment that runs such an API with no sandbox slot or no model
// slot set.
func agentFeatures() []RuntimeFeature {
	uses := func(in RuntimeFeatureInput) bool {
		if in.API.UsesAgents() {
			return true
		}
		for _, db := range in.Databases {
			if db.UsesAgents() {
				return true
			}
		}
		return false
	}
	return []RuntimeFeature{
		{Name: "agents", Interface: ir.RuntimeSandbox, Uses: uses},
		{Name: "agents", Interface: ir.RuntimeModel, Uses: uses},
	}
}
