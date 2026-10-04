package migrate

import (
	"errors"
	"fmt"
)

// ErrRefused marks a refusal: the runner checked the plan, the model or the
// database's state and ran nothing. errors.Is(err, ErrRefused) holds for
// every refusal.
var ErrRefused = errors.New("refused")

type refusal struct{ msg string }

func (r *refusal) Error() string        { return r.msg }
func (r *refusal) Is(target error) bool { return target == ErrRefused }

func refusef(format string, args ...any) error {
	return &refusal{msg: fmt.Sprintf(format, args...)}
}

// StepError is a step that failed. The plan stays in progress, and the next
// apply of the same plan resumes at the step.
type StepError struct {
	Index   int
	Subject string
	// Statement is the statement that failed; empty when the failure was
	// not a statement's, such as a foreign key check.
	Statement string
	// Recovery is true when Statement is one of the step's recovery
	// statements.
	Recovery bool
	Err      error
}

func (e *StepError) Error() string {
	what := "step"
	if e.Recovery {
		what = "recovery of step"
	}
	msg := fmt.Sprintf("%s %d (%s) failed: %v", what, e.Index, e.Subject, e.Err)
	if e.Statement != "" {
		msg += "\nstatement:\n" + e.Statement
	}
	return msg
}

func (e *StepError) Unwrap() error { return e.Err }
