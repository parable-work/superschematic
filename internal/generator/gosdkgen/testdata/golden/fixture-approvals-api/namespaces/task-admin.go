package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-approvals-api"
)

// TaskAdminNamespace contains API methods for the task-admin namespace.
//
// Endpoints:
// - ReassignTask: ReassignTask endpoint. Requires authentication.
type TaskAdminNamespace struct {
	client jsonClient
}

// NewTaskAdminNamespace creates a TaskAdminNamespace.
func NewTaskAdminNamespace(
	client jsonClient,
) *TaskAdminNamespace {
	return &TaskAdminNamespace{
		client: client,
	}
}

// ReassignTask calls the PATCH /api/tasks/%v/assignment endpoint.
// Requires authentication.
func (n *TaskAdminNamespace) ReassignTask(
	ctx context.Context,
	Id string,
	input types.TaskAssignment,
) (types.Task, error) {
	path := fmt.Sprintf("/api/tasks/%v/assignment", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.Task
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.Task
	if err := n.client.DoJSON(
		ctx,
		"PATCH",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.Task
		return zero, err
	}
	return out, nil
}
