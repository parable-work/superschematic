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

// behaviorTimersCases are the cases behaviorTimersTest runs, by name: the
// end-to-end scenario every language's server and behavior worker of a
// table that implements Timers is held to (D103), with the core's words.
var behaviorTimersCases = []string{
	"a create arms the deadline: its sub-table row and its queue message with its due time",
	"a due deadline the worker runs moves the status and records the outcome",
	"a status that leaves the state cancels the deadline by until",
	"a re-armed deadline does not fire on its old arming",
	"resetTimer and snoozeTimer are refused without the config's permission and run with it",
	"fireTimer through the SDK fires a due deadline and spends its message",
	"deleting the row removes its deadlines",
	"a raw SQL update of a Timers field start re-arms its deadline",
	"a raw SQL delete of a ticket clears its deadline messages",
	"a repository's bulk update of a field start re-arms its deadline",
}

// TestTimersGoServerAndWorkerOnPostgres builds fixture-timers-db and
// fixture-timers-api as build-all does, then writes behaviorTimersTest
// into the generated API module and runs it, after go vet: the generated
// Go server's Timers operations (D103), mounted on handlers that call the
// ORM of fixture-timers-db, called through the generated Go SDK with a test
// authenticator in front of the routes, as runBehaviorServerTest calls
// fixture-workflow-api's; and the queues of the generated behavior worker
// TicketTimers, its deadline queue and its change queue, run in process
// with worker.RunAll as its main runs them, on the Postgres
// behaviorServerDatabaseEnv names: a raw SQL write and a repository's bulk
// update of a field start are heard through Ticket's change queue, whose
// trigger create.sql holds, and re-arm the deadline when the worker runs
// them, and a raw delete clears the row's deadline messages. It then plans the
// worker's entrypoint over the generated modules and vets it. The module
// links the core's static archive, which scripts/rules-archive.sh stages;
// the test skips without it, without the database, and in -short mode.
func TestTimersGoServerAndWorkerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server and worker of Timers in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server and worker of Timers against Postgres")
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
	for _, service := range []string{"fixture-timers-db", "fixture-timers-api"} {
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
		if service == "fixture-timers-api" {
			apiSchema = schema
		} else {
			dbSchema = schema
		}
	}

	apiDir := APIDir(outputRoot, "fixture-timers-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-timers-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_timers_server_test.go"), []byte(behaviorTimersTest), 0o644); err != nil {
		t.Fatal(err)
	}
	sdkModule := naming.Default().GoSDKModule("fixture-timers-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-timers-api"},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		cmd.Env = append(cmd.Environ(), env...)
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated API tests:\n%s", out)
	for _, name := range behaviorTimersCases {
		if want := "--- PASS: TestTimers/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}

	vetBehaviorWorkerEntrypoint(t, outputRoot, paths, apiSchema, dbSchema, env, "timers-stack", "TicketTimers")
}

// vetBehaviorWorkerEntrypoint plans the entrypoint of api's behavior
// worker of the class name in stack over the modules the build wrote under
// outputRoot, as a stack's build plans it, writes it, and vets it.
func vetBehaviorWorkerEntrypoint(t *testing.T, outputRoot string, paths naming.LocalPaths, api, db *ir.Schema, env []string, stack, name string) {
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
	if err := os.MkdirAll(implDir, 0o755); err != nil {
		t.Fatal(err)
	}
	impl := "example.com/services/" + api.Name + "/go"
	if err := os.WriteFile(filepath.Join(implDir, "go.mod"), []byte("module "+impl+"\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := api.Name + "-" + codegen.ToKebabCase(name)
	dir := servergen.ServerDir(outputRoot, stack, server)
	in := servergen.Input{
		Stack:  stack,
		Server: server,
		Dir:    dir,
		APIs: []servergen.APIInput{{
			Output: output,
			Implementation: servergen.Implementation{
				Dir:       implDir,
				Import:    impl,
				Module:    impl,
				ModuleDir: implDir,
			},
		}},
		Worker: &servergen.WorkerInput{Name: name, GraceSeconds: 8},
		Modules: []servergen.Module{
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
		},
		Naming:         n,
		RepositoryRoot: testpaths.RepoRoot(t),
		ScalarGo:       paths.ScalarGo,
		RulesGo:        paths.RulesGo,
	}
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatalf("plan the worker: %v", err)
	}
	if !s.BehaviorWorker() {
		t.Fatalf("the plan of %s is no behavior worker's: %+v", name, s.Worker)
	}
	if err := servergen.Write(s, dir); err != nil {
		t.Fatal(err)
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

// behaviorTimersTest runs in the generated module of fixture-timers-api. It
// serves the generated routes over the ORM of fixture-timers-db, on a
// schema of its own in the Postgres behaviorServerDatabaseEnv names, with
// a test authenticator in front of them, calls them through the generated
// Go SDK, and runs the generated worker's queues in process.
const behaviorTimersTest = `package fixturetimersapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	api "example.com/schemas/api/fixture-timers-api"
	orm "example.com/schemas/orm/fixture-timers-db"
	sdk "example.com/schemas/sdk/go/fixture-timers-api"
	types "example.com/schemas/types/go/fixture-timers-api"
	dbtypes "example.com/schemas/types/go/fixture-timers-db"
)

// authenticate is the test's authenticator, in place of the auth an API
// with an authDb wires: Authorization: Bearer <subject>[:<permission>[,...]]
// puts that subject on the request, holding those permissions.
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

// openDatabase is the ORM of fixture-timers-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends, and
// its pool, which the test reads the tables through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server and worker of Timers against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("behavior_timers_go_%d", time.Now().UnixNano())
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

// failure is the status, code and detail of a refused call.
type failure struct {
	status int
	code   string
	detail string
}

func failed(t *testing.T, what string, err error) failure {
	t.Helper()
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: %v, want the API's refusal", what, err)
	}
	return failure{status: apiErr.StatusCode, code: apiErr.Code, detail: apiErr.Message}
}

// deadline is a row's Timers sub-table row of a deadline, its times in
// epoch milliseconds, 0 for NULL; held is whether the row holds one.
type deadline struct {
	held          bool
	baseAt, dueAt int64
	fired         bool
	outcome       string
}

func TestTimers(t *testing.T) {
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
	as := func(token string) *sdk.FixtureTimersApiSDK {
		t.Helper()
		client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		client.SetToken(token)
		return client
	}
	sam, kim := as("sam:tickets.write"), as("kim:tickets.write,tickets.timers")

	// create creates a ticket through the repository and returns its key
	// in its wire form and in its column's.
	create := func(t *testing.T, title string) (string, string) {
		t.Helper()
		row, err := db.GetTicketRepository().CreateOne(ctx, &orm.TicketCreate{Title: title})
		if err != nil {
			t.Fatalf("create a ticket: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String()
	}
	read := func(t *testing.T, column, name string) deadline {
		t.Helper()
		var d deadline
		var baseAt, dueAt *time.Time
		var fired *bool
		var outcome *string
		err := pool.QueryRow(ctx, "SELECT base_at, due_at, fired, outcome FROM ticket_timers_deadlines WHERE ticket_id = $1 AND name = $2", column, name).Scan(&baseAt, &dueAt, &fired, &outcome)
		if err != nil {
			if strings.Contains(err.Error(), "no rows") {
				return d
			}
			t.Fatal(err)
		}
		d.held = true
		if baseAt != nil {
			d.baseAt = baseAt.UnixMilli()
		}
		if dueAt != nil {
			d.dueAt = dueAt.UnixMilli()
		}
		d.fired = fired != nil && *fired
		if outcome != nil {
			d.outcome = *outcome
		}
		return d
	}
	// live is the row's live messages, each name's time and due time.
	live := func(t *testing.T, column string) map[string][2]int64 {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT name, at, due_at FROM ticket_deadline_queue WHERE row_key = $1 AND state IN ('ready', 'claimed')", column)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][2]int64{}
		for rows.Next() {
			var name string
			var at int64
			var dueAt time.Time
			if err := rows.Scan(&name, &at, &dueAt); err != nil {
				t.Fatal(err)
			}
			out[name] = [2]int64{at, dueAt.UnixMilli()}
		}
		return out
	}
	status := func(t *testing.T, column string) string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, "SELECT status FROM ticket WHERE id = $1", column).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// drive runs the worker's queues in process, as its main does, until
	// done holds or ten seconds pass, polling every 100 ms.
	drive := func(t *testing.T, done func() bool) {
		t.Helper()
		running, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() {
			stopped <- worker.RunAll(running, api.TicketTimersQueues(db, zap.NewNop()), worker.Options{Concurrency: 2, Idle: 100 * time.Millisecond})
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

	_, firstColumn := create(t, "Printer")

	t.Run("a create arms the deadline: its sub-table row and its queue message with its due time", func(t *testing.T) {
		respond := read(t, firstColumn, "respond")
		if !respond.held || respond.dueAt != respond.baseAt+1500 || respond.fired {
			t.Fatalf("respond = %+v, want due 1500 ms after the create, not fired", respond)
		}
		if idle := read(t, firstColumn, "idle"); idle.held {
			t.Fatalf("idle = %+v, want none: the ticket never waited", idle)
		}
		if got := live(t, firstColumn); len(got) != 1 || got["respond"] != [2]int64{respond.dueAt, respond.dueAt} {
			t.Fatalf("the live messages are %v, want respond's alone, at and due at %d", got, respond.dueAt)
		}
	})

	t.Run("a due deadline the worker runs moves the status and records the outcome", func(t *testing.T) {
		if queues := api.TicketTimersQueues(db, zap.NewNop()); len(queues) != 2 || queues[0].Name != "TicketDeadline" || queues[1].Name != "TicketChange" {
			t.Fatalf("the worker's queues are %d, want TicketDeadline and TicketChange", len(queues))
		}
		drive(t, func() bool { return status(t, firstColumn) == "escalated" })
		if got := status(t, firstColumn); got != "escalated" {
			t.Fatalf("status = %s, want escalated within ten seconds", got)
		}
		if respond := read(t, firstColumn, "respond"); !respond.fired || respond.outcome != "moved" {
			t.Fatalf("respond = %+v, want fired, moved", respond)
		}
		if got := live(t, firstColumn); len(got) != 0 {
			t.Fatalf("the live messages are %v, want none", got)
		}
	})

	second, secondColumn := create(t, "Router")

	t.Run("a status that leaves the state cancels the deadline by until", func(t *testing.T) {
		if _, err := sam.TicketNamespace.Status.Transition(ctx, second, types.TicketStatusTransitionInput{To: types.TicketStatusState_Waiting}); err != nil {
			t.Fatalf("transition: %v", err)
		}
		if respond := read(t, secondColumn, "respond"); respond.dueAt != 0 || respond.fired {
			t.Fatalf("respond = %+v, want cancelled", respond)
		}
		var entered time.Time
		if err := pool.QueryRow(ctx, "SELECT status_entered_at FROM ticket WHERE id = $1", secondColumn).Scan(&entered); err != nil {
			t.Fatal(err)
		}
		idle := read(t, secondColumn, "idle")
		if idle.dueAt != entered.UnixMilli()+3600000 {
			t.Fatalf("idle = %+v, want due an hour after waiting began at %d", idle, entered.UnixMilli())
		}
		if got := live(t, secondColumn); len(got) != 1 || got["idle"][0] != idle.dueAt {
			t.Fatalf("the live messages are %v, want idle's alone", got)
		}
	})

	t.Run("a re-armed deadline does not fire on its old arming", func(t *testing.T) {
		var idleID string
		if err := pool.QueryRow(ctx, "UPDATE ticket_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = 'idle' RETURNING id::text", secondColumn).Scan(&idleID); err != nil {
			t.Fatal(err)
		}
		core, logs := observer.New(zap.InfoLevel)
		queue := api.TicketTimersQueues(db, zap.New(core))[0]
		messages, err := queue.Claim(ctx, 10, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		var claimed *worker.Message
		for i := range messages {
			if messages[i].ID == idleID {
				claimed = &messages[i]
				continue
			}
			if err := queue.Release(ctx, messages[i].ID, messages[i].Token); err != nil {
				t.Fatal(err)
			}
		}
		if claimed == nil {
			t.Fatalf("the claim took %d messages, idle's not among them", len(messages))
		}
		snoozed := time.Now().Add(2 * time.Hour).UnixMilli()
		result, err := kim.TicketNamespace.Timers.SnoozeTimer(ctx, second, types.TicketTimersSnoozeTimerInput{Deadline: types.TicketTimersDeadline_Idle, DueAt: types.GenericInt64(snoozed)})
		if err != nil || int64(result.DueAt) != snoozed {
			t.Fatalf("snooze = %+v, %v, want due at %d", result, err, snoozed)
		}
		if err := claimed.Handle(ctx); err != nil {
			t.Fatalf("the handler on the old claim: %v", err)
		}
		lost := false
		for _, entry := range logs.FilterMessage("deadline ran nothing").All() {
			lost = lost || (entry.ContextMap()["why"] == "claim lost" && entry.ContextMap()["message"] == idleID)
		}
		if !lost {
			t.Fatalf("the handler logged %v, want its claim lost", logs.All())
		}
		if got := status(t, secondColumn); got != "waiting" {
			t.Fatalf("status = %s, want waiting", got)
		}
		if idle := read(t, secondColumn, "idle"); idle.dueAt != snoozed || idle.fired {
			t.Fatalf("idle = %+v, want due at the snooze, %d", idle, snoozed)
		}
		if got := live(t, secondColumn); len(got) != 1 || got["idle"][0] != snoozed {
			t.Fatalf("the live messages are %v, want idle's alone, at the snooze", got)
		}
	})

	t.Run("resetTimer and snoozeTimer are refused without the config's permission and run with it", func(t *testing.T) {
		_, err := sam.TicketNamespace.Timers.ResetTimer(ctx, second, types.TicketTimersResetTimerInput{Deadline: types.TicketTimersDeadline_Idle})
		got := failed(t, "sam's reset", err)
		want := "sam may not reset deadline idle of Ticket " + second + ": it needs permission tickets.timers"
		if got.status != 403 || got.code != "forbidden" || got.detail != want {
			t.Fatalf("sam's reset refused with %+v, want 403 forbidden %q", got, want)
		}
		_, err = sam.TicketNamespace.Timers.SnoozeTimer(ctx, second, types.TicketTimersSnoozeTimerInput{Deadline: types.TicketTimersDeadline_Idle, DueAt: types.GenericInt64(time.Now().Add(3 * time.Hour).UnixMilli())})
		if got := failed(t, "sam's snooze", err); got.status != 403 || got.code != "forbidden" {
			t.Fatalf("sam's snooze refused with %+v, want 403 forbidden", got)
		}
		reset, err := kim.TicketNamespace.Timers.ResetTimer(ctx, second, types.TicketTimersResetTimerInput{Deadline: types.TicketTimersDeadline_Idle})
		if err != nil {
			t.Fatalf("kim's reset: %v", err)
		}
		if idle := read(t, secondColumn, "idle"); idle.dueAt != int64(reset.DueAt) {
			t.Fatalf("idle = %+v, want due at the reset's %d", idle, reset.DueAt)
		}
		if got := live(t, secondColumn); got["idle"][0] != int64(reset.DueAt) {
			t.Fatalf("the live messages are %v, want idle's at %d", got, reset.DueAt)
		}
		later := time.Now().Add(5 * time.Hour).UnixMilli()
		snoozed, err := kim.TicketNamespace.Timers.SnoozeTimer(ctx, second, types.TicketTimersSnoozeTimerInput{Deadline: types.TicketTimersDeadline_Idle, DueAt: types.GenericInt64(later)})
		if err != nil || int64(snoozed.DueAt) != later {
			t.Fatalf("kim's snooze = %+v, %v, want due at %d", snoozed, err, later)
		}
		if got := live(t, secondColumn); len(got) != 1 || got["idle"][0] != later {
			t.Fatalf("the live messages are %v, want idle's alone, at %d", got, later)
		}
	})

	t.Run("fireTimer through the SDK fires a due deadline and spends its message", func(t *testing.T) {
		third, thirdColumn := create(t, "Desk")
		time.Sleep(1600 * time.Millisecond)
		result, err := sam.TicketNamespace.Timers.FireTimer(ctx, third, types.TicketTimersFireTimerInput{Deadline: types.TicketTimersDeadline_Respond})
		if err != nil {
			t.Fatalf("fire: %v", err)
		}
		if !result.Fired || result.Outcome == nil || *result.Outcome != types.TicketTimersFireTimerResultOutcome_Moved {
			t.Fatalf("fire = %+v, want fired, moved", result)
		}
		if got := live(t, thirdColumn); len(got) != 0 {
			t.Fatalf("the live messages are %v, want none", got)
		}
		if got := status(t, thirdColumn); got != "escalated" {
			t.Fatalf("status = %s, want escalated", got)
		}
	})

	t.Run("deleting the row removes its deadlines", func(t *testing.T) {
		id, err := types.ParseIdentityUUID(second)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.GetTicketRepository().DeleteOne(ctx, id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		var rows, messages int
		if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM ticket_timers_deadlines WHERE ticket_id = $1), (SELECT count(*) FROM ticket_deadline_queue WHERE row_key = $1)", secondColumn).Scan(&rows, &messages); err != nil {
			t.Fatal(err)
		}
		if rows != 0 || messages != 0 {
			t.Fatalf("the row left %d sub-table rows and %d messages, want none", rows, messages)
		}
	})

	// visiting creates a ticket through the repository whose visit is at
	// visit, and returns its key in its wire form and in its column's.
	visiting := func(t *testing.T, title string, visit time.Time) (string, string) {
		t.Helper()
		at := dbtypes.TemporalDateTime(visit)
		row, err := db.GetTicketRepository().CreateOne(ctx, &orm.TicketCreate{Title: title, VisitAt: &at})
		if err != nil {
			t.Fatalf("create a ticket: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String()
	}
	// rearmed reports whether remind is armed a day before visit: its
	// sub-table row due then, and its one live message armed at it.
	rearmed := func(t *testing.T, column string, visit time.Time) bool {
		t.Helper()
		due := visit.UnixMilli() - 86400000
		remind := read(t, column, "remind")
		return remind.held && remind.dueAt == due && !remind.fired && live(t, column)["remind"] == [2]int64{due, due}
	}

	t.Run("a raw SQL update of a Timers field start re-arms its deadline", func(t *testing.T) {
		visit := time.Now().Add(48 * time.Hour).Truncate(time.Millisecond)
		_, column := visiting(t, "Visit", visit)
		if !rearmed(t, column, visit) {
			t.Fatalf("remind = %+v, %v, want armed a day before the visit at the create", read(t, column, "remind"), live(t, column))
		}
		later := visit.Add(24 * time.Hour)
		if _, err := pool.Exec(ctx, "UPDATE ticket SET visit_at = $2 WHERE id = $1", column, later); err != nil {
			t.Fatal(err)
		}
		drive(t, func() bool { return rearmed(t, column, later) })
		if !rearmed(t, column, later) {
			t.Fatalf("remind = %+v, %v, want armed a day before the new visit, %d", read(t, column, "remind"), live(t, column), later.UnixMilli()-86400000)
		}
	})

	t.Run("a raw SQL delete of a ticket clears its deadline messages", func(t *testing.T) {
		visit := time.Now().Add(96 * time.Hour).Truncate(time.Millisecond)
		_, column := visiting(t, "Gone", visit)
		messages := func() int {
			var n int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM ticket_deadline_queue WHERE row_key = $1", column).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if n := messages(); n != 2 {
			t.Fatalf("the ticket has %d deadline messages, want respond's and remind's", n)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM ticket WHERE id = $1", column); err != nil {
			t.Fatal(err)
		}
		drive(t, func() bool { return messages() == 0 })
		if n := messages(); n != 0 {
			t.Fatalf("the deleted ticket keeps %d deadline messages, want none", n)
		}
	})

	t.Run("a repository's bulk update of a field start re-arms its deadline", func(t *testing.T) {
		visit := time.Now().Add(48 * time.Hour).Truncate(time.Millisecond)
		a, aColumn := visiting(t, "Visit A", visit)
		b, bColumn := visiting(t, "Visit B", visit)
		var ids []dbtypes.IdentityUUID
		for _, key := range []string{a, b} {
			id, err := dbtypes.ParseIdentityUUID(key)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		later := dbtypes.TemporalDateTime(visit.Add(24 * time.Hour))
		n, err := db.GetTicketRepository().UpdateMany(ctx, &orm.TicketFilter{Id: &orm.UUIDFilter{In: ids}}, &orm.TicketUpdate{VisitAt: &later})
		if err != nil || n != 2 {
			t.Fatalf("UpdateMany = %d, %v, want the two tickets", n, err)
		}
		at := time.Time(later)
		drive(t, func() bool { return rearmed(t, aColumn, at) && rearmed(t, bColumn, at) })
		for _, column := range []string{aColumn, bColumn} {
			if !rearmed(t, column, at) {
				t.Fatalf("remind of %s = %+v, %v, want armed a day before the new visit", column, read(t, column, "remind"), live(t, column))
			}
		}
	})
}
`
