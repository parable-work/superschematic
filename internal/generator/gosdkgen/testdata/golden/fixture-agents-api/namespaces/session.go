package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-agents-api"
)

// SessionNamespace contains API methods for the session namespace.
type SessionNamespace struct {
	client jsonClient
	// Presence holds the operations of the Presence Session implements (D103).
	Presence *SessionPresenceOperations
	// Status holds the operations of Session's status, a Workflow (D103).
	Status *SessionStatusOperations
}

// NewSessionNamespace creates a SessionNamespace.
func NewSessionNamespace(
	client jsonClient,
) *SessionNamespace {
	return &SessionNamespace{
		client:   client,
		Presence: &SessionPresenceOperations{client: client},
		Status:   &SessionStatusOperations{client: client},
	}
}

// SessionPresenceOperations holds the operations of the Presence Session implements (D103).
//
// Endpoints:
// - Beat: Moves the deadline to ttlMs from now and clears missed, and onBeat moves the status when it is one of its from states. Only the principal whose subject the instance's principalField holds. Requires authentication.
// - Miss: Applies the miss of an instance past its deadline and not missed yet: sets missed, moves the status with onMissed when it is one of its from states, and expires the principal's leases on the releaseLeases schemas that were not renewed after its last beat, recording which in released. Any principal who may write the instance may call it; on any other instance it changes nothing. The engine's runner calls it every sweepMs. Requires authentication.
type SessionPresenceOperations struct {
	client jsonClient
}

// Beat Moves the deadline to ttlMs from now and clears missed, and onBeat moves the status when it is one of its from states. Only the principal whose subject the instance's principalField holds.
// Requires authentication.
func (n *SessionPresenceOperations) Beat(
	ctx context.Context,
	Id string,
) (types.SessionPresenceBeatResult, error) {
	path := fmt.Sprintf("/api/session/%v/presence/beat", pathSegment(Id))

	var requestBody any
	var out types.SessionPresenceBeatResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.SessionPresenceBeatResult
		return zero, err
	}
	return out, nil
}

// Miss Applies the miss of an instance past its deadline and not missed yet: sets missed, moves the status with onMissed when it is one of its from states, and expires the principal's leases on the releaseLeases schemas that were not renewed after its last beat, recording which in released. Any principal who may write the instance may call it; on any other instance it changes nothing. The engine's runner calls it every sweepMs.
// Requires authentication.
func (n *SessionPresenceOperations) Miss(
	ctx context.Context,
	Id string,
) (types.SessionPresenceMissResult, error) {
	path := fmt.Sprintf("/api/session/%v/presence/miss", pathSegment(Id))

	var requestBody any
	var out types.SessionPresenceMissResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.SessionPresenceMissResult
		return zero, err
	}
	return out, nil
}

// SessionStatusOperations holds the operations of Session's status, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type SessionStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *SessionStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.SessionStatusTransitionInput,
) (types.SessionStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/session/%v/status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.SessionStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.SessionStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.SessionStatusTransitionResult
		return zero, err
	}
	return out, nil
}
