package cloudflare_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The cloudflare target's migration runner (docs/stack-model.md, section
// 18.6, D64), over a fake Cloudflare and a stand-in for
// superschematic-migrate that records the command it was run as and the
// job document and plans it was handed.

// The D1 databases of the fixture's account.
const (
	reviewsUUID   = "4b3f0a5e-2d1c-4e8f-9a7b-6c5d4e3f2a1b"
	reviewsPrUUID = "0e9d8c7b-6a5f-4e3d-2c1b-0a9f8e7d6c5b"
	runnerPath    = "/opt/superschematic/bin/superschematic-migrate"
)

// runnerCall is one run of the stand-in runner.
type runnerCall struct {
	args  []string
	token string
	job   map[string]any
	plans map[string]string
}

// migrateFixture is the extension over a fake Cloudflare, with a shell
// naming the runner and a stand-in that records each run; fail makes the
// stand-in's runs fail. outputs are what each environment's applied graph
// outputs for its D1 database's node, by environment.
type migrateFixture struct {
	cloud   *fakeCloud
	shell   shell
	calls   []runnerCall
	fail    error
	reg     *registry.Registry
	outputs map[string]map[string]any
}

// d1Node is the node of reviews-db's D1 database in the D1 fixture.
const d1Node = "reviews-db.d1.reviews-db"

func newMigrateFixture(t *testing.T) *migrateFixture {
	t.Helper()
	f := &migrateFixture{cloud: newFakeCloud(), shell: shell{cloudflare.TokenEnv: "the-deployers-token", cloudflare.MigrateEnv: runnerPath}}
	f.outputs = map[string]map[string]any{
		"Staging": {"uuid": reviewsUUID, "id": reviewsUUID},
		"Preview": {"uuid": reviewsPrUUID, "id": reviewsPrUUID},
	}
	ext := cloudflare.Extension{Cloud: f.cloud, Getenv: f.shell.getenv, Exec: func(cmd *exec.Cmd) error {
		call := runnerCall{args: slices.Clone(cmd.Args), plans: map[string]string{}}
		for _, kv := range cmd.Env {
			if v, ok := strings.CutPrefix(kv, cloudflare.TokenEnv+"="); ok {
				call.token = v
			}
		}
		if len(cmd.Args) == 4 && cmd.Args[1] == "job" && cmd.Args[2] == "--job" {
			data, err := os.ReadFile(cmd.Args[3])
			if err != nil {
				t.Errorf("the runner's job document: %v", err)
			}
			if err := json.Unmarshal(data, &call.job); err != nil {
				t.Errorf("the job document %s: %v", data, err)
			}
			for _, db := range call.job["databases"].([]any) {
				rel := db.(map[string]any)["plan"].(string)
				plan, err := os.ReadFile(filepath.Join(filepath.Dir(cmd.Args[3]), filepath.FromSlash(rel)))
				if err != nil {
					t.Errorf("the plan beside the job document: %v", err)
				}
				call.plans[rel] = string(plan)
			}
		}
		_, _ = cmd.Stdout.Write([]byte("service reviews-db: applied 2 steps\n"))
		f.calls = append(f.calls, call)
		return f.fail
	}}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext, pulumiStub{})
	if err != nil {
		t.Fatal(err)
	}
	f.reg = reg
	return f
}

// request is the phase's request for database reviews-db of the D1
// fixture's environment env, with params, over plans.
func (f *migrateFixture) request(t *testing.T, env string, params map[string]string, phase ir.MigrationPhase, plans ...registry.MigrationPlan) registry.MigrationRequest {
	t.Helper()
	resolved, err := stack.Resolve(f.reg, stack.Input{Stack: d1Stack(), Services: d1Services(), Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	return registry.MigrationRequest{
		Run:      registry.Run{Environment: resolved, Parameters: params},
		Database: "reviews-db",
		Phase:    phase,
		Plans:    plans,
		Outputs: func(context.Context) (map[string]map[string]any, error) {
			out := map[string]map[string]any{"reviews.worker": {"id": "reviews", "name": "reviews-stack-" + strings.ToLower(env) + "-reviews"}}
			if o := f.outputs[env]; o != nil {
				out[d1Node] = o
			}
			return out, nil
		},
		Log: new(bytes.Buffer),
	}
}

func (f *migrateFixture) runner(t *testing.T) registry.MigrationRunner {
	t.Helper()
	target, ok := f.reg.Target(cloudflare.Target)
	if !ok || target.Migrations == nil {
		t.Fatal("the cloudflare target has no migration runner")
	}
	return target.Migrations
}

// plan is a stand-in plan document of reviews-db.
const plan = `{"version":1,"service":"reviews-db","dialect":"sqlite","steps":[]}`

// TestMigrateRunsTheRunnerAgainstD1: a phase with steps reads the uuid of
// reviews-db's D1 database from its node's outputs, and runs superschematic-migrate job, from
// where SUPERSCHEMATIC_MIGRATE says, with the run's token, on a job
// document whose database is d1://<account>/<uuid> with no privileges and
// whose plan is beside it. The runner's output reaches the deploy's log,
// and nothing of the job is left behind.
func TestMigrateRunsTheRunnerAgainstD1(t *testing.T) {
	f := newMigrateFixture(t)
	req := f.request(t, "Staging", nil, ir.MigrationExpand, registry.MigrationPlan{Service: "reviews-db", Plan: json.RawMessage(plan), Steps: 2, Servers: []string{"reviews"}})
	if err := f.runner(t).Migrate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("the runner ran %d times, want once", len(f.calls))
	}
	call := f.calls[0]
	if call.args[0] != runnerPath || call.args[1] != "job" || call.args[2] != "--job" {
		t.Errorf("the runner ran as %v", call.args)
	}
	if call.token != "the-deployers-token" {
		t.Errorf("the runner got the token %q, want the run's", call.token)
	}
	databases := call.job["databases"].([]any)
	db := databases[0].(map[string]any)
	if call.job["version"] != float64(1) || call.job["phase"] != "expand" || len(databases) != 1 || len(db) != 3 ||
		db["service"] != "reviews-db" || db["databaseUrl"] != "d1://"+accountID+"/"+reviewsUUID || !strings.HasPrefix(db["plan"].(string), "plans/reviews-db-") {
		t.Errorf("the job document = %v", call.job)
	}
	if _, ok := call.job["cloudSql"]; ok {
		t.Errorf("the job document reaches Cloud SQL: %v", call.job)
	}
	if got := call.plans[db["plan"].(string)]; got != plan {
		t.Errorf("the plan beside the job document = %s", got)
	}
	if _, err := os.Stat(filepath.Dir(call.args[3])); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the job's directory is left behind: %v", err)
	}
	log := req.Log.(*bytes.Buffer).String()
	for _, want := range []string{"reviews-db: 2 expand steps on D1 database reviews-stack-staging-reviews-db (" + reviewsUUID + ")", "service reviews-db: applied 2 steps"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
}

// TestMigrateAPreviewMember: a member of Preview migrates its own D1
// database, whose uuid its own graph's outputs hold.
func TestMigrateAPreviewMember(t *testing.T) {
	f := newMigrateFixture(t)
	req := f.request(t, "Preview", map[string]string{"pr": "7"}, ir.MigrationContract, registry.MigrationPlan{Service: "reviews-db", Plan: json.RawMessage(plan), Steps: 1})
	if err := f.runner(t).Migrate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	db := f.calls[0].job["databases"].([]any)[0].(map[string]any)
	if f.calls[0].job["phase"] != "contract" || db["databaseUrl"] != "d1://"+accountID+"/"+reviewsPrUUID {
		t.Errorf("the job document = %v", f.calls[0].job)
	}
}

// TestMigrateAPhaseWithNoSteps: the deploy hands the runner a plan with no
// steps when the servers that connect changed, for their privileges, which
// D1 does not have: nothing runs.
func TestMigrateAPhaseWithNoSteps(t *testing.T) {
	f := newMigrateFixture(t)
	req := f.request(t, "Staging", nil, ir.MigrationExpand, registry.MigrationPlan{Service: "reviews-db", Servers: []string{"reviews"}})
	if err := f.runner(t).Migrate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("the runner ran for a phase with no steps: %v", f.calls)
	}
	if log := req.Log.(*bytes.Buffer).String(); !strings.Contains(log, "reviews-db: no expand steps; D1 has no privileges to give") {
		t.Errorf("the log = %q", log)
	}
}

// TestMigrateReadsTheNodesID: a database no Worker binds has no `uuid`
// among the outputs, since no binding references it, and the runner reads
// its node's `id`, which the provider sets to the same uuid and the
// program exports for every node.
func TestMigrateReadsTheNodesID(t *testing.T) {
	f := newMigrateFixture(t)
	f.outputs["Staging"] = map[string]any{"id": reviewsUUID}
	req := f.request(t, "Staging", nil, ir.MigrationExpand, registry.MigrationPlan{Service: "reviews-db", Plan: json.RawMessage(plan), Steps: 1})
	if err := f.runner(t).Migrate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if db := f.calls[0].job["databases"].([]any)[0].(map[string]any); db["databaseUrl"] != "d1://"+accountID+"/"+reviewsUUID {
		t.Errorf("the job document = %v", f.calls[0].job)
	}
}

// TestMigrateRefusals: the runner refuses a run with no token, with no
// runner, with no outputs, and with no uuid among its D1 database node's
// outputs, and fails when the runner fails, naming the phase and the
// database.
func TestMigrateRefusals(t *testing.T) {
	steps := registry.MigrationPlan{Service: "reviews-db", Plan: json.RawMessage(plan), Steps: 1}
	for name, tc := range map[string]struct {
		edit      func(*migrateFixture)
		noOutputs bool
		want      string
	}{
		"no outputs": {
			edit:      func(*migrateFixture) {},
			noOutputs: true,
			want:      "cloudflare: migrate reviews-db: the deploy reads the runner no outputs",
		},
		"no token": {
			edit: func(f *migrateFixture) { delete(f.shell, cloudflare.TokenEnv) },
			want: "cloudflare: set CLOUDFLARE_API_TOKEN",
		},
		"no runner": {
			edit: func(f *migrateFixture) { delete(f.shell, cloudflare.MigrateEnv) },
			want: "superschematic-migrate is not on PATH, and SUPERSCHEMATIC_MIGRATE names no runner",
		},
		"no database": {
			edit: func(f *migrateFixture) { delete(f.outputs, "Staging") },
			want: "cloudflare: migrate reviews-db: the outputs of Staging hold no uuid of node " + d1Node + ", D1 database reviews-stack-staging-reviews-db, which the infrastructure step creates",
		},
		"the runner fails": {
			edit: func(f *migrateFixture) { f.fail = errors.New("exit status 1") },
			want: "cloudflare: the expand phase on reviews-db: superschematic-migrate job: exit status 1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			f := newMigrateFixture(t)
			tc.edit(f)
			req := f.request(t, "Staging", nil, ir.MigrationExpand, steps)
			if tc.noOutputs {
				req.Outputs = nil
			}
			err := f.runner(t).Migrate(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Migrate = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestPrepareMigrations: before a deploy changes anything, the target
// refuses a run whose migrations could not run on this machine.
func TestPrepareMigrations(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	f := newMigrateFixture(t)
	target, _ := f.reg.Target(cloudflare.Target)
	preparer, ok := target.Migrations.(registry.MigrationPreparer)
	if !ok {
		t.Fatal("the runner prepares nothing")
	}
	run := f.request(t, "Staging", nil, ir.MigrationExpand).Run
	log := new(bytes.Buffer)
	if err := preparer.PrepareMigrations(context.Background(), run, log); err != nil || !strings.Contains(log.String(), "the D1 migrations run "+runnerPath+" on this machine") {
		t.Errorf("PrepareMigrations = %v: %s", err, log)
	}
	delete(f.shell, cloudflare.MigrateEnv)
	if err := preparer.PrepareMigrations(context.Background(), run, log); err == nil || !strings.Contains(err.Error(), "install it from a release") {
		t.Errorf("PrepareMigrations with no runner = %v", err)
	}
}
