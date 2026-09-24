package apigen

import (
	"fmt"
	"sort"
)

// FindArrayOfArrays returns the first place the API output carries an array
// of arrays (T[][]): "Type.field" for a field of a type a tool argument can
// reach (TypeFields, in type name order), then "namespace.operation" for a
// response and "namespace.operation(param)" for a parameter or an input
// field. found is false when it carries none. The SDK generators pass the
// output to their nested-arrays guard with it.
func (o *APIOutput) FindArrayOfArrays() (where string, found bool) {
	if o == nil {
		return "", false
	}
	names := make([]string, 0, len(o.TypeFields))
	for name := range o.TypeFields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, field := range o.TypeFields[name] {
			if field.IsArrayOfArrays {
				return name + "." + field.Name, true
			}
		}
	}
	for _, endpoint := range o.Endpoints {
		operation := endpoint.Namespace + "." + endpoint.Name
		if endpoint.OutputIsArrayOfArrays {
			return operation, true
		}
		for _, params := range [][]Param{endpoint.PathParams, endpoint.QueryParams, endpoint.ScalarArgs, endpoint.InputTypeFields} {
			for _, param := range params {
				if param.IsArrayOfArrays {
					return fmt.Sprintf("%s(%s)", operation, param.Name), true
				}
			}
		}
	}
	return "", false
}
