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

// stepsCases are the cases stepsTest runs, by name: the end-to-end scenario
// of a table that implements Dependencies and one that implements Rollups
// over the rows its HasMany lists (D104's step 4), with the core's words.
var stepsCases = []string{
	"addBlocker records the edge, and the gate holds the step's start until its blocker is done",
	"an edge that would close a cycle is refused as cycle",
	"listBlockers pages the edges in the order they were added, and removeBlocker takes one",
	"a batch's done waits for every step that points at it, not_held with the counts",
	"addBlocker of a key no row holds is not found",
}

// TestStepsGoServerOnPostgres builds fixture-steps-db and fixture-steps-api
// as build-all does, then writes stepsTest into the generated API module and
// runs it, after go vet: the generated Go server's Dependencies and
// Workflow operations of Step and Workflow operation of Batch (D104's step
// 4), whose gate Rollups holds, called through the generated Go SDK with a
// test authenticator in front of the routes, and the ORM's creates through
// the core, on the Postgres behaviorServerDatabaseEnv names. The module
// links the core's static archive, which scripts/rules-archive.sh stages;
// the test skips without it, without the database, and in -short mode.
func TestStepsGoServerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server of Rollups and Dependencies in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server of Rollups and Dependencies against Postgres")
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
	for _, service := range []string{"fixture-steps-db", "fixture-steps-api"} {
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
	apiDir := APIDir(outputRoot, "fixture-steps-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-steps-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_steps_server_test.go"), []byte(stepsTest), 0o644); err != nil {
		t.Fatal(err)
	}
	sdkModule := naming.Default().GoSDKModule("fixture-steps-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-steps-api"},
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
	for _, name := range stepsCases {
		if want := "--- PASS: TestSteps/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}

// stepsTest runs in the generated module of fixture-steps-api. It serves
// the generated routes over the ORM of fixture-steps-db, on a schema of its
// own in the Postgres behaviorServerDatabaseEnv names, with a test
// authenticator in front of them, and calls them through the generated Go
// SDK; it creates batches and steps through the ORM.
const stepsTest = `package fixturestepsapi_test

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

	api "example.com/schemas/api/fixture-steps-api"
	orm "example.com/schemas/orm/fixture-steps-db"
	sdk "example.com/schemas/sdk/go/fixture-steps-api"
	types "example.com/schemas/types/go/fixture-steps-api"
	dbtypes "example.com/schemas/types/go/fixture-steps-db"
)

// authenticate puts the bearer token's subject on the request, as an
// identity middleware puts its principal; no header, no caller.
func authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
			ctx := session.ContextWithPrincipalID(r.Context(), token)
			ctx = session.ContextWithRoles(ctx, []session.Role{{Name: "test"}})
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// openDatabase is the ORM of fixture-steps-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends, and
// its pool, which the test reads the tables through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server of Rollups and Dependencies against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("steps_go_%d", time.Now().UnixNano())
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

// sorted is a value's JSON with its keys sorted, as a refusal's details
// are compared.
func sorted(t *testing.T, value any) string {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// wantVetoed holds a call the server refused to the core's veto: 409, the
// code vetoed, the behavior, the veto's code and details, whose JSON is
// vetoDetails with its keys sorted.
func wantVetoed(t *testing.T, what string, err error, behavior, vetoCode, vetoDetails string) {
	t.Helper()
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: %v, want the API's refusal", what, err)
	}
	details := sorted(t, apiErr.Details["vetoDetails"])
	if apiErr.StatusCode != 409 || apiErr.Code != "vetoed" || apiErr.Details["behavior"] != behavior || apiErr.Details["vetoCode"] != vetoCode || (vetoDetails != "" && details != vetoDetails) {
		t.Fatalf("%s refused with %d %s %q (behavior %v, vetoCode %v, vetoDetails %s), want 409 vetoed by %s, vetoCode %s, vetoDetails %s", what, apiErr.StatusCode, apiErr.Code, apiErr.Message, apiErr.Details["behavior"], apiErr.Details["vetoCode"], details, behavior, vetoCode, vetoDetails)
	}
}

func TestSteps(t *testing.T) {
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
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	alice := subject(t)
	client.SetToken(alice)

	batch := func(t *testing.T) dbtypes.IdentityUUID {
		t.Helper()
		row, err := db.GetBatchRepository().CreateOne(ctx, &orm.BatchCreate{Title: "release"})
		if err != nil {
			t.Fatalf("create a batch: %v", err)
		}
		return *row.Id
	}
	step := func(t *testing.T, of dbtypes.IdentityUUID, title string) string {
		t.Helper()
		row, err := db.GetStepRepository().CreateOne(ctx, &orm.StepCreate{Title: title, Batch: dbtypes.Batch{Id: &of}})
		if err != nil {
			t.Fatalf("create a step: %v", err)
		}
		return row.Id.String()
	}
	block := func(t *testing.T, key, by string) types.StepDependenciesAddBlockerResult {
		t.Helper()
		added, err := client.StepNamespace.Dependencies.AddBlocker(ctx, key, types.StepDependenciesAddBlockerInput{Id: by})
		if err != nil {
			t.Fatalf("addBlocker: %v", err)
		}
		return added
	}
	moveStep := func(key string, to types.StepStatusState) error {
		_, err := client.StepNamespace.Status.Transition(ctx, key, types.StepStatusTransitionInput{To: to})
		return err
	}
	finish := func(t *testing.T, key string) {
		t.Helper()
		for _, to := range []types.StepStatusState{types.StepStatusState_Doing, types.StepStatusState_Done} {
			if err := moveStep(key, to); err != nil {
				t.Fatalf("move %s to %s: %v", key, to, err)
			}
		}
	}

	t.Run("addBlocker records the edge, and the gate holds the step's start until its blocker is done", func(t *testing.T) {
		b := batch(t)
		first, second := step(t, b, "build"), step(t, b, "ship")
		added := block(t, second, first)
		if added.Id != first || string(added.Schema) != "Step" || !added.Open || added.Status != "todo" {
			t.Fatalf("addBlocker = %+v, want Step %s, todo and open", added, first)
		}
		var edges int
		var blockerKey, blockerType string
		if err := pool.QueryRow(ctx, "SELECT count(*) OVER (), blocker_key, blocker_type FROM step_dependencies_blockers WHERE number = '0000000001'").Scan(&edges, &blockerKey, &blockerType); err != nil {
			t.Fatal(err)
		}
		if edges != 1 || blockerKey != first || blockerType != "Step" {
			t.Fatalf("the edge = %d %s %s, want one of Step %s", edges, blockerType, blockerKey, first)
		}
		wantVetoed(t, "a start while blocked", moveStep(second, types.StepStatusState_Doing), "Dependencies", "blocked", ` + "`" + `{"blockers":[{"id":"` + "`" + ` + first + ` + "`" + `","schema":"Step","status":"todo"}]}` + "`" + `)
		finish(t, first)
		if err := moveStep(second, types.StepStatusState_Doing); err != nil {
			t.Fatalf("a start once its blocker is done: %v", err)
		}
	})

	t.Run("an edge that would close a cycle is refused as cycle", func(t *testing.T) {
		b := batch(t)
		a, bb, c := step(t, b, "a"), step(t, b, "b"), step(t, b, "c")
		block(t, bb, a)
		block(t, c, bb)
		_, err := client.StepNamespace.Dependencies.AddBlocker(ctx, a, types.StepDependenciesAddBlockerInput{Id: c})
		wantVetoed(t, "a cycle", err, "Dependencies", "cycle", "")
	})

	t.Run("listBlockers pages the edges in the order they were added, and removeBlocker takes one", func(t *testing.T) {
		b := batch(t)
		a, bb, c := step(t, b, "a"), step(t, b, "b"), step(t, b, "c")
		block(t, c, bb)
		block(t, c, a)
		page, err := client.StepNamespace.Dependencies.ListBlockers(ctx, c, types.StepDependenciesListBlockersInput{Limit: types.InputField[types.GenericInt64]{Set: true, Value: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].Id != bb || page.Next == "" {
			t.Fatalf("the first page = %+v, want %s and a cursor", page, bb)
		}
		rest, err := client.StepNamespace.Dependencies.ListBlockers(ctx, c, types.StepDependenciesListBlockersInput{Cursor: types.InputField[string]{Set: true, Value: page.Next}})
		if err != nil {
			t.Fatal(err)
		}
		if len(rest.Items) != 1 || rest.Items[0].Id != a || rest.Next != "" {
			t.Fatalf("the next page = %+v, want %s and no cursor", rest, a)
		}
		removed, err := client.StepNamespace.Dependencies.RemoveBlocker(ctx, c, types.StepDependenciesRemoveBlockerInput{Id: bb})
		if err != nil || removed.Id != bb {
			t.Fatalf("removeBlocker = %+v, %v", removed, err)
		}
		all, err := client.StepNamespace.Dependencies.ListBlockers(ctx, c, types.StepDependenciesListBlockersInput{})
		if err != nil || len(all.Items) != 1 || all.Items[0].Id != a {
			t.Fatalf("listBlockers after removeBlocker = %+v, %v", all, err)
		}
	})

	t.Run("a batch's done waits for every step that points at it, not_held with the counts", func(t *testing.T) {
		b := batch(t)
		a, bb := step(t, b, "a"), step(t, b, "b")
		key := b.String()
		_, err := client.BatchNamespace.Status.Transition(ctx, key, types.BatchStatusTransitionInput{To: types.BatchStatusState_Done})
		wantVetoed(t, "a batch done before its steps", err, "Rollups", "not_held", ` + "`" + `{"counted":0,"linked":2,"over":false,"rollup":"done","to":"done"}` + "`" + `)
		finish(t, a)
		finish(t, bb)
		moved, err := client.BatchNamespace.Status.Transition(ctx, key, types.BatchStatusTransitionInput{To: types.BatchStatusState_Done})
		if err != nil || moved.To != types.BatchStatusState_Done {
			t.Fatalf("a batch done once its steps are = %+v, %v", moved, err)
		}
	})

	t.Run("addBlocker of a key no row holds is not found", func(t *testing.T) {
		_, err := client.StepNamespace.Dependencies.AddBlocker(ctx, subject(t), types.StepDependenciesAddBlockerInput{Id: "x"})
		var apiErr *sdk.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 || apiErr.Code != "not_found" {
			t.Fatalf("addBlocker of a key no row holds = %v, want 404 not_found", err)
		}
	})
}
`
