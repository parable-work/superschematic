package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/union-lists-api"
)

// ShapeNamespace contains API methods for the shape namespace.
//
// Endpoints:
// - GetShape: One shape, by its id.
// - ListShapes: Every shape, circles and squares mixed, in one list that does not page.
// - ShapeRows: The shapes in rows, a list of lists.
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
