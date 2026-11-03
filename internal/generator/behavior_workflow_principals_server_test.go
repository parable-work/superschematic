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

// workflowPrincipalsCases are the cases workflowPrincipalsTest runs, by
// name: the end-to-end scenario every language's server of a table whose
// Workflow names who moves it (by), a permission that moves a caller past
// by (overridePermission), a move to the state the row is in answered as no
// change (repeat) and the state the caller read (from) is held to (D104),
// with the core's words.
var workflowPrincipalsCases = []string{
	"the subject the question is asked of answers it",
	"another caller is refused as not_principal",
	"the question's creator withdraws it, by createdBy",
	"a caller holding the override permission answers it",
	"answering again answers unchanged and leaves the row's version",
	"a repeat on the Workflow without repeat is refused as already_in_state",
	"a from that is not the row's state is refused as stale",
	"a from that is the row's state moves it, and its retry is stale with the state it moved to",
}

// TestWorkflowPrincipalsGoServerOnPostgres builds
// fixture-workflow-principals-db and fixture-workflow-principals-api as
// build-all does, then writes workflowPrincipalsTest into the generated API
// module and runs it, after go vet: the generated Go server's Workflow
// operations of Question (D104), whose status names askedOf and createdBy
// in by, the override permission questions.moderate and repeat unchanged,
// and whose review has no repeat, called through the generated Go SDK with
// a test authenticator in front of the routes, on the Postgres
// behaviorServerDatabaseEnv names. Each caller's subject is a fresh UUID in
// its base62 wire form, as the identity runtime gives a principal's, and
// each question is created through the ORM acting as its asker, which
// stamps createdBy. The module links the core's static archive, which
// scripts/rules-archive.sh stages; the test skips without it, without the
// database, and in -short mode.
func TestWorkflowPrincipalsGoServerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's Workflow principals in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server's Workflow principals against Postgres")
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
	for _, service := range []string{"fixture-workflow-principals-db", "fixture-workflow-principals-api"} {
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

	apiDir := APIDir(outputRoot, "fixture-workflow-principals-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-workflow-principals-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_workflow_principals_server_test.go"), []byte(workflowPrincipalsTest), 0o644); err != nil {
		t.Fatal(err)
	}

	// The test reaches the Go SDK beside what the API module requires, and
	// links the four archives a Go binary of the checkout links.
	sdkModule := naming.Default().GoSDKModule("fixture-workflow-principals-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-workflow-principals-api"},
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
	for _, name := range workflowPrincipalsCases {
		if want := "--- PASS: TestWorkflowPrincipals/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}

// workflowPrincipalsTest runs in the generated module of
// fixture-workflow-principals-api. It serves the generated routes over the
// ORM of fixture-workflow-principals-db, on a schema of its own in the
// Postgres behaviorServerDatabaseEnv names, with a test authenticator in
// front of them, and calls them through the generated Go SDK.
const workflowPrincipalsTest = `package fixtureworkflowprincipalsapi_test

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
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-workflow-principals-api"
	orm "example.com/schemas/orm/fixture-workflow-principals-db"
	sdk "example.com/schemas/sdk/go/fixture-workflow-principals-api"
	types "example.com/schemas/types/go/fixture-workflow-principals-api"
	dbtypes "example.com/schemas/types/go/fixture-workflow-principals-db"
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

// questions is the project's implementation of reassignQuestion, the one
// operation no generated handler serves: the update of askedOf through the
// ORM, which no case calls (the build warns that it lets a caller name
// itself and then answer).
type questions struct{ db orm.DatabaseInterface }

func (q questions) ReassignQuestion(ctx context.Context, id types.IdentityUUID, input *types.QuestionAssignment) (*types.Question, error) {
	row, err := q.db.GetQuestionRepository().UpdateOne(ctx, id, &orm.QuestionUpdate{AskedOf: &input.AskedOf})
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("question", err)
	}
	return row, err
}

// openDatabase is the ORM of fixture-workflow-principals-db over a schema of
// its own, holding the build's create.sql, which it drops when the test
// ends, and its pool, which the test reads the table through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server's Workflow principals against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("workflow_principals_go_%d", time.Now().UnixNano())
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

// failure is the status, code, detail and details of a refused call.
type failure struct {
	status  int
	code    string
	detail  string
	details map[string]any
}

func failed(t *testing.T, what string, err error) failure {
	t.Helper()
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: %v, want the API's refusal", what, err)
	}
	return failure{status: apiErr.StatusCode, code: apiErr.Code, detail: apiErr.Message, details: apiErr.Details}
}

// wantVetoed holds a refused call to Workflow's veto: 409, the code vetoed,
// the core's message, and the veto's code and details, whose JSON is
// vetoDetails with its keys sorted.
func wantVetoed(t *testing.T, what string, err error, message, vetoCode, vetoDetails string) {
	t.Helper()
	got := failed(t, what, err)
	details, jsonErr := json.Marshal(got.details["vetoDetails"])
	if jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if got.status != 409 || got.code != "vetoed" || got.detail != message || got.details["vetoCode"] != vetoCode || string(details) != vetoDetails {
		t.Fatalf("%s refused with %+v (vetoDetails %s), want 409 vetoed %q, vetoCode %s, vetoDetails %s", what, got, details, message, vetoCode, vetoDetails)
	}
}

func TestWorkflowPrincipals(t *testing.T) {
	ctx := context.Background()
	db, pool := openDatabase(t)
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(authenticate)
	if err := api.RegisterRoutes(router, api.Config{
		DB:              db,
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{QuestionAdmin: questions{db: db}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	as := func(token string) *sdk.FixtureWorkflowPrincipalsApiSDK {
		t.Helper()
		client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		client.SetToken(token)
		return client
	}
	// alice asks each question, of bob; carol is another caller, and mod
	// holds the override permission.
	alice, bob, carol, mod := subject(t), subject(t), subject(t), subject(t)
	asAlice, asBob, asCarol, asMod := as(alice), as(bob), as(carol), as(mod+":questions.moderate")
	bobID, err := dbtypes.ParseIdentityUUID(bob)
	if err != nil {
		t.Fatal(err)
	}

	// question creates an open question asked of bob through the ORM acting
	// as alice, which stamps createdBy, and returns its key in its wire form
	// and in its column's.
	question := func(t *testing.T) (string, string) {
		t.Helper()
		row, err := db.GetQuestionRepository().CreateOne(orm.WithCaller(ctx, alice), &orm.QuestionCreate{Prompt: "Which table?", AskedOf: bobID})
		if err != nil {
			t.Fatalf("create a question: %v", err)
		}
		return row.Id.String(), row.Id.ToUUID().String()
	}
	// stored is a question's status, version and when it entered its
	// status, read past the API.
	type stored struct {
		status    string
		version   int64
		enteredAt time.Time
	}
	read := func(t *testing.T, column string) stored {
		t.Helper()
		var s stored
		if err := pool.QueryRow(ctx, "SELECT status, _version, status_entered_at FROM question WHERE id = $1", column).Scan(&s.status, &s.version, &s.enteredAt); err != nil {
			t.Fatal(err)
		}
		return s
	}
	move := func(t *testing.T, as *sdk.FixtureWorkflowPrincipalsApiSDK, q string, input types.QuestionStatusTransitionInput) (types.QuestionStatusTransitionResult, error) {
		t.Helper()
		return as.QuestionNamespace.Status.Transition(ctx, q, input)
	}
	// from is the state a caller read, as the input's optional from holds
	// it; an input that names none leaves it out.
	from := func(state types.QuestionStatusState) types.InputField[*types.QuestionStatusState] {
		return types.InputField[*types.QuestionStatusState]{Set: true, Value: &state}
	}
	wantMoved := func(t *testing.T, what string, result types.QuestionStatusTransitionResult, err error, from, to types.QuestionStatusState) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if result.From != from || result.To != to || result.Unchanged != nil {
			t.Fatalf("%s answered %+v, want from %s to %s and no unchanged", what, result, from, to)
		}
	}

	t.Run("the subject the question is asked of answers it", func(t *testing.T) {
		q, column := question(t)
		result, err := move(t, asBob, q, types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Answered})
		wantMoved(t, "bob's answer", result, err, types.QuestionStatusState_Open, types.QuestionStatusState_Answered)
		if got := read(t, column); got.status != "answered" {
			t.Fatalf("the question is %s, want answered", got.status)
		}
	})

	t.Run("another caller is refused as not_principal", func(t *testing.T) {
		q, column := question(t)
		before := read(t, column)
		_, err := move(t, asCarol, q, types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Answered})
		wantVetoed(t, "carol's answer", err,
			"behavior Workflow vetoes transition of Question "+q+": "+carol+" may not move Question "+q+"'s status from open to answered: the transition is made by the subject askedOf holds",
			"not_principal", "{\"by\":[\"askedOf\"],\"from\":\"open\",\"to\":\"answered\"}")
		if got := read(t, column); got.status != "open" || got.version != before.version {
			t.Fatalf("the question is %s at version %d, want open at version %d", got.status, got.version, before.version)
		}
	})

	t.Run("the question's creator withdraws it, by createdBy", func(t *testing.T) {
		q, column := question(t)
		_, err := move(t, asBob, q, types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Withdrawn})
		wantVetoed(t, "bob's withdrawal", err,
			"behavior Workflow vetoes transition of Question "+q+": "+bob+" may not move Question "+q+"'s status from open to withdrawn: the transition is made by the subject createdBy holds",
			"not_principal", "{\"by\":[\"createdBy\"],\"from\":\"open\",\"to\":\"withdrawn\"}")
		result, err := move(t, asAlice, q, types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Withdrawn})
		wantMoved(t, "alice's withdrawal", result, err, types.QuestionStatusState_Open, types.QuestionStatusState_Withdrawn)
		if got := read(t, column); got.status != "withdrawn" {
			t.Fatalf("the question is %s, want withdrawn", got.status)
		}
	})

	t.Run("a caller holding the override permission answers it", func(t *testing.T) {
		q, column := question(t)
		result, err := move(t, asMod, q, types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Answered})
		wantMoved(t, "mod's answer", result, err, types.QuestionStatusState_Open, types.QuestionStatusState_Answered)
		if got := read(t, column); got.status != "answered" {
			t.Fatalf("the question is %s, want answered", got.status)
		}
	})

	t.Run("answering again answers unchanged and leaves the row's version", func(t *testing.T) {
		q, column := question(t)
		result, err := move(t, asBob, q, types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Answered})
		wantMoved(t, "bob's answer", result, err, types.QuestionStatusState_Open, types.QuestionStatusState_Answered)
		answered := read(t, column)
		again, err := move(t, asBob, q, types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Answered})
		if err != nil {
			t.Fatalf("bob's answer again: %v", err)
		}
		if again.From != types.QuestionStatusState_Answered || again.To != types.QuestionStatusState_Answered || again.Unchanged == nil || !*again.Unchanged {
			t.Fatalf("bob's answer again answered %+v, want from answered to answered, unchanged", again)
		}
		if got := read(t, column); got.status != "answered" || got.version != answered.version || !got.enteredAt.Equal(answered.enteredAt) {
			t.Fatalf("the question is %s at version %d since %s, want answered at version %d since %s", got.status, got.version, got.enteredAt, answered.version, answered.enteredAt)
		}
	})

	t.Run("a repeat on the Workflow without repeat is refused as already_in_state", func(t *testing.T) {
		q, _ := question(t)
		_, err := asBob.QuestionNamespace.Review.Transition(ctx, q, types.QuestionReviewTransitionInput{To: types.QuestionReviewState_Pending})
		wantVetoed(t, "bob's review to pending", err,
			"behavior Workflow vetoes transition of Question "+q+": Question "+q+"'s review is already pending",
			"already_in_state", "{\"from\":\"pending\",\"to\":\"pending\"}")
	})

	t.Run("a from that is not the row's state is refused as stale", func(t *testing.T) {
		q, column := question(t)
		_, err := move(t, asBob, q, types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Answered, From: from(types.QuestionStatusState_Answered)})
		wantVetoed(t, "bob's answer from answered", err,
			"behavior Workflow vetoes transition of Question "+q+": Question "+q+"'s status is open, not answered",
			"stale", "{\"from\":\"answered\",\"status\":\"open\"}")
		if got := read(t, column); got.status != "open" {
			t.Fatalf("the question is %s, want open", got.status)
		}
	})

	t.Run("a from that is the row's state moves it, and its retry is stale with the state it moved to", func(t *testing.T) {
		q, _ := question(t)
		input := types.QuestionStatusTransitionInput{To: types.QuestionStatusState_Answered, From: from(types.QuestionStatusState_Open)}
		result, err := move(t, asBob, q, input)
		wantMoved(t, "bob's answer from open", result, err, types.QuestionStatusState_Open, types.QuestionStatusState_Answered)
		_, err = move(t, asBob, q, input)
		wantVetoed(t, "bob's retry", err,
			"behavior Workflow vetoes transition of Question "+q+": Question "+q+"'s status is answered, not open",
			"stale", "{\"from\":\"open\",\"status\":\"answered\"}")
	})
}
`
