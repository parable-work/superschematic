package cloudflare_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The job runner over each job's Workflow (section 18.9, D52 amended, D64
// amended).

// sweepCartsWorkflow is the Workflow of the web fixture's job in Staging,
// named as its Worker is.
const sweepCartsWorkflow = "web-stack-staging-" + sweepCartsJob

// bundleDigest is a bundle's digest as a deploy manifest records it.
var bundleDigest = "sha256:" + strings.Repeat("b7", 32)

// workflowOutcome is a fake Workflow whose instance completes when its
// payload names want's bundle, and else fails as the class does.
func workflowOutcome(want string) func(map[string]string) cloudflare.WorkflowInstance {
	return func(payload map[string]string) cloudflare.WorkflowInstance {
		if payload["bundle"] != strings.TrimPrefix(want, "sha256:") {
			return erroredInstance("job " + sweepCartsJob + " runs bundle " + strings.TrimPrefix(want, "sha256:") + ", not " + payload["bundle"])
		}
		return cloudflare.WorkflowInstance{Status: cloudflare.WorkflowComplete}
	}
}

func erroredInstance(message string) cloudflare.WorkflowInstance {
	instance := cloudflare.WorkflowInstance{Status: cloudflare.WorkflowErrored}
	instance.Error = &struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}{Name: "Error", Message: message}
	return instance
}

// TestJobWorkflowNode reads SweepCarts' Workflow in Staging: named as the
// Worker is, of the class scheduled.ts exports, on the Worker's name,
// after its deployment; the version's bindings hold the bundle's digest to
// pin. A member of Preview, which runs no schedule, has its Workflow
// named with the parameter's value, so `stack run` runs it.
func TestJobWorkflowNode(t *testing.T) {
	reg := assemble(t)
	env := resolveWeb(t, reg, "Staging")
	workflow := node(t, env, sweepCartsJob+".workflow")
	if workflow.Type != cloudflare.TypeWorkflow || workflow.Phase != ir.PhaseRollout || !slices.Contains(workflow.DependsOn, sweepCartsJob+".deployment") {
		t.Errorf("the Workflow is %s in %s after %v", workflow.Type, workflow.Phase, workflow.DependsOn)
	}
	if got, want := jsonOf(t, workflow.Properties), `{"accountId":"`+accountID+`","className":"JobRun","scriptName":{"$output":{"resource":"`+sweepCartsJob+`.worker","name":"name"}},"workflowName":"`+sweepCartsWorkflow+`"}`; got != want {
		t.Errorf("the Workflow is\n%s, want\n%s", got, want)
	}
	if got, want := bindingsOf(t, node(t, env, sweepCartsJob+".version"))[cloudflare.BundleBinding], `{"name":"SUPERSCHEMATIC_BUNDLE","text":"{bundle-digest}","type":"plain_text"}`; got != want {
		t.Errorf("the bundle binding is %s, want %s", got, want)
	}
	if _, ok := bindingsOf(t, node(t, env, "shop-storefront.version"))[cloudflare.BundleBinding]; ok {
		t.Error("a server's version holds the bundle binding")
	}
	preview := resolveWeb(t, reg, "Preview")
	if got := jsonOf(t, node(t, preview, sweepCartsJob+".workflow").Properties["workflowName"]); got != `{"$concat":["web-stack-preview-shop-storefront-sweep-carts-pr",{"$parameter":"pr"}]}` {
		t.Errorf("the member's Workflow is named %s", got)
	}
}

// TestJobRunnerRunsTheWorkflow runs SweepCarts through the target's job
// runner: it starts an instance of the job's Workflow whose payload names
// the bundle's digest, reads it until it ends, and returns nil when it
// completes; a member's Workflow is the one its parameter names.
func TestJobRunnerRunsTheWorkflow(t *testing.T) {
	cloudflare.SetWorkflowPoll(t, time.Millisecond)
	f := newFixture(t)
	f.cloud.workflows[sweepCartsWorkflow] = workflowOutcome(bundleDigest)
	log := &bytes.Buffer{}
	err := f.target(t).Jobs.RunJob(context.Background(), registry.JobRunRequest{Run: webRun(t, f), Job: sweepCartsJob, Bundle: bundleDigest, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.cloud.Changes(), []string{`run workflow ` + sweepCartsWorkflow + `: {"bundle":"` + strings.Repeat("b7", 32) + `"}`}; !slices.Equal(got, want) {
		t.Errorf("the run changed %q, want %q", got, want)
	}
	if !strings.Contains(log.String(), "run Workflow "+sweepCartsWorkflow+", instance instance-1, and wait for it to end") {
		t.Errorf("the log:\n%s", log)
	}

	member := "web-stack-preview-shop-storefront-sweep-carts-pr7"
	f.cloud.workflows[member] = workflowOutcome(bundleDigest)
	preview := registry.Run{Environment: resolveWeb(t, f.reg, "Preview"), Parameters: map[string]string{"pr": "7"}}
	if err := f.target(t).Jobs.RunJob(context.Background(), registry.JobRunRequest{Run: preview, Job: sweepCartsJob, Bundle: bundleDigest}); err != nil {
		t.Errorf("a member's run: %v", err)
	}
}

// TestJobRunnerReportsTheJobsError: an instance that errors fails the run
// with the error its run threw, which names the last try's; one that is
// terminated says so; a Workflow that is not there says to deploy; and a
// bundle other than the version's fails as the class refuses it.
func TestJobRunnerReportsTheJobsError(t *testing.T) {
	cloudflare.SetWorkflowPoll(t, time.Millisecond)
	f := newFixture(t)
	jobs := f.target(t).Jobs
	run := func() error {
		return jobs.RunJob(context.Background(), registry.JobRunRequest{Run: webRun(t, f), Job: sweepCartsJob, Bundle: bundleDigest})
	}
	if err := run(); err == nil || err.Error() != "cloudflare: job "+sweepCartsJob+" has no Workflow "+sweepCartsWorkflow+" in account "+accountID+"; deploy Staging, then run the job" {
		t.Errorf("with no Workflow: %v", err)
	}

	failure := "job " + sweepCartsJob + " failed on each of its 1 tries: the cart store is down"
	f.cloud.workflows[sweepCartsWorkflow] = func(map[string]string) cloudflare.WorkflowInstance { return erroredInstance(failure) }
	if err := run(); err == nil || err.Error() != "cloudflare: job "+sweepCartsJob+" failed in instance instance-1 of Workflow "+sweepCartsWorkflow+": "+failure {
		t.Errorf("an errored instance: %v", err)
	}
	f.cloud.workflows[sweepCartsWorkflow] = func(map[string]string) cloudflare.WorkflowInstance {
		return cloudflare.WorkflowInstance{Status: cloudflare.WorkflowTerminated}
	}
	if err := run(); err == nil || !strings.HasSuffix(err.Error(), "of Workflow "+sweepCartsWorkflow+" ended terminated") {
		t.Errorf("a terminated instance: %v", err)
	}
	f.cloud.workflows[sweepCartsWorkflow] = workflowOutcome("sha256:" + strings.Repeat("0a", 32))
	if err := run(); err == nil || !strings.Contains(err.Error(), "runs bundle "+strings.Repeat("0a", 32)+", not "+strings.Repeat("b7", 32)) {
		t.Errorf("another bundle: %v", err)
	}
	if err := jobs.RunJob(context.Background(), registry.JobRunRequest{Run: webRun(t, f), Job: sweepCartsJob, Image: "an-image"}); err == nil || !strings.Contains(err.Error(), "no bundle") {
		t.Errorf("a run with no bundle: %v", err)
	}
}

// webDeploy deploys the web fixture's Staging, or the stack s's, with the
// bundles' builds stubbed, and returns the manifest.
func webDeploy(t *testing.T, f *fixture, run registry.Run) (*stack.Manifest, *bytes.Buffer, error) {
	t.Helper()
	f.shell[cloudflare.PassphraseEnv] = "the-passphrase"
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, deployable := range []string{"shop-storefront", sweepCartsJob} {
		pkg := filepath.Join(root, "dist", "server", "web-stack", deployable)
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "bundle.ts"), []byte("// written by the server generator\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "web", "storefront-web"), 0o755); err != nil {
		t.Fatal(err)
	}
	build := &siteBuild{version: "v1"}
	log := &bytes.Buffer{}
	m, err := stack.Deploy(ctx, stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: run, Dir: t.TempDir(), Log: log},
		Sources: &stack.Sources{OutputRoot: filepath.Join(root, "dist"), RepositoryRoot: root, ImplementationRoot: root, Run: build.run},
	})
	return m, log, err
}

// TestStackRunRunsTheJob deploys the web fixture's Staging, then runs its
// job with `stack run`: the runner starts the job's Workflow with the
// bundle the manifest records.
func TestStackRunRunsTheJob(t *testing.T) {
	cloudflare.SetWorkflowPoll(t, time.Millisecond)
	f := newFixture(t)
	run := webRun(t, f)
	m, log, err := webDeploy(t, f, run)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, log)
	}
	digest := m.Bundles[sweepCartsJob].Digest
	f.cloud.workflows[sweepCartsWorkflow] = workflowOutcome(digest)
	from := len(f.cloud.Changes())
	log.Reset()
	if err := stack.RunJob(context.Background(), stack.RunJobOptions{
		Options: stack.Options{Registry: f.reg, Run: run, Dir: t.TempDir(), Log: log},
		Job:     sweepCartsJob,
	}); err != nil {
		t.Fatalf("stack run: %v\n%s", err, log)
	}
	if got := f.cloud.Changes()[from:]; !slices.Equal(got, []string{`run workflow ` + sweepCartsWorkflow + `: {"bundle":"` + strings.TrimPrefix(digest, "sha256:") + `"}`}) {
		t.Errorf("stack run changed %q", got)
	}
	if !strings.Contains(log.String(), "run job "+sweepCartsJob+" of Staging (bundle "+digest+")") {
		t.Errorf("the log:\n%s", log)
	}
}

// TestDeployRunsAJobOnCron: Staging with SweepCarts on every deploy runs
// its Workflow at the jobs step, after the rollout, with the bundle the
// rollout pinned; a job whose instance errors fails the deploy at that
// step with the job's error, and the manifest records it.
func TestDeployRunsAJobOnCron(t *testing.T) {
	cloudflare.SetWorkflowPoll(t, time.Millisecond)
	f := newFixture(t)
	s := webStack()
	on := true
	s.Environments[0].Settings[0].OnDeploy = &on
	env, errs := resolveOver(t, f.reg, s, webServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	if !slices.ContainsFunc(env.DeployOrder, func(step *ir.DeployStep) bool {
		return step.Step == ir.StepJobs && slices.Equal(step.Deployables, []string{sweepCartsJob})
	}) {
		t.Fatalf("Staging's deploy order runs no job: %v", env.DeployOrder)
	}
	// The fake provisioner made no Workflow; any bundle completes here.
	f.cloud.workflows[sweepCartsWorkflow] = func(map[string]string) cloudflare.WorkflowInstance {
		return cloudflare.WorkflowInstance{Status: cloudflare.WorkflowComplete}
	}
	run := registry.Run{Environment: env}
	m, log, err := webDeploy(t, f, run)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, log)
	}
	digest := m.Bundles[sweepCartsJob].Digest
	if !strings.Contains(log.String(), ": jobs\n  run job "+sweepCartsJob+" (bundle "+digest+") and wait for it to end\n  run Workflow "+sweepCartsWorkflow) {
		t.Errorf("the deploy's log:\n%s", log)
	}
	if !slices.Contains(f.cloud.Changes(), `run workflow `+sweepCartsWorkflow+`: {"bundle":"`+strings.TrimPrefix(digest, "sha256:")+`"}`) {
		t.Errorf("the deploy changed %q", f.cloud.Changes())
	}

	f.cloud.workflows[sweepCartsWorkflow] = func(map[string]string) cloudflare.WorkflowInstance {
		return erroredInstance("job " + sweepCartsJob + " failed on each of its 1 tries: the cart store is down")
	}
	m, log, err = webDeploy(t, f, run)
	if err == nil || !strings.Contains(err.Error(), "step jobs: job "+sweepCartsJob+": cloudflare: job "+sweepCartsJob+" failed in instance") || !strings.HasSuffix(err.Error(), "the cart store is down") {
		t.Fatalf("a deploy whose job fails: %v\n%s", err, log)
	}
	if m.Status != stack.StatusFailed || m.Step != "jobs" {
		t.Errorf("the manifest records %s at %s", m.Status, m.Step)
	}
}
