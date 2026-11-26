package registry

import (
	"fmt"
	"slices"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// graphOperationsDecorator registers @graphOperations(Root, { ... }) from
// @superschematic/api (D138): on a class with no members, which the walker
// reads as an operation set, the root of the version graph whose
// operations the set serves, and its options. A class takes it once, and
// not beside a user model's route set (D50) or @behaviorOperations (D103).
// The loader adds the set's operations; the rules that read the API's
// database, and those over the options' names, are the verification
// pass's, so every form meets them.
func graphOperationsDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "graphOperations", Packages: []string{pkgAPI}, Target: TargetOperationSet,
		Apply: func(n Node, args []any, _ Site) error { return applyGraphOperations(args, n.OperationSet) },
	}
}

// applyGraphOperations reads @graphOperations(Root, options): the root as
// a class reference, and the options object's operations, permissions, by
// and overridePermission.
func applyGraphOperations(args []any, set *ir.OperationSet) error {
	switch {
	case set.GraphOperations != nil:
		return fmt.Errorf("@graphOperations appears twice on the class: a set serves one graph")
	case set.BehaviorOperations != nil:
		return fmt.Errorf("@graphOperations contradicts @behaviorOperations on the same class: a class takes one of them")
	case set.UserSessions != nil:
		return fmt.Errorf("@graphOperations contradicts @userSessions on the same class: a class takes one of them")
	case set.UserAdministration != nil:
		return fmt.Errorf("@graphOperations contradicts @userAdministration on the same class: a class takes one of them")
	}
	if len(args) != 1 && len(args) != 2 {
		return fmt.Errorf("@graphOperations takes the root of the version graph the set serves, imported from the API's database, and its options: @graphOperations(Recipe, { by: [\"createdBy\"] })")
	}
	class, err := DecodeClassRef(args[0])
	if err != nil {
		return ArgErrorf(0, "@graphOperations takes the graph's root as a class, imported from the API's database: %v", err)
	}
	cfg := &ir.GraphOperationsConfig{Type: class}
	if len(args) == 2 {
		options, ok := args[1].(map[string]any)
		if !ok {
			return ArgErrorf(1, "@graphOperations takes its options as an object, { operations?, permissions?, by?, overridePermission? }")
		}
		keys := make([]string, 0, len(options))
		for key := range options {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := options[key]
			switch key {
			case "operations":
				names, err := graphOperationsStrings(value, "operations lists the operations the set serves by name, such as \"branch\" or \"save\", at least one")
				if err != nil {
					return err
				}
				cfg.Operations = names
			case "permissions":
				byOperation, ok := value.(map[string]any)
				if !ok {
					return ArgErrorf(1, "@graphOperations: permissions names a permission list per operation, { release: [\"recipes.release\"] }")
				}
				cfg.Permissions = map[string][]string{}
				for operation, list := range byOperation {
					permissions, err := graphOperationsStrings(list, fmt.Sprintf("permissions.%s lists the permissions the operation needs, at least one", operation))
					if err != nil {
						return err
					}
					cfg.Permissions[operation] = permissions
				}
			case "by":
				members, err := graphOperationsStrings(value, "by lists the members of a change set's ref that name who may write it: [\"createdBy\"]")
				if err != nil {
					return err
				}
				cfg.By = members
			case "overridePermission":
				permission, ok := value.(string)
				if !ok || permission == "" {
					return ArgErrorf(1, "@graphOperations: overridePermission names one permission, whose holder writes every change set")
				}
				cfg.OverridePermission = permission
			default:
				return ArgErrorf(1, "@graphOperations takes operations, permissions, by and overridePermission, not %s", key)
			}
		}
	}
	set.GraphOperations = cfg
	return nil
}

// graphOperationsStrings reads a non-empty list of non-empty strings, each
// once in the order written. A name the list holds twice is kept twice
// for an operations list, which the verification pass refuses by name.
func graphOperationsStrings(value any, what string) ([]string, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, ArgErrorf(1, "@graphOperations: %s", what)
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		text, ok := entry.(string)
		if !ok || text == "" {
			return nil, ArgErrorf(1, "@graphOperations: %s", what)
		}
		out = append(out, text)
	}
	return slices.Clip(out), nil
}
