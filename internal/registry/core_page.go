package registry

import (
	"encoding/json"
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// pageArgs is the JSON Schema of @paginated's argument (D60), which is also
// what the data forms write under an operation's `page` key: the
// schema-file JSON Schema takes its $defs/OperationPage from the
// registered spec in place of the one it reflects from ir.OperationPage, so
// the TypeScript frontend, the data forms and the strict loader an engine
// runs check one schema. Both members are optional; the loader fills them
// (ir.OperationPage.WithDefaults).
var pageArgs = json.RawMessage(`{
	"$ref": "#/$defs/OperationPage",
	"$defs": {
		"OperationPage": {
			"description": "The operation's list result pages by an opaque cursor (@paginated): it takes the query parameters cursor and limit and answers {items, next}. A member left out takes its default.",
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"defaultLimit": {"description": "How many items a page holds when the caller sends no limit: 20, or maxLimit when that is less.", "type": "integer", "minimum": 1, "maximum": 1000},
				"maxLimit": {"description": "The most items a caller may ask a page to hold: 1 to 1000, 100 when left out.", "type": "integer", "minimum": 1, "maximum": 1000}
			}
		}
	}
}`)

// paginatedDecorator is @paginated() or @paginated({ defaultLimit,
// maxLimit }) from @superschematic/api, on an operation of an API schema:
// it writes FieldDef.Page, once per operation, with both limits filled in.
// The frontend has held the argument to pageArgs; the loader holds the
// operation to the page's rules (verify.checkPages).
func paginatedDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "paginated", Packages: []string{pkgAPI}, Target: TargetOperation,
		Kinds: []string{string(ir.SchemaKindAPI)}, Args: pageArgs, optionalArg: true,
		Apply: func(n Node, args []any, _ Site) error {
			if n.Field.Page != nil {
				return fmt.Errorf("operation %s has more than one @paginated decorator", n.Field.Name)
			}
			var page ir.OperationPage
			if len(args) == 1 {
				if err := DecodeArgs(args, &page); err != nil {
					return ArgErrorf(0, "@paginated: %v", err)
				}
			}
			page = page.WithDefaults()
			if page.DefaultLimit > page.MaxLimit {
				return ArgErrorf(0, "@paginated defaultLimit %d is more than its maxLimit %d", page.DefaultLimit, page.MaxLimit)
			}
			n.Field.Page = &page
			return nil
		},
	}
}
