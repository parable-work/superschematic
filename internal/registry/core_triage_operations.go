package registry

import (
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// triageOperationsDecorator registers @triageOperations(Party, { ... })
// from @superschematic/api (#746, D157): on a class with no members, which
// the walker reads as an operation set, the @party table a held message is
// settled onto, and its options. A class takes it once, and not beside a
// user model's route set (D50), @behaviorOperations (D103),
// @graphOperations (D138) or @rowFeatureOperations (#723). The loader adds
// the set's operations; the rules that read the API's database are the
// verification pass's, so every form meets them.
func triageOperationsDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "triageOperations", Packages: []string{pkgAPI}, Target: TargetOperationSet,
		Apply: func(n Node, args []any, _ Site) error { return applyTriageOperations(args, n.OperationSet) },
	}
}

// applyTriageOperations reads @triageOperations(Party, options): the party
// table as a class reference, and the options object's refersTo, a class
// reference to the API's type the settle's party refers to.
func applyTriageOperations(args []any, set *ir.OperationSet) error {
	switch {
	case set.TriageOperations != nil:
		return fmt.Errorf("@triageOperations appears twice on the class: an API serves its triage from one set")
	case set.BehaviorOperations != nil:
		return fmt.Errorf("@triageOperations contradicts @behaviorOperations on the same class: a class takes one of them")
	case set.GraphOperations != nil:
		return fmt.Errorf("@triageOperations contradicts @graphOperations on the same class: a class takes one of them")
	case set.RowFeatureOperations != nil:
		return fmt.Errorf("@triageOperations contradicts @rowFeatureOperations on the same class: a class takes one of them")
	case set.AccessGrantsOperations != nil:
		return fmt.Errorf("@triageOperations contradicts @accessGrantsOperations on the same class: a class takes one of them")
	case set.UserSessions != nil:
		return fmt.Errorf("@triageOperations contradicts @userSessions on the same class: a class takes one of them")
	case set.UserAdministration != nil:
		return fmt.Errorf("@triageOperations contradicts @userAdministration on the same class: a class takes one of them")
	}
	if len(args) != 1 && len(args) != 2 {
		return fmt.Errorf("@triageOperations takes the @party table a held message is settled onto, imported from the API's database, and its options: @triageOperations(Contact, { refersTo: ContactView })")
	}
	party, err := DecodeClassRef(args[0])
	if err != nil {
		return ArgErrorf(0, "@triageOperations takes the @party table as a class, imported from the API's database: %v", err)
	}
	cfg := &ir.TriageOperationsConfig{Party: party}
	if len(args) == 2 {
		options, ok := args[1].(map[string]any)
		if !ok {
			return ArgErrorf(1, "@triageOperations takes its options as an object, { refersTo? }")
		}
		keys := make([]string, 0, len(options))
		for key := range options {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			switch key {
			case "refersTo":
				target, err := DecodeClassRef(options[key])
				if err != nil {
					return ArgErrorf(1, "@triageOperations: refersTo names the API's type the settle's party refers to, as a class: { refersTo: ContactView }: %v", err)
				}
				cfg.RefersTo = target
			default:
				return ArgErrorf(1, "@triageOperations takes refersTo, not %s", key)
			}
		}
	}
	set.TriageOperations = cfg
	return nil
}
