package cloudflare

import (
	"time"

	ir "github.com/parable-work/superschematic/ir"
)

// NewPassphraseCheck writes a passphrase's check over a salt, as bootstrap
// does, for tests that stand in for another bootstrap.
var NewPassphraseCheck = newCheck

// SetWorkflowPoll sets how often the job runner reads a Workflow instance,
// for the test's duration.
func SetWorkflowPoll(t interface{ Cleanup(func()) }, every time.Duration) {
	before := workflowPoll
	workflowPoll = every
	t.Cleanup(func() { workflowPoll = before })
}

// SetWorkflowDeadline sets how long the job runner waits for an instance,
// and how many reads it may stay paused, for the test's duration.
func SetWorkflowDeadline(t interface{ Cleanup(func()) }, every time.Duration, stuckPolls int) {
	before, polls := workflowDeadline, workflowStuckPolls
	workflowDeadline = func(*ir.ResolvedJob) time.Duration { return every }
	workflowStuckPolls = stuckPolls
	t.Cleanup(func() { workflowDeadline, workflowStuckPolls = before, polls })
}

// WorkflowDeadline is how long the job runner waits for an instance of job.
func WorkflowDeadline(job *ir.ResolvedJob) time.Duration { return workflowDeadline(job) }
