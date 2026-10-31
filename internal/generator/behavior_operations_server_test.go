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

// behaviorServerDatabaseEnv names the Postgres the generated server's
// behavior operations run against (D103), as the rules module's adapter
// tests do.
const behaviorServerDatabaseEnv = "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL"

// behaviorServerCases are the cases behaviorServerTest runs, by name: the
// end-to-end scenario every server's behavior operations are held to, in
// Go, TypeScript and Rust, with the core's words, which ormgen's
// TestWorkflowTablesOnPostgres holds the Go ORM to.
var behaviorServerCases = []string{
	"a transition moves the field and returns the result",
	"a move the config has no transition for is refused with the core's status, code and message",
	"a caller without the route's permission is refused by the API's auth",
	"a caller without the permission the config names is refused by the core, and one who holds it moves the row",
	"a key no row holds is not found",
	"a table's two Workflows move apart",
}

// TestBehaviorOperationsGoServerServesTheSDK builds fixture-workflow-db and
// fixture-workflow-api as build-all does, then writes behaviorServerTest
// into the generated API module and runs it, after go vet: the generated
// Go server's behavior operations (D103), mounted on handlers that call
// the ORM of fixture-workflow-db, which runs them through the behaviors'
// core on the Postgres behaviorServerDatabaseEnv names, called through the
// generated Go SDK with a test authenticator in front of the routes, as a
// Go server the project writes mounts them: fixture-workflow-api is not
// public and has no user model, so a stack's generated Go server of it,
// which would authenticate no caller, is refused (servergen's
// TestGoServerOfBehaviorOperationsNeedsACaller). It runs behaviorCallerTest
// too. The module links the core's static archive, which
// scripts/rules-archive.sh stages; the test skips without it, without the
// database, and in -short mode.
func TestBehaviorOperationsGoServerServesTheSDK(t *testing.T) {
	runBehaviorServerTest(t, behaviorServerTest)
}

// runBehaviorServerTest builds the two fixtures into a temporary output
// root and runs test, a Go test file's source, in the generated API module
// of fixture-workflow-api, requiring every case of behaviorServerCases to
// pass.
func runBehaviorServerTest(t *testing.T, test string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the generated Go server's behavior operations in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server's behavior operations against Postgres")
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
	for _, service := range []string{"fixture-workflow-db", "fixture-workflow-api"} {
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

	apiDir := APIDir(outputRoot, "fixture-workflow-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-workflow-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_operations_server_test.go"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_caller_internal_test.go"), []byte(behaviorCallerTest), 0o644); err != nil {
		t.Fatal(err)
	}

	// The test reaches the Go SDK beside what the API module requires, and
	// links the four archives a Go binary of the checkout links.
	sdkModule := naming.Default().GoSDKModule("fixture-workflow-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-workflow-api"},
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
	for _, name := range behaviorServerCases {
		if want := "--- PASS: TestBehaviorOperations/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
	if !strings.Contains(string(out), "--- PASS: TestBehaviorCaller ") {
		t.Error("the generated module's TestBehaviorCaller did not pass")
	}
}

// behaviorCallerTest runs in the generated module of fixture-workflow-api,
// in its package: behaviorCaller, the caller a behavior operation runs as
// (D103), in each of its three forms, which the end-to-end cases reach
// only the first of: the request's principal, with its roles'
// permissions, each once, ahead of a calling service; a calling service
// with no principal, standing in for an end user and holding no
// permission (D37); and no one.
const behaviorCallerTest = `package fixtureworkflowapi

import (
	"context"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

func TestBehaviorCaller(t *testing.T) {
	service := serviceauth.WithCaller(context.Background(), &serviceauth.Caller{Deployable: "billing-worker", Subject: "billing"})
	user := session.ContextWithPrincipalID(service, "sam")
	user = session.ContextWithRoles(user, []session.Role{
		{Name: "clerk", Permissions: []string{"orders.write", "orders.ship"}},
		{Name: "lead", Permissions: []string{"orders.ship", "payments.refund"}},
	})
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want rules.Caller
	}{
		{"the principal", user, rules.Caller{Subject: "sam", Permissions: []string{"orders.write", "orders.ship", "payments.refund"}}},
		{"a calling service", service, rules.Caller{Subject: "billing-worker", StandsIn: true}},
		{"no one", context.Background(), rules.Caller{}},
	} {
		if got := behaviorCaller(tc.ctx); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: behaviorCaller = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
`

// archiveLDFlags is CGO_LDFLAGS for a Go binary that links the
// checkout's four static archives, superscalar's, the version graph's, the
// engine's and the behaviors' core's: each staging script's --print.
func archiveLDFlags(t *testing.T) string {
	t.Helper()
	root := testpaths.RepoRoot(t)
	var flags []string
	for _, script := range []string{"superscalar-dep.sh", "versiongraph-archive.sh", "engine-archive.sh", "rules-archive.sh"} {
		out, err := exec.Command(filepath.Join(root, "scripts", script), "--print").Output()
		if err != nil {
			t.Fatalf("scripts/%s --print: %v", script, err)
		}
		flags = append(flags, strings.TrimSpace(string(out)))
	}
	return strings.Join(flags, " ")
}

// behaviorServerTest runs in the generated module of fixture-workflow-api.
// It serves the generated routes over the ORM of fixture-workflow-db, on a
// schema of its own in the Postgres behaviorServerDatabaseEnv names, with
// a test authenticator in front of them, and calls them through the
// generated Go SDK.
const behaviorServerTest = `package fixtureworkflowapi_test

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
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-workflow-api"
	orm "example.com/schemas/orm/fixture-workflow-db"
	sdk "example.com/schemas/sdk/go/fixture-workflow-api"
	types "example.com/schemas/types/go/fixture-workflow-api"
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

// orders is the project's implementation of getOrder, the one operation
// no generated handler serves: the row as the ORM reads it.
type orders struct{ db orm.DatabaseInterface }

func (o orders) GetOrder(ctx context.Context, id types.IdentityUUID) (*types.Order, error) {
	row, err := o.db.GetOrderRepository().GetOne(ctx, id, nil)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("order", err)
	}
	return row, err
}

// openDatabase is the ORM of fixture-workflow-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends.
func openDatabase(t *testing.T) *orm.Database {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server's behavior operations against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("behavior_ops_go_%d", time.Now().UnixNano())
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
	return db
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

func TestBehaviorOperations(t *testing.T) {
	ctx := context.Background()
	db := openDatabase(t)
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(authenticate)
	if err := api.RegisterRoutes(router, api.Config{
		DB:              db,
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Order: orders{db: db}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	as := func(token string) *sdk.FixtureWorkflowApiSDK {
		t.Helper()
		client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			client.SetToken(token)
		}
		return client
	}
	sam, kim, lee, nobody := as("sam:orders.write"), as("kim:orders.write,orders.ship"), as("lee"), as("")

	order, err := db.GetOrderRepository().CreateOne(ctx, &orm.OrderCreate{Title: "o"})
	if err != nil {
		t.Fatalf("create the order: %v", err)
	}
	shipment, err := db.GetShipmentRepository().CreateOne(ctx, &orm.ShipmentCreate{Reference: "S-1"})
	if err != nil {
		t.Fatalf("create the shipment: %v", err)
	}
	o, s := order.Id.String(), shipment.Id.String()
	orderID, shipmentID := *order.Id, *shipment.Id
	row := func(t *testing.T) (string, int64) {
		t.Helper()
		got, err := db.GetOrderRepository().GetOne(ctx, orderID, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got.Status, got.Version
	}

	t.Run("a transition moves the field and returns the result", func(t *testing.T) {
		result, err := sam.OrderNamespace.Status.Transition(ctx, o, types.OrderStatusTransitionInput{To: types.OrderStatusState_Paid})
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
		if result.From != types.OrderStatusState_Draft || result.To != types.OrderStatusState_Paid {
			t.Fatalf("transition answered %+v, want from draft to paid", result)
		}
		if status, version := row(t); status != "paid" || version != 2 {
			t.Fatalf("the row is %s at version %d, want paid at version 2", status, version)
		}
	})

	t.Run("a move the config has no transition for is refused with the core's status, code and message", func(t *testing.T) {
		_, err := sam.OrderNamespace.Status.Transition(ctx, o, types.OrderStatusTransitionInput{To: types.OrderStatusState_Draft})
		got := failed(t, "a move back to draft", err)
		want := "behavior Workflow vetoes transition of Order " + o + ": no transition leads from paid to draft; from paid it can move to shipped"
		if got.status != 409 || got.code != "vetoed" || got.detail != want || got.details["vetoCode"] != "transition_not_allowed" {
			t.Fatalf("refused with %+v, want 409 vetoed %q, vetoCode transition_not_allowed", got, want)
		}
		if status, version := row(t); status != "paid" || version != 2 {
			t.Fatalf("the row is %s at version %d, want it unchanged", status, version)
		}
	})

	t.Run("a caller without the route's permission is refused by the API's auth", func(t *testing.T) {
		_, err := lee.OrderNamespace.Status.Transition(ctx, o, types.OrderStatusTransitionInput{To: types.OrderStatusState_Shipped})
		if got := failed(t, "lee's move", err); got.status != 403 || strings.Contains(got.detail, "may not move") {
			t.Fatalf("lee refused with %+v, want the API's 403, not the core's", got)
		}
		_, err = nobody.OrderNamespace.Status.Transition(ctx, o, types.OrderStatusTransitionInput{To: types.OrderStatusState_Shipped})
		if got := failed(t, "a move with no caller", err); got.status != 401 {
			t.Fatalf("no caller refused with %+v, want 401", got)
		}
		if status, version := row(t); status != "paid" || version != 2 {
			t.Fatalf("the row is %s at version %d, want it unchanged", status, version)
		}
	})

	t.Run("a caller without the permission the config names is refused by the core, and one who holds it moves the row", func(t *testing.T) {
		_, err := sam.OrderNamespace.Status.Transition(ctx, o, types.OrderStatusTransitionInput{To: types.OrderStatusState_Shipped})
		got := failed(t, "sam's move to shipped", err)
		want := "sam may not move Order " + o + "'s status from paid to shipped: the transition needs permission orders.ship"
		if got.status != 403 || got.code != "forbidden" || got.detail != want {
			t.Fatalf("sam refused with %+v, want 403 forbidden %q", got, want)
		}
		result, err := kim.OrderNamespace.Status.Transition(ctx, o, types.OrderStatusTransitionInput{To: types.OrderStatusState_Shipped})
		if err != nil || result.From != types.OrderStatusState_Paid || result.To != types.OrderStatusState_Shipped {
			t.Fatalf("kim's move: %+v, %v, want from paid to shipped", result, err)
		}
		// The output type carries the field the Workflow owns.
		read, err := kim.OrderNamespace.GetOrder(ctx, o)
		if err != nil || read.Status != "shipped" || read.Version != 3 {
			t.Fatalf("getOrder: %+v, %v, want shipped at version 3", read, err)
		}
	})

	t.Run("a key no row holds is not found", func(t *testing.T) {
		missing, err := types.ParseIdentityUUID("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
		if err != nil {
			t.Fatal(err)
		}
		key := missing.String()
		_, err = sam.OrderNamespace.Status.Transition(ctx, key, types.OrderStatusTransitionInput{To: types.OrderStatusState_Paid})
		got := failed(t, "a move of a missing order", err)
		if want := "Order " + key + " does not exist"; got.status != 404 || got.code != "not_found" || got.detail != want {
			t.Fatalf("refused with %+v, want 404 not_found %q", got, want)
		}
	})

	t.Run("a table's two Workflows move apart", func(t *testing.T) {
		packed, err := lee.ShipmentNamespace.Status.Transition(ctx, s, types.ShipmentStatusTransitionInput{To: types.ShipmentStatusState_Packed})
		if err != nil || packed.To != types.ShipmentStatusState_Packed {
			t.Fatalf("status: %+v, %v", packed, err)
		}
		paid, err := lee.ShipmentNamespace.PaymentStatus.Transition(ctx, s, types.ShipmentPaymentStatusTransitionInput{To: types.ShipmentPaymentStatusState_Paid})
		if err != nil || paid.To != types.ShipmentPaymentStatusState_Paid {
			t.Fatalf("paymentStatus: %+v, %v", paid, err)
		}
		got, err := db.GetShipmentRepository().GetOne(ctx, shipmentID, nil)
		if err != nil || got.Status != "packed" || got.PaymentStatus != "paid" {
			t.Fatalf("the shipment is %+v, %v, want packed and paid", got, err)
		}
		_, err = lee.ShipmentNamespace.PaymentStatus.Transition(ctx, s, types.ShipmentPaymentStatusTransitionInput{To: types.ShipmentPaymentStatusState_Refunded})
		refused := failed(t, "lee's refund", err)
		want := "lee may not move Shipment " + s + "'s paymentStatus from paid to refunded: the transition needs permission payments.refund"
		if refused.status != 403 || refused.code != "forbidden" || refused.detail != want {
			t.Fatalf("lee's refund refused with %+v, want 403 forbidden %q", refused, want)
		}
	})
}
`
