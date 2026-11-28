package registry

import (
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// accessGrantsOperationsDecorator registers @accessGrantsOperations({ ... })
// from @superschematic/api (#735): on a class with no members, which the
// walker reads as an operation set, the route of the grants active now,
// and the permission whose holder lists every one. A class takes it once,
// and not beside another set the loader fills. The loader adds the set's
// operation; the rules that read the API's database are the verification
// pass's, so every form meets them.
func accessGrantsOperationsDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "accessGrantsOperations", Packages: []string{pkgAPI}, Target: TargetOperationSet,
		Apply: func(n Node, args []any, _ Site) error { return applyAccessGrantsOperations(args, n.OperationSet) },
	}
}

// applyAccessGrantsOperations reads @accessGrantsOperations(options): the
// options object's permission, a permission name.
func applyAccessGrantsOperations(args []any, set *ir.OperationSet) error {
	switch {
	case set.AccessGrantsOperations != nil:
		return fmt.Errorf("@accessGrantsOperations appears twice on the class: an API lists its active grants from one set")
	case set.BehaviorOperations != nil:
		return fmt.Errorf("@accessGrantsOperations contradicts @behaviorOperations on the same class: a class takes one of them")
	case set.GraphOperations != nil:
		return fmt.Errorf("@accessGrantsOperations contradicts @graphOperations on the same class: a class takes one of them")
	case set.RowFeatureOperations != nil:
		return fmt.Errorf("@accessGrantsOperations contradicts @rowFeatureOperations on the same class: a class takes one of them")
	case set.TriageOperations != nil:
		return fmt.Errorf("@accessGrantsOperations contradicts @triageOperations on the same class: a class takes one of them")
	case set.UserSessions != nil:
		return fmt.Errorf("@accessGrantsOperations contradicts @userSessions on the same class: a class takes one of them")
	case set.UserAdministration != nil:
		return fmt.Errorf("@accessGrantsOperations contradicts @userAdministration on the same class: a class takes one of them")
	}
	if len(args) > 1 {
		return fmt.Errorf("@accessGrantsOperations takes its options alone: @accessGrantsOperations({ permission: \"access.admin\" })")
	}
	cfg := &ir.AccessGrantsOperationsConfig{}
	if len(args) == 1 {
		options, ok := args[0].(map[string]any)
		if !ok {
			return ArgErrorf(0, "@accessGrantsOperations takes its options as an object, { permission? }")
		}
		keys := make([]string, 0, len(options))
		for key := range options {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			switch key {
			case "permission":
				permission, ok := options[key].(string)
				if !ok || permission == "" {
					return ArgErrorf(0, "@accessGrantsOperations: permission names the permission whose holder lists every active grant, as a string: { permission: \"access.admin\" }")
				}
				cfg.Permission = permission
			default:
				return ArgErrorf(0, "@accessGrantsOperations takes permission, not %s", key)
			}
		}
	}
	set.AccessGrantsOperations = cfg
	return nil
}
