package cloudflare

import "time"

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
