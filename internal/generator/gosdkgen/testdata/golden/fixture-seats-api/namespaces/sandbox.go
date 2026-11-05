package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-seats-api"
)

// SandboxNamespace contains API methods for the sandbox namespace.
type SandboxNamespace struct {
	client jsonClient
	// Observed holds the operations of Sandbox's observed, a Reconcile (D103).
	Observed *SandboxObservedOperations
	// Status holds the operations of Sandbox's status, a Workflow (D103).
	Status *SandboxStatusOperations
}

// NewSandboxNamespace creates a SandboxNamespace.
func NewSandboxNamespace(
	client jsonClient,
) *SandboxNamespace {
	return &SandboxNamespace{
		client:   client,
		Observed: &SandboxObservedOperations{client: client},
		Status:   &SandboxStatusOperations{client: client},
	}
}

// SandboxObservedOperations holds the operations of Sandbox's observed, a Reconcile (D103).
//
// Endpoints:
// - Observe: Records the state the provider reports, when at is newer than the last observation's, and moves the Workflow there along the fewest transitions its config allows, each as the caller, so every guard and permission holds each step; a step refused stops there and records drift. An observation no newer is answered unchanged. Needs observePermission. Requires authentication.
type SandboxObservedOperations struct {
	client jsonClient
}

// Observe Records the state the provider reports, when at is newer than the last observation's, and moves the Workflow there along the fewest transitions its config allows, each as the caller, so every guard and permission holds each step; a step refused stops there and records drift. An observation no newer is answered unchanged. Needs observePermission.
// Requires authentication.
func (n *SandboxObservedOperations) Observe(
	ctx context.Context,
	Id string,
	input types.SandboxObservedObserveInput,
) (types.SandboxObservedObserveResult, error) {
	path := fmt.Sprintf("/api/sandbox/%v/observed/observe", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.SandboxObservedObserveResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.SandboxObservedObserveResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.SandboxObservedObserveResult
		return zero, err
	}
	return out, nil
}

// SandboxStatusOperations holds the operations of Sandbox's status, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type SandboxStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *SandboxStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.SandboxStatusTransitionInput,
) (types.SandboxStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/sandbox/%v/status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.SandboxStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.SandboxStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.SandboxStatusTransitionResult
		return zero, err
	}
	return out, nil
}
