package registry

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// behaviorOperationsDecorator registers @behaviorOperations(Type, options?) from
// @superschematic/api (D103): on a class with no members, which the walker
// reads as an operation set, the table whose behaviors' operations the set
// serves, and optional MCP overrides. A class takes it once, and not beside
// a user model's route set (D50). The loader adds the set's operations; the
// rules that read the API's database are the verification pass's.
func behaviorOperationsDecorator(invocation func() ToolInvocationPolicy) DecoratorSpec {
	return DecoratorSpec{
		Name: "behaviorOperations", Packages: []string{pkgAPI}, Target: TargetOperationSet,
		Apply: func(n Node, args []any, _ Site) error {
			return applyBehaviorOperations(args, n.OperationSet, invocation())
		},
	}
}

// applyBehaviorOperations reads @behaviorOperations(Type, options?), the table
// as a class reference and optional { mcp } overrides.
func applyBehaviorOperations(args []any, set *ir.OperationSet, invocation ToolInvocationPolicy) error {
	switch {
	case set.BehaviorOperations != nil:
		return fmt.Errorf("@behaviorOperations appears twice on the class: a set serves one table")
	case set.UserSessions != nil:
		return fmt.Errorf("@behaviorOperations contradicts @userSessions on the same class: a class takes one of them")
	case set.UserAdministration != nil:
		return fmt.Errorf("@behaviorOperations contradicts @userAdministration on the same class: a class takes one of them")
	case set.GraphOperations != nil:
		return fmt.Errorf("@behaviorOperations contradicts @graphOperations on the same class: a class takes one of them")
	case set.RowFeatureOperations != nil:
		return fmt.Errorf("@behaviorOperations contradicts @rowFeatureOperations on the same class: a class takes one of them")
	case set.TriageOperations != nil:
		return fmt.Errorf("@behaviorOperations contradicts @triageOperations on the same class: a class takes one of them")
	}
	if len(args) != 1 && len(args) != 2 {
		return fmt.Errorf("@behaviorOperations takes the table whose behaviors' operations the set serves, imported from the API's database, and optional options: @behaviorOperations(Order, { mcp: { writeInvocationPolicy: \"ask\" } })")
	}
	class, err := DecodeClassRef(args[0])
	if err != nil {
		return ArgErrorf(0, "@behaviorOperations takes the table as a class, imported from the API's database: %v", err)
	}
	cfg := &ir.BehaviorOperationsConfig{Type: class}
	if len(args) == 2 {
		options, ok := args[1].(map[string]any)
		if !ok {
			return ArgErrorf(1, "@behaviorOperations takes its options as an object, { mcp? }")
		}
		for _, key := range slices.Sorted(maps.Keys(options)) {
			switch key {
			case "mcp":
				mcp, err := parseBehaviorOperationsMCP(options[key], invocation)
				if err != nil {
					return err
				}
				cfg.MCP = mcp
			default:
				return ArgErrorf(1, "@behaviorOperations takes mcp, not %s", key)
			}
		}
	}
	set.BehaviorOperations = cfg
	return nil
}

func parseBehaviorOperationsMCP(value any, invocation ToolInvocationPolicy) (*ir.BehaviorOperationsMCP, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, ArgErrorf(1, "@behaviorOperations: mcp must be an object literal")
	}
	out := &ir.BehaviorOperationsMCP{}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		v := obj[key]
		switch key {
		case "hidden":
			b, ok := v.(bool)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: mcp.hidden must be a boolean literal")
			}
			out.Hidden = b
		case "reason":
			s, ok := v.(string)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: mcp.reason must be a string literal")
			}
			out.Reason = s
		case "invocationPolicy":
			s, ok := v.(string)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: mcp.invocationPolicy must be a string literal")
			}
			if err := invocation.CheckValue(s); err != nil {
				return nil, ArgErrorf(1, "@behaviorOperations: mcp.invocationPolicy: %s", err)
			}
			out.InvocationPolicy = s
		case "writeInvocationPolicy":
			s, ok := v.(string)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: mcp.writeInvocationPolicy must be a string literal")
			}
			if err := invocation.CheckValue(s); err != nil {
				return nil, ArgErrorf(1, "@behaviorOperations: mcp.writeInvocationPolicy: %s", err)
			}
			out.WriteInvocationPolicy = s
		case "behaviors":
			byUse, ok := v.(map[string]any)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: mcp.behaviors must be an object literal keyed by behavior member name")
			}
			out.Behaviors = make(map[string]*ir.BehaviorOperationMCP, len(byUse))
			for _, use := range slices.Sorted(maps.Keys(byUse)) {
				rule, err := parseBehaviorOperationMCP(byUse[use], "mcp.behaviors."+use, true, invocation)
				if err != nil {
					return nil, err
				}
				out.Behaviors[use] = rule
			}
		case "operations":
			byOp, ok := v.(map[string]any)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: mcp.operations must be an object literal keyed by \"<use>.<op>\" or operation name")
			}
			out.Operations = make(map[string]*ir.BehaviorOperationMCP, len(byOp))
			for _, opKey := range slices.Sorted(maps.Keys(byOp)) {
				rule, err := parseBehaviorOperationMCP(byOp[opKey], "mcp.operations."+opKey, false, invocation)
				if err != nil {
					return nil, err
				}
				out.Operations[opKey] = rule
			}
		default:
			return nil, ArgErrorf(1, "@behaviorOperations: mcp has unknown key %q; it takes hidden, reason, invocationPolicy, writeInvocationPolicy, behaviors and operations", key)
		}
	}
	if err := checkBehaviorMCPRule("mcp", out.Hidden, out.Reason, out.InvocationPolicy, out.WriteInvocationPolicy); err != nil {
		return nil, ArgErrorf(1, "@behaviorOperations: %s", err)
	}
	return out, nil
}

func parseBehaviorOperationMCP(value any, path string, allowWritePolicy bool, invocation ToolInvocationPolicy) (*ir.BehaviorOperationMCP, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, ArgErrorf(1, "@behaviorOperations: %s must be an object literal", path)
	}
	out := &ir.BehaviorOperationMCP{}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		v := obj[key]
		switch key {
		case "hidden":
			b, ok := v.(bool)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: %s.hidden must be a boolean literal", path)
			}
			out.Hidden = b
		case "reason":
			s, ok := v.(string)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: %s.reason must be a string literal", path)
			}
			out.Reason = s
		case "invocationPolicy":
			s, ok := v.(string)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: %s.invocationPolicy must be a string literal", path)
			}
			if err := invocation.CheckValue(s); err != nil {
				return nil, ArgErrorf(1, "@behaviorOperations: %s.invocationPolicy: %s", path, err)
			}
			out.InvocationPolicy = s
		case "writeInvocationPolicy":
			if !allowWritePolicy {
				return nil, ArgErrorf(1, "@behaviorOperations: %s does not take writeInvocationPolicy; use invocationPolicy on an individual operation", path)
			}
			s, ok := v.(string)
			if !ok {
				return nil, ArgErrorf(1, "@behaviorOperations: %s.writeInvocationPolicy must be a string literal", path)
			}
			if err := invocation.CheckValue(s); err != nil {
				return nil, ArgErrorf(1, "@behaviorOperations: %s.writeInvocationPolicy: %s", path, err)
			}
			out.WriteInvocationPolicy = s
		default:
			if allowWritePolicy {
				return nil, ArgErrorf(1, "@behaviorOperations: %s has unknown key %q; it takes hidden, reason, invocationPolicy and writeInvocationPolicy", path, key)
			}
			return nil, ArgErrorf(1, "@behaviorOperations: %s has unknown key %q; it takes hidden, reason and invocationPolicy", path, key)
		}
	}
	if err := checkBehaviorMCPRule(path, out.Hidden, out.Reason, out.InvocationPolicy, out.WriteInvocationPolicy); err != nil {
		return nil, ArgErrorf(1, "@behaviorOperations: %s", err)
	}
	return out, nil
}

func checkBehaviorMCPRule(path string, hidden bool, reason, invocationPolicy, writeInvocationPolicy string) error {
	if hidden {
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("%s: hidden: true requires a non-empty reason", path)
		}
		if invocationPolicy != "" || writeInvocationPolicy != "" {
			return fmt.Errorf("%s: hidden: true cannot be combined with invocationPolicy or writeInvocationPolicy", path)
		}
		return nil
	}
	if reason != "" {
		return fmt.Errorf("%s: reason applies only when hidden is true", path)
	}
	return nil
}

// applySetPermissions reads @requirePermission([...]) on a class, the
// operation's checks applied to the set's clause (D103).
func applySetPermissions(args []any, set *ir.OperationSet) error {
	perms, err := requirePermissionList(args)
	if err != nil {
		return err
	}
	set.Permissions = append(set.Permissions, perms...)
	return nil
}

// requirePermissionList reads @requirePermission's one argument, a
// non-empty array of permission strings.
func requirePermissionList(args []any) ([]string, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("@requirePermission takes exactly one array argument")
	}
	list, ok := args[0].([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("@requirePermission requires a non-empty array of permission strings")
	}
	out := make([]string, 0, len(list))
	for _, p := range list {
		s, ok := p.(string)
		if !ok {
			return nil, fmt.Errorf("@requirePermission entries must be string literals")
		}
		out = append(out, s)
	}
	return out, nil
}
