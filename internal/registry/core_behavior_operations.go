package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// behaviorOperationsDecorator registers @behaviorOperations(Type) from
// @superschematic/api (D103): on a class with no members, which the walker
// reads as an operation set, the table whose behaviors' operations the set
// serves. A class takes it once, and not beside a user model's route set
// (D50). The loader adds the set's operations; the rules that read the
// API's database are the verification pass's.
func behaviorOperationsDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "behaviorOperations", Packages: []string{pkgAPI}, Target: TargetOperationSet,
		Apply: func(n Node, args []any, _ Site) error { return applyBehaviorOperations(args, n.OperationSet) },
	}
}

// applyBehaviorOperations reads @behaviorOperations(Type), the table as a
// class reference.
func applyBehaviorOperations(args []any, set *ir.OperationSet) error {
	switch {
	case set.BehaviorOperations != nil:
		return fmt.Errorf("@behaviorOperations appears twice on the class: a set serves one table")
	case set.UserSessions != nil:
		return fmt.Errorf("@behaviorOperations contradicts @userSessions on the same class: a class takes one of them")
	case set.UserAdministration != nil:
		return fmt.Errorf("@behaviorOperations contradicts @userAdministration on the same class: a class takes one of them")
	}
	if len(args) != 1 {
		return fmt.Errorf("@behaviorOperations takes one argument, the table whose behaviors' operations the set serves, imported from the API's database: @behaviorOperations(Order)")
	}
	class, err := DecodeClassRef(args[0])
	if err != nil {
		return ArgErrorf(0, "@behaviorOperations takes the table as a class, imported from the API's database: %v", err)
	}
	set.BehaviorOperations = &ir.BehaviorOperationsConfig{Type: class}
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
