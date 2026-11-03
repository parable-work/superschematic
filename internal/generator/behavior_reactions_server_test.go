package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// behaviorReactionsCases are the cases behaviorReactionsTest runs, by name:
// the end-to-end scenario every language's server and behavior workers of
// tables that implement Reactions are held to (D103), with the core's
// words.
var behaviorReactionsCases = []string{
	"a task that starts moves its project to active",
	"a project whose tasks are all done moves to done",
	"a task that fails moves its project to failed",
	"a raw SQL write of a task's status is heard and moves its project",
	"a raw SQL write of a task's priority to urgent starts it, which moves its project",
	"a then whose target has no transition to its state leaves the target as it is and completes the message",
}

// TestReactionsGoServerAndWorkerOnPostgres builds fixture-reactions-db and
// fixture-reactions-api as build-all does, then writes
// behaviorReactionsTest into the generated API module and runs it, after
// go vet: the generated Go server's Workflow operations of Project and Task
// (D103), called through the generated Go SDK with a test authenticator in
// front of the routes, and the queues of the generated behavior workers
// ProjectReactions and TaskReactions, each its table's change queue, run in
// process with worker.RunAll on the Postgres behaviorServerDatabaseEnv
// names. A task's write, through the SDK or a raw SQL statement, is heard
// through Task's change queue, whose trigger create.sql holds, and its
// rules and Project's move the project when the worker runs it. It then
// plans each worker's entrypoint over the generated modules and vets it.
// The module links the core's static archive, which scripts/rules-archive.sh
// stages; the test skips without it, without the database, and in -short
// mode.
func TestReactionsGoServerAndWorkerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server and workers of Reactions in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server and workers of Reactions against Postgres")
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
	for _, service := range []string{"fixture-reactions-db", "fixture-reactions-api"} {
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
		if service == "fixture-reactions-api" {
			apiSchema = schema
		} else {
			dbSchema = schema
		}
	}

	apiDir := APIDir(outputRoot, "fixture-reactions-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-reactions-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_reactions_server_test.go"), []byte(behaviorReactionsTest), 0o644); err != nil {
		t.Fatal(err)
	}
	sdkModule := naming.Default().GoSDKModule("fixture-reactions-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-reactions-api"},
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
	for _, name := range behaviorReactionsCases {
		if want := "--- PASS: TestReactions/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}

	for _, worker := range []string{"ProjectReactions", "TaskReactions"} {
		vetBehaviorWorkerEntrypoint(t, outputRoot, paths, apiSchema, dbSchema, env, "reactions-stack", worker)
	}
}

// behaviorReactionsTest runs in the generated module of
// fixture-reactions-api. It serves the generated routes over the ORM of
// fixture-reactions-db, on a schema of its own in the Postgres
// behaviorServerDatabaseEnv names, with a test authenticator in front of
// them, calls them through the generated Go SDK, and runs both generated
// workers' queues in process.
const behaviorReactionsTest = `package fixturereactionsapi_test

import (
	"context"
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

	api "example.com/schemas/api/fixture-reactions-api"
	orm "example.com/schemas/orm/fixture-reactions-db"
	sdk "example.com/schemas/sdk/go/fixture-reactions-api"
	types "example.com/schemas/types/go/fixture-reactions-api"
	dbtypes "example.com/schemas/types/go/fixture-reactions-db"
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

// openDatabase is the ORM of fixture-reactions-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends, and
// its pool, which the test reads and writes the tables through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server and workers of Reactions against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("behavior_reactions_go_%d", time.Now().UnixNano())
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

func TestReactions(t *testing.T) {
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
	ann, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ann.SetToken("ann:projects.write,tasks.write")

	// project creates a planned project through the repository and returns
	// its key in its wire form and in its column's.
	project := func(t *testing.T, name string) (string, string) {
		t.Helper()
		row, err := db.GetProjectRepository().CreateOne(ctx, &orm.ProjectCreate{Name: name})
		if err != nil {
			t.Fatalf("create a project: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String()
	}
	// task creates a todo task of the project of key through the
	// repository and returns its key in its wire form and in its column's.
	task := func(t *testing.T, title, key string) (string, string) {
		t.Helper()
		id, err := dbtypes.ParseIdentityUUID(key)
		if err != nil {
			t.Fatal(err)
		}
		row, err := db.GetTaskRepository().CreateOne(ctx, &orm.TaskCreate{Title: title, Project: dbtypes.Project{Id: &id}})
		if err != nil {
			t.Fatalf("create a task: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String()
	}
	moveTask := func(t *testing.T, key string, to types.TaskStatusState) {
		t.Helper()
		if _, err := ann.TaskNamespace.Status.Transition(ctx, key, types.TaskStatusTransitionInput{To: to}); err != nil {
			t.Fatalf("move task %s to %s: %v", key, to, err)
		}
	}
	moveProject := func(t *testing.T, key string, to types.ProjectStatusState) {
		t.Helper()
		if _, err := ann.ProjectNamespace.Status.Transition(ctx, key, types.ProjectStatusTransitionInput{To: to}); err != nil {
			t.Fatalf("move project %s to %s: %v", key, to, err)
		}
	}
	status := func(t *testing.T, table, column string) string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, "SELECT status FROM "+table+" WHERE id = $1", column).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// count is how many messages of both change queues meet where:
	// waiting those that wait to run or are running, dead those dead.
	count := func(t *testing.T, where string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM project_change_queue WHERE "+where+") + (SELECT count(*) FROM task_change_queue WHERE "+where+")").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	waiting := func(t *testing.T) int { return count(t, "state IN ('ready', 'claimed')") }
	dead := func(t *testing.T) int { return count(t, "state = 'dead'") }
	// drive runs both workers' queues in process, as their mains do, until
	// done holds or ten seconds pass, polling every 100 ms, and fails the
	// test when a message went dead.
	drive := func(t *testing.T, done func() bool) {
		t.Helper()
		queues := append(api.ProjectReactionsQueues(db, zap.NewNop()), api.TaskReactionsQueues(db, zap.NewNop())...)
		running, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() {
			stopped <- worker.RunAll(running, queues, worker.Options{Concurrency: 2, Idle: 100 * time.Millisecond})
		}()
		deadline := time.Now().Add(10 * time.Second)
		for !done() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		stop()
		if err := <-stopped; err != nil {
			t.Fatalf("the workers: %v", err)
		}
		if n := dead(t); n != 0 {
			t.Fatalf("%d change messages are dead", n)
		}
	}
	drained := func() bool { return waiting(t) == 0 }

	t.Run("a task that starts moves its project to active", func(t *testing.T) {
		p, pColumn := project(t, "One")
		k, _ := task(t, "Plan", p)
		moveTask(t, k, types.TaskStatusState_Doing)
		drive(t, func() bool { return status(t, "project", pColumn) == "active" })
		if got := status(t, "project", pColumn); got != "active" {
			t.Fatalf("the project is %s, want active", got)
		}
	})

	t.Run("a project whose tasks are all done moves to done", func(t *testing.T) {
		p, pColumn := project(t, "Two")
		moveProject(t, p, types.ProjectStatusState_Active)
		first, _ := task(t, "Build", p)
		second, _ := task(t, "Ship", p)
		moveTask(t, first, types.TaskStatusState_Doing)
		moveTask(t, second, types.TaskStatusState_Doing)
		moveTask(t, first, types.TaskStatusState_Done)
		drive(t, drained)
		if got := status(t, "project", pColumn); got != "active" {
			t.Fatalf("the project is %s with one task done, want active", got)
		}
		moveTask(t, second, types.TaskStatusState_Done)
		drive(t, func() bool { return status(t, "project", pColumn) == "done" })
		if got := status(t, "project", pColumn); got != "done" {
			t.Fatalf("the project is %s, want done", got)
		}
	})

	t.Run("a task that fails moves its project to failed", func(t *testing.T) {
		p, pColumn := project(t, "Three")
		moveProject(t, p, types.ProjectStatusState_Active)
		k, _ := task(t, "Risk", p)
		moveTask(t, k, types.TaskStatusState_Doing)
		moveTask(t, k, types.TaskStatusState_Failed)
		drive(t, func() bool { return status(t, "project", pColumn) == "failed" })
		if got := status(t, "project", pColumn); got != "failed" {
			t.Fatalf("the project is %s, want failed", got)
		}
	})

	t.Run("a raw SQL write of a task's status is heard and moves its project", func(t *testing.T) {
		p, pColumn := project(t, "Four")
		_, kColumn := task(t, "Quiet", p)
		if _, err := pool.Exec(ctx, "UPDATE task SET status = 'doing', status_entered_at = now() WHERE id = $1", kColumn); err != nil {
			t.Fatal(err)
		}
		drive(t, func() bool { return status(t, "project", pColumn) == "active" })
		if got := status(t, "project", pColumn); got != "active" {
			t.Fatalf("the project is %s, want active", got)
		}
	})

	t.Run("a raw SQL write of a task's priority to urgent starts it, which moves its project", func(t *testing.T) {
		p, pColumn := project(t, "Urgent")
		_, kColumn := task(t, "Hurry", p)
		drive(t, drained)
		if _, err := pool.Exec(ctx, "UPDATE task SET priority = 'urgent' WHERE id = $1", kColumn); err != nil {
			t.Fatal(err)
		}
		drive(t, func() bool { return status(t, "project", pColumn) == "active" })
		if got := status(t, "task", kColumn); got != "doing" {
			t.Fatalf("the task is %s, want doing: its changes rule starts it", got)
		}
		if got := status(t, "project", pColumn); got != "active" {
			t.Fatalf("the project is %s, want active: the task's start, heard, moves it", got)
		}
	})

	t.Run("a then whose target has no transition to its state leaves the target as it is and completes the message", func(t *testing.T) {
		p, _ := project(t, "Five")
		k, kColumn := task(t, "Done", p)
		moveTask(t, k, types.TaskStatusState_Doing)
		moveTask(t, k, types.TaskStatusState_Done)
		drive(t, drained)
		if _, err := pool.Exec(ctx, "UPDATE task SET priority = 'urgent' WHERE id = $1", kColumn); err != nil {
			t.Fatal(err)
		}
		messages := func() int {
			var n int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM task_change_queue WHERE row_key = $1", kColumn).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if n := messages(); n != 1 {
			t.Fatalf("the raw write left %d messages of the task, want its one", n)
		}
		drive(t, func() bool { return messages() == 0 })
		if n := messages(); n != 0 {
			t.Fatalf("task_change_queue holds %d messages of the task, want none: the message completes", n)
		}
		if got := status(t, "task", kColumn); got != "done" {
			t.Fatalf("the task is %s, want still done", got)
		}
	})
}
`
