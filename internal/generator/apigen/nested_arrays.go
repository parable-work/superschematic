package apigen

import "fmt"

// checkArraysOfArraysInBody refuses an array of arrays that would not
// travel in a request body: a path or query parameter, or a scalar
// argument of a GET operation, which the query string carries. The loader
// refuses the same arguments; this covers IR that did not come through it,
// so no route renders one as T[].
func checkArraysOfArraysInBody(namespace, operation, method string, pathParams, queryParams, scalarArgs []Param) error {
	for _, check := range []struct {
		params []Param
		what   string
	}{
		{pathParams, "a path parameter"},
		{queryParams, "a query parameter"},
	} {
		for _, param := range check.params {
			if param.IsArrayOfArrays {
				return fmt.Errorf("apigen: operation %s.%s argument %s: %s cannot be an array of arrays", namespace, operation, param.Name, check.what)
			}
		}
	}
	if method != "GET" {
		return nil
	}
	for _, arg := range scalarArgs {
		if arg.IsArrayOfArrays {
			return fmt.Errorf("apigen: operation %s.%s argument %s: a GET operation sends its arguments in the query string, which cannot carry an array of arrays", namespace, operation, arg.Name)
		}
	}
	return nil
}

// checkMapsInBody refuses a map (Record<string, T>) that would not travel
// in a request body: a path or query parameter, or an argument of a GET
// operation, which the query string carries and which has no encoding for
// a map. The route would otherwise read it as one value of T.
func checkMapsInBody(namespace, operation, method string, pathParams, queryParams, scalarArgs []Param) error {
	for _, check := range []struct {
		params []Param
		what   string
	}{
		{pathParams, "a path parameter"},
		{queryParams, "a query parameter"},
	} {
		for _, param := range check.params {
			if param.IsMap {
				return fmt.Errorf("apigen: operation %s.%s argument %s: %s cannot be a map", namespace, operation, param.Name, check.what)
			}
		}
	}
	if method != "GET" {
		return nil
	}
	for _, arg := range scalarArgs {
		if arg.IsMap {
			return fmt.Errorf("apigen: operation %s.%s argument %s: a GET operation sends its arguments in the query string, which cannot carry a map; declare a POST, PUT, PATCH or DELETE method, or move it into an input type", namespace, operation, arg.Name)
		}
	}
	return nil
}
