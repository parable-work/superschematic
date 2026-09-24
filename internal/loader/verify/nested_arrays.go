package verify

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkArraysOfArrays refuses an array of arrays (T[][]) where a list of
// lists has no meaning: env config fields, relations, indexed fields and
// index keys, and operation arguments that do not travel in a request body
// (query and path parameters, and any argument of a GET operation or of an
// operation without a method). Fields of DB, API and General types, request
// input types and operation responses accept it. A map value is refused by
// ir.Schema.Validate and by the TypeScript reader; projections refuse it in
// checkProjections.
func checkArraysOfArrays(schema *ir.Schema, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		nested := map[string]bool{}
		for _, fd := range td.Fields {
			if !fd.TypeRef.IsArrayOfArrays {
				continue
			}
			nested[fd.Name] = true
			owner := td.Name + "." + fd.Name
			switch {
			case td.EnvVars:
				r.errorf(td.Owner, "%s: env config fields cannot be arrays of arrays", owner)
			case fd.HasMany || fd.ManyToMany || fd.Relation != nil:
				r.errorf(td.Owner, "%s: a relation (hasMany, manyToMany or relation) cannot be an array of arrays", owner)
			case fd.Key || fd.Unique || fd.SearchField:
				r.errorf(td.Owner, "%s: an indexed field (@key, @unique or @searchField) cannot be an array of arrays", owner)
			}
		}
		for _, idx := range td.Indexes {
			for _, key := range idx.Keys {
				if nested[key] {
					r.errorf(td.Owner, "%s: @index key %q is an array of arrays, which cannot be an index column", td.Name, key)
				}
			}
		}
	}

	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			checkOperationArraysOfArrays(set, op, r)
		}
	}
}

// checkOperationArraysOfArrays applies the argument rule to one operation.
// An argument is a query parameter when it is @query (or the operation maps
// every argument to the query string), a path parameter when the rest path
// names it, and otherwise part of the request body only when the operation
// declares a method other than GET.
func checkOperationArraysOfArrays(set *ir.OperationSet, op *ir.FieldDef, r *Result) {
	pathParams := restPathParams(op.RestPath)
	method := strings.ToUpper(op.HTTPMethod)
	for _, arg := range op.Arguments {
		if arg == nil || !arg.TypeRef.IsArrayOfArrays {
			continue
		}
		owner := set.Name + "." + op.Name
		switch {
		case arg.IsQuery || op.ParamType == "query":
			r.errorf("", "%s: query parameter %q cannot be an array of arrays", owner, arg.Name)
		case pathParams[arg.Name]:
			r.errorf("", "%s: path parameter %q cannot be an array of arrays", owner, arg.Name)
		case method == "" || method == "GET":
			r.errorf("", "%s: argument %q is an array of arrays, which only a request body carries; declare a POST, PUT, PATCH or DELETE method, or move it into an input type", owner, arg.Name)
		}
	}
}

// restPathParams returns the {name} placeholders of a rest path.
func restPathParams(restPath string) map[string]bool {
	params := map[string]bool{}
	for rest := restPath; ; {
		start := strings.IndexByte(rest, '{')
		if start < 0 {
			return params
		}
		end := strings.IndexByte(rest[start:], '}')
		if end < 0 {
			return params
		}
		if name := rest[start+1 : start+end]; name != "" {
			params[name] = true
		}
		rest = rest[start+end+1:]
	}
}
