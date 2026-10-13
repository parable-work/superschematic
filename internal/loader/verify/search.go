package verify

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkSearches holds each SearchQuery argument (D62) to its operation, in
// every form: an operation takes one, an optional query parameter of a GET
// without a default, whose value is one text value (the string primitive
// or a scalar whose values are strings, ir.TextValue). What it searches is
// the implementation's: a table's search_text for a result sourced from a
// table with @searchField fields, or whatever text it holds in memory.
func checkSearches(schema *ir.Schema, r *Result) {
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			owner := set.Name + "." + op.Name
			var first *ir.ArgumentDef
			for _, arg := range op.Arguments {
				if arg == nil || !arg.Search {
					continue
				}
				if first != nil {
					r.errorf("", "%s: has two search arguments, %s and %s", owner, first.Name, arg.Name)
					continue
				}
				first = arg
				if !arg.IsQuery || !strings.EqualFold(op.HTTPMethod, "GET") {
					r.errorf("", "%s: search argument %s must be a query parameter of a GET operation", owner, arg.Name)
				}
				if arg.Required {
					r.errorf("", "%s: search argument %s must be optional", owner, arg.Name)
				}
				if arg.Default != nil {
					r.errorf("", "%s: search argument %s has a default; a search left out searches nothing", owner, arg.Name)
				}
				if !ir.TextValue(schema, arg.TypeRef) {
					r.errorf("", "%s: search argument %s holds %s; a search is text", owner, arg.Name, typeLabel(arg.TypeRef))
				}
			}
		}
	}
}
