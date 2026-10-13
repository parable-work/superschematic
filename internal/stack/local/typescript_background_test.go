package local_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// fulfilOrders is the deployable of shop-orders' worker (D53).
const fulfilOrders = "shop-orders-fulfil-orders"

// tsOrdersShop is the shop with shop-orders in TypeScript, so its server
// Orders, its job ShipOrders and its worker FulfilOrders run on Bun (D51,
// D52, D53).
func tsOrdersShop() []stack.Service {
	services := stacktest.AcmeShop()
	for i := range services {
		if services[i].Name == stacktest.ShopOrders.Name {
			services[i].Language = registry.APILanguageTypeScript
		}
	}
	return services
}

// TestTypeScriptJobAndWorkerRunOnBun applies Dev with shop-orders in
// TypeScript. The program holds its job as a TypeScript job with no binary
// to build, and its worker as a TypeScript process with no port. The
// rollout installs the output root's workspace once, builds only shop-api
// with go, and starts Orders and the worker as bun main.ts in their
// packages, the worker with its concurrency. stack run runs the job as bun
// main.ts in its package, after an install, and builds nothing.
func TestTypeScriptJobAndWorkerRunOnBun(t *testing.T) {
	f := newFixtureWith(t, shop(), tsOrdersShop(), "Dev")
	data, err := os.ReadFile(filepath.Join(f.req.Dir, local.ProgramFile))
	if err != nil {
		t.Fatal(err)
	}
	var prog local.Program
	if err := json.Unmarshal(data, &prog); err != nil {
		t.Fatal(err)
	}
	job := prog.JobOf(shipOrders)
	if job == nil || job.Language != local.LanguageTypeScript || job.Binary != "" {
		t.Fatalf("the program's job = %+v, want a TypeScript job with no binary", job)
	}
	var worker *local.Server
	for _, s := range prog.Servers {
		if s.Deployable == fulfilOrders {
			worker = s
		}
	}
	if worker == nil || worker.Language != local.LanguageTypeScript || worker.Kind != "worker" || worker.Binary != "" || worker.Port != 0 {
		t.Fatalf("the program's worker = %+v, want a TypeScript process with no binary and no port", worker)
	}

	f.runner.rules = append(f.runningContainers("postgres:16-alpine"),
		rule{prefix: migrateStatus, stderr: "superschematic-migrate: service shop-db has no applied model"},
		rule{prefix: "/bin/superschematic-migrate apply", out: "applied 2 steps\n"},
	)
	if err := f.applyAll(t); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	var builds, bun []string
	for _, cmd := range f.runner.commands {
		switch cmd.Path {
		case "/bin/go":
			builds = append(builds, filepath.Base(cmd.Dir))
		case "/bin/bun":
			rel, err := filepath.Rel(f.req.OutputRoot, cmd.Dir)
			if err != nil {
				t.Fatal(err)
			}
			bun = append(bun, filepath.ToSlash(rel)+": bun "+strings.Join(cmd.Args, " "))
		}
	}
	if !slices.Equal(builds, []string{"shop-api"}) {
		t.Errorf("the rollout built %v with go, want shop-api alone", builds)
	}
	modules := "server/" + f.env.Stack + "/"
	if want := []string{".: bun install", modules + "Orders: bun main.ts", modules + fulfilOrders + ": bun main.ts"}; !slices.Equal(bun, want) {
		t.Errorf("the rollout ran %v on Bun, want %v", bun, want)
	}
	want := strconv.Itoa(stacktest.FulfilOrders.Concurrency)
	for _, p := range f.runner.started {
		if strings.HasSuffix(p.cmd.Dir, fulfilOrders) && envValue(p.cmd.Env, local.WorkerConcurrencyVariable) != want {
			t.Errorf("the worker runs with %s=%q, want its decorator's %s", local.WorkerConcurrencyVariable, envValue(p.cmd.Env, local.WorkerConcurrencyVariable), want)
		}
	}

	// stack run, from a provisioner of its own as the command makes: the
	// job runs once on Bun in its package, after an install, and succeeds.
	f.runner.commands = nil
	f.runner.exits = map[string][]error{"bun": {nil}}
	run := &local.Provisioner{Runner: f.runner, Out: f.out, ReadyTimeout: time.Second, PollInterval: time.Millisecond}
	if err := run.RunJob(context.Background(), f.req, shipOrders); err != nil {
		t.Fatalf("RunJob = %v\n%s", err, f.out)
	}
	var ran []string
	for _, cmd := range f.runner.commands {
		if cmd.Path == "/bin/go" {
			t.Errorf("stack run built %s with go", cmd.Dir)
		}
		if cmd.Path == "/bin/bun" {
			rel, _ := filepath.Rel(f.req.OutputRoot, cmd.Dir)
			ran = append(ran, filepath.ToSlash(rel)+": bun "+strings.Join(cmd.Args, " "))
		}
	}
	if want := []string{".: bun install", modules + shipOrders + ": bun main.ts"}; !slices.Equal(ran, want) {
		t.Errorf("stack run ran %v on Bun, want %v", ran, want)
	}
	if out := f.out.String(); !strings.Contains(out, "job "+shipOrders+": the run stack run asked for") || !strings.Contains(out, "succeeded") {
		t.Errorf("the output does not report the run:\n%s", out)
	}
}
