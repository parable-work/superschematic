package cloudflare_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The migration runner on a database on Hyperdrive (D92), over a fake
// Cloudflare that holds finances-db's Hyperdrive config and a stand-in for
// superschematic-migrate that records each run.

// stagingOrigin is the origin connection string the deploying shell holds
// for finances-db in Staging: the database, host and user its Hyperdrive
// config names, and a password nothing may print.
const stagingOrigin = "postgres://finances:s3cret-password@db.example.com:5432/finances?sslmode=require"

// originRun is one run of the stand-in runner: its arguments, the
// DATABASE_URL its environment held, and the plan it was handed.
type originRun struct {
	args        []string
	databaseURL string
	plan        string
}

// originFixture is the extension over a fake Cloudflare holding
// finances-db's Staging config, with a shell that names the runner, the
// token and the origin, and a stand-in that records each run.
type originFixture struct {
	cloud *fakeCloud
	shell shell
	runs  []originRun
	reg   *registry.Registry
}

func newOriginFixture(t *testing.T) *originFixture {
	t.Helper()
	f := &originFixture{cloud: newFakeCloud(), shell: shell{
		cloudflare.TokenEnv:                 "the-deployers-token",
		cloudflare.MigrateEnv:               runnerPath,
		cloudflare.OriginEnv("finances-db"): stagingOrigin,
	}}
	config := &cloudflare.HyperdriveConfig{ID: stagingHyperdrive, Name: "ledger-stack-staging-finances-db"}
	config.Origin.Host, config.Origin.Port, config.Origin.Database, config.Origin.User, config.Origin.Scheme = "db.example.com", 5432, "finances", "finances", "postgres"
	config.Caching.Disabled = true
	f.cloud.hyperdrive[stagingHyperdrive] = config
	ext := cloudflare.Extension{Cloud: f.cloud, Getenv: f.shell.getenv, Exec: func(cmd *exec.Cmd) error {
		run := originRun{args: slices.Clone(cmd.Args)}
		for _, kv := range cmd.Env {
			if v, ok := strings.CutPrefix(kv, "DATABASE_URL="); ok {
				run.databaseURL = v
			}
		}
		if i := slices.Index(cmd.Args, "--plan"); i >= 0 && i+1 < len(cmd.Args) {
			data, err := os.ReadFile(cmd.Args[i+1])
			if err != nil {
				t.Errorf("the plan the runner was handed: %v", err)
			}
			run.plan = string(data)
		}
		_, _ = cmd.Stdout.Write([]byte("service finances-db: applied 3 steps\n"))
		f.runs = append(f.runs, run)
		return nil
	}}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext, pulumiStub{})
	if err != nil {
		t.Fatal(err)
	}
	f.reg = reg
	return f
}

// request is the phase's request for finances-db of the ledger fixture's
// environment env over plans.
func (f *originFixture) request(t *testing.T, env string, phase ir.MigrationPhase, plans ...registry.MigrationPlan) registry.MigrationRequest {
	t.Helper()
	resolved, err := stack.Resolve(f.reg, stack.Input{Stack: ledgerStack(), Services: ledgerServices(), Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	return registry.MigrationRequest{
		Run:      registry.Run{Environment: resolved},
		Database: "finances-db",
		Phase:    phase,
		Plans:    plans,
		Log:      new(bytes.Buffer),
	}
}

func (f *originFixture) runner(t *testing.T) registry.MigrationRunner {
	t.Helper()
	target, _ := f.reg.Target(cloudflare.Target)
	return target.Migrations
}

// ledgerPlan is a stand-in plan document of finances-db.
const ledgerPlan = `{"version":1,"service":"finances-db","dialect":"postgres","steps":[]}`

// TestMigrateTheOriginThroughItsHyperdriveConfig: a phase with steps on
// finances-db reads its Hyperdrive config through Cloudflare's API, finds it
// reaches the database, host and user of the connection string the shell
// holds, with caching off, and runs superschematic-migrate apply on the
// plan, the connection string in DATABASE_URL alone: no argument, no file
// and no line of the log holds its password. A plan with no steps runs
// nothing.
func TestMigrateTheOriginThroughItsHyperdriveConfig(t *testing.T) {
	f := newOriginFixture(t)
	req := f.request(t, "Staging", ir.MigrationExpand,
		registry.MigrationPlan{Service: "finances-db", Plan: json.RawMessage(ledgerPlan), Steps: 3, Servers: []string{"finances-api"}})
	if err := f.runner(t).Migrate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(f.runs) != 1 {
		t.Fatalf("the runner ran %d times, want once", len(f.runs))
	}
	run := f.runs[0]
	if len(run.args) != 6 || run.args[0] != runnerPath || run.args[1] != "apply" || run.args[2] != "--plan" || run.args[4] != "--phase" || run.args[5] != "expand" {
		t.Errorf("the runner ran as %v", run.args)
	}
	if run.databaseURL != stagingOrigin || run.plan != ledgerPlan {
		t.Errorf("the runner got DATABASE_URL %q and plan %s", run.databaseURL, run.plan)
	}
	log := req.Log.(*bytes.Buffer).String()
	for _, want := range []string{
		"finances-db: 3 expand steps on database finances of db.example.com:5432 as finances, through Hyperdrive config " + stagingHyperdrive,
		"service finances-db: applied 3 steps",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
	for _, where := range append(slices.Clone(run.args), log) {
		if strings.Contains(where, "s3cret-password") {
			t.Errorf("%q holds the origin's password", where)
		}
	}
	if _, err := os.Stat(run.args[3]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the plan's file is left behind: %v", err)
	}

	f.runs = nil
	none := f.request(t, "Staging", ir.MigrationContract, registry.MigrationPlan{Service: "finances-db", Plan: json.RawMessage(ledgerPlan), Steps: 0, Servers: []string{"finances-api"}})
	if err := f.runner(t).Migrate(context.Background(), none); err != nil || len(f.runs) != 0 {
		t.Errorf("a phase with no steps = %v, and ran %d times", err, len(f.runs))
	}
}

// TestMigrateTheOriginRefusals: the runner refuses, before it runs
// anything, an origin the shell does not hold, one that is no postgres://
// connection string, a Hyperdrive config the account does not hold, one
// that reaches another database, host, port or user, and one that caches
// reads while the database's settings do not say it may; Production's
// settings say so, so its caching config passes. PrepareMigrations refuses
// a deploy whose shell holds no origin before the deploy changes anything.
func TestMigrateTheOriginRefusals(t *testing.T) {
	steps := registry.MigrationPlan{Service: "finances-db", Plan: json.RawMessage(ledgerPlan), Steps: 1}
	for name, tc := range map[string]struct {
		edit func(*originFixture)
		want string
	}{
		"no origin": {
			edit: func(f *originFixture) { delete(f.shell, cloudflare.OriginEnv("finances-db")) },
			want: "database finances-db is on cloudflare.hyperdrive, and its migrations run from this machine on its origin: set HYPERDRIVE_ORIGIN_FINANCES_DB to the origin's connection string",
		},
		"no connection string": {
			edit: func(f *originFixture) { f.shell[cloudflare.OriginEnv("finances-db")] = "finances.example.com" },
			want: "HYPERDRIVE_ORIGIN_FINANCES_DB is no postgres:// connection string with a host",
		},
		"no config": {
			edit: func(f *originFixture) { delete(f.cloud.hyperdrive, stagingHyperdrive) },
			want: "database finances-db's settings name Hyperdrive config " + stagingHyperdrive + ", which account " + accountID + " does not hold",
		},
		"another database": {
			edit: func(f *originFixture) { f.cloud.hyperdrive[stagingHyperdrive].Origin.Database = "finances_old" },
			want: "database finances-db's Hyperdrive config " + stagingHyperdrive + " reaches database finances_old of db.example.com:5432 as finances, and HYPERDRIVE_ORIGIN_FINANCES_DB reaches database finances of db.example.com:5432 as finances: a deploy would migrate one database while its Workers read another",
		},
		"another user": {
			edit: func(f *originFixture) { f.cloud.hyperdrive[stagingHyperdrive].Origin.User = "app" },
			want: "reaches database finances of db.example.com:5432 as app",
		},
		"caching on": {
			edit: func(f *originFixture) { f.cloud.hyperdrive[stagingHyperdrive].Caching.Disabled = false },
			want: "database finances-db's Hyperdrive config " + stagingHyperdrive + " caches reads, which can answer a request with what the database held before the request's own write; turn its caching off, or set caching: true in the database's settings to keep it",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOriginFixture(t)
			tc.edit(f)
			err := f.runner(t).Migrate(context.Background(), f.request(t, "Staging", ir.MigrationExpand, steps))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Migrate = %v, want it to say %q", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "s3cret-password") {
				t.Errorf("the refusal holds the origin's password: %v", err)
			}
			if len(f.runs) != 0 {
				t.Errorf("the runner ran %d times", len(f.runs))
			}
		})
	}

	f := newOriginFixture(t)
	config := *f.cloud.hyperdrive[stagingHyperdrive]
	config.ID, config.Caching.Disabled = productionHyperdrive, false
	f.cloud.hyperdrive[productionHyperdrive] = &config
	if err := f.runner(t).Migrate(context.Background(), f.request(t, "Production", ir.MigrationExpand, steps)); err != nil || len(f.runs) != 1 {
		t.Errorf("Production, whose settings let its config cache reads = %v, %d runs", err, len(f.runs))
	}

	delete(f.shell, cloudflare.OriginEnv("finances-db"))
	preparer := f.runner(t).(registry.MigrationPreparer)
	err := preparer.PrepareMigrations(context.Background(), f.request(t, "Staging", ir.MigrationExpand).Run, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "set HYPERDRIVE_ORIGIN_FINANCES_DB") {
		t.Errorf("PrepareMigrations with no origin = %v", err)
	}
}
