package verify

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkOperationRoutes refuses two operations of an API that are one
// route: the same HTTP method, and paths that are the same text segment
// for segment with a {parameter}, of any name, in the same places. A
// route serves one operation. Before this check two operations declared
// @rest(HttpMethod.GET, 'logins') in two classes built and were served
// with no word, and a request reached only the one the generated
// TypeScript router mounted first.
//
// Every set's operations are held to it, the ones an expansion added
// among them (a table's behavior operations, a version graph's, the user
// model's session routes), by the method and the path apigen routes each
// at: @rest's, or the set's default method and
// /api/<namespace>/<operation> with a segment for each argument a path
// takes. Each operation after the first on a route is refused against the
// first, naming both.
//
// Two paths that only overlap, a {parameter} where the other has text
// (orders/{id} and orders/latest), are two routes and stay allowed here.
// The checks of the operations an expansion adds (checkTableRoutes,
// checkGraphRoutes) refuse the overlaps with those.
func checkOperationRoutes(schema *ir.Schema, r *Result) {
	type route struct {
		owner, path string
	}
	first := map[string]route{}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		namespace := setNamespace(set.Name)
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			routed := *op
			if routed.HTTPMethod == "" {
				routed.HTTPMethod = defaultSetMethod(set.Name)
			}
			method, path, ok := operationRoute(schema, namespace, &routed)
			if !ok {
				continue
			}
			owner, key := set.Name+"."+op.Name, method+" "+routeShape(path)
			held, taken := first[key]
			if !taken {
				first[key] = route{owner: owner, path: path}
				continue
			}
			at := ""
			if path != held.path {
				at = ", at " + path
			}
			r.errorf("", "%s is routed at %s %s, and so is %s%s: a route serves one operation, and a request would reach only one of the two; give one another method or path", held.owner, method, held.path, owner, at)
		}
	}
}

// defaultSetMethod is the HTTP method of an operation of the set that
// names none, as apigen's defaultMethodForSet gives it: POST in a set of
// mutations or actions, GET in any other.
func defaultSetMethod(set string) string {
	if strings.HasSuffix(set, "Mutations") || strings.HasSuffix(set, "Actions") {
		return "POST"
	}
	return "GET"
}

// routeShape is a route's path with each {parameter} written without its
// name: two paths of one shape reach the same requests.
func routeShape(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, segment := range segments {
		if isPathParameter(segment) {
			segments[i] = "{}"
		}
	}
	return strings.Join(segments, "/")
}
