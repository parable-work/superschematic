package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-agents-api"
)

// JobNamespace contains API methods for the job namespace.
type JobNamespace struct {
	client jsonClient
	// Lease holds the operations of the Lease Job implements (D103).
	Lease *JobLeaseOperations
	// Retries holds the operations of the Retries Job implements (D103).
	Retries *JobRetriesOperations
	// Status holds the operations of Job's status, a Workflow (D103).
	Status *JobStatusOperations
}

// NewJobNamespace creates a JobNamespace.
func NewJobNamespace(
	client jsonClient,
) *JobNamespace {
	return &JobNamespace{
		client:  client,
		Lease:   &JobLeaseOperations{client: client},
		Retries: &JobRetriesOperations{client: client},
		Status:  &JobStatusOperations{client: client},
	}
}

// JobLeaseOperations holds the operations of the Lease Job implements (D103).
//
// Endpoints:
// - ExpireHolder: Expires every lease one principal holds on the schema's instances, active or not, as expire with that holder does on each: for a principal that is gone. With notRenewedAfter, an active lease renewed after that time is spared. Needs overridePermission; refused when the config names none. Requires authentication.
// - Acknowledge: Marks directives of the lease as handled, so heartbeats stop delivering them. Only the holder, presenting the current token as Lease's precondition. Requires authentication.
// - Acquire: Takes the lease for the caller and returns its fencing token. Refused while a lease is active, the caller's own included (heartbeat renews it), without acquirePermission when the config names one, and once the instance has had maxExpiries expiries. Over a lease that expired, it applies that expiry first. Requires authentication.
// - Direct: Sends the holder of the active lease a directive, which its heartbeats deliver until it acknowledges it. With dedupeKey, a directive already sent under the lease with the key is not sent again. A principal needs directPermission, or overridePermission when that is absent, and is refused when the config names neither; another behavior of the type sends one with call(), as the principal it runs for, without either. Requires authentication.
// - Expire: Applies the expiry of a lease past its expiry time or its longest hold: the holder is cleared, the token advances, the expiry is counted unless the instance is in a terminal state of its Workflow, and onExpiry or escalate moves the status. Any principal who may write the instance may call it; on any other instance it changes nothing. With holder, a principal with overridePermission expires that holder's lease at once, active or not, unless notRenewedAfter spares it. The engine's runner calls it on every lapsed lease, every sweepMs. Requires authentication.
// - Heartbeat: Renews the lease, never past its longest hold, acknowledges the directives acknowledge names, and returns the directives sent under it that are not acknowledged. Only the holder, presenting the current token as Lease's precondition, while the lease is active: an expired lease cannot be renewed. Requires authentication.
// - Release: Ends an active lease: the holder, presenting the current token as Lease's precondition, or a principal with overridePermission, who need not. The token advances, so the directives sent under the lease are not delivered again, and onExpiry moves the status when it is still one of its from states. With abandon, the holder gives the work up as failed: the release counts as an expiry, and escalate applies at the one that reaches maxExpiries. Requires authentication.
// - ResetExpiries: Sets the instance's count of expiries back to 0, so a lease can be acquired again after maxExpiries. Needs overridePermission; refused when the config names none. Requires authentication.
type JobLeaseOperations struct {
	client jsonClient
}

// ExpireHolder Expires every lease one principal holds on the schema's instances, active or not, as expire with that holder does on each: for a principal that is gone. With notRenewedAfter, an active lease renewed after that time is spared. Needs overridePermission; refused when the config names none.
// Requires authentication.
func (n *JobLeaseOperations) ExpireHolder(
	ctx context.Context,
	input types.JobLeaseExpireHolderInput,
) (types.JobLeaseExpireHolderResult, error) {
	path := "/api/job/lease/expire-holder"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.JobLeaseExpireHolderResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.JobLeaseExpireHolderResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.JobLeaseExpireHolderResult
		return zero, err
	}
	return out, nil
}

// Acknowledge Marks directives of the lease as handled, so heartbeats stop delivering them. Only the holder, presenting the current token as Lease's precondition.
// Requires authentication.
func (n *JobLeaseOperations) Acknowledge(
	ctx context.Context,
	Id string,
	input types.JobLeaseAcknowledgeInput,
) (types.GenericJSON, error) {
	path := fmt.Sprintf("/api/job/%v/lease/acknowledge", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.GenericJSON
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.GenericJSON
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.GenericJSON
		return zero, err
	}
	return out, nil
}

// Acquire Takes the lease for the caller and returns its fencing token. Refused while a lease is active, the caller's own included (heartbeat renews it), without acquirePermission when the config names one, and once the instance has had maxExpiries expiries. Over a lease that expired, it applies that expiry first.
// Requires authentication.
func (n *JobLeaseOperations) Acquire(
	ctx context.Context,
	Id string,
	input types.JobLeaseAcquireInput,
) (types.JobLeaseAcquireResult, error) {
	path := fmt.Sprintf("/api/job/%v/lease/acquire", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.JobLeaseAcquireResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.JobLeaseAcquireResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.JobLeaseAcquireResult
		return zero, err
	}
	return out, nil
}

// Direct Sends the holder of the active lease a directive, which its heartbeats deliver until it acknowledges it. With dedupeKey, a directive already sent under the lease with the key is not sent again. A principal needs directPermission, or overridePermission when that is absent, and is refused when the config names neither; another behavior of the type sends one with call(), as the principal it runs for, without either.
// Requires authentication.
func (n *JobLeaseOperations) Direct(
	ctx context.Context,
	Id string,
	input types.JobLeaseDirectInput,
) (types.JobLeaseDirectResult, error) {
	path := fmt.Sprintf("/api/job/%v/lease/direct", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.JobLeaseDirectResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.JobLeaseDirectResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.JobLeaseDirectResult
		return zero, err
	}
	return out, nil
}

// Expire Applies the expiry of a lease past its expiry time or its longest hold: the holder is cleared, the token advances, the expiry is counted unless the instance is in a terminal state of its Workflow, and onExpiry or escalate moves the status. Any principal who may write the instance may call it; on any other instance it changes nothing. With holder, a principal with overridePermission expires that holder's lease at once, active or not, unless notRenewedAfter spares it. The engine's runner calls it on every lapsed lease, every sweepMs.
// Requires authentication.
func (n *JobLeaseOperations) Expire(
	ctx context.Context,
	Id string,
	input types.JobLeaseExpireInput,
) (types.JobLeaseExpireResult, error) {
	path := fmt.Sprintf("/api/job/%v/lease/expire", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.JobLeaseExpireResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.JobLeaseExpireResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.JobLeaseExpireResult
		return zero, err
	}
	return out, nil
}

// Heartbeat Renews the lease, never past its longest hold, acknowledges the directives acknowledge names, and returns the directives sent under it that are not acknowledged. Only the holder, presenting the current token as Lease's precondition, while the lease is active: an expired lease cannot be renewed.
// Requires authentication.
func (n *JobLeaseOperations) Heartbeat(
	ctx context.Context,
	Id string,
	input types.JobLeaseHeartbeatInput,
) (types.JobLeaseHeartbeatResult, error) {
	path := fmt.Sprintf("/api/job/%v/lease/heartbeat", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.JobLeaseHeartbeatResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.JobLeaseHeartbeatResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.JobLeaseHeartbeatResult
		return zero, err
	}
	return out, nil
}

// Release Ends an active lease: the holder, presenting the current token as Lease's precondition, or a principal with overridePermission, who need not. The token advances, so the directives sent under the lease are not delivered again, and onExpiry moves the status when it is still one of its from states. With abandon, the holder gives the work up as failed: the release counts as an expiry, and escalate applies at the one that reaches maxExpiries.
// Requires authentication.
func (n *JobLeaseOperations) Release(
	ctx context.Context,
	Id string,
	input types.JobLeaseReleaseInput,
) (types.GenericJSON, error) {
	path := fmt.Sprintf("/api/job/%v/lease/release", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.GenericJSON
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.GenericJSON
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.GenericJSON
		return zero, err
	}
	return out, nil
}

// ResetExpiries Sets the instance's count of expiries back to 0, so a lease can be acquired again after maxExpiries. Needs overridePermission; refused when the config names none.
// Requires authentication.
func (n *JobLeaseOperations) ResetExpiries(
	ctx context.Context,
	Id string,
	input types.JobLeaseResetExpiriesInput,
) (types.JobLeaseResetExpiriesResult, error) {
	path := fmt.Sprintf("/api/job/%v/lease/reset-expiries", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.JobLeaseResetExpiriesResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.JobLeaseResetExpiriesResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.JobLeaseResetExpiriesResult
		return zero, err
	}
	return out, nil
}

// JobRetriesOperations holds the operations of the Retries Job implements (D103).
//
// Endpoints:
// - RecordAttempt: Records an attempt: a success without failure, or a failure of one of the config's classes, counted against its caps, and returns the class's hint. Decides whether its result is kept, and writes a kept result to resultField. Exhausts the instance at a terminal class, a failure past a cap or a stuck signature, moving its status to exhaustedState from the from states. Refused once the instance is exhausted, and without the config's permission. Requires authentication.
type JobRetriesOperations struct {
	client jsonClient
}

// RecordAttempt Records an attempt: a success without failure, or a failure of one of the config's classes, counted against its caps, and returns the class's hint. Decides whether its result is kept, and writes a kept result to resultField. Exhausts the instance at a terminal class, a failure past a cap or a stuck signature, moving its status to exhaustedState from the from states. Refused once the instance is exhausted, and without the config's permission.
// Requires authentication.
func (n *JobRetriesOperations) RecordAttempt(
	ctx context.Context,
	Id string,
	input types.JobRetriesRecordAttemptInput,
) (types.JobRetriesRecordAttemptResult, error) {
	path := fmt.Sprintf("/api/job/%v/retries/record-attempt", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.JobRetriesRecordAttemptResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.JobRetriesRecordAttemptResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.JobRetriesRecordAttemptResult
		return zero, err
	}
	return out, nil
}

// JobStatusOperations holds the operations of Job's status, a Workflow (D103).
//
// Endpoints:
// - Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
type JobStatusOperations struct {
	client jsonClient
}

// Transition Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
// Requires authentication.
func (n *JobStatusOperations) Transition(
	ctx context.Context,
	Id string,
	input types.JobStatusTransitionInput,
) (types.JobStatusTransitionResult, error) {
	path := fmt.Sprintf("/api/job/%v/status/transition", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.JobStatusTransitionResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.JobStatusTransitionResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.JobStatusTransitionResult
		return zero, err
	}
	return out, nil
}
