package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-seats-api"
)

// ProblemNamespace contains API methods for the problem namespace.
type ProblemNamespace struct {
	client jsonClient
	// Seats holds the operations of the Seats Problem implements (D103).
	Seats *ProblemSeatsOperations
	// Status holds the operations of Problem's status, a Workflow (D103).
	Status *ProblemStatusOperations
}

// NewProblemNamespace creates a ProblemNamespace.
func NewProblemNamespace(
	client jsonClient,
) *ProblemNamespace {
	return &ProblemNamespace{
		client: client,
		Seats:  &ProblemSeatsOperations{client: client},
		Status: &ProblemStatusOperations{client: client},
	}
}

// ProblemSeatsOperations holds the operations of the Seats Problem implements (D103).
//
// Endpoints:
// - LeaveSeat: Unseats a subject from a seat: the caller, which needs no permission, or another, with the seat's assign permission. The subject the seat's fromField holds cannot leave while it holds them. A subject not seated is answered unchanged. Requires authentication.
// - ListSeats: Lists the subjects seated on the row, by seat in code point order, each seat's fromField subject first and then the others in the order they were seated; with seat, only its subjects. Requires authentication.
// - TakeSeat: Seats a subject in a seat: the caller, with the seat's take permission when it names one, or another, with its assign permission. A full seat refuses it, or unseats the subject seated longest when its onFull is displace. A subject seated already is answered unchanged. Requires authentication.
type ProblemSeatsOperations struct {
	client jsonClient
}

// LeaveSeat Unseats a subject from a seat: the caller, which needs no permission, or another, with the seat's assign permission. The subject the seat's fromField holds cannot leave while it holds them. A subject not seated is answered unchanged.
// Requires authentication.
func (n *ProblemSeatsOperations) LeaveSeat(
	ctx context.Context,
	Id string,
	input types.ProblemSeatsLeaveSeatInput,
) (types.ProblemSeatsLeaveSeatResult, error) {
	path := fmt.Sprintf("/api/problem/%v/seats/leave-seat", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSeatsLeaveSeatResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSeatsLeaveSeatResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSeatsLeaveSeatResult
		return zero, err
	}
	return out, nil
}

// ListSeats Lists the subjects seated on the row, by seat in code point order, each seat's fromField subject first and then the others in the order they were seated; with seat, only its subjects.
// Requires authentication.
func (n *ProblemSeatsOperations) ListSeats(
	ctx context.Context,
	Id string,
	input types.ProblemSeatsListSeatsInput,
) (types.ProblemSeatsListSeatsResult, error) {
	path := fmt.Sprintf("/api/problem/%v/seats/list-seats", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSeatsListSeatsResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSeatsListSeatsResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSeatsListSeatsResult
		return zero, err
	}
	return out, nil
}

// TakeSeat Seats a subject in a seat: the caller, with the seat's take permission when it names one, or another, with its assign permission. A full seat refuses it, or unseats the subject seated longest when its onFull is displace. A subject seated already is answered unchanged.
// Requires authentication.
func (n *ProblemSeatsOperations) TakeSeat(
	ctx context.Context,
	Id string,
	input types.ProblemSeatsTakeSeatInput,
) (types.ProblemSeatsTakeSeatResult, error) {
	path := fmt.Sprintf("/api/problem/%v/seats/take-seat", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSeatsTakeSeatResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSeatsTakeSeatResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSeatsTakeSeatResult
		return zero, err
	}
	return out, nil
}

// ProblemStatusOperations holds the operations of Problem's status, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type ProblemStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *ProblemStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.ProblemStatusTransitionInput,
) (types.ProblemStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/problem/%v/status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemStatusTransitionResult
		return zero, err
	}
	return out, nil
}
