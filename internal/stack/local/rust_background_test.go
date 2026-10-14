package local_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// binCrate is a Cargo.toml whose package is name and whose one binary is
// bin, as servergen names a Rust job's or worker's crate after its stack
// and deployable and its binary after the deployable (D65).
func binCrate(name, bin string) string {
	return crate(name) + "\n[[bin]]\nname = \"" + bin + "\"\npath = \"src/main.rs\"\n"
}

// writeRustOrders writes the crates of shop-orders' Rust server, worker
// and job into their entrypoint modules, each binary named after its
// deployable.
func (f *fixture) writeRustOrders(t *testing.T) {
	t.Helper()
	f.writeCrate(t, "Orders", binCrate("rust-orders-stack-orders-server", "orders"))
	f.writeCrate(t, fulfilOrders, binCrate("rust-orders-stack-"+fulfilOrders+"-worker", fulfilOrders))
	f.writeCrate(t, shipOrders, binCrate("rust-orders-stack-"+shipOrders+"-job", shipOrders))
}

// TestRustJobAndWorkerRunWithCargo applies Dev with shop-orders in Rust
// (D65). The rollout builds the crates of Orders, its worker and its job
// with cargo, each in its module and all into the program directory's one
// Cargo target directory, builds only shop-api with go, and starts Orders
// and the worker from target/release, the worker with its concurrency and
// no port. stack run builds the job's crate again, runs the binary its
// Cargo.toml names in its module, and reports a failed run's exit after
// its retry.
func TestRustJobAndWorkerRunWithCargo(t *testing.T) {
	f := newFixtureWith(t, rustOrdersStack(), rustOrdersShop(), "Dev")
	f.writeRustOrders(t)
	f.runner.rules = append(f.runningContainers("postgres:16-alpine"),
		rule{prefix: migrateStatus, stderr: "superschematic-migrate: service shop-db has no applied model"},
		rule{prefix: "/bin/superschematic-migrate apply", out: "applied 2 steps\n"},
	)
	if err := f.applyAll(t); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	target, err := filepath.Abs(filepath.Join(f.req.Dir, local.RustTargetDir))
	if err != nil {
		t.Fatal(err)
	}
	module := func(deployable string) string {
		return filepath.Join(f.req.OutputRoot, local.ModulePath(f.env.Stack, deployable))
	}
	release := func(binary string) string { return filepath.Join(target, "release", binary) }
	builds := func() []string {
		var out []string
		for _, cmd := range f.runner.commands {
			switch cmd.Path {
			case "/bin/go":
				out = append(out, "go "+filepath.Base(cmd.Dir))
			case "/bin/cargo":
				if got, want := strings.Join(cmd.Args, " "), "build --release --locked --quiet --target-dir "+target; got != want {
					t.Errorf("cargo ran %q in %s, want %q", got, cmd.Dir, want)
				}
				out = append(out, "cargo "+filepath.Base(cmd.Dir))
			}
		}
		slices.Sort(out)
		return out
	}
	if got, want := builds(), []string{"cargo Orders", "cargo " + fulfilOrders, "cargo " + shipOrders, "go shop-api"}; !slices.Equal(got, want) {
		t.Errorf("the rollout built %v, want %v", got, want)
	}
	started := map[string]*fakeProcess{}
	for _, p := range f.runner.started {
		started[p.cmd.Path] = p
	}
	orders, worker := started[release("orders")], started[release(fulfilOrders)]
	if orders == nil || orders.cmd.Dir != module("Orders") {
		t.Fatalf("Orders started as %+v, want %s in its module", orders, release("orders"))
	}
	if worker == nil || worker.cmd.Dir != module(fulfilOrders) {
		t.Fatalf("the worker started as %+v, want %s in its module", worker, release(fulfilOrders))
	}
	if got, want := envValue(worker.cmd.Env, local.WorkerConcurrencyVariable), strconv.Itoa(stacktest.FulfilOrders.Concurrency); got != want {
		t.Errorf("the worker runs with %s=%q, want its decorator's %s", local.WorkerConcurrencyVariable, got, want)
	}
	if got := envValue(worker.cmd.Env, "PORT"); got != "" {
		t.Errorf("the worker got PORT=%s; it serves nothing", got)
	}
	if got, want := envValue(worker.cmd.Env, "SHOP_DB_DATABASE_URL"), local.DatabaseURL(f.pgPort, "shop_db"); got != want {
		t.Errorf("the worker's SHOP_DB_DATABASE_URL = %q, want %q", got, want)
	}
	if _, ran := started[release(shipOrders)]; ran {
		t.Error("the rollout ran the job, which runs only on its schedule or on demand")
	}
	for _, want := range []string{
		"build " + shipOrders + ": cargo build --release --locked in server/rust-orders-stack/" + shipOrders,
		"build " + fulfilOrders + ": cargo build --release --locked in server/rust-orders-stack/" + fulfilOrders,
		"worker " + fulfilOrders + " is running",
	} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, f.out)
		}
	}

	// stack run, from a provisioner of its own as the command makes: the
	// job's crate is built again into the target directory, and its binary
	// runs once in its module and succeeds.
	f.runner.commands = nil
	f.runner.exits = map[string][]error{shipOrders: {nil}}
	run := &local.Provisioner{Runner: f.runner, Out: f.out, ReadyTimeout: time.Second, PollInterval: time.Millisecond}
	if err := run.RunJob(context.Background(), f.req, shipOrders); err != nil {
		t.Fatalf("RunJob = %v\n%s", err, f.out)
	}
	if got, want := builds(), []string{"cargo " + shipOrders}; !slices.Equal(got, want) {
		t.Errorf("stack run built %v, want %v", got, want)
	}
	var job *fakeProcess
	for _, p := range f.runner.started {
		if p.cmd.Path == release(shipOrders) {
			job = p
		}
	}
	if job == nil || job.cmd.Dir != module(shipOrders) || len(job.cmd.Args) != 0 {
		t.Fatalf("stack run started %+v, want %s in its module", job, release(shipOrders))
	}
	if got, want := envValue(job.cmd.Env, "SHOP_DB_DATABASE_URL"), local.DatabaseURL(f.pgPort, "shop_db"); got != want {
		t.Errorf("the job's SHOP_DB_DATABASE_URL = %q, want %q", got, want)
	}
	if got := envValue(job.cmd.Env, "PORT"); got != "" {
		t.Errorf("the job got PORT=%s; it serves nothing", got)
	}

	// A run whose binary exits 1 fails, and is run again once, its retry.
	f.runner.exits = map[string][]error{shipOrders: {errors.New("exit status 1"), errors.New("exit status 1")}}
	err = run.RunJob(context.Background(), f.req, shipOrders)
	if err == nil || !strings.Contains(err.Error(), "local: job "+shipOrders+" failed: exit status 1") {
		t.Errorf("RunJob of a failing job = %v, want its exit status", err)
	}
	if !strings.Contains(f.out.String(), "the run stack run asked for, try 2 of 2 failed") {
		t.Errorf("the output does not report the retry:\n%s", f.out)
	}
	_ = f.prov.Destroy(context.Background(), f.req)
}

// TestApplyRefusesARustJobItCannotRun: a Rust job's crate that is
// missing, or that names the binary of another Rust deployable of the
// environment, which would be one file in the shared target directory,
// fails the rollout and says why, before anything starts.
func TestApplyRefusesARustJobItCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *fixture)
		want  string
	}{
		{
			name: "no crate",
			setup: func(t *testing.T, f *fixture) {
				f.writeCrate(t, "Orders", binCrate("rust-orders-stack-orders-server", "orders"))
				f.writeCrate(t, fulfilOrders, binCrate("rust-orders-stack-"+fulfilOrders+"-worker", fulfilOrders))
			},
			want: "local: job " + shipOrders + ": no Cargo.toml in ",
		},
		{
			name: "a job naming its server's binary",
			setup: func(t *testing.T, f *fixture) {
				f.writeRustOrders(t)
				f.writeCrate(t, shipOrders, binCrate("rust-orders-stack-"+shipOrders+"-job", "orders"))
			},
			want: "local: server Orders and job " + shipOrders + " both build a binary named orders into one Cargo target directory, target; name each crate's binary after its deployable",
		},
		{
			name: "two binaries",
			setup: func(t *testing.T, f *fixture) {
				f.writeRustOrders(t)
				f.writeCrate(t, fulfilOrders, binCrate("w", "a")+"\n[[bin]]\nname = \"b\"\npath = \"src/b.rs\"\n")
			},
			want: "declares 2 binaries; a worker's crate builds one",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureWith(t, rustOrdersStack(), rustOrdersShop(), "Dev")
			f.keys(t)
			tc.setup(t, f)
			err := f.rollout(t, shipOrders+".job")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
			for _, p := range f.runner.started {
				if strings.Contains(p.cmd.Path, local.RustTargetDir) {
					t.Errorf("a Rust process started: %s", p.cmd.Path)
				}
			}
			_ = f.prov.Destroy(context.Background(), f.req)
		})
	}
}
