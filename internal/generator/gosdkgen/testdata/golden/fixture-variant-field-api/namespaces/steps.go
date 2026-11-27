package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-variant-field-api"
)

// StepsNamespace contains API methods for the steps namespace.
//
// Endpoints:
// - CreateStep: Records a step. Requires authentication.
// - GetStep: One step. Requires authentication.
type StepsNamespace struct {
	client jsonClient
}

// NewStepsNamespace creates a StepsNamespace.
func NewStepsNamespace(
	client jsonClient,
) *StepsNamespace {
	return &StepsNamespace{
		client: client,
	}
}

// CreateStep Records a step.
// Requires authentication.
func (n *StepsNamespace) CreateStep(
	ctx context.Context,
	input types.CreateStepInput,
) (types.StepView, error) {
	path := "/api/steps"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.StepView
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.StepView
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.StepView
		return zero, err
	}
	return out, nil
}

// GetStep One step.
// Requires authentication.
func (n *StepsNamespace) GetStep(
	ctx context.Context,
	Id string,
) (types.StepView, error) {
	path := fmt.Sprintf("/api/steps/%v", pathSegment(Id))

	var requestBody any
	var out types.StepView
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.StepView
		return zero, err
	}
	return out, nil
}
