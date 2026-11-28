package registry

import (
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// rowFeatureOperationsDecorator registers @rowFeatureOperations(Type,
// { ... }) from @superschematic/api (#723, D154): on a class with no
// members, which the walker reads as an operation set, the table whose
// references, backlinks and files the set serves, and its options. A class
// takes it once, and not beside a user model's route set (D50),
// @behaviorOperations (D103) or @graphOperations (D138). The loader adds
// the set's operations; the rules that read the API's database and its
// buckets, and those over the options' names, are the verification
// pass's, so every form meets them.
func rowFeatureOperationsDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "rowFeatureOperations", Packages: []string{pkgAPI}, Target: TargetOperationSet,
		Apply: func(n Node, args []any, _ Site) error { return applyRowFeatureOperations(args, n.OperationSet) },
	}
}

// applyRowFeatureOperations reads @rowFeatureOperations(Type, options):
// the table as a class reference, and the options object's operations,
// permissions and bucket.
func applyRowFeatureOperations(args []any, set *ir.OperationSet) error {
	switch {
	case set.RowFeatureOperations != nil:
		return fmt.Errorf("@rowFeatureOperations appears twice on the class: a set serves one table")
	case set.BehaviorOperations != nil:
		return fmt.Errorf("@rowFeatureOperations contradicts @behaviorOperations on the same class: a class takes one of them")
	case set.GraphOperations != nil:
		return fmt.Errorf("@rowFeatureOperations contradicts @graphOperations on the same class: a class takes one of them")
	case set.UserSessions != nil:
		return fmt.Errorf("@rowFeatureOperations contradicts @userSessions on the same class: a class takes one of them")
	case set.UserAdministration != nil:
		return fmt.Errorf("@rowFeatureOperations contradicts @userAdministration on the same class: a class takes one of them")
	case set.TriageOperations != nil:
		return fmt.Errorf("@rowFeatureOperations contradicts @triageOperations on the same class: a class takes one of them")
	case set.AccessGrantsOperations != nil:
		return fmt.Errorf("@rowFeatureOperations contradicts @accessGrantsOperations on the same class: a class takes one of them")
	}
	if len(args) != 1 && len(args) != 2 {
		return fmt.Errorf("@rowFeatureOperations takes the table whose references and files the set serves, imported from the API's database, and its options: @rowFeatureOperations(Issue, { bucket: \"issue-files\" })")
	}
	class, err := DecodeClassRef(args[0])
	if err != nil {
		return ArgErrorf(0, "@rowFeatureOperations takes the table as a class, imported from the API's database: %v", err)
	}
	cfg := &ir.RowFeatureOperationsConfig{Type: class}
	if len(args) == 2 {
		options, ok := args[1].(map[string]any)
		if !ok {
			return ArgErrorf(1, "@rowFeatureOperations takes its options as an object, { operations?, permissions?, bucket? }")
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
				names, err := rowFeatureStrings(value, "operations lists the operations the set serves by name, such as \"backlinks\" or \"beginUpload\", at least one")
				if err != nil {
					return err
				}
				cfg.Operations = names
			case "permissions":
				byOperation, ok := value.(map[string]any)
				if !ok {
					return ArgErrorf(1, "@rowFeatureOperations: permissions names a permission list per operation, { beginUpload: [\"issues.upload\"] }")
				}
				cfg.Permissions = map[string][]string{}
				for operation, list := range byOperation {
					permissions, err := rowFeatureStrings(list, fmt.Sprintf("permissions.%s lists the permissions the operation needs, at least one", operation))
					if err != nil {
						return err
					}
					cfg.Permissions[operation] = permissions
				}
			case "bucket":
				bucket, ok := value.(string)
				if !ok || bucket == "" {
					return ArgErrorf(1, "@rowFeatureOperations: bucket names one of the API's buckets by its service name, the Bucket service that holds the files: { bucket: \"issue-files\" }")
				}
				cfg.Bucket = bucket
			default:
				return ArgErrorf(1, "@rowFeatureOperations takes operations, permissions and bucket, not %s", key)
			}
		}
	}
	set.RowFeatureOperations = cfg
	return nil
}

// rowFeatureStrings reads a non-empty list of non-empty strings, in the
// order written, as graphOperationsStrings does.
func rowFeatureStrings(value any, what string) ([]string, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, ArgErrorf(1, "@rowFeatureOperations: %s", what)
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		text, ok := entry.(string)
		if !ok || text == "" {
			return nil, ArgErrorf(1, "@rowFeatureOperations: %s", what)
		}
		out = append(out, text)
	}
	return out, nil
}
