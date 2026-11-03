package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-workflow-api"
)

// OrderNamespace contains API methods for the order namespace.
//
// Endpoints:
// - GetOrder: GetOrder endpoint. Requires authentication.
type OrderNamespace struct {
	client jsonClient
	// Status holds the operations of Order's status, a Workflow (D103).
	Status *OrderStatusOperations
}

// NewOrderNamespace creates a OrderNamespace.
func NewOrderNamespace(
	client jsonClient,
) *OrderNamespace {
	return &OrderNamespace{
		client: client,
		Status: &OrderStatusOperations{client: client},
	}
}

// GetOrder calls the GET /api/orders/%v endpoint.
// Requires authentication.
func (n *OrderNamespace) GetOrder(
	ctx context.Context,
	Id string,
) (types.Order, error) {
	path := fmt.Sprintf("/api/orders/%v", pathSegment(Id))

	var requestBody any
	var out types.Order
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.Order
		return zero, err
	}
	return out, nil
}

// OrderStatusOperations holds the operations of Order's status, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type OrderStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *OrderStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.OrderStatusTransitionInput,
) (types.OrderStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/order/%v/status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.OrderStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.OrderStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.OrderStatusTransitionResult
		return zero, err
	}
	return out, nil
}
