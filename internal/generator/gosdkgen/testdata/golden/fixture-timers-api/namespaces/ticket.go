package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-timers-api"
)

// TicketNamespace contains API methods for the ticket namespace.
type TicketNamespace struct {
	client jsonClient
	// Status holds the operations of Ticket's status, a Workflow (D103).
	Status *TicketStatusOperations
	// Timers holds the operations of the Timers Ticket implements (D103).
	Timers *TicketTimersOperations
}

// NewTicketNamespace creates a TicketNamespace.
func NewTicketNamespace(
	client jsonClient,
) *TicketNamespace {
	return &TicketNamespace{
		client: client,
		Status: &TicketStatusOperations{client: client},
		Timers: &TicketTimersOperations{client: client},
	}
}

// TicketStatusOperations holds the operations of Ticket's status, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type TicketStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *TicketStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.TicketStatusTransitionInput,
) (types.TicketStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/ticket/%v/status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.TicketStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.TicketStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.TicketStatusTransitionResult
		return zero, err
	}
	return out, nil
}

// TicketTimersOperations holds the operations of the Timers Ticket implements (D103).
//
// Endpoints:
// - FireTimer: Fires a deadline that is due and has not fired: marks it fired and moves the status by its then, recording the outcome; a veto or a missing permission the move meets leaves the status as it is and the deadline fired. A deadline whose until holds is cancelled instead. On a deadline not due, fired already or not started it changes nothing. Any principal who may write the instance may call it; the engine's runner calls it on every due deadline, every sweepMs. Requires authentication.
// - ResetTimer: Starts a deadline again from what it starts from now, the create, the status's entry into its state or its field's value, so it is due afterMs after it and not fired: a snooze or a fire is undone. Needs the config's permission; refused for a deadline with nothing to start from now, or whose until holds. Requires authentication.
// - SnoozeTimer: Moves a started deadline's due time to dueAt, and a fired one fires again then. A later write keeps it, until what the deadline starts from changes. Needs the config's permission; refused for a deadline not started, cancelled, or whose until holds. Requires authentication.
type TicketTimersOperations struct {
	client jsonClient
}

// FireTimer Fires a deadline that is due and has not fired: marks it fired and moves the status by its then, recording the outcome; a veto or a missing permission the move meets leaves the status as it is and the deadline fired. A deadline whose until holds is cancelled instead. On a deadline not due, fired already or not started it changes nothing. Any principal who may write the instance may call it; the engine's runner calls it on every due deadline, every sweepMs.
// Requires authentication.
func (n *TicketTimersOperations) FireTimer(
	ctx context.Context,
	Id string,
	input types.TicketTimersFireTimerInput,
) (types.TicketTimersFireTimerResult, error) {
	path := fmt.Sprintf("/api/ticket/%v/timers/fire-timer", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.TicketTimersFireTimerResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.TicketTimersFireTimerResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.TicketTimersFireTimerResult
		return zero, err
	}
	return out, nil
}

// ResetTimer Starts a deadline again from what it starts from now, the create, the status's entry into its state or its field's value, so it is due afterMs after it and not fired: a snooze or a fire is undone. Needs the config's permission; refused for a deadline with nothing to start from now, or whose until holds.
// Requires authentication.
func (n *TicketTimersOperations) ResetTimer(
	ctx context.Context,
	Id string,
	input types.TicketTimersResetTimerInput,
) (types.TicketTimersResetTimerResult, error) {
	path := fmt.Sprintf("/api/ticket/%v/timers/reset-timer", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.TicketTimersResetTimerResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.TicketTimersResetTimerResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.TicketTimersResetTimerResult
		return zero, err
	}
	return out, nil
}

// SnoozeTimer Moves a started deadline's due time to dueAt, and a fired one fires again then. A later write keeps it, until what the deadline starts from changes. Needs the config's permission; refused for a deadline not started, cancelled, or whose until holds.
// Requires authentication.
func (n *TicketTimersOperations) SnoozeTimer(
	ctx context.Context,
	Id string,
	input types.TicketTimersSnoozeTimerInput,
) (types.TicketTimersSnoozeTimerResult, error) {
	path := fmt.Sprintf("/api/ticket/%v/timers/snooze-timer", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.TicketTimersSnoozeTimerResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.TicketTimersSnoozeTimerResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.TicketTimersSnoozeTimerResult
		return zero, err
	}
	return out, nil
}
