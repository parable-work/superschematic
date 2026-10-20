// Package sdktest holds schemas and schema edits the SDK generator tests
// and the generated API server test share.
package sdktest

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// QueryListsService is the name of the schema LoadQueryListsService loads.
const QueryListsService = "query-lists-api"

// LoadQueryListsService loads query-lists-api from this package's testdata:
// one GET operation whose list query parameters (QueryParam<T[]>) are of an
// enum, a UUID scalar, an integer scalar, strings with rules and booleans,
// beside a scalar query parameter. It is local to the SDK generator tests,
// so no other generator's goldens read it.
func LoadQueryListsService() (*ir.Schema, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("locate package sdktest")
	}
	return loader.LoadService(filepath.Join(filepath.Dir(file), "testdata", QueryListsService))
}

// OptionalJSONService is the name of the schema LoadOptionalJSONService
// loads.
const OptionalJSONService = "optional-json-api"

// LoadOptionalJSONService loads optional-json-api from this package's
// testdata: note.annotate, a PUT, and note.retract, a DELETE, whose body
// arguments are a required and an optional Generic.JSON, and note.revise, a
// POST whose input type has a required and an optional Generic.JSON field.
// An SDK sends the arguments of the DELETE in the body, as those of the
// PUT, and the optional
// one as null when the caller sets it to null and leaves it out when the
// caller does not set it. It is local to the SDK generator tests, so no
// other generator's goldens read it.
func LoadOptionalJSONService() (*ir.Schema, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("locate package sdktest")
	}
	return loader.LoadService(filepath.Join(filepath.Dir(file), "testdata", OptionalJSONService))
}

// UnionListsService is the name of the schema LoadUnionListsService loads.
const UnionListsService = "union-lists-api"

// LoadUnionListsService loads union-lists-api from this package's testdata:
// the union Shape (Circle | Square) as the response of operations that do
// not page, ShapeQueries.listShapes (a list of it), ShapeQueries.shapeRows
// (a list of lists of it), ShapeQueries.getShape (one of it),
// ShapeQueries.shapesByName (a map of it), ShapeQueries.shapeGroups (a map
// of lists of it), ShapeQueries.findShape (one of it that may be null) and
// ShapeQueries.findShapes (a list of it that may be null). An SDK decodes
// each element through the union. It is local to the SDK generator tests,
// so no other generator's goldens read it.
func LoadUnionListsService() (*ir.Schema, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("locate package sdktest")
	}
	return loader.LoadService(filepath.Join(filepath.Dir(file), "testdata", UnionListsService))
}

// AddCircleOperations adds three operations on Circle, an object type, to
// the loaded union-lists-api schema's ShapeQueries, for an SDK that parses
// an object type's response and not a union's: circlesByName, a map of it
// (GET circle-index), circleGroups, a map of lists of it (GET
// circle-groups), and findCircle, one that may be null (GET
// circle-search/{name}).
func AddCircleOperations(schema *ir.Schema) error {
	for _, set := range schema.OperationSets {
		if set.Name != "ShapeQueries" {
			continue
		}
		set.Operations = append(set.Operations,
			&ir.FieldDef{Name: "circlesByName", TypeRef: ir.TypeRef{Name: "Circle", IsMap: true}, Required: true, HTTPMethod: "GET", RestPath: "circle-index"},
			&ir.FieldDef{Name: "circleGroups", TypeRef: ir.TypeRef{Name: "Circle", IsMap: true, IsArray: true}, Required: true, HTTPMethod: "GET", RestPath: "circle-groups"},
			&ir.FieldDef{
				Name: "findCircle", TypeRef: ir.TypeRef{Name: "Circle"}, HTTPMethod: "GET", RestPath: "circle-search/{name}",
				Arguments: []*ir.ArgumentDef{{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
			},
		)
		return nil
	}
	return fmt.Errorf("schema %s has no ShapeQueries operation set", schema.Name)
}

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

// AddCellOperation adds grid.cell to the loaded fixture-nested-arrays-api
// schema: a GET whose path takes the grid's id and a string label
// (grids/{id}/cells/{label}), and whose response is a string. A label may
// hold any text, %, /, ? and # among it, which a UUID cannot, so an SDK
// test sends it to check each path value is encoded as one segment.
func AddCellOperation(schema *ir.Schema) error {
	for _, set := range schema.OperationSets {
		if set.Name != "GridQueries" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:     "cell",
			Comment:  "One cell of a grid, by its label.",
			TypeRef:  ir.TypeRef{Name: "string"},
			Required: true,
			Arguments: []*ir.ArgumentDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
				{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			},
			HTTPMethod: "GET",
			RestPath:   "grids/{id}/cells/{label}",
		})
		return nil
	}
	return fmt.Errorf("schema %s has no GridQueries operation set", schema.Name)
}

// AddPlaceOrderOperation adds grid.placeOrder to the loaded
// fixture-nested-arrays-api schema: a POST whose input type, PlaceOrderInput,
// bounds its lines (listMin 1, listMax 3) and each gift code (maxLength 8),
// and holds objects with rules of their own, in a list (lines), a field
// (shipTo) and an optional map (extras): each OrderLine's productId
// (minLength 3) and quantity (min 1, max 99), and shipTo's postalCode
// (pattern ^[0-9]{5}$). A typed decoder checks none of these rules; the
// SDK's input validation does.
func AddPlaceOrderOperation(schema *ir.Schema) error {
	listMin, listMax, minLength, maxLength := 1, 3, 3, 8
	minQuantity, maxQuantity := 1.0, 99.0
	schema.Types["OrderLine"] = &ir.TypeDef{
		Name: "OrderLine",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "productId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, ValidateMinLength: &minLength},
			{Name: "quantity", TypeRef: ir.TypeRef{Name: "number"}, Required: true, ValidateMin: &minQuantity, ValidateMax: &maxQuantity},
		},
	}
	schema.Types["ShippingAddress"] = &ir.TypeDef{
		Name: "ShippingAddress",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "postalCode", TypeRef: ir.TypeRef{Name: "string"}, Required: true, ValidatePattern: "^[0-9]{5}$"},
		},
	}
	schema.Types["PlaceOrderInput"] = &ir.TypeDef{
		Name: "PlaceOrderInput",
		Role: ir.RoleAPIInput,
		Fields: []*ir.FieldDef{
			{Name: "lines", TypeRef: ir.TypeRef{Name: "OrderLine", IsArray: true}, Required: true, ValidateListMin: &listMin, ValidateListMax: &listMax},
			{Name: "shipTo", TypeRef: ir.TypeRef{Name: "ShippingAddress"}, Required: true},
			{Name: "giftCodes", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, ValidateMaxLength: &maxLength},
			{Name: "extras", TypeRef: ir.TypeRef{Name: "OrderLine", IsMap: true}},
		},
	}
	for _, set := range schema.OperationSets {
		if set.Name != "GridMutations" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:     "placeOrder",
			Comment:  "Place an order; returns the grid it was placed from.",
			TypeRef:  ir.TypeRef{Name: "GridView"},
			Required: true,
			Arguments: []*ir.ArgumentDef{
				{Name: "input", TypeRef: ir.TypeRef{Name: "PlaceOrderInput"}, Required: true},
			},
			HTTPMethod: "POST",
			RestPath:   "orders",
		})
		return nil
	}
	return fmt.Errorf("schema %s has no GridMutations operation set", schema.Name)
}

// AddShelfItemsOperation adds Catalog.listShelfItems to the loaded
// fixture-paged-api schema: a paged GET (D60, two to a page, up to five)
// whose path takes a shelf's id (shelves/{shelfId}/items) and whose other
// arguments, maker and color, are not QueryParam and so are an SDK's body
// arguments, which a GET sends in its query. Its SDK methods take a path
// parameter and body arguments beside the page, which the fixture's own
// paged operations do not.
func AddShelfItemsOperation(schema *ir.Schema) error {
	for _, set := range schema.OperationSets {
		if set.Name != "Catalog" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:     "listShelfItems",
			Comment:  "The items on one shelf, by maker and color.",
			TypeRef:  ir.TypeRef{Name: "ItemView", IsArray: true},
			Required: true,
			Arguments: []*ir.ArgumentDef{
				{Name: "shelfId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
				{Name: "maker", TypeRef: ir.TypeRef{Name: "string"}},
				{Name: "color", TypeRef: ir.TypeRef{Name: "string"}},
			},
			HTTPMethod: "GET",
			RestPath:   "shelves/{shelfId}/items",
			Page:       &ir.OperationPage{DefaultLimit: 2, MaxLimit: 5},
		})
		return nil
	}
	return fmt.Errorf("schema %s has no Catalog operation set", schema.Name)
}

// AddEntriesOperation adds the union Entry (ItemView | BasketView) and
// Catalog.listEntries, a paged GET (D60, two to a page, up to five) of
// Entry, to the loaded fixture-paged-api schema: the TypeScript schema
// form declares no unions. An SDK decodes each item of its page through
// the union, which the fixture's own paged operations do not need.
func AddEntriesOperation(schema *ir.Schema) error {
	if schema.Unions == nil {
		schema.Unions = map[string]*ir.UnionDef{}
	}
	schema.Unions["Entry"] = &ir.UnionDef{Name: "Entry", Comment: "An item or a placed basket.", Types: []string{"ItemView", "BasketView"}}
	for _, set := range schema.OperationSets {
		if set.Name != "Catalog" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:       "listEntries",
			Comment:    "The items and the placed baskets, in one list.",
			TypeRef:    ir.TypeRef{Name: "Entry", IsArray: true},
			Required:   true,
			HTTPMethod: "GET",
			RestPath:   "entries",
			Page:       &ir.OperationPage{DefaultLimit: 2, MaxLimit: 5},
		})
		return nil
	}
	return fmt.Errorf("schema %s has no Catalog operation set", schema.Name)
}
