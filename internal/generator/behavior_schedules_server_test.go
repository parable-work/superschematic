package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// schedulesCases are the cases schedulesTest runs, by name: the end-to-end
// scenario every language's server and behavior worker of Schedules on a
// table is held to (D104), with the core's words. An agent's record whose
// wake-ups fall due after a delay, at an interval or on a cron line in a
// time zone, at most ten a record, each run handed to the project's
// handler, which tells the agent and reports told, resumed, held or
// missed, a missed one tried again five minutes on; and whose wake-ups
// finish when its Workflow enters done or dropped.
var schedulesCases = []string{
	"setSchedule arms a wake-up after a delay, at an interval and on a cron line in the config's time zone, each a deadline message due at its nextAt",
	"a repeated setSchedule of a live name with the same spec answers unchanged and leaves the row's _version where it was",
	"setSchedule without problems.schedule is 403 forbidden, a delay under minMs is 400 invalid_argument, and an eleventh live wake-up is 409 schedule_limit, each leaving the row's _version where it was",
	"a due wake-up fires on the worker as run 1 and hands the project's handler one run event with its name, run, attempt, kind, spec and times, which reports told",
	"the handler runs once per event: a completed event's message is gone and is not handed to it again",
	"a handler that fails fails its message, which is due again after the queue's backoff, and the next claim hands it to the handler again",
	"an interval overdue by many runs fires once, and its next run is the first of its grid after the fire",
	"a missed run is tried again five minutes on, as attempt 2 of the same run",
	"a report of an older run answers unchanged, and one of a run not had is 409 no_run",
	"pauseSchedule keeps nextAt and deletes its deadline message, and resumeSchedule past nextAt fires at once, once",
	"cancelSchedule finishes a wake-up and frees its place under max",
	"a problem whose status enters done finishes its wake-ups, and a new one is 409 schedules_ended",
	"a raw SQL write of a problem's status to dropped is heard by the worker and finishes its wake-ups",
	"listSchedules reads a problem's wake-ups in name order",
}

// TestSchedulesGoServerOnPostgres builds fixture-schedules-db and
// fixture-schedules-api as build-all does, then writes schedulesTest into
// the generated API module and runs it, after go vet: the generated Go
// server's Schedules and Workflow operations of Problem (D104), called
// through the generated Go SDK with a test authenticator in front of the
// routes; and the three queues of the generated behavior worker
// ProblemWakeUps, its deadlines, changes and events, run in process with
// worker.RunAll, as its main runs them, with the test's own Workers, whose
// ProblemWakeUpsSchedulesRun is the project's handler of the event run. A
// deadline is made due by moving its message's due_at to now, as the
// Timers and Seats tests make one, on the Postgres behaviorServerDatabaseEnv
// names. Each caller's subject is a fresh UUID in its base62 wire form, as
// the identity runtime gives a principal's. It then plans the worker's
// entrypoint over the generated modules and an implementation the API's
// scaffold writes, and vets it: an entrypoint that builds the API's Deps
// and calls the implementation's NewWorkers. The module links the core's
// static archive, which scripts/rules-archive.sh stages; the test skips
// without it, without the database, and in -short mode.
func TestSchedulesGoServerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server and worker of Schedules in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server and worker of Schedules against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	var apiSchema, dbSchema *ir.Schema
	for _, service := range []string{"fixture-schedules-db", "fixture-schedules-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if _, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Paths:          paths,
			Naming:         naming.Default(),
			Clock:          clock,
			LoadDependency: loadDependency,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
		if service == "fixture-schedules-api" {
			apiSchema = schema
		} else {
			dbSchema = schema
		}
	}

	apiDir := APIDir(outputRoot, "fixture-schedules-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-schedules-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_schedules_server_test.go"), []byte(schedulesTest), 0o644); err != nil {
		t.Fatal(err)
	}
	sdkModule := naming.Default().GoSDKModule("fixture-schedules-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-schedules-api"},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		// Environ sets PWD to Dir, which keeps the module's relative
		// replace paths valid under a symlinked temp dir.
		cmd.Env = append(cmd.Environ(), env...)
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated API tests:\n%s", out)
	for _, name := range schedulesCases {
		if want := "--- PASS: TestSchedules/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}

	vetEventWorkerEntrypoint(t, outputRoot, paths, apiSchema, dbSchema, env, "schedules-stack", "ProblemWakeUps")
}

// vetEventWorkerEntrypoint plans the entrypoint of api's behavior worker of
// the class name in stack, whose table's behaviors emit events (D104), over
// the modules the build wrote under outputRoot and the implementation the
// API's scaffold writes, with its module, as a stack's build writes them
// when the implementation is missing, and vets it: the entrypoint builds
// the API's Deps and calls the implementation's NewWorkers, whose methods
// the worker's events go to.
func vetEventWorkerEntrypoint(t *testing.T, outputRoot string, paths naming.LocalPaths, api, db *ir.Schema, env []string, stack, name string) {
	t.Helper()
	n := naming.Default()
	output, err := apigen.Generate(api, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   api.Name,
		ModulePath:   n.GoAPIModule(api.Name),
		TypesModule:  n.GoTypesModule(api.Name),
		Dependencies: map[string]*ir.Schema{db.Name: db},
		Naming:       n,
	})
	if err != nil {
		t.Fatal(err)
	}
	output.EnvConfig = &envgen.ConfigOutput{
		SchemaName: api.Name,
		EnvConfig:  true,
		Derived: []envgen.DerivedField{{
			Key:    strings.ToUpper(strings.ReplaceAll(db.Name, "-", "_")) + "_DATABASE",
			GoName: codegen.ToPascalCase(db.Name) + "Database",
			Kind:   ir.EdgeSQL, Service: db.Name,
		}},
	}
	implDir := filepath.Join(t.TempDir(), "impl")
	impl := "example.com/services/" + api.Name + "/go"
	if wrote, err := apigen.WriteImplementationScaffold(output, implDir); err != nil || !wrote {
		t.Fatalf("write the implementation's scaffold: %t, %v", wrote, err)
	}
	server := api.Name + "-" + codegen.ToKebabCase(name)
	dir := servergen.ServerDir(outputRoot, stack, server)
	modules := []servergen.Module{
		{Path: output.ModulePath, Dir: APIDir(outputRoot, api.Name), Direct: true},
		{Path: output.TypesModule, Dir: TypesDir(outputRoot, "go", api.Name)},
		{Path: n.GoTypesModule(db.Name), Dir: TypesDir(outputRoot, "go", db.Name)},
		{Path: n.GoORMModule(db.Name), Dir: ORMDir(outputRoot, db.Name), Direct: true},
		{Path: n.HTTPRuntimeGoModule, Dir: paths.HTTPRuntimeGo, Direct: true},
		{Path: n.ScalarGoModule, Dir: paths.ScalarGo},
		{Path: n.SchemaRuntimeGoModule, Dir: paths.SchemaRuntimeGo},
		{Path: n.SchemaIRGoModule, Dir: paths.SchemaIR},
		{Path: n.RulesGoModule, Dir: paths.RulesGo},
		{Path: impl, Dir: implDir, Direct: true},
	}
	implementation := servergen.Implementation{Dir: implDir, Import: impl, Module: impl, ModuleDir: implDir}
	if err := servergen.WriteImplementationModule(n, api.Name, implementation, output.ModulePath, modules[:len(modules)-1]); err != nil {
		t.Fatal(err)
	}
	in := servergen.Input{
		Stack:          stack,
		Server:         server,
		Dir:            dir,
		APIs:           []servergen.APIInput{{Output: output, Implementation: implementation}},
		Worker:         &servergen.WorkerInput{Name: name, GraceSeconds: 8},
		Modules:        modules,
		Naming:         n,
		RepositoryRoot: testpaths.RepoRoot(t),
		ScalarGo:       paths.ScalarGo,
		RulesGo:        paths.RulesGo,
	}
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatalf("plan the worker: %v", err)
	}
	if !s.EventWorker() {
		t.Fatalf("the plan of %s is no behavior worker that hands events on: %+v", name, s.Worker)
	}
	if err := servergen.Write(s, dir); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(dir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), ".NewWorkers(") {
		t.Errorf("the worker's main calls no NewWorkers:\n%s", main)
	}
	for _, args := range [][]string{{"vet", "-mod=mod", "."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the worker's entrypoint: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// schedulesTest runs in the generated module of fixture-schedules-api. It
// serves the generated routes over the ORM of fixture-schedules-db, on a
// schema of its own in the Postgres behaviorServerDatabaseEnv names, with a
// test authenticator in front of them, calls them through the generated Go
// SDK, runs the generated worker's queues in process with the test's own
// Workers, and reads the tables and the queues by SQL.
const schedulesTest = `package fixtureschedulesapi_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-schedules-api"
	orm "example.com/schemas/orm/fixture-schedules-db"
	sdk "example.com/schemas/sdk/go/fixture-schedules-api"
	types "example.com/schemas/types/go/fixture-schedules-api"
	dbtypes "example.com/schemas/types/go/fixture-schedules-db"
)

// authenticate is the test's authenticator, in place of the auth an API
// with an authDb wires: Authorization: Bearer <subject>[:<permission>[,...]]
// puts that subject on the request, holding those permissions, as an
// identity middleware puts its principal and roles; no header, no caller.
func authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
			subject, held, _ := strings.Cut(token, ":")
			var permissions []string
			if held != "" {
				permissions = strings.Split(held, ",")
			}
			ctx := session.ContextWithPrincipalID(r.Context(), subject)
			ctx = session.ContextWithRoles(ctx, []session.Role{{Name: "test", Permissions: permissions}})
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// openDatabase is the ORM of fixture-schedules-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends, and
// its pool, which the test reads the tables through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server and worker of Schedules against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("schedules_go_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := orm.ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db, pool
}

// subject is a fresh UUID in its base62 wire form, as the identity runtime
// gives a principal's subject.
func subject(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	id, err := dbtypes.ParseIdentityUUID(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// sortedJSON is a value's JSON with its keys sorted, as a refusal's
// details and a result are compared.
func sortedJSON(t *testing.T, value any) string {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	out, err = json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// as is a value through its JSON as another type: the SDK's result as
// the shape a case reads, whatever the generated type names it holds.
func as[T any](t *testing.T, value any) T {
	t.Helper()
	var out T
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	return out
}

// wantAPIRefused holds a call the server refused whole: its status, its
// code, its message, and its details, whose JSON is details with its keys
// sorted ("null" for none).
func wantAPIRefused(t *testing.T, what string, err error, status int, code, message, details string) {
	t.Helper()
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: %v, want the API's refusal", what, err)
	}
	got := "null"
	if apiErr.Details != nil {
		got = sortedJSON(t, apiErr.Details)
	}
	if apiErr.StatusCode != status || apiErr.Code != code || apiErr.Message != message || got != details {
		t.Fatalf("%s refused with %d %s %q, details %s\nwant %d %s %q, details %s", what, apiErr.StatusCode, apiErr.Code, apiErr.Message, got, status, code, message, details)
	}
}

// view is a schedule as the operations answer it and listSchedules lists
// it, its absent fields nil.
type view struct {
	Name        string  ` + "`json:\"name\"`" + `
	Kind        string  ` + "`json:\"kind\"`" + `
	Spec        string  ` + "`json:\"spec\"`" + `
	TimeZone    *string ` + "`json:\"timeZone\"`" + `
	State       string  ` + "`json:\"state\"`" + `
	SetAt       int64   ` + "`json:\"setAt\"`" + `
	NextAt      *int64  ` + "`json:\"nextAt\"`" + `
	RetryAt     *int64  ` + "`json:\"retryAt\"`" + `
	Runs        int64   ` + "`json:\"runs\"`" + `
	Attempt     *int64  ` + "`json:\"attempt\"`" + `
	LastAt      *int64  ` + "`json:\"lastAt\"`" + `
	LastOutcome *string ` + "`json:\"lastOutcome\"`" + `
	FinishedAt  *int64  ` + "`json:\"finishedAt\"`" + `
	EndedBy     *string ` + "`json:\"endedBy\"`" + `
	Replaced    *bool   ` + "`json:\"replaced\"`" + `
	Unchanged   *bool   ` + "`json:\"unchanged\"`" + `
}

// received is an event the project's handler got: the problem's key and
// what the event's payload says.
type received struct {
	key     string
	payload types.ProblemSchedulesRunPayload
	at      int64
}

// wakeUps is the project's Workers in the test (D104): its handler of
// Problem's Schedules' event run records each event it gets; while the
// test has set it to fail a problem's next events it returns the error
// "the agent was not told" for each; otherwise it reports the run through
// the event's transaction as the worker, with the outcome the test set for
// the schedule's name, else told, and completes an event whose problem is
// gone (not_found).
type wakeUps struct {
	mu       sync.Mutex
	got      []received
	failing  map[string]int
	outcomes map[string]orm.ProblemSchedulesOutcome
}

var _ api.Workers = (*wakeUps)(nil)

func (w *wakeUps) ProblemWakeUpsSchedulesRun(ctx context.Context, tx orm.TxInterface, event types.ProblemSchedulesRunEvent) error {
	key := event.Key.String()
	w.mu.Lock()
	w.got = append(w.got, received{key: key, payload: event.Payload, at: int64(event.At)})
	if w.failing[key] > 0 {
		w.failing[key]--
		w.mu.Unlock()
		return errors.New("the agent was not told")
	}
	outcome, ok := w.outcomes[event.Payload.Name]
	if !ok {
		outcome = "told"
	}
	w.mu.Unlock()
	id, err := dbtypes.ParseIdentityUUID(key)
	if err != nil {
		return err
	}
	attempt := int64(event.Payload.Attempt)
	_, err = tx.GetProblemRepository().Schedules().ReportRun(ctx, id, orm.ProblemSchedulesReportRunParams{Name: event.Payload.Name, Run: int64(event.Payload.Run), Attempt: &attempt, Outcome: outcome})
	if rules.IsNotFound(err) {
		return nil
	}
	return err
}

// of are the events the handler got of a problem, in the order it got them.
func (w *wakeUps) of(key string) []received {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []received
	for _, r := range w.got {
		if r.key == key {
			out = append(out, r)
		}
	}
	return out
}

// fail sets the handler to fail a problem's next n events.
func (w *wakeUps) fail(key string, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failing[key] = n
}

// report sets the outcome the handler reports for a schedule's runs.
func (w *wakeUps) report(name string, outcome orm.ProblemSchedulesOutcome) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.outcomes[name] = outcome
}

func TestSchedules(t *testing.T) {
	ctx := context.Background()
	db, pool := openDatabase(t)
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(authenticate)
	if err := api.RegisterRoutes(router, api.Config{DB: db, Logger: zap.NewNop()}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := func(token string) *sdk.FixtureSchedulesApiSDK {
		t.Helper()
		c, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		c.SetToken(token)
		return c
	}
	// agent sets and changes wake-ups; guest holds no permission.
	agent, guest := subject(t), subject(t)
	asAgent, asGuest := client(agent+":problems.schedule"), client(guest)
	workers := &wakeUps{failing: map[string]int{}, outcomes: map[string]orm.ProblemSchedulesOutcome{}}
	now := func() int64 { return time.Now().UnixMilli() }

	// problem creates a problem through the repository and returns its key
	// in its wire form and in its column's.
	problem := func(t *testing.T) (string, string) {
		t.Helper()
		row, err := db.GetProblemRepository().CreateOne(ctx, &orm.ProblemCreate{Title: "Flaky test"})
		if err != nil {
			t.Fatalf("create a problem: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String()
	}
	version := func(t *testing.T, column string) int64 {
		t.Helper()
		var v int64
		if err := pool.QueryRow(ctx, "SELECT _version FROM problem WHERE id = $1", column).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	unmoved := func(t *testing.T, what, column string, want int64) {
		t.Helper()
		if got := version(t, column); got != want {
			t.Fatalf("the problem is at version %d after %s, want %d: it writes nothing", got, what, want)
		}
	}
	delay := func(ms int64) types.InputField[types.GenericInt64] {
		return types.InputField[types.GenericInt64]{Set: true, Value: types.GenericInt64(ms)}
	}
	text := func(s string) types.InputField[string] {
		return types.InputField[string]{Set: true, Value: s}
	}
	set := func(c *sdk.FixtureSchedulesApiSDK, key string, input types.ProblemSchedulesSetScheduleInput) (view, error) {
		result, err := c.ProblemNamespace.Schedules.SetSchedule(ctx, key, input)
		if err != nil {
			return view{}, err
		}
		return as[view](t, result), nil
	}
	// mustSet sets a schedule as the agent, failing the case on a refusal.
	mustSet := func(t *testing.T, key string, input types.ProblemSchedulesSetScheduleInput) view {
		t.Helper()
		v, err := set(asAgent, key, input)
		if err != nil {
			t.Fatalf("setSchedule %s: %v", input.Name, err)
		}
		return v
	}
	list := func(t *testing.T, key string) []view {
		t.Helper()
		result, err := asAgent.ProblemNamespace.Schedules.ListSchedules(ctx, key, types.ProblemSchedulesListSchedulesInput{})
		if err != nil {
			t.Fatalf("listSchedules: %v", err)
		}
		return as[struct {
			Items []view ` + "`json:\"items\"`" + `
		}](t, result).Items
	}
	named := func(t *testing.T, key, name string) view {
		t.Helper()
		for _, v := range list(t, key) {
			if v.Name == name {
				return v
			}
		}
		t.Fatalf("listSchedules lists no %s", name)
		return view{}
	}
	report := func(c *sdk.FixtureSchedulesApiSDK, key, name string, run int64, outcome types.ProblemSchedulesOutcome) (map[string]any, error) {
		result, err := c.ProblemNamespace.Schedules.ReportRun(ctx, key, types.ProblemSchedulesReportRunInput{Name: name, Run: types.GenericInt64(run), Outcome: outcome})
		if err != nil {
			return nil, err
		}
		return as[map[string]any](t, result), nil
	}
	// armed is the live deadline message of a schedule: its at and due_at
	// in epoch milliseconds; false when there is none.
	armed := func(t *testing.T, column, name string) (int64, int64, bool) {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT at, due_at FROM problem_deadline_queue WHERE row_key = $1 AND use_id = 'Schedules' AND name = $2 AND state IN ('ready', 'claimed')", column, name)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var at int64
		var due time.Time
		found := 0
		for rows.Next() {
			if err := rows.Scan(&at, &due); err != nil {
				t.Fatal(err)
			}
			found++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if found > 1 {
			t.Fatalf("problem_deadline_queue holds %d live messages of %s, want at most one", found, name)
		}
		return at, due.UnixMilli(), found == 1
	}
	// due makes a schedule's live deadline message due now.
	due := func(t *testing.T, column, name string) {
		t.Helper()
		tag, err := pool.Exec(ctx, "UPDATE problem_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = $2 AND state IN ('ready', 'claimed')", column, name)
		if err != nil {
			t.Fatal(err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("problem_deadline_queue holds %d live messages of %s, want one", tag.RowsAffected(), name)
		}
	}
	// events counts a problem's messages of the event queue.
	events := func(t *testing.T, column string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM problem_event_queue WHERE row_key = $1", column).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// drive runs the worker's queues in process, as its main does, with the
	// test's Workers, until done holds or ten seconds pass, polling every
	// 100 ms.
	drive := func(t *testing.T, done func() bool) {
		t.Helper()
		running, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() {
			stopped <- worker.RunAll(running, api.ProblemWakeUpsQueues(db, workers, zap.NewNop()), worker.Options{Concurrency: 2, Idle: 100 * time.Millisecond})
		}()
		deadline := time.Now().Add(10 * time.Second)
		for !done() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		stop()
		if err := <-stopped; err != nil {
			t.Fatalf("the worker: %v", err)
		}
	}
	// settled drives the worker for a second and a half, for what it would
	// do of its own: hand an event on again, say.
	settled := func(t *testing.T) {
		t.Helper()
		start := time.Now()
		drive(t, func() bool { return time.Since(start) > 1500*time.Millisecond })
	}
	// fired drives the worker until the handler got n events of a problem
	// and the problem's event queue is empty: each handed on and completed.
	fired := func(t *testing.T, key, column string, n int) []received {
		t.Helper()
		drive(t, func() bool { return len(workers.of(key)) >= n && events(t, column) == 0 })
		got := workers.of(key)
		if len(got) != n {
			t.Fatalf("the handler got %d events of the problem, want %d: %+v", len(got), n, got)
		}
		if left := events(t, column); left != 0 {
			t.Fatalf("the problem's event queue holds %d messages after its events ran, want none", left)
		}
		return got
	}

	t.Run("setSchedule arms a wake-up after a delay, at an interval and on a cron line in the config's time zone, each a deadline message due at its nextAt", func(t *testing.T) {
		key, column := problem(t)
		before := now()
		nudge := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "nudge", After: delay(600000)})
		hourly := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "hourly", Every: delay(3600000)})
		morning := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "morning", Cron: text("0 9 * * MON-FRI")})
		after := now()
		for _, c := range []struct {
			v                      view
			kind, spec, zone, name string
			step                   int64
		}{
			{nudge, "after", "600000", "", "nudge", 600000},
			{hourly, "every", "3600000", "", "hourly", 3600000},
			{morning, "cron", "0 9 * * MON-FRI", "America/New_York", "morning", 0},
		} {
			v := c.v
			if v.Name != c.name || v.Kind != c.kind || v.Spec != c.spec || v.State != "active" || v.Runs != 0 || v.SetAt < before || v.SetAt > after || v.NextAt == nil || v.Replaced != nil || v.Unchanged != nil || v.Attempt != nil || v.LastAt != nil {
				t.Fatalf("setSchedule %s answered %s, want an active %s schedule of %s, set between %d and %d, with its nextAt", c.name, sortedJSON(t, v), c.kind, c.spec, before, after)
			}
			if (c.zone == "") != (v.TimeZone == nil) || (v.TimeZone != nil && *v.TimeZone != c.zone) {
				t.Fatalf("%s's timeZone is %v, want %q", c.name, v.TimeZone, c.zone)
			}
			if c.step > 0 && *v.NextAt != v.SetAt+c.step {
				t.Fatalf("%s's nextAt is %d, want %d, %d ms after it was set", c.name, *v.NextAt, v.SetAt+c.step, c.step)
			}
			at, dueAt, ok := armed(t, column, c.name)
			if !ok || at != *v.NextAt || dueAt != *v.NextAt {
				t.Fatalf("%s's deadline message is at %d, due at %d (%t), want both at its nextAt %d", c.name, at, dueAt, ok, *v.NextAt)
			}
		}
		zone, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatal(err)
		}
		local := time.UnixMilli(*morning.NextAt).In(zone)
		if local.Hour() != 9 || local.Minute() != 0 || local.Weekday() == time.Saturday || local.Weekday() == time.Sunday || *morning.NextAt <= morning.SetAt || *morning.NextAt-morning.SetAt > 4*24*3600000 {
			t.Fatalf("morning's next run is %s, want 09:00 of the next weekday in New York", local)
		}
	})

	t.Run("a repeated setSchedule of a live name with the same spec answers unchanged and leaves the row's _version where it was", func(t *testing.T) {
		key, column := problem(t)
		first := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "hourly", Every: delay(3600000)})
		at, _, _ := armed(t, column, "hourly")
		v := version(t, column)
		again := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "hourly", Every: delay(3600000)})
		if again.Unchanged == nil || !*again.Unchanged || again.SetAt != first.SetAt || again.NextAt == nil || *again.NextAt != *first.NextAt || again.Replaced != nil {
			t.Fatalf("the repeated setSchedule answered %s, want the first's view, unchanged", sortedJSON(t, again))
		}
		unmoved(t, "the repeated setSchedule", column, v)
		if moved, _, ok := armed(t, column, "hourly"); !ok || moved != at {
			t.Fatalf("hourly's deadline message is at %d (%t) after the repeat, want %d", moved, ok, at)
		}
	})

	t.Run("setSchedule without problems.schedule is 403 forbidden, a delay under minMs is 400 invalid_argument, and an eleventh live wake-up is 409 schedule_limit, each leaving the row's _version where it was", func(t *testing.T) {
		key, column := problem(t)
		v := version(t, column)
		_, err := set(asGuest, key, types.ProblemSchedulesSetScheduleInput{Name: "nudge", After: delay(600000)})
		wantAPIRefused(t, "the guest's setSchedule", err, 403, "forbidden", guest+" may not set a schedule of Problem "+key+": it needs permission problems.schedule", "null")
		unmoved(t, "the guest's setSchedule", column, v)
		_, err = set(asAgent, key, types.ProblemSchedulesSetScheduleInput{Name: "nudge", After: delay(1000)})
		reason := "1000 is outside minMs..maxMs (60000..2592000000): a delay lies between them"
		wantAPIRefused(t, "a setSchedule of a second's delay", err, 400, "invalid_argument", "operation setSchedule of behavior Schedules: /after: "+reason, "{\"issues\":[{\"message\":\""+reason+"\",\"path\":\"/after\"}]}")
		unmoved(t, "a setSchedule of a second's delay", column, v)
		var names []string
		for i := range 10 {
			name := fmt.Sprintf("w%d", i)
			mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: name, After: delay(600000)})
			names = append(names, name)
		}
		v = version(t, column)
		_, err = set(asAgent, key, types.ProblemSchedulesSetScheduleInput{Name: "w10", After: delay(600000)})
		reason = "it holds 10 live schedules (" + strings.Join(names, ", ") + "), and its max is 10: cancel one to set another"
		wantAPIRefused(t, "the eleventh setSchedule", err, 409, "vetoed", "behavior Schedules vetoes setSchedule of Problem "+key+": "+reason,
			"{\"action\":\"setSchedule\",\"behavior\":\"Schedules\",\"reason\":\""+reason+"\",\"vetoCode\":\"schedule_limit\",\"vetoDetails\":{\"live\":"+sortedJSON(t, names)+",\"max\":10}}")
		unmoved(t, "the eleventh setSchedule", column, v)
	})

	t.Run("a due wake-up fires on the worker as run 1 and hands the project's handler one run event with its name, run, attempt, kind, spec and times, which reports told", func(t *testing.T) {
		key, column := problem(t)
		nudge := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "nudge", After: delay(600000)})
		due(t, column, "nudge")
		drive(t, func() bool {
			v := named(t, key, "nudge")
			return v.LastOutcome != nil && *v.LastOutcome == "told" && events(t, column) == 0
		})
		got := workers.of(key)
		if len(got) != 1 {
			t.Fatalf("the handler got %d events of the problem, want one", len(got))
		}
		p := got[0].payload
		// The deadline falls due at its arming's time, which the fire runs at
		// though the worker claims it sooner.
		if p.Name != "nudge" || p.Run != 1 || p.Attempt != 1 || p.Kind != types.ProblemSchedulesKind_After || p.Spec != "600000" || p.TimeZone != "" || int64(p.SetAt) != int64(nudge.SetAt) || int64(p.DueAt) != *nudge.NextAt || int64(p.FiredAt) != *nudge.NextAt || p.NextAt != nil || got[0].at != int64(p.FiredAt) {
			t.Fatalf("the event's payload is %s at %d, want nudge's run 1, attempt 1, an after of 600000 set at %d, due and fired at %d", sortedJSON(t, p), got[0].at, int64(nudge.SetAt), *nudge.NextAt)
		}
		v := named(t, key, "nudge")
		if v.State != "finished" || v.Runs != 1 || v.Attempt == nil || *v.Attempt != 1 || v.LastAt == nil || *v.LastAt != *nudge.NextAt || v.EndedBy == nil || *v.EndedBy != "run" || v.NextAt != nil {
			t.Fatalf("after its run nudge is %s, want finished by its run, run once, told", sortedJSON(t, v))
		}
	})

	t.Run("the handler runs once per event: a completed event's message is gone and is not handed to it again", func(t *testing.T) {
		key, column := problem(t)
		mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "nudge", After: delay(600000)})
		due(t, column, "nudge")
		fired(t, key, column, 1)
		settled(t)
		if got := workers.of(key); len(got) != 1 {
			t.Fatalf("the handler got %d events of the problem after the worker ran on, want one: %+v", len(got), got)
		}
	})

	t.Run("a handler that fails fails its message, which is due again after the queue's backoff, and the next claim hands it to the handler again", func(t *testing.T) {
		key, column := problem(t)
		workers.fail(key, 1)
		mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "flaky", After: delay(600000)})
		due(t, column, "flaky")
		var id, state, lastError string
		var attempts int
		var later bool
		read := func() bool {
			err := pool.QueryRow(ctx, "SELECT id::text, state, attempts, due_at > now() + interval '20 seconds', coalesce(last_error, '') FROM problem_event_queue WHERE row_key = $1", column).Scan(&id, &state, &attempts, &later, &lastError)
			return err == nil
		}
		drive(t, func() bool { return len(workers.of(key)) >= 1 && read() && state == "ready" && attempts == 1 })
		if got := workers.of(key); len(got) != 1 {
			t.Fatalf("the handler got %d events, want the one it failed", len(got))
		}
		if !read() || state != "ready" || attempts != 1 || !later || !strings.Contains(lastError, "the agent was not told") {
			t.Fatalf("the failed event's message is %s, %d attempts, due more than 20 s on: %t, last error %q; want ready, one attempt, after its backoff, naming the handler's error", state, attempts, later, lastError)
		}
		if _, err := pool.Exec(ctx, "UPDATE problem_event_queue SET due_at = now() WHERE id = $1", id); err != nil {
			t.Fatal(err)
		}
		got := fired(t, key, column, 2)
		if got[1].payload.Name != "flaky" || got[1].payload.Run != 1 || got[1].payload.Attempt != 1 || sortedJSON(t, got[0].payload) != sortedJSON(t, got[1].payload) {
			t.Fatalf("the second call got %s, want the same event again", sortedJSON(t, got[1].payload))
		}
		if v := named(t, key, "flaky"); v.LastOutcome == nil || *v.LastOutcome != "told" {
			t.Fatalf("flaky is %s after the handler's second call, want told", sortedJSON(t, v))
		}
	})

	t.Run("an interval overdue by many runs fires once, and its next run is the first of its grid after the fire", func(t *testing.T) {
		key, column := problem(t)
		mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "minutely", Every: delay(60000)})
		if _, err := pool.Exec(ctx, "UPDATE problem_schedules_schedules SET set_at = set_at - interval '10 minutes', next_at = next_at - interval '10 minutes' WHERE problem_id = $1 AND name = $2", column, "minutely"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE problem_deadline_queue SET at = at - 600000, due_at = now() WHERE row_key = $1 AND name = $2 AND state = 'ready'", column, "minutely"); err != nil {
			t.Fatal(err)
		}
		moved := named(t, key, "minutely")
		got := fired(t, key, column, 1)
		settled(t)
		if again := workers.of(key); len(again) != 1 {
			t.Fatalf("the handler got %d events of the overdue interval, want one", len(again))
		}
		p := got[0].payload
		if p.Run != 1 || p.Attempt != 1 || int64(p.DueAt) != *moved.NextAt || p.NextAt == nil {
			t.Fatalf("the overdue interval's event is %s, want run 1, due at %d, with its next run", sortedJSON(t, p), *moved.NextAt)
		}
		next, fire := int64(*p.NextAt), int64(p.FiredAt)
		if next <= fire || next-fire > 60000 || (next-moved.SetAt)%60000 != 0 {
			t.Fatalf("the next run is %d after a fire at %d, want the first point of the grid of %d by 60000 after it", next, fire, moved.SetAt)
		}
		if at, _, ok := armed(t, column, "minutely"); !ok || at != next {
			t.Fatalf("minutely's deadline message is at %d (%t), want its next run %d", at, ok, next)
		}
	})

	t.Run("a missed run is tried again five minutes on, as attempt 2 of the same run", func(t *testing.T) {
		key, column := problem(t)
		workers.report("late", "missed")
		late := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "late", Every: delay(3600000)})
		before := now()
		due(t, column, "late")
		drive(t, func() bool {
			v := named(t, key, "late")
			return v.RetryAt != nil && events(t, column) == 0
		})
		after := now()
		v := named(t, key, "late")
		if v.LastOutcome == nil || *v.LastOutcome != "missed" || v.RetryAt == nil || *v.RetryAt < before+300000 || *v.RetryAt > after+300000 || v.Runs != 1 {
			t.Fatalf("after its missed run late is %s, want missed, tried again 300000 ms after the report", sortedJSON(t, v))
		}
		if at, _, ok := armed(t, column, "late"); !ok || at != *v.RetryAt {
			t.Fatalf("late's deadline message is at %d (%t), want its retry at %d", at, ok, *v.RetryAt)
		}
		retryAt := *v.RetryAt
		workers.report("late", "told")
		due(t, column, "late")
		got := fired(t, key, column, 2)
		p := got[1].payload
		if p.Name != "late" || p.Run != 1 || p.Attempt != 2 || int64(p.DueAt) != retryAt || p.NextAt == nil || int64(*p.NextAt) != *late.NextAt+3600000 {
			t.Fatalf("the retry's event is %s, want run 1's attempt 2, due at its retry %d, its next regular run at %d", sortedJSON(t, p), retryAt, *late.NextAt+3600000)
		}
	})

	t.Run("a report of an older run answers unchanged, and one of a run not had is 409 no_run", func(t *testing.T) {
		key, column := problem(t)
		mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "ticks", Every: delay(3600000)})
		due(t, column, "ticks")
		fired(t, key, column, 1)
		due(t, column, "ticks")
		fired(t, key, column, 2)
		drive(t, func() bool { v := named(t, key, "ticks"); return v.LastOutcome != nil })
		v := version(t, column)
		older, err := report(asAgent, key, "ticks", 1, types.ProblemSchedulesOutcome_Held)
		if err != nil {
			t.Fatalf("the report of run 1: %v", err)
		}
		if sortedJSON(t, older) != "{\"name\":\"ticks\",\"outcome\":\"held\",\"run\":1,\"unchanged\":true}" {
			t.Fatalf("the report of run 1 answered %s, want it unchanged", sortedJSON(t, older))
		}
		unmoved(t, "the report of an older run", column, v)
		_, err = report(asAgent, key, "ticks", 3, types.ProblemSchedulesOutcome_Told)
		reason := "its schedule \"ticks\" has had 2 runs, and no run 3 to report"
		wantAPIRefused(t, "the report of run 3", err, 409, "vetoed", "behavior Schedules vetoes reportRun of Problem "+key+": "+reason,
			"{\"action\":\"reportRun\",\"behavior\":\"Schedules\",\"reason\":\""+strings.ReplaceAll(reason, "\"", "\\\"")+"\",\"vetoCode\":\"no_run\",\"vetoDetails\":{\"name\":\"ticks\",\"run\":3,\"runs\":2}}")
		unmoved(t, "the report of a run not had", column, v)
	})

	t.Run("pauseSchedule keeps nextAt and deletes its deadline message, and resumeSchedule past nextAt fires at once, once", func(t *testing.T) {
		key, column := problem(t)
		hourly := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "hourly", Every: delay(3600000)})
		paused, err := asAgent.ProblemNamespace.Schedules.PauseSchedule(ctx, key, types.ProblemSchedulesPauseScheduleInput{Name: "hourly"})
		if err != nil {
			t.Fatalf("pauseSchedule: %v", err)
		}
		if p := as[view](t, paused); p.State != "paused" || p.NextAt == nil || *p.NextAt != *hourly.NextAt {
			t.Fatalf("pauseSchedule answered %s, want paused, its nextAt %d kept", sortedJSON(t, p), *hourly.NextAt)
		}
		if _, _, ok := armed(t, column, "hourly"); ok {
			t.Fatal("a paused schedule keeps its deadline message")
		}
		if _, err := pool.Exec(ctx, "UPDATE problem_schedules_schedules SET next_at = now() - interval '1 hour' WHERE problem_id = $1 AND name = $2", column, "hourly"); err != nil {
			t.Fatal(err)
		}
		resumed, err := asAgent.ProblemNamespace.Schedules.ResumeSchedule(ctx, key, types.ProblemSchedulesResumeScheduleInput{Name: "hourly"})
		if err != nil {
			t.Fatalf("resumeSchedule: %v", err)
		}
		r := as[view](t, resumed)
		if r.State != "active" || r.NextAt == nil || *r.NextAt >= now() {
			t.Fatalf("resumeSchedule answered %s, want active, its nextAt past", sortedJSON(t, r))
		}
		got := fired(t, key, column, 1)
		settled(t)
		if again := workers.of(key); len(again) != 1 {
			t.Fatalf("the handler got %d events of the resumed schedule, want one", len(again))
		}
		p := got[0].payload
		if p.Run != 1 || int64(p.DueAt) != *r.NextAt || p.NextAt == nil || int64(*p.NextAt) != hourly.SetAt+3600000 {
			t.Fatalf("the resumed schedule's event is %s, want run 1, due at %d, its next run the first of its grid after the fire, %d", sortedJSON(t, p), *r.NextAt, hourly.SetAt+3600000)
		}
	})

	t.Run("cancelSchedule finishes a wake-up and frees its place under max", func(t *testing.T) {
		key, column := problem(t)
		for i := range 10 {
			mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: fmt.Sprintf("w%d", i), After: delay(600000)})
		}
		before := now()
		cancelled, err := asAgent.ProblemNamespace.Schedules.CancelSchedule(ctx, key, types.ProblemSchedulesCancelScheduleInput{Name: "w0"})
		if err != nil {
			t.Fatalf("cancelSchedule: %v", err)
		}
		if c := as[view](t, cancelled); c.State != "finished" || c.EndedBy == nil || *c.EndedBy != "cancel" || c.FinishedAt == nil || *c.FinishedAt < before || c.NextAt != nil {
			t.Fatalf("cancelSchedule answered %s, want w0 finished by cancel", sortedJSON(t, c))
		}
		if _, _, ok := armed(t, column, "w0"); ok {
			t.Fatal("a cancelled schedule keeps its deadline message")
		}
		if v := mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "w10", After: delay(600000)}); v.State != "active" {
			t.Fatalf("the eleventh name after a cancel is %s, want active", sortedJSON(t, v))
		}
	})

	t.Run("a problem whose status enters done finishes its wake-ups, and a new one is 409 schedules_ended", func(t *testing.T) {
		key, column := problem(t)
		mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "hourly", Every: delay(3600000)})
		mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "nudge", After: delay(600000)})
		for _, to := range []types.ProblemStatusState{types.ProblemStatusState_Working, types.ProblemStatusState_Done} {
			if _, err := asAgent.ProblemNamespace.Status.Transition(ctx, key, types.ProblemStatusTransitionInput{To: to}); err != nil {
				t.Fatalf("the transition to %s: %v", to, err)
			}
		}
		for _, v := range list(t, key) {
			if v.State != "finished" || v.EndedBy == nil || *v.EndedBy != "endOn" || v.NextAt != nil || v.FinishedAt == nil {
				t.Fatalf("after done %s is %s, want finished by endOn", v.Name, sortedJSON(t, v))
			}
			if _, _, ok := armed(t, column, v.Name); ok {
				t.Fatalf("%s keeps its deadline message after done", v.Name)
			}
		}
		v := version(t, column)
		_, err := set(asAgent, key, types.ProblemSchedulesSetScheduleInput{Name: "again", After: delay(600000)})
		reason := "its status is done, which ends its schedules (endOn): it takes no live schedule"
		wantAPIRefused(t, "a setSchedule after done", err, 409, "vetoed", "behavior Schedules vetoes setSchedule of Problem "+key+": "+reason,
			"{\"action\":\"setSchedule\",\"behavior\":\"Schedules\",\"reason\":\""+reason+"\",\"vetoCode\":\"schedules_ended\",\"vetoDetails\":{\"status\":\"done\"}}")
		unmoved(t, "a setSchedule after done", column, v)
	})

	t.Run("a raw SQL write of a problem's status to dropped is heard by the worker and finishes its wake-ups", func(t *testing.T) {
		key, column := problem(t)
		mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: "hourly", Every: delay(3600000)})
		if _, err := pool.Exec(ctx, "UPDATE problem SET status = 'dropped' WHERE id = $1", column); err != nil {
			t.Fatal(err)
		}
		drive(t, func() bool { v := named(t, key, "hourly"); return v.State == "finished" })
		if v := named(t, key, "hourly"); v.State != "finished" || v.EndedBy == nil || *v.EndedBy != "endOn" {
			t.Fatalf("after the raw write hourly is %s, want finished by endOn within ten seconds", sortedJSON(t, v))
		}
		if _, _, ok := armed(t, column, "hourly"); ok {
			t.Fatal("hourly keeps its deadline message after the raw write")
		}
	})

	t.Run("listSchedules reads a problem's wake-ups in name order", func(t *testing.T) {
		key, _ := problem(t)
		for _, name := range []string{"c", "a", "b"} {
			mustSet(t, key, types.ProblemSchedulesSetScheduleInput{Name: name, After: delay(600000)})
		}
		var names []string
		for _, v := range list(t, key) {
			names = append(names, v.Name)
		}
		if !slices.Equal(names, []string{"a", "b", "c"}) {
			t.Fatalf("listSchedules lists %q, want a, b, c", names)
		}
	})
}
`
