package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-approvals-api"
)

// TaskNamespace contains API methods for the task namespace.
type TaskNamespace struct {
	client jsonClient
	// Approvals holds the operations of the Approvals Task implements (D103).
	Approvals *TaskApprovalsOperations
	// Status holds the operations of Task's status, a Workflow (D103).
	Status *TaskStatusOperations
}

// NewTaskNamespace creates a TaskNamespace.
func NewTaskNamespace(
	client jsonClient,
) *TaskNamespace {
	return &TaskNamespace{
		client:    client,
		Approvals: &TaskApprovalsOperations{client: client},
		Status:    &TaskStatusOperations{client: client},
	}
}

// TaskApprovalsOperations holds the operations of the Approvals Task implements (D103).
//
// Endpoints:
// - ListSignOffs: Lists the instance's sign-offs, oldest first, one page at a time, the consumed ones included; with state, only those for it. Requires authentication.
// - SignOff: Signs off on the instance for a gated state, as the caller. Needs the gate's permission; refused for the instance's creator unless the gate allows it, while the instance is in the state, and for a caller whose sign-off for the state is not yet consumed. Requires authentication.
// - WithdrawSignOff: Withdraws the caller's sign-off for a gated state, one not yet consumed. Requires authentication.
type TaskApprovalsOperations struct {
	client jsonClient
}

// ListSignOffs Lists the instance's sign-offs, oldest first, one page at a time, the consumed ones included; with state, only those for it.
// Requires authentication.
func (n *TaskApprovalsOperations) ListSignOffs(
	ctx context.Context,
	Id string,
	input types.TaskApprovalsListSignOffsInput,
) (types.TaskApprovalsListSignOffsResult, error) {
	path := fmt.Sprintf("/api/task/%v/approvals/list-sign-offs", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.TaskApprovalsListSignOffsResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.TaskApprovalsListSignOffsResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.TaskApprovalsListSignOffsResult
		return zero, err
	}
	return out, nil
}

// SignOff Signs off on the instance for a gated state, as the caller. Needs the gate's permission; refused for the instance's creator unless the gate allows it, while the instance is in the state, and for a caller whose sign-off for the state is not yet consumed.
// Requires authentication.
func (n *TaskApprovalsOperations) SignOff(
	ctx context.Context,
	Id string,
	input types.TaskApprovalsSignOffInput,
) (types.TaskApprovalsSignOffResult, error) {
	path := fmt.Sprintf("/api/task/%v/approvals/sign-off", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.TaskApprovalsSignOffResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.TaskApprovalsSignOffResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.TaskApprovalsSignOffResult
		return zero, err
	}
	return out, nil
}

// WithdrawSignOff Withdraws the caller's sign-off for a gated state, one not yet consumed.
// Requires authentication.
func (n *TaskApprovalsOperations) WithdrawSignOff(
	ctx context.Context,
	Id string,
	input types.TaskApprovalsWithdrawSignOffInput,
) (types.TaskApprovalsWithdrawSignOffResult, error) {
	path := fmt.Sprintf("/api/task/%v/approvals/withdraw-sign-off", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.TaskApprovalsWithdrawSignOffResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.TaskApprovalsWithdrawSignOffResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.TaskApprovalsWithdrawSignOffResult
		return zero, err
	}
	return out, nil
}

// TaskStatusOperations holds the operations of Task's status, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type TaskStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *TaskStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.TaskStatusTransitionInput,
) (types.TaskStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/task/%v/status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.TaskStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.TaskStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.TaskStatusTransitionResult
		return zero, err
	}
	return out, nil
}
