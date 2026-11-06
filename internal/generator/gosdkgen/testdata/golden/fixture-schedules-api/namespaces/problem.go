package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-schedules-api"
)

// ProblemNamespace contains API methods for the problem namespace.
type ProblemNamespace struct {
	client jsonClient
	// Schedules holds the operations of the Schedules Problem implements (D103).
	Schedules *ProblemSchedulesOperations
	// Status holds the operations of Problem's status, a Workflow (D103).
	Status *ProblemStatusOperations
}

// NewProblemNamespace creates a ProblemNamespace.
func NewProblemNamespace(
	client jsonClient,
) *ProblemNamespace {
	return &ProblemNamespace{
		client:    client,
		Schedules: &ProblemSchedulesOperations{client: client},
		Status:    &ProblemStatusOperations{client: client},
	}
}

// ProblemSchedulesOperations holds the operations of the Schedules Problem implements (D103).
//
// Endpoints:
// - CancelSchedule: Finishes a live schedule, clearing its deadline, so it no longer counts toward max; it stays, finished, for its latest run's report and a read. A finished one is answered unchanged. Requires authentication.
// - ListSchedules: Lists the row's schedules by name in code point order, live and finished; with name, only that one. Requires authentication.
// - PauseSchedule: Pauses an active schedule: it keeps its next run and the retry of its latest one, and its deadline is cleared until it resumes. A paused one is answered unchanged. Requires authentication.
// - ReportRun: Records the outcome of a schedule's latest run, one of the config's outcomes. The config's retry outcome tries the run again afterMs on, as its next attempt, unless the next regular run comes first; another outcome cancels a retry armed for it. A report of an older run, of a replaced schedule's latest run, or of an attempt other than the latest, or a repeat of the outcome recorded, is answered unchanged. Requires authentication.
// - ResumeSchedule: Resumes a paused schedule, arming its next run, or the retry of its latest, again: one past its time falls due at once and fires once. An active one is answered unchanged; a row in a state endOn names resumes none. Requires authentication.
// - SetSchedule: Sets a schedule of the row by its name: exactly one of after, every and cron, of a kind the config's kinds take, within minMs and maxMs, a cron line read in timeZone. A live schedule of the name is replaced and its runs start again; one with the same kind, spec and time zone is answered unchanged. A new name needs room under max, and a row in a state endOn names takes none. Requires authentication.
type ProblemSchedulesOperations struct {
	client jsonClient
}

// CancelSchedule Finishes a live schedule, clearing its deadline, so it no longer counts toward max; it stays, finished, for its latest run's report and a read. A finished one is answered unchanged.
// Requires authentication.
func (n *ProblemSchedulesOperations) CancelSchedule(
	ctx context.Context,
	Id string,
	input types.ProblemSchedulesCancelScheduleInput,
) (types.ProblemSchedulesCancelScheduleResult, error) {
	path := fmt.Sprintf("/api/problem/%v/schedules/cancel-schedule", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSchedulesCancelScheduleResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSchedulesCancelScheduleResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSchedulesCancelScheduleResult
		return zero, err
	}
	return out, nil
}

// ListSchedules Lists the row's schedules by name in code point order, live and finished; with name, only that one.
// Requires authentication.
func (n *ProblemSchedulesOperations) ListSchedules(
	ctx context.Context,
	Id string,
	input types.ProblemSchedulesListSchedulesInput,
) (types.ProblemSchedulesListSchedulesResult, error) {
	path := fmt.Sprintf("/api/problem/%v/schedules/list-schedules", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSchedulesListSchedulesResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSchedulesListSchedulesResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSchedulesListSchedulesResult
		return zero, err
	}
	return out, nil
}

// PauseSchedule Pauses an active schedule: it keeps its next run and the retry of its latest one, and its deadline is cleared until it resumes. A paused one is answered unchanged.
// Requires authentication.
func (n *ProblemSchedulesOperations) PauseSchedule(
	ctx context.Context,
	Id string,
	input types.ProblemSchedulesPauseScheduleInput,
) (types.ProblemSchedulesPauseScheduleResult, error) {
	path := fmt.Sprintf("/api/problem/%v/schedules/pause-schedule", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSchedulesPauseScheduleResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSchedulesPauseScheduleResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSchedulesPauseScheduleResult
		return zero, err
	}
	return out, nil
}

// ReportRun Records the outcome of a schedule's latest run, one of the config's outcomes. The config's retry outcome tries the run again afterMs on, as its next attempt, unless the next regular run comes first; another outcome cancels a retry armed for it. A report of an older run, of a replaced schedule's latest run, or of an attempt other than the latest, or a repeat of the outcome recorded, is answered unchanged.
// Requires authentication.
func (n *ProblemSchedulesOperations) ReportRun(
	ctx context.Context,
	Id string,
	input types.ProblemSchedulesReportRunInput,
) (types.ProblemSchedulesReportRunResult, error) {
	path := fmt.Sprintf("/api/problem/%v/schedules/report-run", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSchedulesReportRunResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSchedulesReportRunResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSchedulesReportRunResult
		return zero, err
	}
	return out, nil
}

// ResumeSchedule Resumes a paused schedule, arming its next run, or the retry of its latest, again: one past its time falls due at once and fires once. An active one is answered unchanged; a row in a state endOn names resumes none.
// Requires authentication.
func (n *ProblemSchedulesOperations) ResumeSchedule(
	ctx context.Context,
	Id string,
	input types.ProblemSchedulesResumeScheduleInput,
) (types.ProblemSchedulesResumeScheduleResult, error) {
	path := fmt.Sprintf("/api/problem/%v/schedules/resume-schedule", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSchedulesResumeScheduleResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSchedulesResumeScheduleResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSchedulesResumeScheduleResult
		return zero, err
	}
	return out, nil
}

// SetSchedule Sets a schedule of the row by its name: exactly one of after, every and cron, of a kind the config's kinds take, within minMs and maxMs, a cron line read in timeZone. A live schedule of the name is replaced and its runs start again; one with the same kind, spec and time zone is answered unchanged. A new name needs room under max, and a row in a state endOn names takes none.
// Requires authentication.
func (n *ProblemSchedulesOperations) SetSchedule(
	ctx context.Context,
	Id string,
	input types.ProblemSchedulesSetScheduleInput,
) (types.ProblemSchedulesSetScheduleResult, error) {
	path := fmt.Sprintf("/api/problem/%v/schedules/set-schedule", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.ProblemSchedulesSetScheduleResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.ProblemSchedulesSetScheduleResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ProblemSchedulesSetScheduleResult
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
