package namespaces

import (
	"context"
	"fmt"
	"net/url"

	"example.com/schemas/sdk/go/fixture-nested-arrays-api/runtime"
	types "example.com/schemas/types/go/fixture-nested-arrays-api"
)

// GridNamespace contains API methods for the grid namespace.
//
// Endpoints:
// - GetGrid: GetGrid endpoint.
// - GridLabels: One grid's labels as a bare list of lists, at most `limit` rows.
// - ReplaceLabels: Replace a grid's labels; the body argument is a list of lists.
// - SaveGrid: Store a grid from a request body.
type GridNamespace struct {
	client jsonClient
}

// NewGridNamespace creates a GridNamespace.
func NewGridNamespace(
	client jsonClient,
) *GridNamespace {
	return &GridNamespace{
		client: client,
	}
}

// GetGrid calls the GET /api/grids/%v endpoint.
func (n *GridNamespace) GetGrid(
	ctx context.Context,
	Id string,
) (types.GridView, error) {
	path := fmt.Sprintf("/api/grids/%v", Id)

	var requestBody any
	var out types.GridView
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.GridView
		return zero, err
	}
	return out, nil
}

type GridGridLabelsQueryParams struct {
	Limit *float64 `json:"limit,omitempty"`
}

// GridLabels One grid's labels as a bare list of lists, at most `limit` rows.
func (n *GridNamespace) GridLabels(
	ctx context.Context,
	Id string,
	query *GridGridLabelsQueryParams,
) ([][]string, error) {
	path := fmt.Sprintf("/api/grids/%v/labels", Id)
	var queryValues url.Values
	if query != nil {
		queryValues = url.Values{}
		if query.Limit != nil {
			runtime.AddQueryParam(queryValues, "limit", *query.Limit)
		}
	}

	var requestBody any
	var out [][]string
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		queryValues,
		requestBody,
		&out,
	); err != nil {
		var zero [][]string
		return zero, err
	}
	return out, nil
}

type GridReplaceLabelsInput struct {
	Labels [][]string `json:"labels"`
}

// ReplaceLabels Replace a grid's labels; the body argument is a list of lists.
func (n *GridNamespace) ReplaceLabels(
	ctx context.Context,
	Id string,
	input GridReplaceLabelsInput,
) (types.GridView, error) {
	path := fmt.Sprintf("/api/grids/%v/labels", Id)
	validationErrors := types.NewValidationErrors()
	// labels is an array of arrays: an inner list is never nil, reported
	// at labels[i]; an empty one is valid.
	if input.Labels == nil {
		validationErrors.AddFieldError("labels", "required", "required field")
	}
	for i, row := range input.Labels {
		if row == nil {
			validationErrors.AddFieldError(fmt.Sprintf("labels[%d]", i), "required", "required field")
		}
	}
	if validationErrors.HasErrors() {
		var zero types.GridView
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.GridView
	if err := n.client.DoJSON(
		ctx,
		"PUT",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.GridView
		return zero, err
	}
	return out, nil
}

// SaveGrid Store a grid from a request body.
func (n *GridNamespace) SaveGrid(
	ctx context.Context,
	input types.SaveGridInput,
) (types.GridView, error) {
	path := "/api/grids"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.GridView
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.GridView
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.GridView
		return zero, err
	}
	return out, nil
}
