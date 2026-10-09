package local_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// fakeRunner stands in for the machine. It records every command, answers
// each from a rule, starts fake processes, and answers readiness probes.
type fakeRunner struct {
	mu       sync.Mutex
	commands []local.Command
	started  []*fakeProcess

	// rules answer Run: the first rule whose prefix the command line
	// starts with answers it; a command no rule answers succeeds with no
	// output.
	rules []rule
	// missing are the executables LookPath does not find.
	missing []string
	// inUse are the ports something listens on.
	inUse []int
	// notReady is how many probes of a URL answer 503 first.
	notReady int
	probes   map[string]int
	// exitOnStart makes every process exit as it starts.
	exitOnStart bool
	// exits, by the base name of a binary, are how its next processes
	// exit, one each, as soon as they start; a process with none left
	// runs until it is stopped.
	exits map[string][]error
}

type rule struct {
	prefix string
	// times limits the rule to its first n matches; zero is every match.
	times  int
	used   int
	out    string
	stderr string
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if slices.Contains(f.missing, name) {
		return "", errors.New("executable file not found in $PATH")
	}
	return "/bin/" + name, nil
}

func (f *fakeRunner) Run(_ context.Context, cmd local.Command) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	line := cmd.String()
	for i := range f.rules {
		r := &f.rules[i]
		if !strings.HasPrefix(line, r.prefix) || (r.times > 0 && r.used >= r.times) {
			continue
		}
		r.used++
		if cmd.Stdout != nil && r.out != "" {
			_, _ = cmd.Stdout.Write([]byte(r.out))
		}
		if r.stderr != "" {
			return []byte(r.out), &local.RunError{Command: line, Err: errors.New("exit status 1"), Stderr: r.stderr}
		}
		return []byte(r.out), nil
	}
	return nil, nil
}

func (f *fakeRunner) Start(cmd local.Command) (local.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	p := &fakeProcess{cmd: cmd, done: make(chan struct{})}
	f.started = append(f.started, p)
	_, _ = fmt.Fprintf(cmd.Stdout, "listening on %s\n", envValue(cmd.Env, "PORT"))
	_, _ = cmd.Stderr.Write([]byte("a line with no end"))
	if f.exitOnStart {
		p.exit(errors.New("exit status 2"))
	}
	if exits := f.exits[filepath.Base(cmd.Path)]; len(exits) > 0 {
		f.exits[filepath.Base(cmd.Path)] = exits[1:]
		p.exit(exits[0])
	}
	return p, nil
}

func (f *fakeRunner) Get(_ context.Context, url string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.probes == nil {
		f.probes = map[string]int{}
	}
	f.probes[url]++
	// As net/http does, a worker's readiness, which is no URL, fails.
	if !strings.HasPrefix(url, "http://") {
		return 0, fmt.Errorf("Get %q: unsupported protocol scheme", url)
	}
	if f.probes[url] <= f.notReady {
		return 503, nil
	}
	return 200, nil
}

func (f *fakeRunner) PortInUse(port int) bool { return slices.Contains(f.inUse, port) }

// lines returns each command line Run or Start got, the program's
// directory written as DIR.
func (f *fakeRunner) lines(dir string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, cmd := range f.commands {
		out = append(out, strings.ReplaceAll(cmd.String(), dir, "DIR"))
	}
	return out
}

// fakeProcess is a process fakeRunner started.
type fakeProcess struct {
	cmd     local.Command
	once    sync.Once
	done    chan struct{}
	err     error
	stopped bool
}

func (p *fakeProcess) exit(err error) {
	p.once.Do(func() {
		p.err = err
		close(p.done)
	})
}

func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) Err() error            { <-p.done; return p.err }
func (p *fakeProcess) Stop(time.Duration) error {
	p.stopped = true
	p.exit(nil)
	return nil
}

// envValue is name's value in env: the last, as os/exec keeps it.
func envValue(env []string, name string) string {
	value := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			value = v
		}
	}
	return value
}

// fixture is a resolved local environment with its program rendered, a
// model written for each hosted DB schema, an entrypoint module directory
// per server, and a secrets file.
type fixture struct {
	env     *ir.ResolvedEnvironment
	req     registry.ProvisionRequest
	runner  *fakeRunner
	prov    *local.Provisioner
	out     *bytes.Buffer
	pgPort  int
	apiPort int
	ordPort int
}

// ledgerModel is the model the runner's compiler vectors start from,
// renamed to the service given: a DB schema with one table.
func ledgerModel(t *testing.T, service string) *sqlmigrate.Model {
	t.Helper()
	return vectorModel(t, "01-create.plan.json", service)
}

// vectorModel is the model a plan of the runner's `columns` compiler
// vectors ends at, renamed to the service given.
func vectorModel(t *testing.T, plan, service string) *sqlmigrate.Model {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "runtime", "migrate", "testdata", "plans", "columns", plan))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		ToModel sqlmigrate.Model `json:"toModel"`
	}
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	vector.ToModel.Service = service
	return &vector.ToModel
}

func newFixture(t *testing.T, envName string) *fixture {
	t.Helper()
	return newFixtureOf(t, shop(), envName)
}

// newFixtureOf is newFixture of the stack s, whose TypeScript servers, if
// any, the output root's Bun workspace holds.
func newFixtureOf(t *testing.T, s *ir.Stack, envName string) *fixture {
	t.Helper()
	resolved := resolve(t, assemble(t), s, stacktest.AcmeShop(), envName)
	data, err := stack.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	env, err := stack.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "program")
	outputRoot := filepath.Join(root, "dist")
	for _, d := range env.Deployables {
		if d.Kind.HasImage() {
			if err := os.MkdirAll(filepath.Join(outputRoot, local.ModulePath(env.Stack, d.Name)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(outputRoot, "package.json"), []byte(`{"name": "@acme/workspace", "private": true, "workspaces": ["server/*/*"]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model, err := ledgerModel(t, "shop-db").CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, local.ModelsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, local.ModelsDir, "shop-db.json"), model, 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir, err := local.EnsureStateDir(filepath.Join(root, "schemas"), env.Stack, env.Environment)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.WriteSecret(filepath.Join(stateDir, local.SecretsFile), "PaymentsSecrets.STRIPE_KEY", "sk_test_123"); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	out := new(bytes.Buffer)
	senv := registry.StackEnvironment{Stack: env.Stack, Name: env.Environment, Values: env.Values}
	// The container's published port takes connections once it runs.
	runner.inUse = []int{local.PostgresPort(senv)}
	prov := &local.Provisioner{Runner: runner, Out: out, ReadyTimeout: time.Second, PollInterval: time.Millisecond}
	if err := prov.Render(env, dir); err != nil {
		t.Fatal(err)
	}
	return &fixture{
		env:     env,
		req:     registry.ProvisionRequest{Environment: env, Dir: dir, OutputRoot: outputRoot, Backend: local.StateBackend(stateDir)},
		runner:  runner,
		prov:    prov,
		out:     out,
		pgPort:  local.PostgresPort(senv),
		apiPort: local.ServerPort(senv, *env.Deployable("shop-api")),
		ordPort: local.ServerPort(senv, *env.Deployable("Orders")),
	}
}

// keys applies the environment's key pairs alone, as its infrastructure
// step would.
func (f *fixture) keys(t *testing.T) {
	t.Helper()
	var ids []string
	for _, res := range f.env.Resources.Resources {
		if res.Type == local.TypeKeyPair {
			ids = append(ids, res.ID)
		}
	}
	if err := f.prov.Apply(context.Background(), f.req, ir.DeployStep{Step: ir.StepInfrastructure, Resources: ids}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) applyAll(t *testing.T) error {
	t.Helper()
	for _, step := range f.env.DeployOrder {
		if err := f.prov.Apply(context.Background(), f.req, *step); err != nil {
			return err
		}
	}
	return nil
}

const (
	inspectPrefix = "/bin/docker container inspect --format"
	migrateStatus = "/bin/superschematic-migrate status --service shop-db --model"
)

// TestApplyFromNothing applies Dev to a machine with no container: it
// creates the container, waits for Postgres, creates the database, plans
// the migration from an empty database and applies both phases, builds
// each server, starts it with its resolved environment, callees first, and
// waits until each is ready. Destroy stops the callers first, then the
// container.
func TestApplyFromNothing(t *testing.T) {
	f := newFixture(t, "Dev")
	f.runner.notReady = 1
	f.runner.rules = []rule{
		{prefix: inspectPrefix, stderr: "Error response from daemon: No such container: superschematic-shop-stack-dev-postgres"},
		{prefix: "/bin/docker exec superschematic-shop-stack-dev-postgres pg_isready", times: 1, stderr: "no response"},
		{prefix: migrateStatus, stderr: "superschematic-migrate: service shop-db has no applied model"},
		{prefix: "/bin/superschematic-migrate apply", out: "applied 2 steps\n"},
	}
	if err := f.applyAll(t); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}

	dir := f.req.Dir
	url := local.DatabaseURL(f.pgPort, "shop_db")
	want := []string{
		inspectPrefix + " {{.State.Running}}|{{.Config.Image}}|{{json .HostConfig.PortBindings}} superschematic-shop-stack-dev-postgres",
		fmt.Sprintf("/bin/docker run --detach --name superschematic-shop-stack-dev-postgres --label superschematic.environment=Dev --label superschematic.stack=shop-stack --env POSTGRES_HOST_AUTH_METHOD=trust --publish 127.0.0.1:%d:5432 postgres:16-alpine", f.pgPort),
		"/bin/docker exec superschematic-shop-stack-dev-postgres pg_isready --host 127.0.0.1 --port 5432 --username postgres --quiet",
		"/bin/docker exec superschematic-shop-stack-dev-postgres pg_isready --host 127.0.0.1 --port 5432 --username postgres --quiet",
		"/bin/docker exec superschematic-shop-stack-dev-postgres psql --host 127.0.0.1 --port 5432 --username postgres --dbname postgres --tuples-only --no-align --command SELECT 1 FROM pg_database WHERE datname = 'shop_db'",
		"/bin/docker exec superschematic-shop-stack-dev-postgres createdb --host 127.0.0.1 --port 5432 --username postgres shop_db",
		migrateStatus + " --database-url " + url,
		"/bin/superschematic-migrate apply --plan DIR/migrations/shop-db.plan.json --phase all --database-url " + url,
		"/bin/go build -o DIR/bin/shop-api .",
		"DIR/bin/shop-api",
		// shop-orders' worker builds and starts with Orders, in the
		// rollout after its callee, a process with no port (D53).
		"/bin/go build -o DIR/bin/orders .",
		"/bin/go build -o DIR/bin/shop-orders-fulfil-orders .",
		"DIR/bin/orders",
		"DIR/bin/shop-orders-fulfil-orders",
		// shop-orders' job builds in the rollout after its callee, and
		// runs only on its schedule (D52).
		"/bin/go build -o DIR/bin/shop-orders-ship-orders .",
	}
	if got := f.runner.lines(dir); !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The plan the runner applied starts from an empty database.
	data, err := os.ReadFile(filepath.Join(dir, local.MigrationsDir, "shop-db.plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan sqlmigrate.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Service != "shop-db" || plan.From != "" || len(plan.Steps) == 0 {
		t.Errorf("plan: service %s from %q with %d steps, want shop-db from an empty database", plan.Service, plan.From, len(plan.Steps))
	}

	// Each server builds in its module and runs with its resolved
	// environment: literals, the secret's value, each derived variable and
	// PORT.
	for _, cmd := range f.runner.commands {
		if strings.HasSuffix(cmd.Path, "/go") {
			if !strings.HasPrefix(cmd.Dir, f.req.OutputRoot+string(filepath.Separator)+"server") || envValue(cmd.Env, "GOWORK") != "off" ||
				!strings.HasSuffix(envValue(cmd.Env, "GOFLAGS"), "-mod=mod") {
				t.Errorf("go build in %s with GOWORK=%q GOFLAGS=%q, want the module under the output root with GOWORK=off and -mod=mod",
					cmd.Dir, envValue(cmd.Env, "GOWORK"), envValue(cmd.Env, "GOFLAGS"))
			}
		}
	}
	if len(f.runner.started) != 3 {
		t.Fatalf("started %d processes, want 3", len(f.runner.started))
	}
	api, orders, worker := f.runner.started[0].cmd.Env, f.runner.started[1].cmd.Env, f.runner.started[2].cmd.Env
	// The worker takes its API's config and its own edges, and the
	// concurrency its platform sets, and listens on no port (D53).
	for name, want := range map[string]string{
		"PORT":                 "",
		"WORKER_CONCURRENCY":   "4",
		"FULFILLMENT_REGION":   "us",
		"STRIPE_KEY":           "sk_test_123",
		"SHOP_DB_DATABASE_URL": url,
		"SHOP_API_SERVICE_URL": local.ServerURL(f.apiPort),
	} {
		if got := envValue(worker, name); got != want {
			t.Errorf("the worker's %s = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"PORT":                 fmt.Sprint(f.apiPort),
		"LOG_LEVEL":            "info",
		"STRIPE_KEY":           "sk_test_123",
		"SHOP_DB_DATABASE_URL": url,
	} {
		if got := envValue(api, name); got != want {
			t.Errorf("shop-api %s = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"PORT":                 fmt.Sprint(f.ordPort),
		"FULFILLMENT_REGION":   "us",
		"MAX_LINE_ITEMS":       "50",
		"STRIPE_KEY":           "sk_test_123",
		"SHOP_DB_DATABASE_URL": url,
		"SHOP_API_SERVICE_URL": local.ServerURL(f.apiPort),
	} {
		if got := envValue(orders, name); got != want {
			t.Errorf("Orders %s = %q, want %q", name, got, want)
		}
	}
	if envValue(api, "SHOP_API_SERVICE_URL") != "" {
		t.Error("shop-api got Orders' SHOP_API_SERVICE_URL")
	}
	checkServiceCredential(t, f, api, orders)

	// Each process's lines reach the output prefixed with its name, and
	// the migration runner's with the schema it migrates.
	for _, want := range []string{
		fmt.Sprintf("[shop-api] listening on %d\n", f.apiPort),
		fmt.Sprintf("[Orders] listening on %d\n", f.ordPort),
		"[migrate shop-db] applied 2 steps\n",
		"migrate contract: shop-db ran its contract steps with its expand steps",
		"Orders is ready at " + local.ServerURL(f.ordPort),
		"start worker shop-orders-fulfil-orders",
		"worker shop-orders-fulfil-orders is running",
	} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, f.out)
		}
	}

	outputs, err := f.prov.Outputs(context.Background(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	if got := outputs["shop-api.process"]["url"]; got != local.ServerURL(f.apiPort) {
		t.Errorf("shop-api url output = %v", got)
	}

	// Destroy stops Orders, then shop-api, then the container, which keeps
	// its data; the unfinished stderr lines are flushed.
	f.runner.commands = nil
	f.runner.rules = []rule{{prefix: inspectPrefix, out: fmt.Sprintf(`true|postgres:16-alpine|{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"%d"}]}`, f.pgPort)}}
	if err := f.prov.Destroy(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	if !f.runner.started[0].stopped || !f.runner.started[1].stopped || !f.runner.started[2].stopped {
		t.Error("Destroy left a server or the worker running")
	}
	if _, ok := outputs["shop-orders-fulfil-orders.process"]; ok {
		t.Error("the worker's process has outputs; it has no port and no URL")
	}
	if got := f.runner.lines(dir); len(got) != 2 || got[1] != "/bin/docker stop superschematic-shop-stack-dev-postgres" {
		t.Errorf("Destroy ran %v", got)
	}
	out := f.out.String()
	if i, j := strings.Index(out, "stop Orders"), strings.Index(out, "stop shop-api"); i < 0 || j < i {
		t.Errorf("Destroy did not stop Orders before shop-api:\n%s", out)
	}
	if !strings.Contains(out, "[shop-api] a line with no end\n") {
		t.Errorf("an unfinished line was not flushed:\n%s", out)
	}
}

// TestApplyRunsATypeScriptServer applies Dev of the shop with its
// TypeScript storefront: the Go servers build as before, and the
// storefront's wave installs the output root's Bun workspace once, then
// runs its entrypoint's main.ts on Bun in its module, with its resolved
// environment. Applying the wave again restarts the storefront without a
// second install.
func TestApplyRunsATypeScriptServer(t *testing.T) {
	f := newFixtureOf(t, storefront(), "Dev")
	f.runner.rules = []rule{
		{prefix: inspectPrefix, stderr: "Error response from daemon: No such container: superschematic-storefront-stack-dev-postgres"},
		{prefix: migrateStatus, stderr: "superschematic-migrate: service shop-db has no applied model"},
	}
	if err := f.applyAll(t); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	senv := registry.StackEnvironment{Stack: f.env.Stack, Name: f.env.Environment, Values: f.env.Values}
	port := local.ServerPort(senv, *f.env.Deployable("shop-storefront"))
	module := filepath.Join(f.req.OutputRoot, local.ModulePath(f.env.Stack, "shop-storefront"))
	var installs []local.Command
	var bun *fakeProcess
	for _, cmd := range f.runner.commands {
		if cmd.Path == "/bin/bun" && len(cmd.Args) > 0 && cmd.Args[0] == "install" {
			installs = append(installs, cmd)
		}
	}
	for _, p := range f.runner.started {
		if p.cmd.Path == "/bin/bun" {
			bun = p
		}
	}
	if len(installs) != 1 || installs[0].Dir != f.req.OutputRoot || strings.Join(installs[0].Args, " ") != "install" {
		t.Errorf("bun install ran %+v, want once, in the output root", installs)
	}
	if bun == nil || strings.Join(bun.cmd.Args, " ") != local.TypeScriptEntrypoint || bun.cmd.Dir != module {
		t.Fatalf("the storefront started as %+v, want bun main.ts in %s", bun, module)
	}
	if got := envValue(bun.cmd.Env, "PORT"); got != fmt.Sprint(port) {
		t.Errorf("the storefront's PORT = %q, want %d", got, port)
	}
	if envValue(bun.cmd.Env, "SHOP_DB_DATABASE_URL") != "" {
		t.Error("the storefront got another server's database")
	}
	for _, line := range f.runner.lines(f.req.Dir) {
		if strings.Contains(line, "go build") && strings.Contains(line, "storefront") {
			t.Errorf("the storefront was built with go: %s", line)
		}
	}
	for _, want := range []string{
		"install the TypeScript workspace: bun install in " + f.req.OutputRoot,
		fmt.Sprintf("[shop-storefront] listening on %d\n", port),
		"shop-storefront is ready at " + local.ServerURL(port),
	} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, f.out)
		}
	}

	for _, step := range f.env.DeployOrder {
		if step.Step == ir.StepRollout && slices.Contains(step.Resources, "shop-storefront.process") {
			if err := f.prov.Apply(context.Background(), f.req, *step); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !bun.stopped {
		t.Error("applying the storefront's wave again did not restart it")
	}
	count := 0
	for _, cmd := range f.runner.commands {
		if cmd.Path == "/bin/bun" && len(cmd.Args) > 0 && cmd.Args[0] == "install" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("bun install ran %d times, want once", count)
	}
	f.runner.rules = []rule{{prefix: inspectPrefix, stderr: "Error: No such object: superschematic-storefront-stack-dev-postgres"}}
	if err := f.prov.Destroy(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
}

// TestApplyRefusesATypeScriptServerItCannotRun: without bun on PATH, or
// with no Bun workspace at the output root, the storefront's wave fails
// and says why.
func TestApplyRefusesATypeScriptServerItCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *fixture)
		want  string
	}{
		{
			name:  "no bun",
			setup: func(t *testing.T, f *fixture) { f.runner.missing = []string{"bun"} },
			want:  "bun is not on PATH; the local target runs each TypeScript server on Bun",
		},
		{
			name: "no workspace",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Remove(filepath.Join(f.req.OutputRoot, "package.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "no Bun workspace at",
		},
		{
			name: "an install that fails",
			setup: func(t *testing.T, f *fixture) {
				f.runner.rules = []rule{{prefix: "/bin/bun install", stderr: "error: lockfile had changes"}}
			},
			want: "install the TypeScript workspace at",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureOf(t, storefront(), "Dev")
			f.keys(t)
			tc.setup(t, f)
			var err error
			for _, step := range f.env.DeployOrder {
				if step.Step == ir.StepRollout && slices.Contains(step.Resources, "shop-storefront.process") {
					err = f.prov.Apply(context.Background(), f.req, *step)
				}
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
			_ = f.prov.Destroy(context.Background(), f.req)
		})
	}
}

// TestApplyAgain applies Dev to a machine where the container runs and the
// database holds the model already: nothing is created, and the migration
// is up to date.
func TestApplyAgain(t *testing.T) {
	f := newFixture(t, "Dev")
	model, err := ledgerModel(t, "shop-db").CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	f.runner.rules = []rule{
		{prefix: inspectPrefix, out: fmt.Sprintf(`true|postgres:16-alpine|{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"%d"}]}`+"\n", f.pgPort)},
		{prefix: "/bin/docker exec superschematic-shop-stack-dev-postgres psql", out: "1\n"},
		{prefix: migrateStatus, out: string(model) + "\n"},
	}
	if err := f.applyAll(t); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	for _, line := range f.runner.lines(f.req.Dir) {
		for _, refused := range []string{"docker run", "docker start", "createdb", "migrate apply"} {
			if strings.Contains(line, refused) {
				t.Errorf("ran %s", line)
			}
		}
	}
	if !strings.Contains(f.out.String(), "migrate shop-db: up to date") {
		t.Errorf("output lacks the up-to-date migration:\n%s", f.out)
	}

	// Applying a wave again restarts its server.
	rollout := f.env.DeployOrder[2]
	if err := f.prov.Apply(context.Background(), f.req, *rollout); err != nil {
		t.Fatal(err)
	}
	if len(f.runner.started) != 4 || !f.runner.started[0].stopped {
		t.Errorf("applying wave 1 again did not restart shop-api")
	}
	if err := f.prov.Purge(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	if lines := f.runner.lines(f.req.Dir); lines[len(lines)-1] != "/bin/docker rm --force --volumes superschematic-shop-stack-dev-postgres" {
		t.Errorf("Purge ran %v", lines[len(lines)-1])
	}
}

// TestPlan: on a machine with nothing, Plan creates the container and the
// database, migrates, and starts each server.
func TestPlan(t *testing.T) {
	f := newFixture(t, "Dev")
	f.runner.rules = []rule{{prefix: inspectPrefix, stderr: "Error: No such object: superschematic-shop-stack-dev-postgres"}}
	changes, err := f.prov.Plan(context.Background(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		got = append(got, c.Action+" "+c.Resource)
	}
	want := []string{
		"create postgres.container",
		"create shop-db.database.shop-db",
		"migrate shop-db.database.shop-db",
		"create Orders.calls.shop-api.key",
		"create shop-orders-fulfil-orders.calls.shop-api.key",
		"create shop-orders-ship-orders.calls.shop-api.key",
		"start shop-api.process",
		"start Orders.process",
		"start shop-orders-fulfil-orders.process",
		"build shop-orders-ship-orders.job",
	}
	if !slices.Equal(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

// TestApplyRefusals: what stops an apply, with the reason.
func TestApplyRefusals(t *testing.T) {
	steps := func(f *fixture, kinds ...ir.DeployStepKind) error {
		for _, step := range f.env.DeployOrder {
			if slices.Contains(kinds, step.Step) {
				if err := f.prov.Apply(context.Background(), f.req, *step); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *fixture)
		kinds []ir.DeployStepKind
		want  string
		// wantf, when set, gives want from the fixture.
		wantf func(f *fixture) string
	}{
		{
			name:  "no rendered program",
			setup: func(t *testing.T, f *fixture) { f.req.Dir = t.TempDir() },
			kinds: []ir.DeployStepKind{ir.StepInfrastructure},
			want:  "render environment Dev first",
		},
		{
			name: "a program of another environment",
			setup: func(t *testing.T, f *fixture) {
				f.env.Deployable("shop-api").Bindings[0].Value = "debug"
				f.env.Resources.Resource("shop-api.process").Properties["env"].([]any)[0].(map[string]any)["value"] = "debug"
			},
			kinds: []ir.DeployStepKind{ir.StepRollout},
			want:  "is not the program of environment Dev as it resolves now; render it again",
		},
		{
			name: "a container of another image",
			setup: func(t *testing.T, f *fixture) {
				f.runner.rules = []rule{{prefix: inspectPrefix, out: fmt.Sprintf(`true|postgres:15|{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"%d"}]}`, f.pgPort)}}
			},
			kinds: []ir.DeployStepKind{ir.StepInfrastructure},
			want:  "exists, but it runs postgres:15, not postgres:16-alpine",
		},
		{
			name:  "no docker",
			setup: func(t *testing.T, f *fixture) { f.runner.missing = []string{"docker"} },
			kinds: []ir.DeployStepKind{ir.StepInfrastructure},
			want:  "docker is not on PATH",
		},
		{
			name: "no migration runner",
			setup: func(t *testing.T, f *fixture) {
				t.Setenv(local.MigrateEnv, "")
				f.runner.missing = []string{local.MigrateBinary}
			},
			kinds: []ir.DeployStepKind{ir.StepMigrate},
			want:  "superschematic-migrate is not on PATH, and SUPERSCHEMATIC_MIGRATE names no runner",
		},
		{
			name: "no model",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Remove(filepath.Join(f.req.Dir, local.ModelsDir, "shop-db.json")); err != nil {
					t.Fatal(err)
				}
			},
			kinds: []ir.DeployStepKind{ir.StepMigrate},
			want:  "migrate shop-db: no model to migrate to",
		},
		{
			name: "a missing secret",
			setup: func(t *testing.T, f *fixture) {
				dir, _ := strings.CutPrefix(f.req.Backend.URL, "file://")
				if err := os.Remove(filepath.Join(dir, local.SecretsFile)); err != nil {
					t.Fatal(err)
				}
			},
			kinds: []ir.DeployStepKind{ir.StepRollout},
			want:  "has no value for secret PaymentsSecrets.STRIPE_KEY: set each in",
		},
		{
			name: "no entrypoint module",
			setup: func(t *testing.T, f *fixture) {
				if err := os.RemoveAll(filepath.Join(f.req.OutputRoot, "server")); err != nil {
					t.Fatal(err)
				}
			},
			kinds: []ir.DeployStepKind{ir.StepRollout},
			want:  "server shop-api: no entrypoint module at",
		},
		{
			name:  "a port in use",
			setup: func(t *testing.T, f *fixture) { f.runner.inUse = append(f.runner.inUse, f.apiPort) },
			kinds: []ir.DeployStepKind{ir.StepRollout},
			wantf: func(f *fixture) string { return fmt.Sprintf("server shop-api: port %d is in use", f.apiPort) },
		},
		{
			name:  "a server that exits",
			setup: func(t *testing.T, f *fixture) { f.runner.exitOnStart = true },
			kinds: []ir.DeployStepKind{ir.StepRollout},
			want:  "server shop-api exited before it was ready: exit status 2",
		},
		{
			name:  "a server that never gets ready",
			setup: func(t *testing.T, f *fixture) { f.runner.notReady = 1 << 30 },
			kinds: []ir.DeployStepKind{ir.StepRollout},
			want:  "server shop-api is not ready at http://127.0.0.1:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "Dev")
			if slices.Contains(tc.kinds, ir.StepRollout) {
				f.keys(t)
			}
			tc.setup(t, f)
			want := tc.want
			if tc.wantf != nil {
				want = tc.wantf(f)
			}
			err := steps(f, tc.kinds...)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("got %v, want an error containing %q", err, want)
			}
			_ = f.prov.Destroy(context.Background(), f.req)
		})
	}
}

// TestWaitReportsAnExit: Wait returns nil once its context is done, and an
// error that names the server once a server exits.
func TestWaitReportsAnExit(t *testing.T) {
	f := newFixture(t, "Dev")
	f.keys(t)
	for _, step := range f.env.DeployOrder {
		if step.Step == ir.StepRollout {
			if err := f.prov.Apply(context.Background(), f.req, *step); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.prov.Wait(ctx, f.req); err != nil {
		t.Errorf("Wait after cancel = %v, want nil", err)
	}
	f.runner.started[1].exit(errors.New("exit status 1"))
	if err := f.prov.Wait(context.Background(), f.req); err == nil || !strings.Contains(err.Error(), "server Orders exited: exit status 1") {
		t.Errorf("Wait = %v, want Orders' exit", err)
	}
	f.runner.rules = []rule{{prefix: inspectPrefix, stderr: "Error: No such object: superschematic-shop-stack-dev-postgres"}}
	if err := f.prov.Destroy(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
}

// TestSecretsFile: a secrets file round-trips values that need quoting,
// keeps the others when one is set, and its directory ignores itself.
func TestSecretsFile(t *testing.T) {
	root := t.TempDir()
	dir, err := local.EnsureStateDir(root, "shop-stack", "Dev")
	if err != nil {
		t.Fatal(err)
	}
	ignore, err := os.ReadFile(filepath.Join(root, local.StateRoot, ".gitignore"))
	if err != nil || !strings.Contains(string(ignore), "\n*\n") {
		t.Errorf(".gitignore = %q, %v; want one that ignores everything", ignore, err)
	}
	path := filepath.Join(dir, local.SecretsFile)
	values := map[string]string{
		"PaymentsSecrets.STRIPE_KEY": "sk_test_123",
		"Keys.PEM":                   "-----BEGIN KEY-----\nabc\n-----END KEY-----",
		"Keys.HASH":                  `a#b "c"`,
	}
	for id, value := range values {
		if err := local.WriteSecret(path, id, value); err != nil {
			t.Fatal(err)
		}
	}
	got, err := local.ReadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range values {
		if got[id] != want {
			t.Errorf("%s = %q, want %q", id, got[id], want)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("secrets file mode = %v, want 0600", info.Mode().Perm())
	}
	if err := local.WriteSecret(path, "not an id", "x"); err == nil {
		t.Error("WriteSecret took a malformed ID")
	}
	if err := os.WriteFile(path, []byte("STRIPE_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := local.ReadSecrets(path); err == nil || !strings.Contains(err.Error(), ":1: want <Type>.<FIELD>=<value>") {
		t.Errorf("ReadSecrets of a bare field = %v", err)
	}
}

// checkServiceCredential checks the edge from Orders to shop-api: Orders
// signs with the private key the provisioner generated into the state
// directory, readable by its owner alone, whose public half is the key
// pair's output, and neither environment.json nor the program holds it.
// shop-api gets no key yet: its service-auth field is not defined.
func checkServiceCredential(t *testing.T, f *fixture, api, orders []string) {
	t.Helper()
	for name, want := range map[string]string{
		"SHOP_API_SERVICE_CREDENTIAL_SOURCE":   "signed-token",
		"SHOP_API_SERVICE_CREDENTIAL_ISSUER":   "Orders",
		"SHOP_API_SERVICE_CREDENTIAL_AUDIENCE": "shop-api",
	} {
		if got := envValue(orders, name); got != want {
			t.Errorf("Orders %s = %q, want %q", name, got, want)
		}
	}
	var private local.JWK
	if err := json.Unmarshal([]byte(envValue(orders, "SHOP_API_SERVICE_CREDENTIAL_KEY")), &private); err != nil {
		t.Fatalf("Orders' key: %v", err)
	}
	if private.D == "" || private.Kid != local.Thumbprint(private.X) {
		t.Errorf("Orders' key = %+v, want a private JWK whose kid is its thumbprint", private)
	}
	for _, kv := range api {
		if strings.Contains(kv, private.X) {
			t.Errorf("shop-api got %s", kv)
		}
	}
	stateDir, _ := strings.CutPrefix(f.req.Backend.URL, "file://")
	info, err := os.Stat(filepath.Join(stateDir, local.KeysDir, "Orders.calls.shop-api.key.jwk"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want one readable by its owner alone", info.Mode().Perm())
	}

	outputs, err := f.prov.Outputs(context.Background(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	var public local.JWK
	if err := json.Unmarshal([]byte(fmt.Sprint(outputs["Orders.calls.shop-api.key"]["publicJwk"])), &public); err != nil {
		t.Fatalf("publicJwk output: %v", err)
	}
	if public.D != "" || public.X != private.X || public.Kid != private.Kid {
		t.Fatalf("publicJwk output = %+v, want the public half of Orders' key", public)
	}
	seed, err := base64.RawURLEncoding.DecodeString(private.D)
	if err != nil {
		t.Fatal(err)
	}
	x, err := base64.RawURLEncoding.DecodeString(public.X)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("header.claims")
	if !ed25519.Verify(x, message, ed25519.Sign(ed25519.NewKeyFromSeed(seed), message)) {
		t.Error("the public key does not verify what Orders' private key signs")
	}

	program, err := os.ReadFile(filepath.Join(f.req.Dir, local.ProgramFile))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := stack.Marshal(f.env)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{local.ProgramFile: program, "environment.json": environment} {
		if bytes.Contains(data, []byte(private.D)) {
			t.Errorf("%s holds the private key", name)
		}
	}
}
