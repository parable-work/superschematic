package verify

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkPages holds each @paginated operation (D60) to what a page asks of
// it, in every form: a GET whose result is a list of one level, not a map
// and not nullable, since the page's items are that list; no argument
// named cursor, limit or page, the query parameters the page adds and the
// name an implementation takes them by; a route a generated router mounts,
// so not @manualRouteRegistration; a JSON response, so neither encrypted
// nor a binary stream; and no operation of the service named as the SDKs
// name the paged one's iterator, <operation>All. A page on a type's field,
// which only a data form can write, is refused too. Each limit's bounds
// are ir.ValidateOperationPage's, which every reader runs before this
// pass.
func checkPages(schema *ir.Schema, r *Result) {
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			td := types[name]
			if td == nil {
				continue
			}
			for _, field := range td.Fields {
				if field != nil && field.Page != nil {
					r.errorf("", "%s.%s: a page on a field; @paginated sits on an operation", td.Name, field.Name)
				}
			}
		}
	}
	operations := map[string]string{}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op != nil {
				if _, seen := operations[op.Name]; !seen {
					operations[op.Name] = set.Name + "." + op.Name
				}
			}
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil || op.Page == nil {
				continue
			}
			owner := set.Name + "." + op.Name
			checkPage(owner, set, op, r)
			if other, ok := operations[op.Name+"All"]; ok {
				r.errorf("", "%s: an SDK names %sAll the paged %s's iterator; rename one of them", other, op.Name, owner)
			}
		}
	}
}

// checkPage is checkPages for one paged operation.
func checkPage(owner string, set *ir.OperationSet, op *ir.FieldDef, r *Result) {
	switch method := strings.ToUpper(op.HTTPMethod); method {
	case "GET":
	case "":
		r.errorf("", "%s: @paginated needs a GET operation, not one without a method", owner)
	default:
		r.errorf("", "%s: @paginated needs a GET operation, not %s", owner, method)
	}
	result := op.TypeRef
	switch {
	case result.IsArrayOfArrays:
		r.errorf("", "%s: @paginated needs a list of one level, not %s[][]", owner, result.Name)
	case !result.IsArray || result.IsMap || !op.Required:
		r.errorf("", "%s: @paginated needs a list result, not %s", owner, resultExpr(op))
	}
	for _, arg := range op.Arguments {
		if arg != nil && slices.Contains(ir.PageParameterNames, arg.Name) {
			r.errorf("", "%s: @paginated adds the query parameters cursor and limit, and an implementation takes them as page; the operation declares an argument named %s", owner, arg.Name)
		}
	}
	if op.ManualRouteRegistration {
		r.errorf("", "%s: @paginated on a @manualRouteRegistration operation, which no generated router reads", owner)
	}
	if set.Encrypted || op.Encrypted {
		r.errorf("", "%s: @paginated on an encrypted operation", owner)
	}
	if op.HTTPBinaryStream != nil {
		r.errorf("", "%s: @paginated on an operation with @httpBinaryStream, which answers no JSON list", owner)
	}
}

// resultExpr writes an operation's result as the TypeScript form declares
// it: Item, Item[], Record<string, Item>, Nullable<Item[]>.
func resultExpr(op *ir.FieldDef) string {
	expr := op.TypeRef.Name
	if op.TypeRef.IsArray {
		expr += "[]"
	}
	if op.TypeRef.IsMap {
		expr = fmt.Sprintf("Record<string, %s>", expr)
	}
	if !op.Required {
		expr = fmt.Sprintf("Nullable<%s>", expr)
	}
	return expr
}
