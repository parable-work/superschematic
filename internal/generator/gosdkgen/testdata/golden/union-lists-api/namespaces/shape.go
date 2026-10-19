package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/union-lists-api"
)

// ShapeNamespace contains API methods for the shape namespace.
//
// Endpoints:
// - FindShape: The shape with a name, or null when no shape has it (Nullable<Shape>).
// - FindShapes: The shapes found, or null when the search found none (Nullable<Shape[]>).
// - GetShape: One shape, by its id.
// - ListShapes: Every shape, circles and squares mixed, in one list that does not page.
// - ShapeGroups: The shapes in named groups, a map of lists (Record<string, Shape[]>).
// - ShapeRows: The shapes in rows, a list of lists.
// - ShapesByName: Every shape by its name, a map (Record<string, Shape>).
type ShapeNamespace struct {
	client jsonClient
}

// NewShapeNamespace creates a ShapeNamespace.
func NewShapeNamespace(
	client jsonClient,
) *ShapeNamespace {
	return &ShapeNamespace{
		client: client,
	}
}

// FindShape The shape with a name, or null when no shape has it (Nullable<Shape>).
func (n *ShapeNamespace) FindShape(
	ctx context.Context,
	Name string,
) (types.Shape, error) {
	path := fmt.Sprintf("/api/shape-search/%v", pathSegment(Name))

	var requestBody any
	var out *types.ShapeWrapper
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.Shape
		return zero, err
	}
	if out == nil {
		return nil, nil
	}
	if out.Value == nil {
		var zero types.Shape
		return zero, fmt.Errorf("decode FindShape response: Shape union value is nil")
	}
	return out.Value, nil
}

// FindShapes The shapes found, or null when the search found none (Nullable<Shape[]>).
func (n *ShapeNamespace) FindShapes(
	ctx context.Context,
) ([]types.Shape, error) {
	path := "/api/shape-search"

	var requestBody any
	var out []types.ShapeWrapper
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero []types.Shape
		return zero, err
	}
	if out == nil {
		return nil, nil
	}
	items := make([]types.Shape, len(out))
	for i, item := range out {
		if item.Value == nil {
			var zero []types.Shape
			return zero, fmt.Errorf("decode FindShapes response: item %d: Shape union value is nil", i)
		}
		items[i] = item.Value
	}
	return items, nil
}

// GetShape One shape, by its id.
func (n *ShapeNamespace) GetShape(
	ctx context.Context,
	Id string,
) (types.Shape, error) {
	path := fmt.Sprintf("/api/shapes/%v", pathSegment(Id))

	var requestBody any
	var out types.ShapeWrapper
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.Shape
		return zero, err
	}
	if out.Value == nil {
		var zero types.Shape
		return zero, fmt.Errorf("decode GetShape response: Shape union value is nil")
	}
	return out.Value, nil
}

// ListShapes Every shape, circles and squares mixed, in one list that does not page.
func (n *ShapeNamespace) ListShapes(
	ctx context.Context,
) ([]types.Shape, error) {
	path := "/api/shapes"

	var requestBody any
	var out []types.ShapeWrapper
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero []types.Shape
		return zero, err
	}
	if out == nil {
		return nil, nil
	}
	items := make([]types.Shape, len(out))
	for i, item := range out {
		if item.Value == nil {
			var zero []types.Shape
			return zero, fmt.Errorf("decode ListShapes response: item %d: Shape union value is nil", i)
		}
		items[i] = item.Value
	}
	return items, nil
}

// ShapeGroups The shapes in named groups, a map of lists (Record<string, Shape[]>).
func (n *ShapeNamespace) ShapeGroups(
	ctx context.Context,
) (map[string][]types.Shape, error) {
	path := "/api/shape-groups"

	var requestBody any
	var out map[string][]types.ShapeWrapper
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero map[string][]types.Shape
		return zero, err
	}
	if out == nil {
		return nil, nil
	}
	values := make(map[string][]types.Shape, len(out))
	for key, list := range out {
		items := make([]types.Shape, len(list))
		for i, item := range list {
			if item.Value == nil {
				var zero map[string][]types.Shape
				return zero, fmt.Errorf("decode ShapeGroups response: item [%q][%d]: Shape union value is nil", key, i)
			}
			items[i] = item.Value
		}
		values[key] = items
	}
	return values, nil
}

// ShapeRows The shapes in rows, a list of lists.
func (n *ShapeNamespace) ShapeRows(
	ctx context.Context,
) ([][]types.Shape, error) {
	path := "/api/shapes/rows"

	var requestBody any
	var out [][]types.ShapeWrapper
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero [][]types.Shape
		return zero, err
	}
	if out == nil {
		return nil, nil
	}
	rows := make([][]types.Shape, len(out))
	for i, row := range out {
		rows[i] = make([]types.Shape, len(row))
		for j, item := range row {
			if item.Value == nil {
				var zero [][]types.Shape
				return zero, fmt.Errorf("decode ShapeRows response: item [%d][%d]: Shape union value is nil", i, j)
			}
			rows[i][j] = item.Value
		}
	}
	return rows, nil
}

// ShapesByName Every shape by its name, a map (Record<string, Shape>).
func (n *ShapeNamespace) ShapesByName(
	ctx context.Context,
) (map[string]types.Shape, error) {
	path := "/api/shape-index"

	var requestBody any
	var out map[string]types.ShapeWrapper
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero map[string]types.Shape
		return zero, err
	}
	if out == nil {
		return nil, nil
	}
	values := make(map[string]types.Shape, len(out))
	for key, item := range out {
		if item.Value == nil {
			var zero map[string]types.Shape
			return zero, fmt.Errorf("decode ShapesByName response: item [%q]: Shape union value is nil", key)
		}
		values[key] = item.Value
	}
	return values, nil
}
