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

// agentsCases are the cases agentsTest runs, by name: the end-to-end
// scenario every language's server and behavior workers of the work
// queue's behaviors on tables are held to (D104), with the core's words. A
// job a worker leases with a fencing token, which every write under the
// lease presents as Lease's precondition, retried with a backoff whose end
// moves it and once per revision; a session a device's principal beats,
// whose miss expires the device's leases on jobs; and an account whose
// meters reset hourly, count a rolling window, take a provider's observed
// usage and charge its workspace through a to-one relation.
var agentsCases = []string{
	"a worker acquires a job's lease and gets its fencing token, and a heartbeat that presents it renews the lease",
	"under requireToken a write that presents no token is refused as token_required, and the holder's write that presents it is saved",
	"a token a later lease replaced is refused as token_stale",
	"another caller's acquire of a job whose lease is active is refused as held_by_another",
	"a failure in a class with a backoff sets retryAt, and until it passes the move into running and the lease's acquire are refused as backing_off",
	"when the backoff ends its then, run by the worker, moves the waiting job back to queued",
	"a lease that lapses expires when its deadline falls due on the worker: onExpiry moves the job back to queued and the token advances",
	"expireHolder expires every lease a gone holder holds, for a caller holding jobs.override, and is forbidden without it",
	"a second build failure of one revision exhausts the job, and a new revision starts its counts again",
	"a precondition for a use whose behavior takes none is refused as invalid_argument",
	"a session's create arms its miss ttlMs on: its deadline and its queue message",
	"only the device's principal beats a session: another caller is 409 not_principal, and the device's beat moves the deadline ttlMs on",
	"a session's transition into turn arms its miss fifteen minutes from the entry (ttlByState)",
	"a due miss the worker runs moves the session to missing and expires the leases its device holds on jobs (releaseLeases)",
	"a beat recovers a missed session: missed clears and onBeat moves it back to idle",
	"an update that changes a session's device is 409 principal_fixed",
	"recordUsage charges the account's hourly meter and its workspace's through the to-one relation",
	"a reservation past the account's limit is 409 over_limit, and leaves the rows as they were",
	"an hourly meter starts again at the next hour: its next write counts from zero",
	"a rolling meter counts the last day's buckets: usage that left the window no longer counts",
	"observe without observePermission is 403 forbidden, and with it the provider's figure replaces what the meter counts until resetsAt",
	"usage that takes the account over its limit moves it out (escalate), and counts all the same",
}

// TestAgentsGoServerOnPostgres builds fixture-agents-db and
// fixture-agents-api as build-all does, then writes agentsTest into the
// generated API module and runs it, after go vet: the generated Go
// server's Lease, Retries, Presence, Budget and Workflow operations of Job,
// Session and Account (D104), called through the generated Go SDK with a
// test authenticator in front of the routes, a Job operation presenting
// Lease's fencing token in its input's preconditions member; the ORM's
// creates and updates of the tables through the core, an update of a job
// presenting the token under orm.WithPreconditions; and the queues of the
// generated behavior workers JobDeadlines and SessionPresence run in
// process with worker.RunAll, as their mains run them, a deadline made due
// by moving its message's due_at to now, as the Timers test makes one, on
// the Postgres behaviorServerDatabaseEnv names. Each caller's subject is a
// fresh UUID in its base62 wire form, as the identity runtime gives a
// principal's. It then plans each worker's entrypoint over the generated
// modules and vets it. The module links the core's static archive, which
// scripts/rules-archive.sh stages; the test skips without it, without the
// database, and in -short mode.
func TestAgentsGoServerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server and workers of Lease, Retries, Presence and Budget in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server and workers of Lease, Retries, Presence and Budget against Postgres")
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
	for _, service := range []string{"fixture-agents-db", "fixture-agents-api"} {
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
		if service == "fixture-agents-api" {
			apiSchema = schema
		} else {
			dbSchema = schema
		}
	}

	apiDir := APIDir(outputRoot, "fixture-agents-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-agents-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_agents_server_test.go"), []byte(agentsTest), 0o644); err != nil {
		t.Fatal(err)
	}
	sdkModule := naming.Default().GoSDKModule("fixture-agents-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-agents-api"},
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
	for _, name := range agentsCases {
		if want := "--- PASS: TestAgents/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}

	vetBehaviorWorkerEntrypoint(t, outputRoot, paths, apiSchema, dbSchema, env, "agents-stack", "JobDeadlines")
	vetBehaviorWorkerEntrypoint(t, outputRoot, paths, apiSchema, dbSchema, env, "agents-stack", "SessionPresence")
}

// agentsTest runs in the generated module of fixture-agents-api. It serves
// the generated routes over the ORM of fixture-agents-db, on a schema of
// its own in the Postgres behaviorServerDatabaseEnv names, with a test
// authenticator in front of them, calls them through the generated Go SDK,
// creates and updates rows through the ORM, runs the generated workers'
// queues in process, and reads the tables, their sub-tables and their
// deadline queues by SQL.
const agentsTest = `package fixtureagentsapi_test

import (
	"bytes"
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
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-agents-api"
	orm "example.com/schemas/orm/fixture-agents-db"
	sdk "example.com/schemas/sdk/go/fixture-agents-api"
	types "example.com/schemas/types/go/fixture-agents-api"
	dbtypes "example.com/schemas/types/go/fixture-agents-db"
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

// openDatabase is the ORM of fixture-agents-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends, and
// its pool, which the test reads the tables through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server and workers of Lease, Retries, Presence and Budget against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("agents_go_%d", time.Now().UnixNano())
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

// uuidOf is a subject as the ORM's UUID.
func uuidOf(t *testing.T, subject string) dbtypes.IdentityUUID {
	t.Helper()
	id, err := dbtypes.ParseIdentityUUID(subject)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// detailsJSON is a value's JSON with its keys sorted, as a refusal's
// details are compared.
func detailsJSON(t *testing.T, value any) string {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// sortedJSON is a JSON text with its keys sorted, "null" for none.
func sortedJSON(t *testing.T, raw []byte) string {
	t.Helper()
	if len(raw) == 0 {
		return "null"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return detailsJSON(t, value)
}

// apiRefused is the API's refusal err holds, failing the case when it
// holds none.
func apiRefused(t *testing.T, what string, err error) *sdk.APIError {
	t.Helper()
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: %v, want the API's refusal", what, err)
	}
	return apiErr
}

// wantAPIVetoed holds a call the server refused to the core's veto: 409,
// the code vetoed, and the veto's code and, unless vetoDetails is "", its
// details, whose JSON is vetoDetails with its keys sorted.
func wantAPIVetoed(t *testing.T, what string, err error, vetoCode, vetoDetails string) {
	t.Helper()
	apiErr := apiRefused(t, what, err)
	details := detailsJSON(t, apiErr.Details["vetoDetails"])
	if apiErr.StatusCode != 409 || apiErr.Code != "vetoed" || apiErr.Details["vetoCode"] != vetoCode || (vetoDetails != "" && details != vetoDetails) {
		t.Fatalf("%s refused with %d %s %q (vetoCode %v, vetoDetails %s), want 409 vetoed, vetoCode %s, vetoDetails %s", what, apiErr.StatusCode, apiErr.Code, apiErr.Message, apiErr.Details["vetoCode"], details, vetoCode, vetoDetails)
	}
}

// wantVetoed holds a write the repository refused to the core's veto: 409,
// the code vetoed, and the veto's code.
func wantVetoed(t *testing.T, what string, err error, vetoCode string) {
	t.Helper()
	refusal, ok := rules.AsRefusal(err)
	if !ok {
		t.Fatalf("%s: %v, want the core's refusal", what, err)
	}
	var details struct {
		VetoCode string ` + "`json:\"vetoCode\"`" + `
	}
	if err := json.Unmarshal(refusal.Details, &details); err != nil {
		t.Fatalf("%s: details %s: %v", what, refusal.Details, err)
	}
	if refusal.Status != 409 || refusal.Code != rules.CodeVetoed || details.VetoCode != vetoCode {
		t.Fatalf("%s refused with %d %s %q (details %s), want 409 vetoed, vetoCode %s", what, refusal.Status, refusal.Code, refusal.Message, refusal.Details, vetoCode)
	}
}

// within fails the case unless at, in epoch milliseconds, lies between
// from and to.
func within(t *testing.T, what string, at, from, to int64) {
	t.Helper()
	if at < from || at > to {
		t.Fatalf("%s is %d, want between %d and %d", what, at, from, to)
	}
}

func millis(at *time.Time) int64 {
	if at == nil {
		return 0
	}
	return at.UnixMilli()
}

func TestAgents(t *testing.T) {
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
	as := func(token string) *sdk.FixtureAgentsApiSDK {
		t.Helper()
		client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		client.SetToken(token)
		return client
	}
	// The worker leases jobs, other is any other caller, a device beats its
	// sessions, and observer holds accounts.observe.
	workerSubject, other, device, observer := subject(t), subject(t), subject(t), subject(t)
	asWorker, asOther, asDevice, asObserver := as(workerSubject), as(other), as(device), as(observer+":accounts.observe")
	now := func() int64 { return time.Now().UnixMilli() }

	// token is Lease's precondition presenting the fencing token n, as a Job
	// operation's input carries it in its member preconditions.
	token := func(n int64) types.InputField[*types.JobPreconditions] {
		return types.InputField[*types.JobPreconditions]{Set: true, Value: &types.JobPreconditions{
			Lease: types.InputField[*types.JobLeasePrecondition]{Set: true, Value: &types.JobLeasePrecondition{Token: types.GenericInt64(n)}},
		}}
	}
	// calling is ctx whose repository commands run as caller.
	calling := func(caller string) context.Context {
		return orm.WithBehaviorCaller(ctx, rules.Caller{Subject: caller})
	}
	// presenting is ctx whose repository commands run as caller presenting
	// Lease's fencing token n (D104).
	presenting := func(caller string, n int64) context.Context {
		return orm.WithPreconditions(calling(caller), rules.Preconditions{"Lease": orm.JobLeasePrecondition{Token: n}})
	}
	// drive runs a worker's queues in process, as its main does, until done
	// holds or ten seconds pass, polling every 100 ms.
	drive := func(t *testing.T, queues []worker.Queue, done func() bool) {
		t.Helper()
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
			t.Fatalf("the worker: %v", err)
		}
	}
	// due makes a row's live deadline message of a name due now, in a
	// table's deadline queue, as the Timers tests make one.
	due := func(t *testing.T, queue, column, name string) {
		t.Helper()
		tag, err := pool.Exec(ctx, "UPDATE "+queue+" SET due_at = now() WHERE row_key = $1 AND name = $2 AND state IN ('ready', 'claimed')", column, name)
		if err != nil {
			t.Fatal(err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("%s holds %d live %s messages of %s, want one", queue, tag.RowsAffected(), name, column)
		}
	}

	// job creates a job through the repository at revision 1 and returns
	// its key in its wire form, in its column's, and as the ORM's.
	job := func(t *testing.T) (string, string, dbtypes.IdentityUUID) {
		t.Helper()
		row, err := db.GetJobRepository().CreateOne(ctx, &orm.JobCreate{Title: "Build the site", Revision: 1})
		if err != nil {
			t.Fatalf("create a job: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String(), *row.Id
	}
	// jobRow is a job's lease, retries and status as its columns hold them.
	type jobRow struct {
		holder             *string
		token, expiries    *int64
		ended              []byte
		expiresAt, retryAt *time.Time
		total              *int64
		exhausted          *bool
		keyedBy            *string
		classAttempts      []byte
		status             string
		version            int64
	}
	readJob := func(t *testing.T, column string) jobRow {
		t.Helper()
		var r jobRow
		if err := pool.QueryRow(ctx, "SELECT lease_holder, lease_token, lease_expiries, lease_ended, lease_expires_at, retries_retry_at, retries_total, retries_exhausted, retries_keyed_by, retries_class_attempts, status, _version FROM job WHERE id = $1", column).
			Scan(&r.holder, &r.token, &r.expiries, &r.ended, &r.expiresAt, &r.retryAt, &r.total, &r.exhausted, &r.keyedBy, &r.classAttempts, &r.status, &r.version); err != nil {
			t.Fatal(err)
		}
		return r
	}
	unmovedJob := func(t *testing.T, what, column string, version int64) {
		t.Helper()
		if got := readJob(t, column).version; got != version {
			t.Fatalf("the job is at version %d after %s, want %d: a refused write writes nothing", got, what, version)
		}
	}
	acquire := func(t *testing.T, client *sdk.FixtureAgentsApiSDK, key string) types.JobLeaseAcquireResult {
		t.Helper()
		acquired, err := client.JobNamespace.Lease.Acquire(ctx, key, types.JobLeaseAcquireInput{})
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		return acquired
	}
	move := func(t *testing.T, client *sdk.FixtureAgentsApiSDK, key string, to types.JobStatusState, n int64) {
		t.Helper()
		if _, err := client.JobNamespace.Status.Transition(ctx, key, types.JobStatusTransitionInput{To: to, Preconditions: token(n)}); err != nil {
			t.Fatalf("the move to %s presenting token %d: %v", to, n, err)
		}
	}
	release := func(t *testing.T, client *sdk.FixtureAgentsApiSDK, key string, n int64) {
		t.Helper()
		if _, err := client.JobNamespace.Lease.Release(ctx, key, types.JobLeaseReleaseInput{Preconditions: token(n)}); err != nil {
			t.Fatalf("release presenting token %d: %v", n, err)
		}
	}
	record := func(client *sdk.FixtureAgentsApiSDK, key string, failure types.JobRetriesClass, n int64) (types.JobRetriesRecordAttemptResult, error) {
		return client.JobNamespace.Retries.RecordAttempt(ctx, key, types.JobRetriesRecordAttemptInput{
			Failure:       types.InputField[*types.JobRetriesClass]{Set: true, Value: &failure},
			Preconditions: token(n),
		})
	}
	title := func(value string) *string { return &value }

	t.Run("a worker acquires a job's lease and gets its fencing token, and a heartbeat that presents it renews the lease", func(t *testing.T) {
		key, column, _ := job(t)
		before := now()
		acquired := acquire(t, asWorker, key)
		after := now()
		if acquired.Token != 1 || acquired.HeartbeatMs != 20000 {
			t.Fatalf("acquire answered %+v, want token 1 and a heartbeat every 20000 ms", acquired)
		}
		within(t, "the lease's expiresAt", int64(acquired.ExpiresAt), before+60000, after+60000)
		renewed, err := asWorker.JobNamespace.Lease.Heartbeat(ctx, key, types.JobLeaseHeartbeatInput{Preconditions: token(1)})
		if err != nil {
			t.Fatalf("the heartbeat presenting token 1: %v", err)
		}
		if len(renewed.Directives) != 0 || renewed.ExpiresAt < acquired.ExpiresAt {
			t.Fatalf("the heartbeat answered %+v, want no directives and the lease renewed from %d", renewed, acquired.ExpiresAt)
		}
		row := readJob(t, column)
		if row.holder == nil || *row.holder != workerSubject || row.token == nil || *row.token != 1 {
			t.Fatalf("the job's lease is held by %v at token %v, want the worker at token 1", row.holder, row.token)
		}
	})

	t.Run("under requireToken a write that presents no token is refused as token_required, and the holder's write that presents it is saved", func(t *testing.T) {
		key, column, id := job(t)
		acquire(t, asWorker, key)
		before := readJob(t, column).version
		_, err := asWorker.JobNamespace.Status.Transition(ctx, key, types.JobStatusTransitionInput{To: types.JobStatusState_Running})
		wantAPIVetoed(t, "the worker's move with no token", err, "token_required", "")
		unmovedJob(t, "the worker's move with no token", column, before)
		moved, err := asWorker.JobNamespace.Status.Transition(ctx, key, types.JobStatusTransitionInput{To: types.JobStatusState_Running, Preconditions: token(1)})
		if err != nil {
			t.Fatalf("the worker's move presenting token 1: %v", err)
		}
		if moved.From != types.JobStatusState_Queued || moved.To != types.JobStatusState_Running {
			t.Fatalf("the move answered %+v, want from queued to running", moved)
		}
		before = readJob(t, column).version
		_, err = db.GetJobRepository().UpdateOne(calling(workerSubject), id, &orm.JobUpdate{Title: title("Mine")})
		wantVetoed(t, "the worker's update with no token", err, "token_required")
		unmovedJob(t, "the worker's update with no token", column, before)
		saved, err := db.GetJobRepository().UpdateOne(presenting(workerSubject, 1), id, &orm.JobUpdate{Title: title("Mine")})
		if err != nil {
			t.Fatalf("the worker's update presenting token 1: %v", err)
		}
		if saved.Title != "Mine" {
			t.Fatalf("the job's title is %q, want Mine", saved.Title)
		}
	})

	t.Run("a token a later lease replaced is refused as token_stale", func(t *testing.T) {
		key, column, id := job(t)
		acquire(t, asWorker, key)
		release(t, asWorker, key, 1)
		if again := acquire(t, asOther, key); again.Token != 3 {
			t.Fatalf("the other caller's acquire answered token %d, want 3", again.Token)
		}
		before := readJob(t, column).version
		_, err := asWorker.JobNamespace.Lease.Heartbeat(ctx, key, types.JobLeaseHeartbeatInput{Preconditions: token(1)})
		wantAPIVetoed(t, "the worker's heartbeat presenting token 1", err, "token_stale", "{\"current\":3,\"token\":1}")
		_, err = db.GetJobRepository().UpdateOne(presenting(workerSubject, 1), id, &orm.JobUpdate{Title: title("Late")})
		wantVetoed(t, "the worker's update presenting token 1", err, "token_stale")
		unmovedJob(t, "the stale writes", column, before)
	})

	t.Run("another caller's acquire of a job whose lease is active is refused as held_by_another", func(t *testing.T) {
		key, column, _ := job(t)
		held := acquire(t, asOther, key)
		before := readJob(t, column).version
		_, err := asWorker.JobNamespace.Lease.Acquire(ctx, key, types.JobLeaseAcquireInput{})
		wantAPIVetoed(t, "the worker's acquire", err, "held_by_another", fmt.Sprintf("{\"expiresAt\":%d}", held.ExpiresAt))
		unmovedJob(t, "the worker's acquire", column, before)
	})

	t.Run("a failure in a class with a backoff sets retryAt, and until it passes the move into running and the lease's acquire are refused as backing_off", func(t *testing.T) {
		key, column, _ := job(t)
		acquire(t, asWorker, key)
		move(t, asWorker, key, types.JobStatusState_Running, 1)
		before := now()
		failed, err := record(asWorker, key, types.JobRetriesClass_Timeout, 1)
		after := now()
		if err != nil {
			t.Fatalf("the timeout: %v", err)
		}
		if failed.Exhausted || failed.RetryAt == nil {
			t.Fatalf("the timeout answered %+v, want not exhausted, with retryAt", failed)
		}
		within(t, "retryAt", int64(*failed.RetryAt), before+60000, after+60000)
		release(t, asWorker, key, 1)
		if got := readJob(t, column).status; got != "queued" {
			t.Fatalf("the job is %s after the release, want queued (onExpiry)", got)
		}
		waits := fmt.Sprintf("{\"retryAt\":%d}", *failed.RetryAt)
		version := readJob(t, column).version
		_, err = asOther.JobNamespace.Status.Transition(ctx, key, types.JobStatusTransitionInput{To: types.JobStatusState_Running})
		wantAPIVetoed(t, "the other caller's move into running", err, "backing_off", waits)
		_, err = asOther.JobNamespace.Lease.Acquire(ctx, key, types.JobLeaseAcquireInput{})
		wantAPIVetoed(t, "the other caller's acquire", err, "backing_off", waits)
		unmovedJob(t, "the moves that wait", column, version)
	})

	t.Run("when the backoff ends its then, run by the worker, moves the waiting job back to queued", func(t *testing.T) {
		key, column, _ := job(t)
		acquire(t, asWorker, key)
		move(t, asWorker, key, types.JobStatusState_Running, 1)
		failed, err := record(asWorker, key, types.JobRetriesClass_Timeout, 1)
		if err != nil || failed.RetryAt == nil {
			t.Fatalf("the timeout = %+v, %v, want retryAt", failed, err)
		}
		move(t, asWorker, key, types.JobStatusState_Waiting, 1)
		release(t, asWorker, key, 1)
		if got := readJob(t, column).status; got != "waiting" {
			t.Fatalf("the job is %s after the release, want waiting", got)
		}
		due(t, "job_deadline_queue", column, "retry")
		drive(t, api.JobDeadlinesQueues(db, zap.NewNop()), func() bool { return readJob(t, column).status == "queued" })
		row := readJob(t, column)
		if row.status != "queued" {
			t.Fatalf("the job is %s, want queued within ten seconds", row.status)
		}
		if millis(row.retryAt) != int64(*failed.RetryAt) {
			t.Fatalf("the job's retryAt is %d, want %d kept", millis(row.retryAt), *failed.RetryAt)
		}
	})

	t.Run("a lease that lapses expires when its deadline falls due on the worker: onExpiry moves the job back to queued and the token advances", func(t *testing.T) {
		key, column, _ := job(t)
		acquired := acquire(t, asWorker, key)
		move(t, asWorker, key, types.JobStatusState_Running, 1)
		due(t, "job_deadline_queue", column, "expire")
		drive(t, api.JobDeadlinesQueues(db, zap.NewNop()), func() bool { return readJob(t, column).status == "queued" })
		row := readJob(t, column)
		if row.status != "queued" {
			t.Fatalf("the job is %s, want queued within ten seconds", row.status)
		}
		if row.holder != nil || row.token == nil || *row.token != 2 || row.expiries == nil || *row.expiries != 1 {
			t.Fatalf("the job's lease is held by %v at token %v after %v expiries, want none at token 2 after 1", row.holder, row.token, row.expiries)
		}
		if got, want := sortedJSON(t, row.ended), fmt.Sprintf("{\"at\":%d,\"reason\":\"ttl\"}", acquired.ExpiresAt); got != want {
			t.Fatalf("the lease ended %s, want %s", got, want)
		}
	})

	t.Run("expireHolder expires every lease a gone holder holds, for a caller holding jobs.override, and is forbidden without it", func(t *testing.T) {
		// A worker of its own, so no other case's lease is the gone
		// holder's, leases two jobs and moves them to running; another
		// worker leases a third.
		gone := subject(t)
		asGone := as(gone)
		var keys, columns []string
		for range 2 {
			key, column, _ := job(t)
			acquire(t, asGone, key)
			move(t, asGone, key, types.JobStatusState_Running, 1)
			keys = append(keys, key)
			columns = append(columns, column)
		}
		otherKey, otherColumn, _ := job(t)
		acquire(t, asOther, otherKey)
		move(t, asOther, otherKey, types.JobStatusState_Running, 1)
		versions := map[string]int64{}
		for _, column := range append(slices.Clone(columns), otherColumn) {
			versions[column] = readJob(t, column).version
		}
		// expireHolder is an operation of Job's table as a whole (D104): its
		// route and its SDK method take no key.
		var input types.JobLeaseExpireHolderInput
		if err := json.Unmarshal([]byte(fmt.Sprintf("{\"holder\":%q}", gone)), &input); err != nil {
			t.Fatal(err)
		}
		operator := subject(t)
		_, err := as(operator).JobNamespace.Lease.ExpireHolder(ctx, input)
		apiErr := apiRefused(t, "an expireHolder without jobs.override", err)
		if want := operator + " may not expire the leases of a holder on Job: it needs permission jobs.override"; apiErr.StatusCode != 403 || apiErr.Code != "forbidden" || apiErr.Message != want {
			t.Fatalf("an expireHolder without jobs.override refused with %d %s %q, want 403 forbidden %q", apiErr.StatusCode, apiErr.Code, apiErr.Message, want)
		}
		for column, version := range versions {
			unmovedJob(t, "an expireHolder without jobs.override", column, version)
		}
		result, err := as(operator+":jobs.override").JobNamespace.Lease.ExpireHolder(ctx, input)
		if err != nil {
			t.Fatalf("expireHolder: %v", err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var expired struct {
			Expired int64            ` + "`json:\"expired\"`" + `
			Reasons map[string]int64 ` + "`json:\"reasons\"`" + `
			Ids     []string         ` + "`json:\"ids\"`" + `
		}
		if err := json.Unmarshal(data, &expired); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		// A UUID key pages in UUID order on Postgres, so ids is a set here.
		ids := slices.Sorted(slices.Values(expired.Ids))
		if expired.Expired != 2 || len(expired.Reasons) != 1 || expired.Reasons["holder"] != 2 || !slices.Equal(ids, slices.Sorted(slices.Values(keys))) {
			t.Fatalf("expireHolder answered %s, want 2 expired by holder, the jobs %q", data, keys)
		}
		for _, column := range columns {
			row := readJob(t, column)
			if row.status != "queued" || row.holder != nil || row.token == nil || *row.token != 2 {
				t.Fatalf("an expired job is %s, held by %v at token %v, want queued (onExpiry), held by none, at token 2", row.status, row.holder, row.token)
			}
		}
		if row := readJob(t, otherColumn); row.version != versions[otherColumn] || row.status != "running" || row.holder == nil || *row.holder != other {
			t.Fatalf("the other worker's job is %s at version %d, held by %v, want it untouched", row.status, row.version, row.holder)
		}
	})

	t.Run("a second build failure of one revision exhausts the job, and a new revision starts its counts again", func(t *testing.T) {
		key, column, id := job(t)
		acquire(t, asWorker, key)
		move(t, asWorker, key, types.JobStatusState_Running, 1)
		first, err := record(asWorker, key, types.JobRetriesClass_Build, 1)
		if err != nil || first.Exhausted || first.ClassAttempts["build"] != 1 {
			t.Fatalf("the first build failure = %+v, %v, want not exhausted, one build", first, err)
		}
		second, err := record(asWorker, key, types.JobRetriesClass_Build, 1)
		if err != nil || !second.Exhausted {
			t.Fatalf("the second build failure = %+v, %v, want exhausted", second, err)
		}
		if got := readJob(t, column).status; got != "failed" {
			t.Fatalf("the job is %s, want failed", got)
		}
		release(t, asWorker, key, 1)
		before := readJob(t, column).version
		_, err = asOther.JobNamespace.Status.Transition(ctx, key, types.JobStatusTransitionInput{To: types.JobStatusState_Queued})
		wantAPIVetoed(t, "the move of the exhausted job", err, "exhausted", "")
		unmovedJob(t, "the move of the exhausted job", column, before)
		revision := dbtypes.GenericInt64(2)
		if _, err := db.GetJobRepository().UpdateOne(calling(other), id, &orm.JobUpdate{Revision: &revision}); err != nil {
			t.Fatalf("the update to revision 2: %v", err)
		}
		row := readJob(t, column)
		if row.exhausted == nil || *row.exhausted || row.total == nil || *row.total != 0 || row.keyedBy == nil || *row.keyedBy != "2" {
			t.Fatalf("the job's retries are exhausted %v, total %v, keyed by %v, want false, 0 and 2", row.exhausted, row.total, row.keyedBy)
		}
		if _, err := asOther.JobNamespace.Status.Transition(ctx, key, types.JobStatusTransitionInput{To: types.JobStatusState_Queued}); err != nil {
			t.Fatalf("the move to queued at revision 2: %v", err)
		}
	})

	t.Run("a precondition for a use whose behavior takes none is refused as invalid_argument", func(t *testing.T) {
		key, column, id := job(t)
		before := readJob(t, column).version
		_, err := db.GetJobRepository().UpdateOne(orm.WithPreconditions(calling(workerSubject), rules.Preconditions{"Retries": map[string]int64{"token": 1}}), id, &orm.JobUpdate{Title: title("Mine")})
		refusal, ok := rules.AsRefusal(err)
		want := "preconditions of Job: /Retries: behavior Retries takes no precondition"
		if !ok || refusal.Status != 400 || refusal.Code != rules.CodeInvalidArgument || refusal.Class != "PreconditionsError" || refusal.Message != want {
			t.Fatalf("the update presenting Retries' precondition: %v, want 400 invalid_argument PreconditionsError %q", err, want)
		}
		unmovedJob(t, "the update presenting Retries' precondition", column, before)
		request, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/job/"+key+"/lease/heartbeat", bytes.NewReader([]byte("{\"preconditions\":{\"Lease\":{\"token\":-1}}}")))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+workerSubject)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("a heartbeat presenting token -1 answered %d, want the route's 400", response.StatusCode)
		}
	})

	// sessionOf creates a session of a device through the repository and
	// returns its key in its wire form, in its column's, and as the ORM's.
	sessionOf := func(t *testing.T, device string) (string, string, dbtypes.IdentityUUID) {
		t.Helper()
		row, err := db.GetSessionRepository().CreateOne(ctx, &orm.SessionCreate{Device: uuidOf(t, device), Agent: "planner"})
		if err != nil {
			t.Fatalf("create a session: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String(), *row.Id
	}
	// sessionRow is a session's presence and status as its columns hold
	// them.
	type sessionRow struct {
		deadline, lastBeatAt, enteredAt *time.Time
		missed                          *bool
		released                        []byte
		status                          string
		version                         int64
	}
	readSession := func(t *testing.T, column string) sessionRow {
		t.Helper()
		var r sessionRow
		if err := pool.QueryRow(ctx, "SELECT presence_deadline, presence_last_beat_at, status_entered_at, presence_missed, presence_released, status, _version FROM session WHERE id = $1", column).
			Scan(&r.deadline, &r.lastBeatAt, &r.enteredAt, &r.missed, &r.released, &r.status, &r.version); err != nil {
			t.Fatal(err)
		}
		return r
	}
	// misses is the times of a session's live miss messages.
	misses := func(t *testing.T, column string) []int64 {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT at FROM session_deadline_queue WHERE row_key = $1 AND name = 'miss' AND state IN ('ready', 'claimed')", column)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	armedAt := func(t *testing.T, column string, at int64) {
		t.Helper()
		if got := misses(t, column); len(got) != 1 || got[0] != at {
			t.Fatalf("the session's live miss messages are at %v, want one at %d", got, at)
		}
	}

	t.Run("a session's create arms its miss ttlMs on: its deadline and its queue message", func(t *testing.T) {
		before := now()
		_, column, _ := sessionOf(t, device)
		after := now()
		row := readSession(t, column)
		within(t, "the session's deadline", millis(row.deadline), before+180000, after+180000)
		if row.missed == nil || *row.missed {
			t.Fatalf("the session's missed is %v, want false", row.missed)
		}
		armedAt(t, column, millis(row.deadline))
	})

	t.Run("only the device's principal beats a session: another caller is 409 not_principal, and the device's beat moves the deadline ttlMs on", func(t *testing.T) {
		key, column, _ := sessionOf(t, device)
		version := readSession(t, column).version
		_, err := asOther.SessionNamespace.Presence.Beat(ctx, key)
		wantAPIVetoed(t, "the other caller's beat", err, "not_principal", "")
		if got := readSession(t, column).version; got != version {
			t.Fatalf("the session is at version %d after the other caller's beat, want %d", got, version)
		}
		before := now()
		beat, err := asDevice.SessionNamespace.Presence.Beat(ctx, key)
		after := now()
		if err != nil {
			t.Fatalf("the device's beat: %v", err)
		}
		within(t, "the beat's deadline", int64(beat.Deadline), before+180000, after+180000)
		row := readSession(t, column)
		if row.lastBeatAt == nil || millis(row.deadline) != int64(beat.Deadline) {
			t.Fatalf("the session beat at %v with its deadline %d, want a beat and the deadline %d", row.lastBeatAt, millis(row.deadline), beat.Deadline)
		}
		armedAt(t, column, int64(beat.Deadline))
	})

	t.Run("a session's transition into turn arms its miss fifteen minutes from the entry (ttlByState)", func(t *testing.T) {
		key, column, _ := sessionOf(t, device)
		if _, err := asDevice.SessionNamespace.Status.Transition(ctx, key, types.SessionStatusTransitionInput{To: types.SessionStatusState_Turn}); err != nil {
			t.Fatalf("the move into turn: %v", err)
		}
		row := readSession(t, column)
		if row.status != "turn" || row.enteredAt == nil || millis(row.deadline) != millis(row.enteredAt)+900000 {
			t.Fatalf("the session is %s, entered at %v, with its deadline %d, want turn and the deadline fifteen minutes after the entry", row.status, row.enteredAt, millis(row.deadline))
		}
		armedAt(t, column, millis(row.deadline))
	})

	// The miss's device has no other session, which, present, would keep
	// its leases (D16): the device of the cases before stands for three.
	lone := subject(t)
	asLone := as(lone)
	missedKey, missedColumn := "", ""
	t.Run("a due miss the worker runs moves the session to missing and expires the leases its device holds on jobs (releaseLeases)", func(t *testing.T) {
		key, column, _ := sessionOf(t, lone)
		missedKey, missedColumn = key, column
		jobKey, jobColumn, _ := job(t)
		acquired := acquire(t, asLone, jobKey)
		if _, err := asLone.SessionNamespace.Presence.Beat(ctx, key); err != nil {
			t.Fatalf("the device's beat: %v", err)
		}
		due(t, "session_deadline_queue", column, "miss")
		drive(t, api.SessionPresenceQueues(db, zap.NewNop()), func() bool { return readSession(t, column).status == "missing" })
		row := readSession(t, column)
		if row.status != "missing" || row.missed == nil || !*row.missed {
			t.Fatalf("the session is %s, missed %v, want missing and missed within ten seconds", row.status, row.missed)
		}
		if got, want := sortedJSON(t, row.released), fmt.Sprintf("{\"Job\":[%q]}", jobKey); got != want {
			t.Fatalf("the miss released %s, want %s", got, want)
		}
		leased := readJob(t, jobColumn)
		if leased.holder != nil || leased.token == nil || *leased.token != int64(acquired.Token)+1 {
			t.Fatalf("the device's job is held by %v at token %v, want none at token %d", leased.holder, leased.token, acquired.Token+1)
		}
	})

	t.Run("a beat recovers a missed session: missed clears and onBeat moves it back to idle", func(t *testing.T) {
		if missedKey == "" {
			t.Fatal("no session was missed")
		}
		if _, err := asLone.SessionNamespace.Presence.Beat(ctx, missedKey); err != nil {
			t.Fatalf("the device's beat: %v", err)
		}
		row := readSession(t, missedColumn)
		if row.status != "idle" || row.missed == nil || *row.missed {
			t.Fatalf("the session is %s, missed %v, want idle and not missed", row.status, row.missed)
		}
	})

	t.Run("an update that changes a session's device is 409 principal_fixed", func(t *testing.T) {
		_, column, id := sessionOf(t, device)
		version := readSession(t, column).version
		moved := uuidOf(t, subject(t))
		_, err := db.GetSessionRepository().UpdateOne(calling(device), id, &orm.SessionUpdate{Device: &moved})
		wantVetoed(t, "the device's change", err, "principal_fixed")
		if got := readSession(t, column).version; got != version {
			t.Fatalf("the session is at version %d after the refused update, want %d", got, version)
		}
	})

	// account creates a workspace and an account in it through the
	// repository and returns the account's key in its wire form and in its
	// column's, and the workspace's column.
	account := func(t *testing.T) (string, string, string) {
		t.Helper()
		workspace, err := db.GetWorkspaceRepository().CreateOne(ctx, &orm.WorkspaceCreate{Name: "Studio"})
		if err != nil {
			t.Fatalf("create a workspace: %v", err)
		}
		row, err := db.GetAccountRepository().CreateOne(ctx, &orm.AccountCreate{Name: "Agent", Workspace: *workspace})
		if err != nil {
			t.Fatalf("create an account: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String(), workspace.Id.ToUUID().String()
	}
	// meter is a meter's row of a table's meters sub-table: what it has
	// used and the start of its period; held is whether the row holds one.
	type meterRow struct {
		held        bool
		used        int64
		periodStart int64
	}
	readMeter := func(t *testing.T, table, row, column, name string) meterRow {
		t.Helper()
		var m meterRow
		var used *int64
		var start *time.Time
		err := pool.QueryRow(ctx, "SELECT used, period_start FROM "+table+" WHERE "+row+" = $1 AND meter = $2", column, name).Scan(&used, &start)
		if errors.Is(err, pgx.ErrNoRows) {
			return m
		}
		if err != nil {
			t.Fatal(err)
		}
		m.held = true
		if used != nil {
			m.used = *used
		}
		m.periodStart = millis(start)
		return m
	}
	accountVersion := func(t *testing.T, column string) int64 {
		t.Helper()
		var version int64
		if err := pool.QueryRow(ctx, "SELECT _version FROM account WHERE id = $1", column).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	use := func(t *testing.T, key string, meter types.AccountBudgetMeter, amount int64) types.AccountBudgetRecordUsageResult {
		t.Helper()
		used, err := asOther.AccountNamespace.Budget.RecordUsage(ctx, key, types.AccountBudgetRecordUsageInput{Meter: meter, Amount: types.GenericInt64(amount)})
		if err != nil {
			t.Fatalf("recordUsage of %d %s: %v", amount, meter, err)
		}
		return used
	}
	reserve := func(key string, meter types.AccountBudgetMeter, amount int64) (types.AccountBudgetReserveResult, error) {
		return asOther.AccountNamespace.Budget.Reserve(ctx, key, types.AccountBudgetReserveInput{
			Meter:  types.InputField[*types.AccountBudgetMeter]{Set: true, Value: &meter},
			Amount: types.InputField[types.GenericInt64]{Set: true, Value: types.GenericInt64(amount)},
		})
	}
	// hour is the start of the UTC hour of an instant.
	hour := func(at int64) int64 { return time.UnixMilli(at).UTC().Truncate(time.Hour).UnixMilli() }
	// inHour fails the case unless a meter's period starts at the UTC hour
	// of the call, made between from and to.
	inHour := func(t *testing.T, what string, m meterRow, from, to int64) {
		t.Helper()
		if m.periodStart != hour(from) && m.periodStart != hour(to) {
			t.Fatalf("%s's period starts at %d, want the UTC hour of the call, %d", what, m.periodStart, hour(from))
		}
	}

	t.Run("recordUsage charges the account's hourly meter and its workspace's through the to-one relation", func(t *testing.T) {
		key, column, workspace := account(t)
		before := now()
		used := use(t, key, types.AccountBudgetMeter_Questions, 1)
		after := now()
		if got := detailsJSON(t, used); got != "{\"directed\":false,\"meter\":\"questions\",\"overruns\":[],\"released\":0,\"used\":1}" {
			t.Fatalf("recordUsage answered %s, want questions used 1 and nothing over", got)
		}
		own := readMeter(t, "account_budget_meters", "account_id", column, "questions")
		scope := readMeter(t, "workspace_budget_meters", "workspace_id", workspace, "questions")
		if !own.held || own.used != 1 || !scope.held || scope.used != 1 {
			t.Fatalf("the account's questions are %+v and its workspace's %+v, want each used 1", own, scope)
		}
		inHour(t, "the account's questions", own, before, after)
		inHour(t, "the workspace's questions", scope, before, after)
	})

	t.Run("a reservation past the account's limit is 409 over_limit, and leaves the rows as they were", func(t *testing.T) {
		key, column, workspace := account(t)
		use(t, key, types.AccountBudgetMeter_Questions, 1)
		version := accountVersion(t, column)
		own, scope := readMeter(t, "account_budget_meters", "account_id", column, "questions"), readMeter(t, "workspace_budget_meters", "workspace_id", workspace, "questions")
		_, err := reserve(key, types.AccountBudgetMeter_Questions, 40)
		wantAPIVetoed(t, "the reservation of 40 questions", err, "over_limit",
			fmt.Sprintf("{\"amount\":40,\"limit\":40,\"meter\":\"questions\",\"remaining\":39,\"scope\":{\"id\":%q,\"schema\":\"Account\"}}", key))
		if got := accountVersion(t, column); got != version {
			t.Fatalf("the account is at version %d after the refused reservation, want %d", got, version)
		}
		if got := readMeter(t, "account_budget_meters", "account_id", column, "questions"); got != own {
			t.Fatalf("the account's questions are %+v after the refused reservation, want %+v", got, own)
		}
		if got := readMeter(t, "workspace_budget_meters", "workspace_id", workspace, "questions"); got != scope {
			t.Fatalf("the workspace's questions are %+v after the refused reservation, want %+v", got, scope)
		}
	})

	t.Run("an hourly meter starts again at the next hour: its next write counts from zero", func(t *testing.T) {
		key, column, _ := account(t)
		use(t, key, types.AccountBudgetMeter_Questions, 1)
		// A repair past the core, as an operator's SQL: the period started
		// an hour before.
		if _, err := pool.Exec(ctx, "UPDATE account_budget_meters SET period_start = period_start - interval '1 hour' WHERE account_id = $1 AND meter = 'questions'", column); err != nil {
			t.Fatal(err)
		}
		before := now()
		used := use(t, key, types.AccountBudgetMeter_Questions, 1)
		after := now()
		if used.Used != 1 {
			t.Fatalf("recordUsage answered %+v, want used 1 in the new hour", used)
		}
		inHour(t, "the account's questions", readMeter(t, "account_budget_meters", "account_id", column, "questions"), before, after)
	})

	t.Run("a rolling meter counts the last day's buckets: usage that left the window no longer counts", func(t *testing.T) {
		key, column, _ := account(t)
		use(t, key, types.AccountBudgetMeter_Tokens, 900000)
		var buckets int
		var startsAt time.Time
		if err := pool.QueryRow(ctx, "SELECT count(*), min(starts_at) FROM account_budget_buckets WHERE account_id = $1", column).Scan(&buckets, &startsAt); err != nil {
			t.Fatal(err)
		}
		if buckets != 1 || startsAt.UnixMilli()%1440000 != 0 {
			t.Fatalf("the account holds %d buckets starting at %d, want one, at a multiple of 24 minutes", buckets, startsAt.UnixMilli())
		}
		_, err := reserve(key, types.AccountBudgetMeter_Tokens, 200000)
		wantAPIVetoed(t, "the reservation of 200000 tokens", err, "over_limit", "")
		if remaining := apiRefused(t, "the reservation", err).Details["vetoDetails"].(map[string]any)["remaining"]; remaining != float64(100000) {
			t.Fatalf("the reservation's remaining is %v, want 100000", remaining)
		}
		// A repair past the core: the usage was two days ago.
		if _, err := pool.Exec(ctx, "UPDATE account_budget_buckets SET starts_at = starts_at - interval '2 days' WHERE account_id = $1", column); err != nil {
			t.Fatal(err)
		}
		reserved, err := reserve(key, types.AccountBudgetMeter_Tokens, 200000)
		if err != nil {
			t.Fatalf("the reservation of 200000 tokens after the window moved on: %v", err)
		}
		if reserved.Reserved["tokens"] != 200000 {
			t.Fatalf("the reservation answered %+v, want 200000 tokens", reserved)
		}
	})

	t.Run("observe without observePermission is 403 forbidden, and with it the provider's figure replaces what the meter counts until resetsAt", func(t *testing.T) {
		key, _, _ := account(t)
		resetsAt := now() + 3600000
		input := types.AccountBudgetObserveInput{
			Meter:    types.AccountBudgetMeter_Tokens,
			Used:     990000,
			Limit:    types.InputField[types.GenericInt64]{Set: true, Value: 1000000},
			ResetsAt: types.InputField[types.GenericInt64]{Set: true, Value: types.GenericInt64(resetsAt)},
		}
		_, err := asOther.AccountNamespace.Budget.Observe(ctx, key, input)
		if refused := apiRefused(t, "the other caller's observe", err); refused.StatusCode != 403 || refused.Code != "forbidden" {
			t.Fatalf("the other caller's observe refused with %d %s %q, want 403 forbidden", refused.StatusCode, refused.Code, refused.Message)
		}
		observed, err := asObserver.AccountNamespace.Budget.Observe(ctx, key, input)
		if err != nil {
			t.Fatalf("the observer's observe: %v", err)
		}
		if observed.Meter != types.AccountBudgetMeter_Tokens || observed.Used != 990000 || observed.Limit == nil || *observed.Limit != 1000000 || observed.ResetsAt == nil || int64(*observed.ResetsAt) != resetsAt {
			t.Fatalf("observe answered %+v, want tokens used 990000 of 1000000 until %d", observed, resetsAt)
		}
		_, err = reserve(key, types.AccountBudgetMeter_Tokens, 20000)
		wantAPIVetoed(t, "the reservation of 20000 tokens", err, "over_limit", "")
		if remaining := apiRefused(t, "the reservation", err).Details["vetoDetails"].(map[string]any)["remaining"]; remaining != float64(10000) {
			t.Fatalf("the reservation's remaining is %v, want 10000", remaining)
		}
		if used := use(t, key, types.AccountBudgetMeter_Tokens, 5000); used.Used != 995000 {
			t.Fatalf("recordUsage answered %+v, want used 995000", used)
		}
	})

	t.Run("usage that takes the account over its limit moves it out (escalate), and counts all the same", func(t *testing.T) {
		key, column, _ := account(t)
		used := use(t, key, types.AccountBudgetMeter_Questions, 41)
		if len(used.Overruns) != 1 || used.Used != 41 {
			t.Fatalf("recordUsage answered %+v, want 41 used and one overrun", used)
		}
		if got, want := detailsJSON(t, used.Overruns[0]), fmt.Sprintf("{\"escalated\":true,\"id\":%q,\"limit\":40,\"schema\":\"Account\",\"used\":41}", key); got != want {
			t.Fatalf("the overrun is %s, want %s", got, want)
		}
		var status string
		if err := pool.QueryRow(ctx, "SELECT status FROM account WHERE id = $1", column).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "out" {
			t.Fatalf("the account is %s, want out", status)
		}
	})
}
`
