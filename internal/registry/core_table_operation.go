package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// tableOperationDecorator is @tableOperation() from @superschematic/api,
// on an operation of an API schema (D106): it writes an empty
// FieldDef.TableOperation, once per operation. It takes no argument: the
// method's HTTP method and shape say what the operation does and its views
// which table it reads or writes, and the loader fills both in
// (verify.CheckTableOperations, tableops.Resolve).
func tableOperationDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "tableOperation", Packages: []string{pkgAPI}, Target: TargetOperation,
		Kinds: []string{string(ir.SchemaKindAPI)},
		Apply: func(n Node, args []any, _ Site) error {
			if len(args) != 0 {
				return fmt.Errorf("@tableOperation takes no argument: the method's HTTP method says what it does, and its @source views which table it reads or writes")
			}
			if n.Field.TableOperation != nil {
				return fmt.Errorf("operation %s has more than one @tableOperation decorator", n.Field.Name)
			}
			n.Field.TableOperation = &ir.TableOperation{}
			return nil
		},
	}
}
