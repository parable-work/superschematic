package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/parable-work/superschematic/registry"
)

// The job runner (docs/stack-model.md, section 18.9, D52 amended, D64
// amended): it runs a job on Cron once, for `stack run` and for a job that
// runs on every deploy, through the Workflow every job's Worker is
// (`<job>.workflow`, of the class WorkflowClass that scheduled.ts exports).
// Cloudflare has no call that fires a deployed Worker's scheduled handler,
// and a Worker on Cron has no public address to call, so the runner starts
// an instance of the Workflow with Cloudflare's API and the run's token,
// which reaches nothing a deploy's token does not, and polls the instance
// until it ends:
//
//  1. it reads the Workflow's name from its node, with the run's parameter
//     values;
//  2. it starts an instance whose payload names the bundle the deploy
//     manifest records, `{"bundle": "<hex>"}`;
//  3. the instance's run compares that digest with the version's
//     BundleBinding, and fails naming both when they differ: a deploy under
//     way, or a version made outside one, as gcp's runner refuses an image
//     the manifest does not record;
//  4. it runs the job's tries within one step, as a Cron Trigger's firing
//     runs them, each logged to Workers Logs;
//  5. the runner returns nil when the instance completes, and else the
//     error the instance's run threw, which names the job and the last
//     try's error, as gcp's runner returns the last try's error from Cloud
//     Logging.

// jobRunner is the cloudflare target's JobRunner.
type jobRunner struct{ ext Extension }

var _ registry.JobRunner = jobRunner{}

// The instance's poll: every workflowPoll, and Cloudflare's errors for up
// to workflowPollErrors calls in a row before the run fails.
var (
	workflowPoll       = 2 * time.Second
	workflowPollErrors = 5
)

// RunJob runs one instance of req's job's Workflow and waits for it to
// end. It refuses a job the Cron platform does not run, and a job with no
// bundle.
func (r jobRunner) RunJob(ctx context.Context, req registry.JobRunRequest) error {
	if err := req.Check(); err != nil {
		return err
	}
	env := req.Run.Environment
	if d := env.Deployable(req.Job); d.Platform != Cron {
		return fmt.Errorf("cloudflare: job %s runs on platform %s, not %s, so the cloudflare target does not run it", req.Job, d.Platform, Cron)
	}
	digest, ok := strings.CutPrefix(req.Bundle, "sha256:")
	if !ok || digest == "" {
		return fmt.Errorf("cloudflare: job %s: bundle %q is not sha256:<hex>", req.Job, req.Bundle)
	}
	v, err := envValues(env)
	if err != nil {
		return err
	}
	workflow, err := nodeString(env, workflowID(req.Job), "workflowName", req.Run.Parameters)
	if err != nil {
		return err
	}
	cloud, err := r.ext.cloud()
	if err != nil {
		return err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }
	id, err := cloud.CreateWorkflowInstance(ctx, v.accountID, workflow, map[string]string{"bundle": digest})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("cloudflare: job %s has no Workflow %s in account %s; deploy %s, then run the job", req.Job, workflow, v.accountID, req.Run.Name())
	case err != nil:
		return fmt.Errorf("cloudflare: job %s: start an instance of Workflow %s: %w", req.Job, workflow, err)
	}
	logf("run Workflow %s, instance %s, and wait for it to end (logs: the Worker's Workers Logs)", workflow, id)
	failures := 0
	for {
		instance, err := cloud.WorkflowInstance(ctx, v.accountID, workflow, id)
		switch {
		case err != nil && ctx.Err() != nil:
			return fmt.Errorf("cloudflare: job %s: stopped waiting for instance %s of Workflow %s, which runs on: %w", req.Job, id, workflow, ctx.Err())
		case err != nil:
			failures++
			if failures >= workflowPollErrors {
				return fmt.Errorf("cloudflare: job %s: read instance %s of Workflow %s: %w", req.Job, id, workflow, err)
			}
		case instance.Done():
			return instanceResult(req.Job, workflow, id, instance)
		default:
			failures = 0
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cloudflare: job %s: stopped waiting for instance %s of Workflow %s, which runs on: %w", req.Job, id, workflow, ctx.Err())
		case <-time.After(workflowPoll):
		}
	}
}

// instanceResult is nil for an instance that completed, and else the error
// its run threw, or how it ended.
func instanceResult(job, workflow, id string, instance *WorkflowInstance) error {
	if instance.Status == WorkflowComplete {
		return nil
	}
	if instance.Error != nil && instance.Error.Message != "" {
		return fmt.Errorf("cloudflare: job %s failed in instance %s of Workflow %s: %s", job, id, workflow, instance.Error.Message)
	}
	return fmt.Errorf("cloudflare: job %s: instance %s of Workflow %s ended %s", job, id, workflow, instance.Status)
}
