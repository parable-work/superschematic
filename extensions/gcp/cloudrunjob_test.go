package gcp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// shipOrders is shop-orders' job's deployable, and its Cloud Run job's key
// in the fake cloud in Staging's project and region.
const (
	shipOrders    = stacktest.ShipOrdersJob
	shipOrdersKey = "acme-staging/us-east1/" + shipOrders
)

// jobSettings returns the settings element of shop-orders' job in env of
// s, adding one when env has none.
func jobSettings(s *ir.Stack, env string) *ir.DeployableSettings {
	e := s.Environment(env)
	for _, set := range e.Settings {
		if set.Of.Job == "ShipOrders" {
			return set
		}
	}
	set := &ir.DeployableSettings{Of: stacktest.JobOf(stacktest.ShopOrders, "ShipOrders")}
	e.Settings = append(e.Settings, set)
	return set
}

// TestJobPlatform checks the Cloud Run job a job lowers to (D52): one
// task, the decorator's timeout and retries, the job's own account and
// its API's Cloud SQL volume and egress; its schedule as a Cloud Scheduler
// job that POSTs to the Admin API's run method with an OAuth token for the
// job's account, which may run that job alone; and the connectors from
// the job platform, which name the job's account.
func TestJobPlatform(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Staging")
	d := env.Deployable(shipOrders)
	if d.Platform != gcp.CloudRunJob || d.Job.Schedule != "0 * * * *" || d.Job.TimeZone != "America/New_York" {
		t.Fatalf("the job is %s on %s, run %+v", d.Name, d.Platform, d.Job)
	}
	if got := strings.Join(ownedBy(env, shipOrders), ", "); got != "network, network.nat, network.router, network.subnet, secret.PaymentsSecrets.STRIPE_KEY, "+
		shipOrders+".account, "+shipOrders+".job, "+shipOrders+".reads.PaymentsSecrets.STRIPE_KEY, "+
		shipOrders+".schedule, "+shipOrders+".schedule-invoker, "+shipOrders+".trace-agent" {
		t.Errorf("the job lowers to %s", got)
	}
	for _, e := range env.Edges {
		if e.From == shipOrders {
			want := gcp.JobHTTPConnector
			if e.Kind == ir.EdgeSQL {
				want = gcp.JobSQLConnector
			}
			if e.Connector != want {
				t.Errorf("edge %s is connected by %s, want %s", e.ID, e.Connector, want)
			}
		}
	}

	job := node(t, env, shipOrders+".job")
	if job.Type != gcp.TypeJob || job.Properties["name"] != shipOrders || job.Properties["deletionProtection"] != false {
		t.Errorf("the job node: %s %v", job.Type, job.Properties)
	}
	template := job.Properties["template"].(map[string]any)
	task := template["template"].(map[string]any)
	wantJSON(t, "task", map[string]any{"taskCount": template["taskCount"], "timeout": task["timeout"], "maxRetries": task["maxRetries"],
		"serviceAccount": task["serviceAccount"], "volumes": task["volumes"], "egress": task["vpcAccess"].(map[string]any)["egress"]},
		`{"egress":"ALL_TRAFFIC","maxRetries":1,"serviceAccount":{"$output":{"resource":"shop-orders-ship-orders.account","name":"email"}},"taskCount":1,"timeout":"300s",`+
			`"volumes":[{"cloudSqlInstance":{"instances":[{"$output":{"resource":"shop-db.instance","name":"connectionName"}}]},"name":"cloudsql"}]}`)
	container := task["containers"].([]any)[0].(map[string]any)
	if container["image"] != "us-east1-docker.pkg.dev/acme-staging/shop/shop-orders-ship-orders" {
		t.Errorf("the job runs %v, want its repository path, which the deploy pins", container["image"])
	}
	if _, ok := container["ports"]; ok {
		t.Error("the job's container has a port")
	}
	wantJSON(t, "the job's account", node(t, env, shipOrders+".account").Properties,
		`{"accountId":"shop-orders-ship-orders","displayName":"Shop Staging job shop-orders-ship-orders","project":"acme-staging"}`)

	wantJSON(t, "the schedule's grant", node(t, env, shipOrders+".schedule-invoker").Properties,
		`{"location":"us-east1","member":{"$output":{"resource":"shop-orders-ship-orders.account","name":"member"}},`+
			`"name":{"$output":{"resource":"shop-orders-ship-orders.job","name":"name"}},"project":"acme-staging","role":"roles/run.invoker"}`)
	schedule := node(t, env, shipOrders+".schedule")
	wantJSON(t, "the schedule", schedule.Properties,
		`{"description":"Runs job shop-orders-ship-orders of Shop Staging on its schedule","httpTarget":{"httpMethod":"POST",`+
			`"oauthToken":{"serviceAccountEmail":{"$output":{"resource":"shop-orders-ship-orders.account","name":"email"}}},`+
			`"uri":{"$concat":["https://run.googleapis.com/v2/projects/acme-staging/locations/us-east1/jobs/",{"$output":{"resource":"shop-orders-ship-orders.job","name":"name"}},":run"]}},`+
			`"name":"shop-orders-ship-orders","project":"acme-staging","region":"us-east1","schedule":"0 * * * *","timeZone":"America/New_York"}`)
	if schedule.Type != gcp.TypeSchedulerJob || !strings.Contains(strings.Join(schedule.DependsOn, ","), shipOrders+".schedule-invoker") {
		t.Errorf("the schedule is a %s depending on %v, want a scheduler job after its grant", schedule.Type, schedule.DependsOn)
	}

	// The job's sql edge: its own IAM database user, which the migration
	// job gives its privileges.
	wantJSON(t, "the job's database user", node(t, env, shipOrders+".database-user.shop-db").Properties["name"], `"shop-orders-ship-orders@acme-staging.iam"`)
}

// TestJobSchedule: an environment that turns the schedule off leaves the
// job's Cloud Run job, which runs on demand, and no scheduler node or
// grant; a preview member that turns one on gets them under its
// parameter. The decorator's schedule runs in UTC unless set.
func TestJobSchedule(t *testing.T) {
	reg := assemble(t)
	s := shop()
	off := false
	jobSettings(s, "Production").Enabled = &off
	env := resolve(t, reg, s, stacktest.WithoutBuckets(stacktest.AcmeShop()), "Production")
	if d := env.Deployable(shipOrders); d.Job.Schedule != "" {
		t.Errorf("Production runs %q", d.Job.Schedule)
	}
	if env.Resources.Resource(shipOrders+".job") == nil || env.Resources.Resource(shipOrders+".schedule") != nil ||
		env.Resources.Resource(shipOrders+".schedule-invoker") != nil {
		t.Errorf("a job with its schedule off lowers to %v", ownedBy(env, shipOrders))
	}

	if env := resolve(t, reg, shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Preview"); env.Resources.Resource(shipOrders+".schedule") != nil {
		t.Error("a preview member runs a schedule it did not turn on")
	}
	s = shop()
	on := true
	jobSettings(s, "Preview").Enabled = &on
	env = resolve(t, reg, s, stacktest.WithoutBuckets(stacktest.AcmeShop()), "Preview")
	schedule := node(t, env, shipOrders+".schedule").Properties
	wantJSON(t, "the member's schedule", map[string]any{"name": schedule["name"], "schedule": schedule["schedule"], "timeZone": schedule["timeZone"]},
		`{"name":{"$concat":["shop-orders-ship-orders-pr",{"$parameter":"pr"}]},"schedule":"0 * * * *","timeZone":"America/New_York"}`)
	if node(t, env, shipOrders+".schedule-invoker").Inherited {
		t.Error("the member's grant is inherited from Staging")
	}

	prod := resolve(t, reg, shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Production")
	if p := node(t, prod, shipOrders+".schedule").Properties; p["schedule"] != "*/15 * * * *" || p["timeZone"] != "UTC" {
		t.Errorf("Production runs %v in %v, want the decorator's schedule in UTC", p["schedule"], p["timeZone"])
	}
}

// TestJobRefusals: Cloud Run retries a task 10 times at most, a job's
// account id fits 30 characters, and no job takes the name of the stack's
// migration job, which the migration runner owns.
func TestJobRefusals(t *testing.T) {
	reg := assemble(t)
	refused := func(t *testing.T, s *ir.Stack, services []stack.Service, want string) {
		t.Helper()
		_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: services, Environment: "Staging"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to say %q", err, want)
		}
	}
	withJob := func(job ir.Job) []stack.Service {
		services := stacktest.WithoutBuckets(stacktest.AcmeShop())
		for i := range services {
			if services[i].Name == "shop-orders" {
				services[i].Jobs = append(services[i].Jobs, job)
			}
		}
		return services
	}
	t.Run("retries", func(t *testing.T) {
		refused(t, shop(), withJob(ir.Job{Name: "Reindex", Retries: 11}), "Cloud Run retries a job's task 10 times at most")
		if _, err := stack.Resolve(reg, stack.Input{Stack: shop(), Services: withJob(ir.Job{Name: "Reindex", Retries: 10}), Environment: "Staging"}); err != nil {
			t.Errorf("10 retries: %v", err)
		}
	})
	t.Run("account id", func(t *testing.T) {
		refused(t, shop(), withJob(ir.Job{Name: "ReconcileWarehouseStock"}), "give the job's class or its API a shorter name")
	})
	t.Run("the migration job's name", func(t *testing.T) {
		s := shop()
		s.Name = "ShopOrders"
		refused(t, s, withJob(ir.Job{Name: "Migrate"}), "the stack's migration job")
	})
}

// newJobRunFixture deploys Staging with fakes, then makes the Cloud Run
// job of shop-orders' job as the provisioner would, running the image the
// deploy rolled out.
func newJobRunFixture(t *testing.T) (*deployFixture, stack.RunJobOptions) {
	t.Helper()
	f := newDeployFixture(t, true)
	f.cloud.log = f.prov
	f.ready(t)
	env := resolve(t, f.reg, shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Staging")
	o := stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()}
	if _, err := stack.Deploy(context.Background(), stack.DeployOptions{Options: o, Images: shopImages("acme-staging", 1), Planner: shopPlanner}); err != nil {
		t.Fatal(err)
	}
	f.cloud.graphJobs[shipOrdersKey] = shopImages("acme-staging", 1)[shipOrders]
	return f, stack.RunJobOptions{Options: o, Job: shipOrders}
}

// TestRunJob runs shop-orders' job on demand through the target's job
// runner (D52): an execution of its Cloud Run job as it is, with no
// override, after checking the job runs the image the manifest records.
func TestRunJob(t *testing.T) {
	f, o := newJobRunFixture(t)
	var log bytes.Buffer
	o.Log = &log
	if err := stack.RunJob(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if last := f.cloud.runs[len(f.cloud.runs)-1]; last != nil {
		t.Errorf("the job ran with overrides %q", last)
	}
	if calls := f.prov.Calls(); calls[len(calls)-1] != "cloud run "+shipOrders {
		t.Errorf("the last call is %s", calls[len(calls)-1])
	}
	for _, want := range []string{"run Cloud Run job shop-orders-ship-orders in acme-staging, us-east1", "execution " + shipOrdersKey + "/executions/1 succeeded"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log.String())
		}
	}
	if len(f.cloud.stderrReads) > 0 {
		t.Errorf("a run that succeeded read its stderr: %+v", f.cloud.stderrReads)
	}
}

// TestRunJobReportsItsError: a failed execution's error is the last try's,
// from the lines Cloud Logging holds of its `job failed` lines and panics,
// with Cloud Run's account of the try; Cloud Run's alone when it holds
// none.
func TestRunJobReportsItsError(t *testing.T) {
	const exited = "Task shop-orders-ship-orders-x7k2p-task0 failed with exit code: 1 and message: The container exited with an error."
	const logs = "(logs: https://console.cloud.google.com/logs/x"
	for _, c := range []struct {
		name   string
		stderr []string
		want   string
	}{{
		name: "the last try's error",
		stderr: []string{
			`{"caller":"shop-orders-ship-orders/main.go:40","error":"ship orders: connection refused","job":"shop-orders-ship-orders","level":"error","msg":"job failed","took":0.4}`,
			`{"caller":"shop-orders-ship-orders/main.go:40","error":"ship orders: deadline exceeded","job":"shop-orders-ship-orders","level":"error","msg":"job failed","took":1.2}`,
		},
		want: "gcp: job shop-orders-ship-orders failed in execution " + shipOrdersKey + "/executions/1: ship orders: deadline exceeded; Cloud Run: " + exited + " " + logs + ")",
	}, {
		name:   "a panic",
		stderr: []string{"panic: runtime error: index out of range [3] with length 3"},
		want:   "/executions/1: panic: runtime error: index out of range [3] with length 3; Cloud Run: " + exited + " " + logs + ")",
	}, {
		name: "no stderr",
		want: "/executions/1: " + exited + " " + logs + "; Cloud Logging held no stderr of it after 30s)",
	}} {
		t.Run(c.name, func(t *testing.T) {
			f, o := newJobRunFixture(t)
			f.cloud.failRun[shipOrders] = fakeFailure{message: exited, stderr: c.stderr}
			err := stack.RunJob(context.Background(), o)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("run = %v\nwant it to hold %s", err, c.want)
			}
			if len(f.cloud.stderrReads) != 1 {
				t.Fatalf("stderr reads %+v", f.cloud.stderrReads)
			}
			read := f.cloud.stderrReads[0]
			if read.execution != shipOrdersKey+"/executions/1" || read.wait != 30*time.Second ||
				!strings.Contains(read.match, `jsonPayload.msg="job failed"`) || !strings.Contains(read.match, `textPayload=~"^panic: "`) {
				t.Errorf("stderr read %+v, want the job's failure lines", read)
			}
		})
	}
}

// TestRunJobRefusals: the runner refuses a job whose Cloud Run job does
// not exist, and one that runs another image than the manifest records,
// before it runs anything.
func TestRunJobRefusals(t *testing.T) {
	f, o := newJobRunFixture(t)
	runs := len(f.cloud.runs)

	f.cloud.graphJobs[shipOrdersKey] = shopImages("acme-staging", 2)[shipOrders]
	err := stack.RunJob(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "but the last deploy of Staging rolled out "+shopImages("acme-staging", 1)[shipOrders]) {
		t.Errorf("another image: %v", err)
	}

	delete(f.cloud.graphJobs, shipOrdersKey)
	err = stack.RunJob(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "has no Cloud Run job shop-orders-ship-orders in acme-staging, us-east1; deploy Staging, then run the job") {
		t.Errorf("no job: %v", err)
	}
	if len(f.cloud.runs) != runs {
		t.Errorf("a refused run ran %d execution(s)", len(f.cloud.runs)-runs)
	}
}

// TestRunJobOfAMember runs a preview member's job: its Cloud Run job is
// named with the member's parameter.
func TestRunJobOfAMember(t *testing.T) {
	f := newDeployFixture(t, true)
	f.cloud.log = f.prov
	f.ready(t)
	env := resolve(t, f.reg, shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Preview")
	run := registry.Run{Environment: env, Parameters: map[string]string{"pr": "7"}}
	image := shopImages("acme-staging", 1)[shipOrders]
	f.cloud.graphJobs["acme-staging/us-east1/shop-orders-ship-orders-pr7"] = image
	target, _ := f.reg.Target(gcp.Target)
	if err := target.Jobs.RunJob(context.Background(), registry.JobRunRequest{Run: run, Job: shipOrders, Image: image}); err != nil {
		t.Fatal(err)
	}
	if calls := f.prov.Calls(); calls[len(calls)-1] != "cloud run shop-orders-ship-orders-pr7" {
		t.Errorf("the member's run is %s", calls[len(calls)-1])
	}
}

// TestBootstrapEnablesSchedulerForASchedule: a project whose environment
// runs no schedule needs no Cloud Scheduler.
func TestBootstrapEnablesSchedulerForASchedule(t *testing.T) {
	f := newDeployFixture(t, false)
	env := resolve(t, f.reg, shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Preview")
	target, _ := f.reg.Target(gcp.Target)
	req := registry.BootstrapRequest{Environment: env, Provisioner: f.prov, Dir: t.TempDir()}
	if _, err := target.Bootstrap.Bootstrap(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	changes := strings.Join(f.cloud.Changes(), "\n")
	if strings.Contains(changes, "cloudscheduler") || !strings.Contains(changes, "enable run.googleapis.com") {
		t.Errorf("a bootstrap for Preview, which runs no schedule:\n%s", changes)
	}
}
