package registry

import (
	"fmt"
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// tableOperationDecorator is @tableOperation() from @superschematic/api,
// on an operation of an API schema (D106): it writes
// FieldDef.TableOperation, once per operation. The method's HTTP method
// and shape say what the operation does and its views which table it
// reads or writes, and the loader fills both in
// (verify.CheckTableOperations). Its one argument, when written, is the
// row rule (D108): `{ by: [...], overridePermission? }`, the row's members
// that name who may reach it and the permission that reaches every row.
// The loader holds each member to the table.
func tableOperationDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "tableOperation", Packages: []string{pkgAPI}, Target: TargetOperation,
		Kinds: []string{string(ir.SchemaKindAPI)},
		Apply: func(n Node, args []any, _ Site) error {
			if n.Field.TableOperation != nil {
				return fmt.Errorf("operation %s has more than one @tableOperation decorator", n.Field.Name)
			}
			operation := &ir.TableOperation{}
			switch len(args) {
			case 0:
			case 1:
				rule, ok := args[0].(map[string]any)
				if !ok {
					return ArgErrorf(0, "@tableOperation takes no argument, or its row rule, { by: [...], overridePermission? }")
				}
				for key, value := range rule {
					switch key {
					case "by":
						list, ok := value.([]any)
						if !ok || len(list) == 0 {
							return ArgErrorf(0, "@tableOperation: by lists the row's members that name who may reach it, at least one")
						}
						for _, entry := range list {
							member, ok := entry.(string)
							if !ok || member == "" {
								return ArgErrorf(0, "@tableOperation: by lists members of the row by name, such as \"assigneeId\" or \"createdBy\"")
							}
							if slices.Contains(operation.By, member) {
								return ArgErrorf(0, "@tableOperation: by names %s twice", member)
							}
							operation.By = append(operation.By, member)
						}
					case "overridePermission":
						permission, ok := value.(string)
						if !ok || permission == "" {
							return ArgErrorf(0, "@tableOperation: overridePermission names one permission, whose holder reaches every row")
						}
						operation.OverridePermission = permission
					default:
						return ArgErrorf(0, "@tableOperation takes by and overridePermission, not %s: the method's HTTP method says what it does, and its @source views which table it reads or writes", key)
					}
				}
				if operation.OverridePermission != "" && len(operation.By) == 0 {
					return ArgErrorf(0, "@tableOperation: overridePermission lifts a row rule, and the operation has none: name the row's members in by")
				}
			default:
				return fmt.Errorf("@tableOperation takes one argument, its row rule, and this has %d", len(args))
			}
			n.Field.TableOperation = operation
			return nil
		},
	}
}
