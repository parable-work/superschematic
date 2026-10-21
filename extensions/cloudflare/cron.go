package cloudflare

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Cron platform (section 18.9, D52, D64): a TypeScript job is a
// Cloudflare Worker of its own, named as a server's is (workerName),
// which runs the bundle its package's bundle.ts writes from the generated
// scheduled.ts (section 18.5). Its Worker is the three nodes a server's is (workerNodes),
// with workers.dev off, since a job is never exposed, and its version holds
// the bundle, the job's config as bindings (bundleVersion) and the bundle's
// digest (BundleBinding). A schedule the environment runs is a Cron Trigger
// on the Worker, in UTC. Every job's Worker is also a Workflow,
// `<job>.workflow`, of the class scheduled.ts exports (WorkflowClass), named
// as the Worker is: the target's job runner (jobs.go) starts an instance of
// it through Cloudflare's API to run the job once, for `stack run` and for a
// job that runs on every deploy (D52, amended; D64, amended). A job with no
// schedule has its Worker, its Workflow and no trigger.
//
// scheduled.ts runs the job's tries within one invocation, as `stack dev`
// runs them one after the other, each bounded by the job's timeout, so the
// tries of one firing must fit within Cloudflare's limit on a scheduled
// run's duration (MaxScheduledRunSeconds). The Workflow runs the same tries
// within one step.

// Cron runs TypeScript jobs as Cloudflare Workers under Cron Triggers.
const Cron = "cloudflare.cron"

// A job's edges are its API's (D52): its calls to a server on Workers, its
// buckets on R2 and its database on D1. Their connectors are a server's,
// whose values read nothing of the caller's platform (connectWorkers,
// connectR2, connectD1): a job calls through a service binding, since it
// has no app of its own to call in process.
const (
	CronHTTPConnector = "cloudflare.cron-workers"
	CronR2Connector   = "cloudflare.cron-r2"
	CronD1Connector   = "cloudflare.cron-d1"
)

// TypeWorkersCronTrigger is a Worker's Cron Triggers, and TypeWorkflow a
// Workflow of a Worker's class, pinned in schemas/ at ProviderVersion.
const (
	TypeWorkersCronTrigger = "cloudflare:index/workersCronTrigger:WorkersCronTrigger"
	TypeWorkflow           = "cloudflare:index/workflow:Workflow"
)

// WorkflowClass is the class scheduled.ts exports, a WorkflowEntrypoint
// whose run runs the job's tries within one step, as a Cron Trigger's
// firing runs them: the class every job's Workflow names.
const WorkflowClass = ir.JobRunClass

// BundleBinding is the plain_text binding a job's version holds the
// digest of its bundle in, `{bundle-digest}`, which the deploy pins: the
// Workflow compares it with the digest the job runner asks for, so a run
// never runs another bundle than the one the manifest records.
const BundleBinding = ir.BundleDigestVariable

// MaxScheduledRunSeconds is the longest a Cron Trigger's run of a Worker
// lasts, wall-clock, on both plans: 15 minutes, by the Duration table of
// Cloudflare's Workers limits (developers.cloudflare.com/workers/platform/
// limits, read on 2026-10-20). A job's timeout times its tries must fit in
// it. CPU time is limited apart: 30 seconds by default, which cpuMs raises
// to 5 minutes.
const MaxScheduledRunSeconds = 15 * 60

// cronSettings is the schema of a job's settings on Cron (section 18.1):
// Workers Logs, on by default, and cpuMs, the CPU time a run may take,
// Cloudflare's default when unset.
const cronSettings = `{
  "type": "object",
  "properties": {
    "logs": {"type": "boolean"},
    "cpuMs": {"type": "integer", "minimum": 1, "maximum": 300000}
  },
  "additionalProperties": false
}`

// utcZones are the time zones a Cron Trigger's schedule may be written in:
// Cloudflare reads every schedule in UTC.
var utcZones = []string{ir.DefaultJobTimeZone, "Etc/UTC"}

// cronPlatform is the Cron platform's registration.
func cronPlatform() registry.PlatformSpec {
	return registry.PlatformSpec{
		Name:      Cron,
		Kind:      ir.DeployableJob,
		Languages: []string{registry.APILanguageTypeScript},
		Artifact:  ir.ArtifactBundle,
		Settings:  json.RawMessage(cronSettings),
		NameOf:    workerName,
		AddressOf: func(registry.PlatformContext) any { return nil },
		Lower:     lowerJob,
	}
}

// scheduleID is the ID of a job's Cron Trigger node, and workflowID of its
// Workflow's.
func scheduleID(deployable string) string { return deployable + ".schedule" }
func workflowID(deployable string) string { return deployable + ".workflow" }

// lowerJob lowers a job to its Worker's three nodes, workers.dev off, whose
// version runs its bundle, and for a schedule the environment runs, a Cron
// Trigger on the Worker once its deployment is there. It refuses, as
// unrealizable, a schedule in a time zone other than UTC, which a Cron
// Trigger cannot read, and a timeout times tries over
// MaxScheduledRunSeconds, which Cloudflare would cut short.
func lowerJob(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := targetValuesOf(env.Values)
	run := d.Job
	if run == nil {
		return registry.Lowered{}, fmt.Errorf("job %s has no run", d.Name)
	}
	if err := checkWorkerName("the Worker of job "+d.Name, d.ResourceName, workerNameParts(env, d, "the job")); err != nil {
		return registry.Lowered{}, err
	}
	if run.Schedule != "" && !isUTC(run.TimeZone) {
		return registry.Lowered{}, registry.Unrealizable("job %s runs on %q in time zone %s, and a Cron Trigger reads its schedule in UTC alone, where daylight saving cannot move it; write the schedule in UTC with timeZone UTC, or turn it off in environment %s (enabled: false)", d.Name, run.Schedule, run.TimeZone, env.Name)
	}
	timeout := run.TimeoutSeconds
	if timeout <= 0 {
		timeout = ir.DefaultJobTimeoutSeconds
	}
	tries := run.Retries + 1
	if timeout*tries > MaxScheduledRunSeconds {
		return registry.Lowered{}, registry.Unrealizable("job %s runs %d tries of up to %s each, %s in all, within one run of its Cron Trigger, and Cloudflare stops a scheduled run after %s; give its @job a shorter timeout or fewer retries", d.Name, tries, seconds(timeout), seconds(timeout*tries), seconds(MaxScheduledRunSeconds))
	}
	version, err := bundleVersion(ctx, v)
	if err != nil {
		return registry.Lowered{}, err
	}
	if err := addBinding(version, map[string]any{"type": bindingPlainText, "name": BundleBinding, "text": ir.BundleDigestToken}); err != nil {
		return registry.Lowered{}, fmt.Errorf("job %s: %w", d.Name, err)
	}
	out := registry.Lowered{Resources: workerNodes(d, v, false, settingBool(d.Settings, "logs", true), version)}
	// The Worker waits for each Worker its service bindings name, so a
	// destroy removes it, with its bindings, first (calls.go).
	out.Resources[0].DependsOn = bindingDependencies(d)
	// The Workflow through which the job runner runs the job once, of the
	// class the deployment's version exports, so it waits for the
	// deployment.
	out.Resources = append(out.Resources, &ir.Resource{
		ID:        workflowID(d.Name),
		Type:      TypeWorkflow,
		DependsOn: []string{deploymentID(d.Name)},
		Properties: map[string]any{
			"accountId":    v.accountID,
			"workflowName": d.ResourceName,
			"scriptName":   ir.Output{Resource: workerID(d.Name), Name: "name"},
			"className":    WorkflowClass,
		},
	})
	if run.Schedule != "" {
		out.Resources = append(out.Resources, &ir.Resource{
			ID:        scheduleID(d.Name),
			Type:      TypeWorkersCronTrigger,
			DependsOn: []string{deploymentID(d.Name)},
			Properties: map[string]any{
				"accountId":  v.accountID,
				"scriptName": ir.Output{Resource: workerID(d.Name), Name: "name"},
				"schedules":  []any{map[string]any{"cron": run.Schedule}},
			},
		})
	}
	return out, nil
}

// isUTC reports whether a job's time zone is UTC.
func isUTC(zone string) bool {
	return zone == "" || slices.Contains(utcZones, zone)
}

// seconds writes a whole number of seconds as a job's timeout is written,
// Go's duration without its zero units: 10m0s as 10m, 2h0m0s as 2h,
// 1h30m0s as 1h30m, and 90s as 1m30s.
func seconds(n int) string {
	s := (time.Duration(n) * time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// addBinding adds a binding to a version's, keeping them sorted by name,
// and refuses one whose name a binding holds already.
func addBinding(version map[string]any, binding map[string]any) error {
	bindings, _ := version["bindings"].([]any)
	name := binding["name"].(string)
	i, found := slices.BinarySearchFunc(bindings, name, func(b any, name string) int {
		return strings.Compare(b.(map[string]any)["name"].(string), name)
	})
	if found {
		return fmt.Errorf("a binding of its config is named %s, which the platform's own binding takes", name)
	}
	version["bindings"] = slices.Insert(bindings, i, any(binding))
	return nil
}
