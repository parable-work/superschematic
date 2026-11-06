package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-pool-api"
)

// AccountNamespace contains API methods for the account namespace.
type AccountNamespace struct {
	client jsonClient
	// Pool holds the operations of the Pool Account implements (D103).
	Pool *AccountPoolOperations
	// Budget holds the operations of the Budget Account implements (D103).
	Budget *AccountBudgetOperations
}

// NewAccountNamespace creates a AccountNamespace.
func NewAccountNamespace(
	client jsonClient,
) *AccountNamespace {
	return &AccountNamespace{
		client: client,
		Pool:   &AccountPoolOperations{client: client},
		Budget: &AccountBudgetOperations{client: client},
	}
}

// AccountPoolOperations holds the operations of the Pool Account implements (D103).
//
// Endpoints:
// - Pick: Picks a member to run work on, a read of the table as a whole that writes nothing: of the members match keeps, leaving out those exclude names, the first by order of the open members prefer keeps, then of its near ones, then of the other open and near ones. A member counts as out while its table's Capacity has no room for it. With none it answers member null and until, the soonest time a member match keeps may be back, so a caller waits on a deadline of its own. It reads at most 2000 members. Requires authentication.
// - MarkOut: Puts the member out until until, or outForMs from now when it names none, for reason: what a caller a provider refused tells the pool. An out member keeps the later of its outUntil and the new one and takes the new reason, and a call that moves neither is answered unchanged. Arms the member's recheck at its outUntil. Needs permission. Requires authentication.
// - ReportRoom: Records a check of the member at its provider, made now. With room it opens an out member whose outUntil has come, near when near says so, and keeps an open or near one open or near as near says; a check with room before an out member's outUntil records checkedAt alone. With no room the member is out until the later of its outUntil and resetsAt, or outForMs from now when the check names no time, and its recheck is armed then, or recheckAfterMs from now when that is later. Needs permission. Requires authentication.
type AccountPoolOperations struct {
	client jsonClient
}

// Pick Picks a member to run work on, a read of the table as a whole that writes nothing: of the members match keeps, leaving out those exclude names, the first by order of the open members prefer keeps, then of its near ones, then of the other open and near ones. A member counts as out while its table's Capacity has no room for it. With none it answers member null and until, the soonest time a member match keeps may be back, so a caller waits on a deadline of its own. It reads at most 2000 members.
// Requires authentication.
func (n *AccountPoolOperations) Pick(
	ctx context.Context,
	input types.AccountPoolPickInput,
) (types.AccountPoolPickResult, error) {
	path := "/api/account/pool/pick"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountPoolPickResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountPoolPickResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountPoolPickResult
		return zero, err
	}
	return out, nil
}

// MarkOut Puts the member out until until, or outForMs from now when it names none, for reason: what a caller a provider refused tells the pool. An out member keeps the later of its outUntil and the new one and takes the new reason, and a call that moves neither is answered unchanged. Arms the member's recheck at its outUntil. Needs permission.
// Requires authentication.
func (n *AccountPoolOperations) MarkOut(
	ctx context.Context,
	Id string,
	input types.AccountPoolMarkOutInput,
) (types.AccountPoolMarkOutResult, error) {
	path := fmt.Sprintf("/api/account/%v/pool/mark-out", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountPoolMarkOutResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountPoolMarkOutResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountPoolMarkOutResult
		return zero, err
	}
	return out, nil
}

// ReportRoom Records a check of the member at its provider, made now. With room it opens an out member whose outUntil has come, near when near says so, and keeps an open or near one open or near as near says; a check with room before an out member's outUntil records checkedAt alone. With no room the member is out until the later of its outUntil and resetsAt, or outForMs from now when the check names no time, and its recheck is armed then, or recheckAfterMs from now when that is later. Needs permission.
// Requires authentication.
func (n *AccountPoolOperations) ReportRoom(
	ctx context.Context,
	Id string,
	input types.AccountPoolReportRoomInput,
) (types.AccountPoolReportRoomResult, error) {
	path := fmt.Sprintf("/api/account/%v/pool/report-room", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountPoolReportRoomResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountPoolReportRoomResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountPoolReportRoomResult
		return zero, err
	}
	return out, nil
}

// AccountBudgetOperations holds the operations of the Budget Account implements (D103).
//
// Endpoints:
// - CheckReserve: Whether reserve with the same parameters would fit now, here and in every enclosing scope, after the reservations of a lease that is no longer active are settled, as reserve settles them; it changes nothing and does not ask for a lease. Queue copies its answer so that a claim that cannot fit is not tried. Requires authentication.
// - Observe: Records the usage of a meter a provider reports, in place of what the meter counts until resetsAt (until the meter's next reset without it, and until the next observe for a meter with no reset): the meter's used is the figure given, usage recorded after it adds to it, and limit, when given, is the meter's limit while it holds. Nothing passes up to an enclosing scope. Needs observePermission. A table's alone: an engine type refuses it. Requires authentication.
// - RecordUsage: Records usage of a meter, which is never refused. The part of it the instance's own reservation covers is released, here and in every enclosing scope, and the usage counts in each of them. Reports every instance of the chain whose usage is over its limit after it, moves the status of each whose config gives escalate, and sends onExceeded's directive to the holder of the active lease once per meter per lease. Requires authentication.
// - RecordUsageFor: Counts usage of a meter by an instance whose scope link points here, releases the part of what this instance holds for it that its own reservation covered, never more than that one no longer holds, and passes both on up this instance's own scope. Never refused for the amount. Requires authentication.
// - Reserve: Reserves an amount of one meter, or of every meter the config gives a reservation, for the instance and every enclosing scope. Refused when the instance or an enclosing scope cannot fit it, and then nothing is reserved anywhere. On a type that composes Lease it needs an active lease, and the reservation is settled when that lease ends. Requires authentication.
// - ReserveFor: Holds an amount of a meter for an instance whose scope link points here, as that instance's reserve does, and passes it on up this instance's own scope. Refused when the amount does not fit, when that instance does not draw the meter from here, and beyond what it has reserved and this instance does not hold for it yet. Requires authentication.
// - SetLimit: Sets a meter's limit on the instance, raising or lowering it; through limitField when the meter has one. Needs limitPermission, and is refused when the config names none, and below what the instance has used and reserved. Requires authentication.
// - Settle: Releases the instance's own reservation of one meter, or of every meter, here and in every enclosing scope. Requires authentication.
// - SettleFor: Releases up to an amount of what this instance holds of a meter for another, never more than that one no longer holds itself, and passes the release on up this instance's own scope. Requires authentication.
type AccountBudgetOperations struct {
	client jsonClient
}

// CheckReserve Whether reserve with the same parameters would fit now, here and in every enclosing scope, after the reservations of a lease that is no longer active are settled, as reserve settles them; it changes nothing and does not ask for a lease. Queue copies its answer so that a claim that cannot fit is not tried.
// Requires authentication.
func (n *AccountBudgetOperations) CheckReserve(
	ctx context.Context,
	Id string,
	input types.AccountBudgetCheckReserveInput,
) (types.AccountBudgetCheckReserveResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/check-reserve", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetCheckReserveResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetCheckReserveResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetCheckReserveResult
		return zero, err
	}
	return out, nil
}

// Observe Records the usage of a meter a provider reports, in place of what the meter counts until resetsAt (until the meter's next reset without it, and until the next observe for a meter with no reset): the meter's used is the figure given, usage recorded after it adds to it, and limit, when given, is the meter's limit while it holds. Nothing passes up to an enclosing scope. Needs observePermission. A table's alone: an engine type refuses it.
// Requires authentication.
func (n *AccountBudgetOperations) Observe(
	ctx context.Context,
	Id string,
	input types.AccountBudgetObserveInput,
) (types.AccountBudgetObserveResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/observe", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetObserveResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetObserveResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetObserveResult
		return zero, err
	}
	return out, nil
}

// RecordUsage Records usage of a meter, which is never refused. The part of it the instance's own reservation covers is released, here and in every enclosing scope, and the usage counts in each of them. Reports every instance of the chain whose usage is over its limit after it, moves the status of each whose config gives escalate, and sends onExceeded's directive to the holder of the active lease once per meter per lease.
// Requires authentication.
func (n *AccountBudgetOperations) RecordUsage(
	ctx context.Context,
	Id string,
	input types.AccountBudgetRecordUsageInput,
) (types.AccountBudgetRecordUsageResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/record-usage", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetRecordUsageResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetRecordUsageResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetRecordUsageResult
		return zero, err
	}
	return out, nil
}

// RecordUsageFor Counts usage of a meter by an instance whose scope link points here, releases the part of what this instance holds for it that its own reservation covered, never more than that one no longer holds, and passes both on up this instance's own scope. Never refused for the amount.
// Requires authentication.
func (n *AccountBudgetOperations) RecordUsageFor(
	ctx context.Context,
	Id string,
	input types.AccountBudgetRecordUsageForInput,
) (types.AccountBudgetRecordUsageForResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/record-usage-for", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetRecordUsageForResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetRecordUsageForResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetRecordUsageForResult
		return zero, err
	}
	return out, nil
}

// Reserve Reserves an amount of one meter, or of every meter the config gives a reservation, for the instance and every enclosing scope. Refused when the instance or an enclosing scope cannot fit it, and then nothing is reserved anywhere. On a type that composes Lease it needs an active lease, and the reservation is settled when that lease ends.
// Requires authentication.
func (n *AccountBudgetOperations) Reserve(
	ctx context.Context,
	Id string,
	input types.AccountBudgetReserveInput,
) (types.AccountBudgetReserveResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/reserve", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetReserveResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetReserveResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetReserveResult
		return zero, err
	}
	return out, nil
}

// ReserveFor Holds an amount of a meter for an instance whose scope link points here, as that instance's reserve does, and passes it on up this instance's own scope. Refused when the amount does not fit, when that instance does not draw the meter from here, and beyond what it has reserved and this instance does not hold for it yet.
// Requires authentication.
func (n *AccountBudgetOperations) ReserveFor(
	ctx context.Context,
	Id string,
	input types.AccountBudgetReserveForInput,
) (types.AccountBudgetReserveForResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/reserve-for", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetReserveForResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetReserveForResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetReserveForResult
		return zero, err
	}
	return out, nil
}

// SetLimit Sets a meter's limit on the instance, raising or lowering it; through limitField when the meter has one. Needs limitPermission, and is refused when the config names none, and below what the instance has used and reserved.
// Requires authentication.
func (n *AccountBudgetOperations) SetLimit(
	ctx context.Context,
	Id string,
	input types.AccountBudgetSetLimitInput,
) (types.AccountBudgetSetLimitResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/set-limit", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetSetLimitResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetSetLimitResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetSetLimitResult
		return zero, err
	}
	return out, nil
}

// Settle Releases the instance's own reservation of one meter, or of every meter, here and in every enclosing scope.
// Requires authentication.
func (n *AccountBudgetOperations) Settle(
	ctx context.Context,
	Id string,
	input types.AccountBudgetSettleInput,
) (types.AccountBudgetSettleResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/settle", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetSettleResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetSettleResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetSettleResult
		return zero, err
	}
	return out, nil
}

// SettleFor Releases up to an amount of what this instance holds of a meter for another, never more than that one no longer holds itself, and passes the release on up this instance's own scope.
// Requires authentication.
func (n *AccountBudgetOperations) SettleFor(
	ctx context.Context,
	Id string,
	input types.AccountBudgetSettleForInput,
) (types.AccountBudgetSettleForResult, error) {
	path := fmt.Sprintf("/api/account/%v/budget/settle-for", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.AccountBudgetSettleForResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.AccountBudgetSettleForResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.AccountBudgetSettleForResult
		return zero, err
	}
	return out, nil
}
