package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-workflow-api"
)

// ShipmentNamespace contains API methods for the shipment namespace.
type ShipmentNamespace struct {
	client jsonClient
	// PaymentStatus holds the operations of Shipment's paymentStatus, a Workflow (D103).
	PaymentStatus *ShipmentPaymentStatusOperations
	// Status holds the operations of Shipment's status, a Workflow (D103).
	Status *ShipmentStatusOperations
}

// NewShipmentNamespace creates a ShipmentNamespace.
func NewShipmentNamespace(
	client jsonClient,
) *ShipmentNamespace {
	return &ShipmentNamespace{
		client:        client,
		PaymentStatus: &ShipmentPaymentStatusOperations{client: client},
		Status:        &ShipmentStatusOperations{client: client},
	}
}

// ShipmentPaymentStatusOperations holds the operations of Shipment's paymentStatus, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type ShipmentPaymentStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *ShipmentPaymentStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.ShipmentPaymentStatusTransitionInput,
) (types.ShipmentPaymentStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/shipment/%v/payment-status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ShipmentPaymentStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ShipmentPaymentStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ShipmentPaymentStatusTransitionResult
		return zero, err
	}
	return out, nil
}

// ShipmentStatusOperations holds the operations of Shipment's status, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type ShipmentStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *ShipmentStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.ShipmentStatusTransitionInput,
) (types.ShipmentStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/shipment/%v/status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ShipmentStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ShipmentStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ShipmentStatusTransitionResult
		return zero, err
	}
	return out, nil
}
