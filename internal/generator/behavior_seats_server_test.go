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

// seatsCases are the cases seatsTest runs, by name: the end-to-end
// scenario every language's server and behavior worker of Seats and
// Reconcile on tables is held to (D104), with the core's words. A problem
// whose one accountable person the next to take the seat displaces, whose
// own agent is always among its responsible subjects, and whose handles
// number at most five, each released by the agent that claimed it or by a
// person who may release another's; a Workflow transition and a StateRules
// state whose by names a seat; and a sandbox whose status follows its
// provider's reports through Reconcile, to unreachable when the provider
// stops answering.
var seatsCases = []string{
	"a caller holding take seats itself as accountable, and the next to take the seat displaces it (onFull displace)",
	"a take of a seat the caller holds already answers unchanged and leaves the row's _version where it was",
	"a take by a caller without the seat's take permission is 403 forbidden",
	"the row's agent is seated as responsible through its field: listSeats lists it first, and its leaveSeat is 409 held_by_field",
	"a sixth handle is 409 seat_full",
	"a handle is released by the agent that claimed it or by a caller holding assign, and another caller's release is 403 forbidden",
	"a leave of a seat the caller does not hold answers unchanged",
	"a transition whose by names seat:responsible is made by a subject seated there, and another caller is 409 not_principal naming the seat",
	"a caller holding overridePermission moves past by",
	"in review the accountable subject edits the title StateRules freezes, and another caller's edit is 409 frozen_fields naming seat:accountable",
	"observe moves the sandbox along the fewest transitions to the observed state and records observed, observedAt and reconciledAt",
	"an observation no newer than observedAt answers unchanged and leaves the row's _version where it was",
	"a step the observer may not make stops the path and records drift as forbidden, and an observer who may completes it and clears drift",
	"observe without observePermission is 403 forbidden, and a state the Workflow lacks is 400 invalid_argument",
	"the stale deadline falls due on the worker five minutes after the last observation and moves the sandbox to unreachable",
	"a newer observation moves the sandbox back from unreachable",
}

// TestSeatsGoServerOnPostgres builds fixture-seats-db and fixture-seats-api
// as build-all does, then writes seatsTest into the generated API module
// and runs it, after go vet: the generated Go server's Seats and Workflow
// operations of Problem and Reconcile and Workflow operations of Sandbox
// (D104), called through the generated Go SDK with a test authenticator in
// front of the routes; the ORM's creates and updates of the tables through
// the core, an update of a problem's title under orm.WithBehaviorCaller;
// and the queues of the generated behavior worker SandboxDeadlines run in
// process with worker.RunAll, as its main runs them, a deadline made due by
// moving its message's due_at to now, as the Timers test makes one, on the
// Postgres behaviorServerDatabaseEnv names. Each caller's subject is a
// fresh UUID in its base62 wire form, as the identity runtime gives a
// principal's. It then plans the worker's entrypoint over the generated
// modules and vets it. The module links the core's static archive, which
// scripts/rules-archive.sh stages; the test skips without it, without the
// database, and in -short mode.
func TestSeatsGoServerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server and worker of Seats and Reconcile in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server and worker of Seats and Reconcile against Postgres")
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
	for _, service := range []string{"fixture-seats-db", "fixture-seats-api"} {
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
		if service == "fixture-seats-api" {
			apiSchema = schema
		} else {
			dbSchema = schema
		}
	}

	apiDir := APIDir(outputRoot, "fixture-seats-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-seats-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_seats_server_test.go"), []byte(seatsTest), 0o644); err != nil {
		t.Fatal(err)
	}
	sdkModule := naming.Default().GoSDKModule("fixture-seats-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-seats-api"},
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
	for _, name := range seatsCases {
		if want := "--- PASS: TestSeats/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}

	vetBehaviorWorkerEntrypoint(t, outputRoot, paths, apiSchema, dbSchema, env, "seats-stack", "SandboxDeadlines")
}

// seatsTest runs in the generated module of fixture-seats-api. It serves
// the generated routes over the ORM of fixture-seats-db, on a schema of its
// own in the Postgres behaviorServerDatabaseEnv names, with a test
// authenticator in front of them, calls them through the generated Go SDK,
// creates and updates rows through the ORM, runs the generated worker's
// queues in process, and reads the tables and the deadline queue by SQL.
const seatsTest = `package fixtureseatsapi_test

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

	api "example.com/schemas/api/fixture-seats-api"
	orm "example.com/schemas/orm/fixture-seats-db"
	sdk "example.com/schemas/sdk/go/fixture-seats-api"
	types "example.com/schemas/types/go/fixture-seats-api"
	dbtypes "example.com/schemas/types/go/fixture-seats-db"
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

// openDatabase is the ORM of fixture-seats-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends, and
// its pool, which the test reads the tables through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server and worker of Seats and Reconcile against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("seats_go_%d", time.Now().UnixNano())
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

// wantAPIRefused holds a call the server refused with a status and a code.
func wantAPIRefused(t *testing.T, what string, err error, status int, code string) {
	t.Helper()
	apiErr := apiRefused(t, what, err)
	if apiErr.StatusCode != status || apiErr.Code != code {
		t.Fatalf("%s refused with %d %s %q, want %d %s", what, apiErr.StatusCode, apiErr.Code, apiErr.Message, status, code)
	}
}

// wantAPIVetoed holds a call the server refused to the core's veto: 409,
// the code vetoed, and the veto's code and details, whose JSON is
// vetoDetails with its keys sorted.
func wantAPIVetoed(t *testing.T, what string, err error, vetoCode, vetoDetails string) {
	t.Helper()
	apiErr := apiRefused(t, what, err)
	details := sortedJSON(t, apiErr.Details["vetoDetails"])
	if apiErr.StatusCode != 409 || apiErr.Code != "vetoed" || apiErr.Details["vetoCode"] != vetoCode || details != vetoDetails {
		t.Fatalf("%s refused with %d %s %q (vetoCode %v, vetoDetails %s), want 409 vetoed, vetoCode %s, vetoDetails %s", what, apiErr.StatusCode, apiErr.Code, apiErr.Message, apiErr.Details["vetoCode"], details, vetoCode, vetoDetails)
	}
}

// wantVetoed holds a write the repository refused to the core's veto: 409,
// the code vetoed, and the veto's code and details, whose JSON is
// vetoDetails with its keys sorted.
func wantVetoed(t *testing.T, what string, err error, vetoCode, vetoDetails string) {
	t.Helper()
	refusal, ok := rules.AsRefusal(err)
	if !ok {
		t.Fatalf("%s: %v, want the core's refusal", what, err)
	}
	var details struct {
		VetoCode    string          ` + "`json:\"vetoCode\"`" + `
		VetoDetails json.RawMessage ` + "`json:\"vetoDetails\"`" + `
	}
	if err := json.Unmarshal(refusal.Details, &details); err != nil {
		t.Fatalf("%s: details %s: %v", what, refusal.Details, err)
	}
	var vetoed any
	if err := json.Unmarshal(details.VetoDetails, &vetoed); err != nil {
		t.Fatalf("%s: vetoDetails %s: %v", what, details.VetoDetails, err)
	}
	if got := sortedJSON(t, vetoed); refusal.Status != 409 || refusal.Code != rules.CodeVetoed || details.VetoCode != vetoCode || got != vetoDetails {
		t.Fatalf("%s refused with %d %s %q (details %s), want 409 vetoed, vetoCode %s, vetoDetails %s", what, refusal.Status, refusal.Code, refusal.Message, refusal.Details, vetoCode, vetoDetails)
	}
}

// seated is an item of listSeats' result, as the case reads it.
type seated struct {
	Seat      string  ` + "`json:\"seat\"`" + `
	Subject   string  ` + "`json:\"subject\"`" + `
	SeatedAt  *int64  ` + "`json:\"seatedAt\"`" + `
	SeatedBy  *string ` + "`json:\"seatedBy\"`" + `
	FromField bool    ` + "`json:\"fromField\"`" + `
}

// observation is observe's result, as the case reads it.
type observation struct {
	Observed   string  ` + "`json:\"observed\"`" + `
	ObservedAt int64   ` + "`json:\"observedAt\"`" + `
	Status     *string ` + "`json:\"status\"`" + `
	Moves      []struct {
		From string ` + "`json:\"from\"`" + `
		To   string ` + "`json:\"to\"`" + `
	} ` + "`json:\"moves\"`" + `
	Drift     map[string]any ` + "`json:\"drift\"`" + `
	Unchanged *bool          ` + "`json:\"unchanged\"`" + `
}

// path is an observation's moves as from>to, joined with ", ".
func (o observation) path() string {
	var steps []string
	for _, move := range o.Moves {
		steps = append(steps, move.From+">"+move.To)
	}
	return strings.Join(steps, ", ")
}

func millis(at *time.Time) int64 {
	if at == nil {
		return 0
	}
	return at.UnixMilli()
}

func TestSeats(t *testing.T) {
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
	client := func(token string) *sdk.FixtureSeatsApiSDK {
		t.Helper()
		c, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		c.SetToken(token)
		return c
	}
	// person and person2 take the accountable seat, agent and agent2 are
	// responsible, releaser releases another's handle, moderator moves past
	// by, nobody holds no permission; poller observes sandboxes, and
	// operator observes and stops them.
	person, person2, agent, agent2, nobody, releaser, moderator, poller, operator := subject(t), subject(t), subject(t), subject(t), subject(t), subject(t), subject(t), subject(t), subject(t)
	asPerson := client(person + ":problems.own,problems.assign")
	asPerson2 := client(person2 + ":problems.own")
	asAgent, asAgent2 := client(agent+":problems.work"), client(agent2+":problems.work")
	asNobody, asReleaser, asModerator := client(nobody), client(releaser+":problems.release"), client(moderator+":problems.override")
	asPoller, asOperator := client(poller+":sandboxes.observe"), client(operator+":sandboxes.observe,sandboxes.stop")
	now := func() int64 { return time.Now().UnixMilli() }

	// problem creates a problem whose agent is agent through the repository
	// and returns its key in its wire form, in its column's, and as the
	// ORM's.
	agentID := uuidOf(t, agent)
	problem := func(t *testing.T) (string, string, dbtypes.IdentityUUID) {
		t.Helper()
		row, err := db.GetProblemRepository().CreateOne(ctx, &orm.ProblemCreate{Title: "Flaky test", Agent: &agentID})
		if err != nil {
			t.Fatalf("create a problem: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String(), *row.Id
	}
	problemVersion := func(t *testing.T, column string) int64 {
		t.Helper()
		var version int64
		if err := pool.QueryRow(ctx, "SELECT _version FROM problem WHERE id = $1", column).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	unmoved := func(t *testing.T, what, column string, version int64) {
		t.Helper()
		if got := problemVersion(t, column); got != version {
			t.Fatalf("the problem is at version %d after %s, want %d: it writes nothing", got, what, version)
		}
	}
	// someone names another subject to seat or unseat, and self the
	// caller, naming none.
	someone := func(subject string) types.InputField[string] {
		return types.InputField[string]{Set: true, Value: subject}
	}
	self := types.InputField[string]{}
	take := func(c *sdk.FixtureSeatsApiSDK, key string, name types.ProblemSeatsSeat, subject types.InputField[string]) (types.ProblemSeatsTakeSeatResult, error) {
		return c.ProblemNamespace.Seats.TakeSeat(ctx, key, types.ProblemSeatsTakeSeatInput{Seat: name, Subject: subject})
	}
	leave := func(c *sdk.FixtureSeatsApiSDK, key string, name types.ProblemSeatsSeat, subject types.InputField[string]) (types.ProblemSeatsLeaveSeatResult, error) {
		return c.ProblemNamespace.Seats.LeaveSeat(ctx, key, types.ProblemSeatsLeaveSeatInput{Seat: name, Subject: subject})
	}
	// taken holds a take that seated subject, displacing those named.
	taken := func(t *testing.T, what string, result types.ProblemSeatsTakeSeatResult, err error, name types.ProblemSeatsSeat, subject string, displaced ...string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if displaced == nil {
			displaced = []string{}
		}
		if result.Seat != name || result.Subject != subject || !slices.Equal(result.Displaced, displaced) || result.Unchanged != nil {
			t.Fatalf("%s answered %s, want %s seated in %s, displacing %q", what, sortedJSON(t, result), subject, name, displaced)
		}
	}
	// list is the subjects listSeats lists, of one seat or of every seat.
	list := func(t *testing.T, c *sdk.FixtureSeatsApiSDK, key string, only *types.ProblemSeatsSeat) []seated {
		t.Helper()
		input := types.ProblemSeatsListSeatsInput{}
		if only != nil {
			input.Seat = types.InputField[*types.ProblemSeatsSeat]{Set: true, Value: only}
		}
		listed, err := c.ProblemNamespace.Seats.ListSeats(ctx, key, input)
		if err != nil {
			t.Fatalf("listSeats: %v", err)
		}
		return as[struct {
			Items []seated ` + "`json:\"items\"`" + `
		}](t, listed).Items
	}
	move := func(c *sdk.FixtureSeatsApiSDK, key string, to types.ProblemStatusState) (types.ProblemStatusTransitionResult, error) {
		return c.ProblemNamespace.Status.Transition(ctx, key, types.ProblemStatusTransitionInput{To: to})
	}
	moved := func(t *testing.T, what string, result types.ProblemStatusTransitionResult, err error, from, to types.ProblemStatusState) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if result.From != from || result.To != to || result.Unchanged != nil {
			t.Fatalf("%s answered %s, want from %s to %s", what, sortedJSON(t, result), from, to)
		}
	}
	accountable, responsible, handles := types.ProblemSeatsSeat_Accountable, types.ProblemSeatsSeat_Responsible, types.ProblemSeatsSeat_Handles

	t.Run("a caller holding take seats itself as accountable, and the next to take the seat displaces it (onFull displace)", func(t *testing.T) {
		key, _, _ := problem(t)
		result, err := take(asPerson, key, accountable, self)
		taken(t, "person's take", result, err, accountable, person)
		result, err = take(asPerson2, key, accountable, self)
		taken(t, "person2's take", result, err, accountable, person2, person)
		listed := list(t, asPerson, key, &accountable)
		if len(listed) != 1 || listed[0].Subject != person2 || listed[0].FromField || listed[0].SeatedBy == nil || *listed[0].SeatedBy != person2 || listed[0].SeatedAt == nil {
			t.Fatalf("listSeats of accountable lists %s, want person2 alone, seated by itself", sortedJSON(t, listed))
		}
	})

	t.Run("a take of a seat the caller holds already answers unchanged and leaves the row's _version where it was", func(t *testing.T) {
		key, column, _ := problem(t)
		result, err := take(asPerson, key, accountable, self)
		taken(t, "person's take", result, err, accountable, person)
		version := problemVersion(t, column)
		again, err := take(asPerson, key, accountable, self)
		if err != nil {
			t.Fatalf("person's take again: %v", err)
		}
		if again.Seat != accountable || again.Subject != person || len(again.Displaced) != 0 || again.Unchanged == nil || !*again.Unchanged {
			t.Fatalf("person's take again answered %s, want person in accountable, unchanged", sortedJSON(t, again))
		}
		unmoved(t, "person's take again", column, version)
	})

	t.Run("a take by a caller without the seat's take permission is 403 forbidden", func(t *testing.T) {
		key, column, _ := problem(t)
		version := problemVersion(t, column)
		_, err := take(asAgent, key, accountable, self)
		wantAPIRefused(t, "the agent's take of accountable", err, 403, "forbidden")
		unmoved(t, "the agent's take of accountable", column, version)
	})

	t.Run("the row's agent is seated as responsible through its field: listSeats lists it first, and its leaveSeat is 409 held_by_field", func(t *testing.T) {
		key, column, _ := problem(t)
		result, err := take(asAgent2, key, responsible, self)
		taken(t, "agent2's take", result, err, responsible, agent2)
		listed := list(t, asNobody, key, &responsible)
		if len(listed) != 2 || listed[0].Subject != agent || !listed[0].FromField || listed[0].SeatedAt != nil || listed[0].SeatedBy != nil || listed[1].Subject != agent2 || listed[1].FromField {
			t.Fatalf("listSeats of responsible lists %s, want the agent through its field first, then agent2", sortedJSON(t, listed))
		}
		again, err := take(asAgent, key, responsible, self)
		if err != nil || again.Unchanged == nil || !*again.Unchanged {
			t.Fatalf("the agent's take = %s, %v, want unchanged", sortedJSON(t, again), err)
		}
		version := problemVersion(t, column)
		_, err = leave(asAgent, key, responsible, self)
		wantAPIVetoed(t, "the agent's leave", err, "held_by_field", fmt.Sprintf("{\"field\":\"agent\",\"seat\":\"responsible\",\"subject\":%q}", agent))
		unmoved(t, "the agent's leave", column, version)
	})

	t.Run("a sixth handle is 409 seat_full", func(t *testing.T) {
		key, column, _ := problem(t)
		var holders []string
		for range 5 {
			holder := subject(t)
			result, err := take(client(holder), key, handles, self)
			taken(t, "a handle's take", result, err, handles, holder)
			holders = append(holders, holder)
		}
		version := problemVersion(t, column)
		_, err := take(asAgent, key, handles, self)
		wantAPIVetoed(t, "the sixth take", err, "seat_full", fmt.Sprintf("{\"max\":5,\"seat\":\"handles\",\"seated\":%s}", sortedJSON(t, holders)))
		unmoved(t, "the sixth take", column, version)
	})

	t.Run("a handle is released by the agent that claimed it or by a caller holding assign, and another caller's release is 403 forbidden", func(t *testing.T) {
		key, column, _ := problem(t)
		result, err := take(asAgent, key, handles, self)
		taken(t, "the agent's claim", result, err, handles, agent)
		left, err := leave(asAgent, key, handles, self)
		if err != nil || left.Seat != handles || left.Subject != agent || left.Unchanged != nil {
			t.Fatalf("the agent's release = %s, %v, want agent unseated from handles", sortedJSON(t, left), err)
		}
		result, err = take(asAgent2, key, handles, self)
		taken(t, "agent2's claim", result, err, handles, agent2)
		version := problemVersion(t, column)
		_, err = leave(asNobody, key, handles, someone(agent2))
		wantAPIRefused(t, "another caller's release of agent2's handle", err, 403, "forbidden")
		unmoved(t, "another caller's release", column, version)
		left, err = leave(asReleaser, key, handles, someone(agent2))
		if err != nil || left.Seat != handles || left.Subject != agent2 || left.Unchanged != nil {
			t.Fatalf("the releaser's release = %s, %v, want agent2 unseated from handles", sortedJSON(t, left), err)
		}
		if listed := list(t, asNobody, key, &handles); len(listed) != 0 {
			t.Fatalf("handles holds %s after the releases, want no one", sortedJSON(t, listed))
		}
	})

	t.Run("a leave of a seat the caller does not hold answers unchanged", func(t *testing.T) {
		key, column, _ := problem(t)
		version := problemVersion(t, column)
		left, err := leave(asNobody, key, handles, self)
		if err != nil || left.Seat != handles || left.Subject != nobody || left.Unchanged == nil || !*left.Unchanged {
			t.Fatalf("nobody's leave = %s, %v, want unchanged", sortedJSON(t, left), err)
		}
		unmoved(t, "nobody's leave", column, version)
	})

	t.Run("a transition whose by names seat:responsible is made by a subject seated there, and another caller is 409 not_principal naming the seat", func(t *testing.T) {
		key, column, _ := problem(t)
		version := problemVersion(t, column)
		_, err := move(asNobody, key, types.ProblemStatusState_Working)
		wantAPIVetoed(t, "nobody's move to working", err, "not_principal", "{\"by\":[\"seat:responsible\"],\"from\":\"open\",\"to\":\"working\"}")
		unmoved(t, "nobody's move to working", column, version)
		result, err := move(asAgent, key, types.ProblemStatusState_Working)
		moved(t, "the agent's move to working", result, err, types.ProblemStatusState_Open, types.ProblemStatusState_Working)
	})

	t.Run("a caller holding overridePermission moves past by", func(t *testing.T) {
		key, _, _ := problem(t)
		result, err := move(asModerator, key, types.ProblemStatusState_Working)
		moved(t, "the moderator's move to working", result, err, types.ProblemStatusState_Open, types.ProblemStatusState_Working)
	})

	t.Run("in review the accountable subject edits the title StateRules freezes, and another caller's edit is 409 frozen_fields naming seat:accountable", func(t *testing.T) {
		key, column, id := problem(t)
		result, err := move(asAgent, key, types.ProblemStatusState_Working)
		moved(t, "the agent's move to working", result, err, types.ProblemStatusState_Open, types.ProblemStatusState_Working)
		result, err = move(asAgent, key, types.ProblemStatusState_Review)
		moved(t, "the agent's move to review", result, err, types.ProblemStatusState_Working, types.ProblemStatusState_Review)
		claimed, err := take(asPerson, key, accountable, self)
		taken(t, "person's take", claimed, err, accountable, person)
		title := "Flaky test, reviewed"
		saved, err := db.GetProblemRepository().UpdateOne(orm.WithBehaviorCaller(ctx, rules.Caller{Subject: person}), id, &orm.ProblemUpdate{Title: &title})
		if err != nil {
			t.Fatalf("person's edit of the title: %v", err)
		}
		if saved.Title != title {
			t.Fatalf("the problem's title is %q, want %q", saved.Title, title)
		}
		version := problemVersion(t, column)
		other := "Mine"
		_, err = db.GetProblemRepository().UpdateOne(orm.WithBehaviorCaller(ctx, rules.Caller{Subject: agent2}), id, &orm.ProblemUpdate{Title: &other})
		wantVetoed(t, "agent2's edit of the title", err, "frozen_fields", "{\"by\":[\"seat:accountable\"],\"fields\":[\"title\"],\"state\":\"review\"}")
		unmoved(t, "agent2's edit of the title", column, version)
	})

	// sandbox creates a sandbox through the repository and returns its key
	// in its wire form, in its column's, and as the ORM's.
	sandbox := func(t *testing.T) (string, string, dbtypes.IdentityUUID) {
		t.Helper()
		row, err := db.GetSandboxRepository().CreateOne(ctx, &orm.SandboxCreate{Name: "box"})
		if err != nil {
			t.Fatalf("create a sandbox: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String(), *row.Id
	}
	// sandboxRow is a sandbox's status and observation as its columns hold
	// them.
	type sandboxRow struct {
		status                   string
		observed                 *string
		observedAt, reconciledAt *time.Time
		drift                    []byte
		version                  int64
	}
	readSandbox := func(t *testing.T, column string) sandboxRow {
		t.Helper()
		var r sandboxRow
		if err := pool.QueryRow(ctx, "SELECT status, observed, observed_observed_at, observed_reconciled_at, observed_drift, _version FROM sandbox WHERE id = $1", column).
			Scan(&r.status, &r.observed, &r.observedAt, &r.reconciledAt, &r.drift, &r.version); err != nil {
			t.Fatal(err)
		}
		return r
	}
	// stales is the times of a sandbox's live stale messages.
	stales := func(t *testing.T, column string) []int64 {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT at FROM sandbox_deadline_queue WHERE row_key = $1 AND use_id = 'observed' AND name = 'stale' AND state IN ('ready', 'claimed')", column)
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
		if got := stales(t, column); len(got) != 1 || got[0] != at {
			t.Fatalf("the sandbox's live stale messages are at %v, want one at %d", got, at)
		}
	}
	observe := func(c *sdk.FixtureSeatsApiSDK, key string, state types.SandboxObservedState, at int64) (observation, error) {
		result, err := c.SandboxNamespace.Observed.Observe(ctx, key, types.SandboxObservedObserveInput{State: state, At: types.GenericInt64(at)})
		if err != nil {
			return observation{}, err
		}
		return as[observation](t, result), nil
	}
	// observed holds an observation that recorded state at at and left the
	// status at status along path, with no drift.
	observed := func(t *testing.T, what string, result observation, err error, state string, at int64, status, path string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if result.Observed != state || result.ObservedAt != at || result.Status == nil || *result.Status != status || result.path() != path || result.Drift != nil || result.Unchanged != nil {
			t.Fatalf("%s answered %s, want %s observed at %d, the status %s along %q, no drift", what, sortedJSON(t, result), state, at, status, path)
		}
	}
	// due makes a sandbox's live stale message due now, as the Timers tests
	// make one.
	due := func(t *testing.T, column string) {
		t.Helper()
		tag, err := pool.Exec(ctx, "UPDATE sandbox_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = 'stale' AND state IN ('ready', 'claimed')", column)
		if err != nil {
			t.Fatal(err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("sandbox_deadline_queue holds %d live stale messages of %s, want one", tag.RowsAffected(), column)
		}
	}
	// drive runs the worker's queues in process, as its main does, until
	// done holds or ten seconds pass, polling every 100 ms.
	drive := func(t *testing.T, done func() bool) {
		t.Helper()
		running, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() {
			stopped <- worker.RunAll(running, api.SandboxDeadlinesQueues(db, zap.NewNop()), worker.Options{Concurrency: 2, Idle: 100 * time.Millisecond})
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

	t.Run("observe moves the sandbox along the fewest transitions to the observed state and records observed, observedAt and reconciledAt", func(t *testing.T) {
		key, column, _ := sandbox(t)
		at := now()
		result, err := observe(asPoller, key, types.SandboxObservedState_Running, at)
		after := now()
		observed(t, "the poller's observation", result, err, "running", at, "running", "pending>starting, starting>running")
		row := readSandbox(t, column)
		if row.status != "running" || row.observed == nil || *row.observed != "running" || millis(row.observedAt) != at || row.reconciledAt == nil || millis(row.reconciledAt) < at || millis(row.reconciledAt) > after || row.drift != nil {
			t.Fatalf("the sandbox is %s, observed %v at %d, reconciled at %d, drift %s, want running, observed running at %d and reconciled by %d", row.status, row.observed, millis(row.observedAt), millis(row.reconciledAt), row.drift, at, after)
		}
		armedAt(t, column, at+300000)
	})

	t.Run("an observation no newer than observedAt answers unchanged and leaves the row's _version where it was", func(t *testing.T) {
		key, column, _ := sandbox(t)
		at := now()
		result, err := observe(asPoller, key, types.SandboxObservedState_Running, at)
		observed(t, "the poller's observation", result, err, "running", at, "running", "pending>starting, starting>running")
		version := readSandbox(t, column).version
		// One at the same time, and one older, each in another state.
		for _, older := range []struct {
			name  string
			state types.SandboxObservedState
			at    int64
		}{
			{"the observation at the same time", types.SandboxObservedState_Pending, at},
			{"the older observation", types.SandboxObservedState_Stopped, at - 1000},
		} {
			again, err := observe(asPoller, key, older.state, older.at)
			if err != nil {
				t.Fatalf("%s: %v", older.name, err)
			}
			if again.Observed != "running" || again.ObservedAt != at || again.Status == nil || *again.Status != "running" || len(again.Moves) != 0 || again.Unchanged == nil || !*again.Unchanged {
				t.Fatalf("%s answered %s, want running observed at %d, unchanged", older.name, sortedJSON(t, again), at)
			}
			if got := readSandbox(t, column); got.version != version || got.status != "running" {
				t.Fatalf("after %s the sandbox is %s at version %d, want running at version %d", older.name, got.status, got.version, version)
			}
		}
	})

	t.Run("a step the observer may not make stops the path and records drift as forbidden, and an observer who may completes it and clears drift", func(t *testing.T) {
		key, column, _ := sandbox(t)
		at := now()
		result, err := observe(asPoller, key, types.SandboxObservedState_Running, at)
		observed(t, "the poller's observation of running", result, err, "running", at, "running", "pending>starting, starting>running")
		stopped := at + 1
		for now() < stopped {
			time.Sleep(time.Millisecond)
		}
		drifted, err := observe(asPoller, key, types.SandboxObservedState_Stopped, stopped)
		if err != nil {
			t.Fatalf("the poller's observation of stopped: %v", err)
		}
		if drifted.Observed != "stopped" || drifted.Status == nil || *drifted.Status != "running" || len(drifted.Moves) != 0 || sortedJSON(t, drifted.Drift) != "{\"code\":\"forbidden\",\"observed\":\"stopped\",\"status\":\"running\"}" {
			t.Fatalf("the poller's observation of stopped answered %s, want the status running and drift forbidden", sortedJSON(t, drifted))
		}
		row := readSandbox(t, column)
		var drift any
		if err := json.Unmarshal(row.drift, &drift); err != nil || row.status != "running" || sortedJSON(t, drift) != "{\"code\":\"forbidden\",\"observed\":\"stopped\",\"status\":\"running\"}" {
			t.Fatalf("the sandbox is %s with drift %s, want running, its drift forbidden", row.status, row.drift)
		}
		again := stopped + 1
		for now() < again {
			time.Sleep(time.Millisecond)
		}
		result, err = observe(asOperator, key, types.SandboxObservedState_Stopped, again)
		observed(t, "the operator's observation of stopped", result, err, "stopped", again, "stopped", "running>stopping, stopping>stopped")
		if row := readSandbox(t, column); row.status != "stopped" || row.drift != nil {
			t.Fatalf("the sandbox is %s with drift %s, want stopped and no drift", row.status, row.drift)
		}
	})

	t.Run("observe without observePermission is 403 forbidden, and a state the Workflow lacks is 400 invalid_argument", func(t *testing.T) {
		key, column, id := sandbox(t)
		version := readSandbox(t, column).version
		_, err := observe(asNobody, key, types.SandboxObservedState_Running, now())
		wantAPIRefused(t, "nobody's observation", err, 403, "forbidden")
		// The input types state by the Workflow's states, so the route
		// refuses hibernating as it decodes the body, before the core runs,
		// as its 400 bad_request; the repository hands it to the core, which
		// refuses it in Reconcile's words.
		request, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/sandbox/"+key+"/observed/observe", bytes.NewReader([]byte(fmt.Sprintf("{\"state\":\"hibernating\",\"at\":%d}", now()))))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+poller+":sandboxes.observe")
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Code string ` + "`json:\"code\"`" + `
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&body)
		_ = response.Body.Close()
		if decodeErr != nil || response.StatusCode != 400 || body.Code != "bad_request" {
			t.Fatalf("the route answered %d %s (%v) to a state the Workflow lacks, want its input's 400 bad_request", response.StatusCode, body.Code, decodeErr)
		}
		_, err = db.GetSandboxRepository().Observed().Observe(orm.WithBehaviorCaller(ctx, rules.Caller{Subject: poller, Permissions: []string{"sandboxes.observe"}}), id, orm.SandboxObservedObserveParams{State: "hibernating", At: now()})
		refusal, ok := rules.AsRefusal(err)
		want := "operation observe of behavior Reconcile on field observed: /state: \"hibernating\" is not a state of Sandbox.status (pending, starting, running, stopping, stopped, failed, unreachable)"
		if !ok || refusal.Status != 400 || refusal.Code != rules.CodeInvalidArgument || refusal.Class != "OperationParamsError" || refusal.Message != want {
			t.Fatalf("the repository's observation of hibernating: %v, want 400 invalid_argument OperationParamsError %q", err, want)
		}
		if got := readSandbox(t, column).version; got != version {
			t.Fatalf("the sandbox is at version %d after the refused observations, want %d", got, version)
		}
	})

	staleKey, staleColumn := "", ""
	var staleAt int64
	t.Run("the stale deadline falls due on the worker five minutes after the last observation and moves the sandbox to unreachable", func(t *testing.T) {
		key, column, _ := sandbox(t)
		staleKey, staleColumn = key, column
		at := now()
		result, err := observe(asPoller, key, types.SandboxObservedState_Running, at)
		observed(t, "the poller's observation", result, err, "running", at, "running", "pending>starting, starting>running")
		staleAt = at
		armedAt(t, column, at+300000)
		due(t, column)
		drive(t, func() bool { return readSandbox(t, column).status == "unreachable" })
		row := readSandbox(t, column)
		if row.status != "unreachable" || row.observed == nil || *row.observed != "unreachable" || millis(row.observedAt) != at || row.drift != nil {
			t.Fatalf("the sandbox is %s, observed %v at %d, drift %s, want unreachable within ten seconds, observed unreachable at %d, no drift", row.status, row.observed, millis(row.observedAt), row.drift, at)
		}
		if got := stales(t, column); len(got) != 0 {
			t.Fatalf("the sandbox's live stale messages are at %v after the fire, want none", got)
		}
	})

	t.Run("a newer observation moves the sandbox back from unreachable", func(t *testing.T) {
		if staleKey == "" {
			t.Fatal("no sandbox went stale")
		}
		at := now()
		for at <= staleAt {
			time.Sleep(time.Millisecond)
			at = now()
		}
		result, err := observe(asPoller, staleKey, types.SandboxObservedState_Running, at)
		observed(t, "the poller's observation", result, err, "running", at, "running", "unreachable>running")
		if row := readSandbox(t, staleColumn); row.status != "running" || row.observed == nil || *row.observed != "running" || millis(row.observedAt) != at {
			t.Fatalf("the sandbox is %s, observed %v at %d, want running, observed running at %d", row.status, row.observed, millis(row.observedAt), at)
		}
		armedAt(t, staleColumn, at+300000)
	})
}
`
