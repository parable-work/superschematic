// Package migrate applies a migration plan to a database (D27 in
// docs/DECISIONS.md). The compiler writes plans (superschematic migrate
// plan); this package reads them, checks them and runs their steps, and it
// never computes one. runtime/migrate/README.md is the contract between the
// two: the plan document, its canonical JSON and hashes, the state tables
// and what apply, status and adopt do.
//
// A Runner reaches the database through a Driver. Package postgres
// implements it over one pgx connection and package sqlite over one
// connection of the pure-Go SQLite driver.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// DefaultWaits are the pauses before each retry of a step that hit a lock
// timeout. The step fails after the last retry.
var DefaultWaits = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// Runner applies plans to one database and records them in its state
// tables.
type Runner struct {
	Driver Driver
	// Log receives a line per step and per change of state. Nil discards
	// them.
	Log io.Writer
	// Waits are the pauses before each retry of a step that hit a lock
	// timeout. Nil is DefaultWaits.
	Waits []time.Duration
	// Sleep pauses for d or until ctx ends. Nil uses a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// AfterStep, when set, runs after each step this run finishes. An
	// error it returns stops the run as a failed step does. Tests use it
	// to stop a run part-way.
	AfterStep func(ctx context.Context, step *Step) error
}

// Result is what one Apply did.
type Result struct {
	// AlreadyApplied is true when the database's applied model was the
	// plan's to and nothing ran.
	AlreadyApplied bool
	// Ran lists the steps this run ran, by index.
	Ran []int
	// ExpandDone is true when every expand step of the plan has finished.
	ExpandDone bool
	// Finished is true when the plan has finished: the database's applied
	// model is the plan's to.
	Finished bool
	// Superseded is the plan whose pending contract this plan superseded,
	// or empty.
	Superseded string
}

// Apply runs the steps of plan in phase (Expand, Contract or All) that have
// not run yet, as runtime/migrate/README.md describes. plan comes from
// ReadPlan. A refusal wraps ErrRefused; a failed step is a *StepError, and
// the next Apply of the same plan resumes at it.
func (r *Runner) Apply(ctx context.Context, plan *Plan, phase Phase) (*Result, error) {
	if plan.model == nil {
		return nil, errors.New("migrate: Apply takes a plan ReadPlan returned")
	}
	switch phase {
	case "":
		phase = All
	case Expand, Contract, All:
	default:
		return nil, fmt.Errorf("migrate: phase %q; apply runs expand, contract or all", phase)
	}
	if dialect := r.Driver.Dialect(); plan.Dialect != dialect {
		return nil, refusef("the plan is for %s and the database is %s", plan.Dialect, dialect)
	}
	sql := stateSQL[plan.Dialect]

	unlock, err := r.Driver.Lock(ctx, plan.Service)
	if err != nil {
		return nil, fmt.Errorf("take the lock of service %s: %w", plan.Service, err)
	}
	defer func() { _ = unlock(context.WithoutCancel(ctx)) }()
	if err := r.createTables(ctx, sql); err != nil {
		return nil, err
	}

	result := &Result{}
	var resume bool
	err = r.transact(ctx, func(ctx context.Context, conn Conn) error {
		st, err := readState(ctx, conn, sql, plan.Service)
		if err != nil {
			return err
		}
		started := false
		if st.planHash != "" && st.planHash != plan.Hash && st.planPhase == PlanPhaseExpanded {
			if started, err = contractStarted(ctx, conn, st.planHash); err != nil {
				return err
			}
		}
		d, err := decide(st, plan, started)
		if err != nil {
			return err
		}
		result.AlreadyApplied, resume = d == alreadyApplied, d == resumePlan
		result.Superseded = ""
		if d == supersede {
			result.Superseded = st.planHash
		}
		if result.AlreadyApplied {
			return nil
		}
		if phase == Contract {
			// Only the plan in progress has a contract to run. A plan that
			// is not, such as one a newer plan superseded, may start from
			// the database's model again when its expand had nothing to
			// do, and running its contract then would drop what the
			// servers that superseded it still use.
			if !resume {
				return refusef("plan %s is not in progress for service %s: --phase contract runs the contract of the plan in progress once its expand phase has run; apply this plan with --phase expand first, or with --phase all",
					plan.Hash, plan.Service)
			}
			if err := expandFinished(ctx, conn, plan, resume); err != nil {
				return err
			}
		}
		if resume {
			return nil
		}
		return startPlan(ctx, conn, plan)
	})
	if err != nil {
		return nil, err
	}
	if result.AlreadyApplied {
		r.logf("plan %s is already applied: service %s is at model %s", plan.Hash, plan.Service, plan.To)
		result.ExpandDone, result.Finished = true, true
		return result, nil
	}
	if result.Superseded != "" {
		r.logf("plan %s supersedes plan %s, whose contract was pending: service %s is at that plan's expanded model %s, and its contract will not run",
			plan.Hash, result.Superseded, plan.Service, plan.From)
	}
	if resume {
		r.logf("resuming plan %s for service %s", plan.Hash, plan.Service)
	} else {
		r.logf("applying plan %s to service %s (%s): %d steps, from %s to %s",
			plan.Hash, plan.Service, plan.Dialect, len(plan.Steps), describeModel(plan.From), plan.To)
	}

	for _, step := range plan.Steps {
		if phase != All && step.Phase != phase {
			continue
		}
		outcome, err := r.runStep(ctx, sql, plan, step)
		if err != nil {
			return result, err
		}
		if outcome == otherRunnerFinished {
			r.logf("another runner finished plan %s", plan.Hash)
			result.ExpandDone, result.Finished = true, true
			return result, nil
		}
		if outcome == ran {
			result.Ran = append(result.Ran, step.Index)
			if r.AfterStep != nil {
				if err := r.AfterStep(ctx, step); err != nil {
					return result, err
				}
			}
		}
	}

	// A phase with no steps, or a run that resumed after its last step
	// committed, changes the state here.
	err = r.transact(ctx, func(ctx context.Context, conn Conn) error {
		st, err := readState(ctx, conn, sql, plan.Service)
		if err != nil {
			return err
		}
		if st.planHash != plan.Hash {
			if st.planHash == "" && st.modelHash == plan.To {
				result.ExpandDone, result.Finished = true, true
				return nil
			}
			return fmt.Errorf("plan %s is no longer in progress for service %s", plan.Hash, plan.Service)
		}
		finished, err := finishedSteps(ctx, conn, plan.Hash)
		if err != nil {
			return err
		}
		return r.advance(ctx, conn, plan, st, finished, result)
	})
	if err != nil {
		return result, err
	}
	switch {
	case result.Finished:
		r.logf("plan %s applied: service %s is at model %s", plan.Hash, plan.Service, plan.To)
	case result.ExpandDone:
		r.logf("plan %s: expand is done; apply --phase contract after the rollout", plan.Hash)
	}
	return result, nil
}

// decision is what step 4 of apply decides to do with a plan.
type decision int

const (
	// start: no plan is in progress and the database is at the plan's
	// from.
	start decision = iota
	// resumePlan: the plan is in progress.
	resumePlan
	// alreadyApplied: the database is at the plan's to.
	alreadyApplied
	// supersede: another plan is in progress with only its contract left,
	// none of it started, and the database is at that plan's expanded
	// model, the plan's from.
	supersede
)

// decide is step 4 of apply: resume the plan, report it applied, start it,
// start it in place of a pending contract, or refuse. contractStarted is
// whether a contract step of the plan in progress has started; decide reads
// it only when that plan recorded its expanded model.
func decide(st state, plan *Plan, contractStarted bool) (decision, error) {
	pending := st.planHash != "" && st.planPhase == PlanPhaseExpanded && st.modelHash == plan.From
	switch {
	case st.planHash == plan.Hash:
		return resumePlan, nil
	case st.planHash == "" && st.modelHash == plan.To && plan.To != plan.From:
		return alreadyApplied, nil
	case pending && contractStarted:
		return 0, refusef("service %s has plan %s in progress, and its contract has started; finish it before applying plan %s",
			plan.Service, st.planHash, plan.Hash)
	case st.planHash != "" && !pending:
		return 0, refusef("service %s has plan %s in progress (%s); finish it before applying plan %s",
			plan.Service, st.planHash, describePhase(st.planPhase), plan.Hash)
	case st.modelHash != plan.From:
		applied := "has no applied model"
		if st.modelHash != "" {
			applied = "is at model " + st.modelHash
		}
		return 0, refusef("the plan starts from %s, but service %s %s; plan again from the applied model (superschematic-migrate status --model)",
			describeModel(plan.From), plan.Service, applied)
	case st.recorded && st.dialect != plan.Dialect:
		return 0, refusef("service %s's state records dialect %s; the plan is for %s", plan.Service, st.dialect, plan.Dialect)
	case pending:
		return supersede, nil
	}
	return start, nil
}

// expandFinished is step 5 of apply: --phase contract runs only once every
// expand step has finished.
func expandFinished(ctx context.Context, conn Conn, plan *Plan, resume bool) error {
	expand := plan.steps(Expand)
	if len(expand) == 0 {
		return nil
	}
	finished := map[int]bool{}
	if resume {
		var err error
		if finished, err = finishedSteps(ctx, conn, plan.Hash); err != nil {
			return err
		}
	}
	for _, step := range expand {
		if !finished[step.Index] {
			return refusef("plan %s's expand step %d has not run; apply --phase expand first", plan.Hash, step.Index)
		}
	}
	return nil
}

// advance is step 8 of apply: when every expand step has finished and the
// plan has contract steps, plan_phase records it, with the plan's expanded
// model as the applied model when it has one; when every step has
// finished, the plan's model becomes the applied model.
func (r *Runner) advance(ctx context.Context, conn Conn, plan *Plan, st state, finished map[int]bool, result *Result) error {
	expandDone, allDone := true, true
	for _, step := range plan.Steps {
		if !finished[step.Index] {
			allDone = false
			if step.Phase == Expand {
				expandDone = false
			}
		}
	}
	result.ExpandDone = expandDone
	switch {
	case allDone:
		result.Finished = true
		return setModel(ctx, conn, plan.model)
	case expandDone && st.planPhase == "":
		return setExpandDone(ctx, conn, plan)
	}
	return nil
}

// stepOutcome is what one step did.
type stepOutcome int

const (
	ran stepOutcome = iota
	// alreadyRan: the step's log row has finished_at.
	alreadyRan
	// otherRunnerFinished: another runner finished the plan (SQLite, where
	// runners take turns per step).
	otherRunnerFinished
)

// runStep runs one step, retrying it after a lock timeout.
func (r *Runner) runStep(ctx context.Context, sql dialectSQL, plan *Plan, step *Step) (stepOutcome, error) {
	waits := r.Waits
	if waits == nil {
		waits = DefaultWaits
	}
	label := fmt.Sprintf("step %d/%d %s %s", step.Index, len(plan.Steps), step.Phase, step.Subject)
	for attempt := 0; ; attempt++ {
		began := time.Now()
		var outcome stepOutcome
		var err error
		if step.Transactional {
			outcome, err = r.transactionalStep(ctx, sql, plan, step)
		} else {
			outcome, err = r.sessionStep(ctx, plan, step)
		}
		if err == nil {
			switch outcome {
			case ran:
				r.logf("%s: done in %s", label, time.Since(began).Round(time.Millisecond))
			case alreadyRan:
				r.logf("%s: already done", label)
			}
			return outcome, nil
		}
		if !r.Driver.IsLockTimeout(err) {
			return 0, err
		}
		if attempt == len(waits) {
			var stepErr *StepError
			if errors.As(err, &stepErr) {
				stepErr.Err = fmt.Errorf("%w; gave up after %d retries", stepErr.Err, len(waits))
			}
			return 0, err
		}
		r.logf("%s: lock timeout; retrying in %s", label, waits[attempt])
		if err := r.sleep(ctx, waits[attempt]); err != nil {
			return 0, err
		}
	}
}

// transactionalStep runs a step and its log row in one transaction. On
// SQLite the transaction is the lock, so it checks the state again first.
func (r *Runner) transactionalStep(ctx context.Context, sql dialectSQL, plan *Plan, step *Step) (stepOutcome, error) {
	outcome := ran
	result := &Result{}
	err := r.Driver.Transact(ctx, TxOptions{ForeignKeysOff: step.ForeignKeysOff}, func(ctx context.Context, conn Conn) error {
		st, err := readState(ctx, conn, sql, plan.Service)
		if err != nil {
			return err
		}
		if st.planHash != plan.Hash {
			if st.planHash == "" && st.modelHash == plan.To {
				outcome = otherRunnerFinished
				return nil
			}
			return fmt.Errorf("plan %s is no longer in progress for service %s", plan.Hash, plan.Service)
		}
		finished, err := finishedSteps(ctx, conn, plan.Hash)
		if err != nil {
			return err
		}
		if finished[step.Index] {
			outcome = alreadyRan
			return nil
		}
		startedAt := now()
		for _, statement := range step.Statements {
			if err := conn.Exec(ctx, statement); err != nil {
				return &StepError{Index: step.Index, Subject: step.Subject, Statement: statement, Err: err}
			}
		}
		if err := insertLog(ctx, conn, plan, step, startedAt, now()); err != nil {
			return err
		}
		finished[step.Index] = true
		return r.advance(ctx, conn, plan, st, finished, result)
	})
	if err != nil {
		return 0, asStepError(step, err)
	}
	return outcome, nil
}

// sessionStep runs a step that is not transactional (Postgres): it logs the
// step's start, runs its recovery first when an earlier start did not
// finish, runs each statement on its own, then logs its end.
func (r *Runner) sessionStep(ctx context.Context, plan *Plan, step *Step) (stepOutcome, error) {
	outcome := ran
	err := r.Driver.Session(ctx, func(ctx context.Context, conn Conn) error {
		exists, finished, err := logStep(ctx, conn, plan.Hash, step.Index)
		if err != nil {
			return err
		}
		switch {
		case finished:
			outcome = alreadyRan
			return nil
		case exists:
			r.logf("step %d %s: an earlier run started it and did not finish; running its recovery", step.Index, step.Subject)
			for _, statement := range step.Recovery {
				if err := conn.Exec(ctx, statement); err != nil {
					return &StepError{Index: step.Index, Subject: step.Subject, Statement: statement, Recovery: true, Err: err}
				}
			}
		default:
			if err := insertLog(ctx, conn, plan, step, now(), ""); err != nil {
				return err
			}
		}
		for _, statement := range step.Statements {
			if err := conn.Exec(ctx, statement); err != nil {
				return &StepError{Index: step.Index, Subject: step.Subject, Statement: statement, Err: err}
			}
		}
		return nil
	})
	if err != nil || outcome == alreadyRan {
		return outcome, asStepError(step, err)
	}
	result := &Result{}
	err = r.Driver.Transact(ctx, TxOptions{}, func(ctx context.Context, conn Conn) error {
		if err := finishLog(ctx, conn, plan, step); err != nil {
			return err
		}
		st, err := readState(ctx, conn, stateSQL[plan.Dialect], plan.Service)
		if err != nil {
			return err
		}
		finished, err := finishedSteps(ctx, conn, plan.Hash)
		if err != nil {
			return err
		}
		return r.advance(ctx, conn, plan, st, finished, result)
	})
	return outcome, asStepError(step, err)
}

// asStepError names the step in an error that does not name it yet.
func asStepError(step *Step, err error) error {
	if err == nil {
		return nil
	}
	var stepErr *StepError
	if errors.As(err, &stepErr) {
		return err
	}
	return &StepError{Index: step.Index, Subject: step.Subject, Err: err}
}

// Status is a service's row of the state table and the log of the plan in
// progress.
type Status struct {
	Service string
	// Recorded is false when the database has no state for the service.
	Recorded bool
	Dialect  Dialect
	// ModelHash is the applied model's hash; empty while no plan has
	// finished and no model was adopted. Model is its canonical JSON.
	ModelHash string
	Model     []byte
	// PlanHash is the plan in progress. PlanPhase is empty until its expand
	// steps have finished, then "expanded" when ModelHash is the plan's
	// expanded model, which a plan from it supersedes, or "expand" when the
	// plan has none and ModelHash is still its from.
	PlanHash  string
	PlanPhase string
	UpdatedAt string
	// Steps are the log rows of the plan in progress.
	Steps []LoggedStep
}

// Status reads a service's state without changing anything; it creates no
// table.
func (r *Runner) Status(ctx context.Context, service string) (*Status, error) {
	sql := stateSQL[r.Driver.Dialect()]
	status := &Status{Service: service}
	err := r.Driver.Transact(ctx, TxOptions{ReadOnly: true}, func(ctx context.Context, conn Conn) error {
		var tables int64
		if err := conn.Query(ctx, sql.exists, nil, func(scan func(dest ...any) error) error { return scan(&tables) }); err != nil {
			return fmt.Errorf("look for the state table: %w", err)
		}
		if tables == 0 {
			return nil
		}
		st, err := readState(ctx, conn, sql, service)
		if err != nil || !st.recorded {
			return err
		}
		status.Recorded = true
		status.Dialect = st.dialect
		status.ModelHash = st.modelHash
		if st.model != "" {
			status.Model = []byte(st.model)
		}
		status.PlanHash, status.PlanPhase, status.UpdatedAt = st.planHash, st.planPhase, st.updatedAt
		if st.planHash != "" {
			status.Steps, err = readLog(ctx, conn, sql, st.planHash)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return status, nil
}

// Adopt records model as the applied model of its service without running
// anything, and returns the hash it replaces ("" for none). It refuses while
// a plan is in progress.
func (r *Runner) Adopt(ctx context.Context, model *Model) (replaced string, err error) {
	if dialect := r.Driver.Dialect(); model.Dialect != dialect {
		return "", refusef("the model is for %s and the database is %s", model.Dialect, dialect)
	}
	sql := stateSQL[model.Dialect]
	unlock, err := r.Driver.Lock(ctx, model.Service)
	if err != nil {
		return "", fmt.Errorf("take the lock of service %s: %w", model.Service, err)
	}
	defer func() { _ = unlock(context.WithoutCancel(ctx)) }()
	if err := r.createTables(ctx, sql); err != nil {
		return "", err
	}
	err = r.transact(ctx, func(ctx context.Context, conn Conn) error {
		st, err := readState(ctx, conn, sql, model.Service)
		if err != nil {
			return err
		}
		if st.planHash != "" {
			return refusef("service %s has plan %s in progress (%s); adopt refuses while a plan is in progress",
				model.Service, st.planHash, describePhase(st.planPhase))
		}
		replaced = st.modelHash
		return setModel(ctx, conn, model)
	})
	if err != nil {
		return "", err
	}
	return replaced, nil
}

// createTables creates the state tables when they are missing. Runs for two
// services can create them at once on Postgres; the one that loses fails on
// the catalog's unique index and finds the tables when it tries again.
func (r *Runner) createTables(ctx context.Context, sql dialectSQL) error {
	create := func(ctx context.Context, conn Conn) error {
		for _, statement := range sql.create {
			if err := conn.Exec(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}
	err := r.transact(ctx, create)
	if err != nil && !r.Driver.IsLockTimeout(err) {
		err = r.transact(ctx, create)
	}
	if err != nil {
		return fmt.Errorf("create the state tables: %w", err)
	}
	return nil
}

// transact runs one write transaction on the state tables, retrying it after
// a lock timeout as a step is.
func (r *Runner) transact(ctx context.Context, fn func(ctx context.Context, conn Conn) error) error {
	waits := r.Waits
	if waits == nil {
		waits = DefaultWaits
	}
	for attempt := 0; ; attempt++ {
		err := r.Driver.Transact(ctx, TxOptions{}, fn)
		if err == nil || !r.Driver.IsLockTimeout(err) || attempt == len(waits) {
			return err
		}
		r.logf("lock timeout on the state tables; retrying in %s", waits[attempt])
		if err := r.sleep(ctx, waits[attempt]); err != nil {
			return err
		}
	}
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		_, _ = fmt.Fprintf(r.Log, format+"\n", args...)
	}
}

func describeModel(hash string) string {
	if hash == "" {
		return "an empty database"
	}
	return "model " + hash
}

func describePhase(phase string) string {
	if phase == PlanPhaseExpand || phase == PlanPhaseExpanded {
		return "its expand steps are done"
	}
	return "its expand steps are not all done"
}
