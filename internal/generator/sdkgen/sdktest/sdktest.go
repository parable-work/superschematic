// Package sdktest holds schema edits the SDK generator tests and the
// generated API server test share.
package sdktest

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// AddPaintOperation adds grid.paint to the loaded fixture-nested-arrays-api
// schema: a PUT whose body arguments are a required list of lists of the
// Shade enum and an optional list of lists of the Point object, and whose
// response is a list of lists of Point. The fixture's own list-of-lists
// argument and response are strings, which carry no validation or parsing
// of their own; paint covers the element types that do.
func AddPaintOperation(schema *ir.Schema) error {
	nested := func(name string) ir.TypeRef { return ir.TypeRef{Name: name, IsArray: true, IsArrayOfArrays: true} }
	for _, set := range schema.OperationSets {
		if set.Name != "GridMutations" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:     "paint",
			Comment:  "Paint a grid's cells and polygons; returns the stored polygons.",
			TypeRef:  nested("Point"),
			Required: true,
			Arguments: []*ir.ArgumentDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
				{Name: "shades", TypeRef: nested("Shade"), Required: true},
				{Name: "polygons", TypeRef: nested("Point")},
			},
			HTTPMethod: "PUT",
			RestPath:   "grids/{id}/paint",
		})
		return nil
	}
	return fmt.Errorf("schema %s has no GridMutations operation set", schema.Name)
}
