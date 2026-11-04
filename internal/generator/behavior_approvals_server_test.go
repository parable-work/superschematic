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

// approvalsCases are the cases approvalsTest runs, by name: the end-to-end
// scenario every language's server of a table that implements Constants,
// StateRules and Approvals beside its Workflow is held to (D104), with the
// core's words: a field that moves one way, a field a state freezes but for
// the subject a field of the row holds, a gate whose sign-offs a change of
// the proof consumes and that pins the version its signer read, and a
// field its create sets for good.
var approvalsCases = []string{
	"a oneWay field moved back is refused as one_way, and moved forward is saved",
	"a caller holding tasks.admin moves a oneWay field back",
	"the creator edits the plan while the task is planned",
	"another caller editing the plan while the task is planned is refused as frozen_fields",
	"a move into proving without its sign-offs is refused as approvals_missing",
	"a pinned signOff with no version is refused as invalid_argument, with a stale one as stale, and with the row's version is saved",
	"editing the proof consumes the done gate's sign-offs and keeps the proving gate's",
	"an update of the owner, a constant, is refused as invalid_instance",
}

// TestApprovalsGoServerOnPostgres builds fixture-approvals-db and
// fixture-approvals-api as build-all does, then writes approvalsTest into
// the generated API module and runs it, after go vet: the generated Go
// server's Approvals and Workflow operations of Task (D104), called through
// the generated Go SDK with a test authenticator in front of the routes,
// and the ORM's creates and updates of Task through the core, on the
// Postgres behaviorServerDatabaseEnv names. Each caller's subject is a
// fresh UUID in its base62 wire form, as the identity runtime gives a
// principal's; each task is created through the ORM acting as alice, which
// stamps createdBy, and updated through it as a behavior caller
// (WithBehaviorCaller) with the permissions the case names. The module
// links the core's static archive, which scripts/rules-archive.sh stages;
// the test skips without it, without the database, and in -short mode.
func TestApprovalsGoServerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server of Constants, StateRules and Approvals in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server of Constants, StateRules and Approvals against Postgres")
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
	for _, service := range []string{"fixture-approvals-db", "fixture-approvals-api"} {
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
	}

	apiDir := APIDir(outputRoot, "fixture-approvals-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-approvals-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_approvals_server_test.go"), []byte(approvalsTest), 0o644); err != nil {
		t.Fatal(err)
	}

	// The test reaches the Go SDK beside what the API module requires, and
	// links the four archives a Go binary of the checkout links.
	sdkModule := naming.Default().GoSDKModule("fixture-approvals-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-approvals-api"},
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
	for _, name := range approvalsCases {
		if want := "--- PASS: TestApprovals/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}

// approvalsTest runs in the generated module of fixture-approvals-api. It
// serves the generated routes over the ORM of fixture-approvals-db, on a
// schema of its own in the Postgres behaviorServerDatabaseEnv names, with a
// test authenticator in front of them, and calls them through the
// generated Go SDK; it creates and updates tasks through the ORM.
const approvalsTest = `package fixtureapprovalsapi_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
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
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-approvals-api"
	orm "example.com/schemas/orm/fixture-approvals-db"
	sdk "example.com/schemas/sdk/go/fixture-approvals-api"
	types "example.com/schemas/types/go/fixture-approvals-api"
	dbtypes "example.com/schemas/types/go/fixture-approvals-db"
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

// tasks is the project's implementation of reassignTask, the one operation
// no generated handler serves: the update of reviewer and owner through the
// ORM, which no case calls (the build warns that it lets a caller name
// itself the reviewer and then edit the plan).
type tasks struct{ db orm.DatabaseInterface }

func (s tasks) ReassignTask(ctx context.Context, id types.IdentityUUID, input *types.TaskAssignment) (*types.Task, error) {
	row, err := s.db.GetTaskRepository().UpdateOne(ctx, id, &orm.TaskUpdate{Reviewer: input.Reviewer, Owner: input.Owner})
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("task", err)
	}
	return row, err
}

// openDatabase is the ORM of fixture-approvals-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends, and
// its pool, which the test reads the tables through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server of Constants, StateRules and Approvals against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("approvals_go_%d", time.Now().UnixNano())
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

// wantAPIVetoed holds a call the server refused to the core's veto: 409,
// the code vetoed, and the veto's code and details, whose JSON is
// vetoDetails with its keys sorted.
func wantAPIVetoed(t *testing.T, what string, err error, vetoCode, vetoDetails string) {
	t.Helper()
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: %v, want the API's refusal", what, err)
	}
	details := detailsJSON(t, apiErr.Details["vetoDetails"])
	if apiErr.StatusCode != 409 || apiErr.Code != "vetoed" || apiErr.Details["vetoCode"] != vetoCode || details != vetoDetails {
		t.Fatalf("%s refused with %d %s %q (vetoCode %v, vetoDetails %s), want 409 vetoed, vetoCode %s, vetoDetails %s", what, apiErr.StatusCode, apiErr.Code, apiErr.Message, apiErr.Details["vetoCode"], details, vetoCode, vetoDetails)
	}
}

// wantVetoed holds an update the repository refused to the core's veto:
// 409, the code vetoed, the veto's code, reason and details, whose JSON is
// vetoDetails with its keys sorted.
func wantVetoed(t *testing.T, what string, err error, vetoCode, reason, vetoDetails string) {
	t.Helper()
	refusal, ok := rules.AsRefusal(err)
	if !ok {
		t.Fatalf("%s: %v, want the core's refusal", what, err)
	}
	var details struct {
		VetoCode    string          ` + "`json:\"vetoCode\"`" + `
		Reason      string          ` + "`json:\"reason\"`" + `
		VetoDetails json.RawMessage ` + "`json:\"vetoDetails\"`" + `
	}
	if err := json.Unmarshal(refusal.Details, &details); err != nil {
		t.Fatalf("%s: details %s: %v", what, refusal.Details, err)
	}
	var sorted any
	if err := json.Unmarshal(details.VetoDetails, &sorted); err != nil {
		t.Fatalf("%s: vetoDetails %s: %v", what, details.VetoDetails, err)
	}
	if got := detailsJSON(t, sorted); refusal.Status != 409 || refusal.Code != rules.CodeVetoed || details.VetoCode != vetoCode || details.Reason != reason || got != vetoDetails {
		t.Fatalf("%s refused with %d %s (vetoCode %s, reason %q, vetoDetails %s), want 409 vetoed, vetoCode %s, reason %q, vetoDetails %s", what, refusal.Status, refusal.Code, details.VetoCode, details.Reason, got, vetoCode, reason, vetoDetails)
	}
}

func TestApprovals(t *testing.T) {
	ctx := context.Background()
	db, pool := openDatabase(t)
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(authenticate)
	if err := api.RegisterRoutes(router, api.Config{
		DB:              db,
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{TaskAdmin: tasks{db: db}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	as := func(token string) *sdk.FixtureApprovalsApiSDK {
		t.Helper()
		client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		client.SetToken(token)
		return client
	}
	// alice creates each task, bob owns it, carol reviews it and holds
	// tasks.review, and dave holds tasks.admin.
	alice, bob, carol, dave := subject(t), subject(t), subject(t), subject(t)
	asBob, asCarol := as(bob), as(carol+":tasks.review")
	bobID, carolID, daveID := uuidOf(t, bob), uuidOf(t, carol), uuidOf(t, dave)

	// task creates a planned task through the ORM acting as alice, which
	// stamps createdBy, owned by bob and reviewed by carol, and returns its
	// key in its wire form and its ORM's.
	task := func(t *testing.T) (string, dbtypes.IdentityUUID) {
		t.Helper()
		row, err := db.GetTaskRepository().CreateOne(orm.WithCaller(ctx, alice), &orm.TaskCreate{
			Title: "Launch", Owner: bobID, Reviewer: &carolID, Plan: "First plan", Urgent: false, Attempts: 0,
		})
		if err != nil {
			t.Fatalf("create a task: %v", err)
		}
		return row.Id.String(), *row.Id
	}
	// update updates a task through the ORM as a behavior caller holding
	// permissions.
	update := func(id dbtypes.IdentityUUID, caller string, patch *orm.TaskUpdate, permissions ...string) (*dbtypes.Task, error) {
		return db.GetTaskRepository().UpdateOne(orm.WithBehaviorCaller(ctx, rules.Caller{Subject: caller, Permissions: permissions}), id, patch)
	}
	// saved is an update the case expects the core to take.
	saved := func(t *testing.T, what string, id dbtypes.IdentityUUID, caller string, patch *orm.TaskUpdate, permissions ...string) {
		t.Helper()
		if _, err := update(id, caller, patch, permissions...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// stored is a task's own fields and status, read past the API.
	type stored struct {
		urgent   bool
		attempts float64
		plan     string
		proof    *string
		status   string
		version  int64
	}
	read := func(t *testing.T, id dbtypes.IdentityUUID) stored {
		t.Helper()
		var s stored
		if err := pool.QueryRow(ctx, "SELECT urgent, attempts, plan, proof, status, _version FROM task WHERE id = $1", id.ToUUID().String()).Scan(&s.urgent, &s.attempts, &s.plan, &s.proof, &s.status, &s.version); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// unmoved fails the case when a refused write moved the task's
	// version.
	unmoved := func(t *testing.T, what string, id dbtypes.IdentityUUID, version int64) {
		t.Helper()
		if got := read(t, id).version; got != version {
			t.Fatalf("the task is at version %d after %s, want %d: a refused write writes nothing", got, what, version)
		}
	}
	flag := func(value bool) *bool { return &value }
	number := func(value float64) *float64 { return &value }
	text := func(value string) *string { return &value }
	// version is the version a signer read, as the input's optional
	// version holds it.
	version := func(value int64) types.InputField[types.GenericInt64] {
		return types.InputField[types.GenericInt64]{Set: true, Value: types.GenericInt64(value)}
	}
	// working moves a task bob owns to working, its proof given.
	working := func(t *testing.T, key string, id dbtypes.IdentityUUID) {
		t.Helper()
		if _, err := asBob.TaskNamespace.Status.Transition(ctx, key, types.TaskStatusTransitionInput{To: types.TaskStatusState_Working}); err != nil {
			t.Fatalf("bob's move to working: %v", err)
		}
		saved(t, "bob's proof", id, bob, &orm.TaskUpdate{Proof: text("Screenshots")})
	}

	t.Run("a oneWay field moved back is refused as one_way, and moved forward is saved", func(t *testing.T) {
		key, id := task(t)
		saved(t, "bob's urgent and attempts", id, bob, &orm.TaskUpdate{Urgent: flag(true), Attempts: number(1)})
		before := read(t, id).version
		_, err := update(id, bob, &orm.TaskUpdate{Urgent: flag(false)})
		wantVetoed(t, "bob's urgent back", err, "one_way",
			"Task "+key+"'s urgent only turns true: an update may not move it from true to false; only a caller with tasks.admin may move it back",
			"{\"attempted\":false,\"current\":true,\"direction\":\"up\",\"field\":\"urgent\"}")
		unmoved(t, "bob's urgent back", id, before)
		saved(t, "bob's attempts", id, bob, &orm.TaskUpdate{Attempts: number(3)})
		if got := read(t, id); !got.urgent || got.attempts != 3 {
			t.Fatalf("the task holds urgent %t and attempts %v, want true and 3", got.urgent, got.attempts)
		}
	})

	t.Run("a caller holding tasks.admin moves a oneWay field back", func(t *testing.T) {
		key, id := task(t)
		saved(t, "bob's urgent and attempts", id, bob, &orm.TaskUpdate{Urgent: flag(true), Attempts: number(2)})
		_, err := update(id, bob, &orm.TaskUpdate{Attempts: number(0)})
		wantVetoed(t, "bob's attempts back", err, "one_way",
			"Task "+key+"'s attempts only grows: an update may not move it from 2 to 0; only a caller with tasks.admin may move it back",
			"{\"attempted\":0,\"current\":2,\"direction\":\"up\",\"field\":\"attempts\"}")
		saved(t, "dave's urgent and attempts back", id, dave, &orm.TaskUpdate{Urgent: flag(false), Attempts: number(0)}, "tasks.admin")
		if got := read(t, id); got.urgent || got.attempts != 0 {
			t.Fatalf("the task holds urgent %t and attempts %v, want false and 0", got.urgent, got.attempts)
		}
	})

	t.Run("the creator edits the plan while the task is planned", func(t *testing.T) {
		_, id := task(t)
		saved(t, "alice's plan", id, alice, &orm.TaskUpdate{Plan: text("Second plan")})
		if got := read(t, id); got.plan != "Second plan" || got.status != "planned" {
			t.Fatalf("the task is %s with the plan %q, want planned with the second plan", got.status, got.plan)
		}
	})

	t.Run("another caller editing the plan while the task is planned is refused as frozen_fields", func(t *testing.T) {
		key, id := task(t)
		before := read(t, id).version
		_, err := update(id, bob, &orm.TaskUpdate{Plan: text("Bob's plan")})
		wantVetoed(t, "bob's plan", err, "frozen_fields",
			"Task "+key+" is planned, which freezes plan: it does not change while it is planned; only the subject createdBy holds may change it",
			"{\"by\":[\"createdBy\"],\"fields\":[\"plan\"],\"state\":\"planned\"}")
		if got := read(t, id); got.plan != "First plan" {
			t.Fatalf("the task's plan is %q, want the first plan", got.plan)
		}
		unmoved(t, "bob's plan", id, before)
	})

	t.Run("a move into proving without its sign-offs is refused as approvals_missing", func(t *testing.T) {
		key, id := task(t)
		moved, err := asBob.TaskNamespace.Status.Transition(ctx, key, types.TaskStatusTransitionInput{To: types.TaskStatusState_Working})
		if err != nil {
			t.Fatalf("bob's move to working: %v", err)
		}
		if moved.From != types.TaskStatusState_Planned || moved.To != types.TaskStatusState_Working {
			t.Fatalf("bob's move to working answered %+v, want from planned to working", moved)
		}
		saved(t, "bob's proof", id, bob, &orm.TaskUpdate{Proof: text("Screenshots")})
		before := read(t, id).version
		_, err = asBob.TaskNamespace.Status.Transition(ctx, key, types.TaskStatusTransitionInput{To: types.TaskStatusState_Proving})
		wantAPIVetoed(t, "bob's move to proving", err, "approvals_missing", "{\"count\":0,\"required\":1,\"state\":\"proving\"}")
		if got := read(t, id); got.status != "working" {
			t.Fatalf("the task is %s, want working", got.status)
		}
		unmoved(t, "bob's move to proving", id, before)
	})

	t.Run("a pinned signOff with no version is refused as invalid_argument, with a stale one as stale, and with the row's version is saved", func(t *testing.T) {
		key, id := task(t)
		working(t, key, id)
		current := read(t, id).version
		_, err := asCarol.TaskNamespace.Approvals.SignOff(ctx, key, types.TaskApprovalsSignOffInput{State: types.TaskApprovalsGate_Done})
		var apiErr *sdk.APIError
		missing := "operation signOff of behavior Approvals: /version: is required: the gate on done is pinned, so a sign-off names the row's version its signer read"
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Code != "invalid_argument" || apiErr.Message != missing {
			t.Fatalf("carol's sign-off with no version: %v, want 400 invalid_argument %q", err, missing)
		}
		_, err = asCarol.TaskNamespace.Approvals.SignOff(ctx, key, types.TaskApprovalsSignOffInput{State: types.TaskApprovalsGate_Done, Version: version(current - 1)})
		wantAPIVetoed(t, "carol's stale sign-off", err, "stale", fmt.Sprintf("{\"current\":%d,\"version\":%d}", current, current-1))
		unmoved(t, "carol's stale sign-off", id, current)
		signed, err := asCarol.TaskNamespace.Approvals.SignOff(ctx, key, types.TaskApprovalsSignOffInput{State: types.TaskApprovalsGate_Done, Version: version(current)})
		if err != nil {
			t.Fatalf("carol's sign-off: %v", err)
		}
		if signed.State != types.TaskApprovalsGate_Done || signed.Subject != carol || len(signed.SignedBy) != 1 || signed.SignedBy[0] != carol || signed.Count != 1 || signed.At == 0 {
			t.Fatalf("carol's sign-off answered %+v, want done by carol, signed by carol, of 1", signed)
		}
		if got := read(t, id).version; got != current+1 {
			t.Fatalf("the task is at version %d after the sign-off, want %d", got, current+1)
		}
	})

	t.Run("editing the proof consumes the done gate's sign-offs and keeps the proving gate's", func(t *testing.T) {
		key, id := task(t)
		working(t, key, id)
		if _, err := asCarol.TaskNamespace.Approvals.SignOff(ctx, key, types.TaskApprovalsSignOffInput{State: types.TaskApprovalsGate_Proving}); err != nil {
			t.Fatalf("carol's sign-off for proving: %v", err)
		}
		if _, err := asCarol.TaskNamespace.Approvals.SignOff(ctx, key, types.TaskApprovalsSignOffInput{State: types.TaskApprovalsGate_Done, Version: version(read(t, id).version)}); err != nil {
			t.Fatalf("carol's sign-off for done: %v", err)
		}
		saved(t, "bob's new proof", id, bob, &orm.TaskUpdate{Proof: text("New screenshots")})
		listed, err := asCarol.TaskNamespace.Approvals.ListSignOffs(ctx, key, types.TaskApprovalsListSignOffsInput{})
		if err != nil {
			t.Fatalf("carol's list: %v", err)
		}
		consumed := map[string]bool{}
		for _, item := range listed.Items {
			consumed[string(item.State)] = item.ConsumedAt != nil
		}
		if len(listed.Items) != 2 || consumed["proving"] || !consumed["done"] {
			t.Fatalf("the sign-offs are %+v, want proving's live and done's consumed", listed.Items)
		}
		moved, err := asBob.TaskNamespace.Status.Transition(ctx, key, types.TaskStatusTransitionInput{To: types.TaskStatusState_Proving})
		if err != nil {
			t.Fatalf("bob's move to proving: %v", err)
		}
		if moved.To != types.TaskStatusState_Proving {
			t.Fatalf("bob's move to proving answered %+v", moved)
		}
	})

	t.Run("an update of the owner, a constant, is refused as invalid_instance", func(t *testing.T) {
		_, id := task(t)
		before := read(t, id).version
		_, err := update(id, bob, &orm.TaskUpdate{Owner: &daveID})
		refusal, ok := rules.AsRefusal(err)
		if !ok {
			t.Fatalf("bob's owner: %v, want the core's refusal", err)
		}
		want := "{\"issues\":[{\"message\":\"owner is a constant of Task: its create sets it and nothing changes it after; only a caller with tasks.admin may change it\",\"path\":\"owner\",\"rule\":\"constant\"}]}"
		var details any
		if err := json.Unmarshal(refusal.Details, &details); err != nil {
			t.Fatal(err)
		}
		if got := detailsJSON(t, details); refusal.Status != 422 || refusal.Code != rules.CodeInvalidInstance || got != want {
			t.Fatalf("bob's owner refused with %d %s %s, want 422 invalid_instance %s", refusal.Status, refusal.Code, got, want)
		}
		unmoved(t, "bob's owner", id, before)
	})
}
`
