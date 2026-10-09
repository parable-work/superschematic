package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Cloud Run job platform (docs/stack-model.md, sections 7.2 and 8.7,
// D52): a job is a Cloud Run job, which runs its image to completion as
// one task, with its own service account and what a server of its API
// takes (lowerWorkload): the API's secrets, its Cloud SQL volume, its VPC
// egress when it calls a server, and its config. The decorator's timeout
// bounds each try, and its retries are the task's.
//
// A schedule the environment runs is a Cloud Scheduler job that POSTs to
// the Cloud Run Admin API's `jobs/<job>:run` with an OAuth token for the
// job's own account, which holds roles/run.invoker on that job alone. The
// account so runs that job and no other. An account per stack, with a
// grant on each job, could run them all; an account per schedule would add
// an account for each, and a name that must fit 30 characters beside the
// job's, to run what the job's own account may run already. The job's
// account holds what its runs reach, so starting a run adds no reach to
// it. Cloud Scheduler's service agent mints the token, through the role
// Google grants it when the project enables Cloud Scheduler; the deployer
// that names the account in the scheduler job acts as it, through
// roles/iam.serviceAccountUser (section 7.3).
//
// `stack run` runs the deployed job once, as an execution of it (jobRunner),
// and reads a failed execution's error from Cloud Logging, as the
// migration job's (migrations.go).

// lowerJob lowers a job to what lowerWorkload makes of it, the Cloud Run
// job, named after the deployable as a server's service is (serviceName),
// and for a schedule the environment runs, the scheduler's nodes
// (scheduleNodes). It refuses more retries than Cloud Run gives a task, and
// the name of the stack's migration job, which the migration runner owns.
func lowerJob(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	run := d.Job
	if run == nil {
		return registry.Lowered{}, fmt.Errorf("job %s has no run", d.Name)
	}
	if run.Retries > maxJobRetries {
		return registry.Lowered{}, fmt.Errorf("job %s runs a failed run again %d times, and Cloud Run retries a job's task %d times at most; give its @job %d retries or fewer", d.Name, run.Retries, maxJobRetries, maxJobRetries)
	}
	if name, ok := d.ResourceName.(string); ok && name == migrationJobName(env.Stack) {
		return registry.Lowered{}, fmt.Errorf("job %s would be Cloud Run job %s, the stack's migration job (section 8.4); rename the job's class or its API", d.Name, name)
	}
	out, w, err := lowerWorkload(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	timeout := run.TimeoutSeconds
	if timeout <= 0 {
		timeout = ir.DefaultJobTimeoutSeconds
	}
	task := w.template
	task["containers"] = []any{w.container}
	task["timeout"] = fmt.Sprintf("%ds", timeout)
	// Cloud Run's default is 3 retries, so none is written as 0.
	task["maxRetries"] = run.Retries
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".job", Type: TypeJob, Properties: map[string]any{
		"project":            v.project,
		"location":           v.region,
		"name":               d.ResourceName,
		"deletionProtection": false,
		"template": map[string]any{
			"taskCount": 1,
			"template":  task,
		},
	}})
	if run.Schedule != "" {
		out.Resources = append(out.Resources, scheduleNodes(env, v, d)...)
	}
	return out, nil
}

// scheduleNodes are the nodes of a job's schedule (D52): the job's
// account's roles/run.invoker on the job, which grants run.jobs.run and
// nothing else a job takes, and the Cloud Scheduler job, named as the
// job is, that runs it on the schedule in the time zone, as that account,
// once the grant is there.
func scheduleNodes(env registry.StackEnvironment, v values, d ir.ResolvedDeployable) []*ir.Resource {
	job := ir.Output{Resource: d.Name + ".job", Name: "name"}
	account := d.Name + ".account"
	invoker := d.Name + ".schedule-invoker"
	return []*ir.Resource{
		{
			ID:   invoker,
			Type: TypeJobIAMMember,
			Properties: map[string]any{
				"project":  v.project,
				"location": v.region,
				"name":     job,
				"role":     "roles/run.invoker",
				"member":   ir.Output{Resource: account, Name: "member"},
			},
		},
		{
			ID:        d.Name + ".schedule",
			Type:      TypeSchedulerJob,
			DependsOn: []string{invoker},
			Properties: map[string]any{
				"project":     v.project,
				"region":      v.region,
				"name":        d.ResourceName,
				"description": fmt.Sprintf("Runs job %s of %s %s on its schedule", d.Name, env.Stack, env.Name),
				"schedule":    d.Job.Schedule,
				"timeZone":    d.Job.TimeZone,
				"httpTarget": map[string]any{
					"httpMethod": "POST",
					"uri":        join("https://run.googleapis.com/v2/projects/", v.project, "/locations/", v.region, "/jobs/", job, ":run"),
					"oauthToken": map[string]any{"serviceAccountEmail": ir.Output{Resource: account, Name: "email"}},
				},
			},
		},
	}
}

// jobFailedMessage is the message of the line a job's entrypoint logs, as
// JSON, when its run fails, with the run's error under jobErrorKey
// (section 8.7).
const (
	jobFailedMessage = "job failed"
	jobErrorKey      = "error"
)

// jobFailedLines selects, in a job's stderr, the line each try writes when
// it fails, and the first line of a panic: whatever else a run logs, the
// last of them is the last try's. Cloud Run logs a JSON line as a JSON
// payload.
const jobFailedLines = `jsonPayload.msg="` + jobFailedMessage + `" OR textPayload=~"^panic: "`

// jobError is the last try's error in the lines jobFailedLines selects:
// the error of its `job failed` line, or its panic.
func jobError(lines []string) string {
	last := lines[len(lines)-1]
	var entry map[string]any
	if json.Unmarshal([]byte(last), &entry) == nil {
		if msg, _ := entry[jobErrorKey].(string); msg != "" {
			return msg
		}
	}
	return last
}

// jobRunner is the gcp target's JobRunner (D52).
type jobRunner struct{ ext Extension }

var _ registry.JobRunner = jobRunner{}

// RunJob runs an execution of req's job, waits for it to end, and returns
// the last try's error, which it reads from Cloud Logging, when it fails.
// It refuses a job the gcp job platform does not run, and one whose Cloud
// Run job runs another image than the deploy manifest records: a deploy
// under way, or a change made outside one.
func (r jobRunner) RunJob(ctx context.Context, req registry.JobRunRequest) error {
	if err := req.Check(); err != nil {
		return err
	}
	env := req.Run.Environment
	if d := env.Deployable(req.Job); d.Platform != CloudRunJob {
		return fmt.Errorf("gcp: job %s runs on platform %s, not %s, so the gcp target does not run it", req.Job, d.Platform, CloudRunJob)
	}
	v, err := envValues(env)
	if err != nil {
		return err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }
	name, err := nodeString(env, req.Job+".job", "name", req.Run.Parameters)
	if err != nil {
		return err
	}
	cloud := r.ext.cloud()
	image, err := cloud.JobImage(ctx, v.project, v.region, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("gcp: job %s has no Cloud Run job %s in %s, %s; deploy %s, then run the job", req.Job, name, v.project, v.region, req.Run.Name())
	case err != nil:
		return err
	case image != req.Image:
		// The deploy pins the image as the manifest records it,
		// `<repository>@sha256:<digest>`, and Cloud Run keeps it as given.
		return fmt.Errorf("gcp: Cloud Run job %s runs %s, but the last deploy of %s rolled out %s: a deploy may be under way, or the job changed outside one; deploy %s, then run the job", name, image, req.Run.Name(), req.Image, req.Run.Name())
	}
	logf("run Cloud Run job %s in %s, %s, and wait for it to end", name, v.project, v.region)
	run, err := cloud.RunJob(ctx, v.project, v.region, name, nil)
	if err != nil {
		return err
	}
	if !run.Succeeded {
		pick := func(lines []string) string {
			msg := jobError(lines)
			if run.Message != "" {
				msg += "; Cloud Run: " + run.Message
			}
			return msg
		}
		return fmt.Errorf("gcp: job %s failed in execution %s: %s", req.Job, run.Name, failure(ctx, cloud, run, jobFailedLines, pick))
	}
	logf("execution %s succeeded (logs: %s)", run.Name, run.LogURI)
	return nil
}
