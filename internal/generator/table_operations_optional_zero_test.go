package generator

import (
	"os"
	"os/exec"
	"path/filepath"
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

// keepZeroIssueFields are the optional Issue members the test adds to the
// tracker's table and to each of its views: a number and a boolean
// without a default, and a string whose default is not "". Under
// go.optional_zero = "keep" (D161) the Go types hold each as a pointer,
// and the table operations must copy them as typegen declares them.
func keepZeroIssueFields() []*ir.FieldDef {
	triage := "triage"
	return []*ir.FieldDef{
		{Name: "weight", TypeRef: ir.TypeRef{Name: codegen.PrimitiveNumber}},
		{Name: "flagged", TypeRef: ir.TypeRef{Name: codegen.PrimitiveBoolean}},
		{Name: "label", TypeRef: ir.TypeRef{Name: codegen.PrimitiveString}, Default: &triage},
	}
}

// TestTableOperationsKeepEveryStatedValue builds fixture-tracker-db and
// fixture-tracker-api with three optional Issue members added to the table
// and its views, under go.optional_zero = "keep", and runs a test inside
// the generated API module (D106, D161). The module compiles only when the
// table operations hold each member as typegen does: tables.go copies
// between the API's types and the ORM's rows. Over HTTP, on the Postgres
// behaviorServerDatabaseEnv names, a create that states 0, false and ""
// stores and returns them, a read returns them, and an update that sets
// them to null clears them. The test skips without the database and in
// -short mode.
func TestTableOperationsKeepEveryStatedValue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's table operations in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server's table operations against Postgres")
	}
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	keep := naming.Default()
	keep.Go.OptionalZero = naming.GoOptionalKeep

	load := func(name string) (*ir.Schema, error) {
		schema, err := loader.LoadService(filepath.Join(tsFixtures, name))
		if err != nil {
			return nil, err
		}
		types := []string{"Issue"}
		if name == "fixture-tracker-api" {
			types = []string{"IssueView", "CreateIssueInput", "UpdateIssueInput"}
		}
		for _, typeName := range types {
			schema.Types[typeName].Fields = append(schema.Types[typeName].Fields, keepZeroIssueFields()...)
		}
		return schema, nil
	}
	for _, service := range []string{"fixture-tracker-db", "fixture-tracker-api"} {
		_, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		schema, err := load(service)
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if _, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Paths:          paths,
			Naming:         keep,
			Clock:          clock,
			LoadDependency: load,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	apiDir := APIDir(outputRoot, "fixture-tracker-api")
	types, err := os.ReadFile(filepath.Join(TypesDir(outputRoot, "go", "fixture-tracker-api"), "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Weight *float64 `json:\"weight,omitempty\"`", "Flagged *bool `json:\"flagged,omitempty\"`", "Label *string `json:\"label,omitempty\"`"} {
		if !strings.Contains(string(types), want) {
			t.Errorf("IssueView lacks %s", want)
		}
	}
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-tracker-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "keep_zero_test.go"), []byte(keepZeroTableTest), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{{"mod", "tidy"}, {"vet", "./..."}, {"test", "-count=1", "-v", "-run", "TestTableOperationsKeepZero", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		cmd.Env = append(cmd.Environ(), env...)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if !strings.Contains(string(out), "--- PASS: TestTableOperationsKeepZero ") {
		t.Fatalf("the generated module's test did not pass:\n%s", out)
	}
}

// keepZeroTableTest runs in the generated module of fixture-tracker-api.
const keepZeroTableTest = `package fixturetrackerapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-tracker-api"
	orm "example.com/schemas/orm/fixture-tracker-db"
	types "example.com/schemas/types/go/fixture-tracker-api"
)

type summaries struct{}

func (summaries) SummarizeTeam(ctx context.Context, id types.IdentityUUID) (*types.TeamSummary, error) {
	return &types.TeamSummary{TeamId: id}, nil
}

func TestTableOperationsKeepZero(t *testing.T) {
	target := os.Getenv("` + behaviorServerDatabaseEnv + `")
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("table_ops_keep_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(target)
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
		t.Fatal(err)
	}
	db, err := orm.ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := session.ContextWithPrincipalID(r.Context(), "11111111-1111-4111-8111-111111111111")
			ctx = session.ContextWithRoles(ctx, []session.Role{{Name: "test", Permissions: []string{"teams.write", "issues.read", "issues.write"}}})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	if err := api.RegisterRoutes(router, api.Config{
		DB:              db,
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Teams: summaries{}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	call := func(method, path, body string) map[string]any {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+"/api"+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
		var envelope struct {
			Data map[string]any ` + "`json:\"data\"`" + `
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
		return envelope.Data
	}
	// column reads a column of the one issue; the wire's id is in its
	// public form, so the row is found by its title.
	column := func(_ any, name string) any {
		t.Helper()
		var value any
		if err := pool.QueryRow(ctx, "SELECT "+name+" FROM issue WHERE title = 't'").Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	members := func(what string, data map[string]any, weight, flagged, label any) {
		t.Helper()
		for name, want := range map[string]any{"weight": weight, "flagged": flagged, "label": label} {
			got, present := data[name]
			if want == nil && present {
				t.Errorf("%s: %s = %v, want it left out", what, name, got)
			}
			if want != nil && got != want {
				t.Errorf("%s: %s = %#v (present %t), want %#v", what, name, got, present, want)
			}
		}
	}

	team := call("POST", "/teams", ` + "`" + `{"key":"core","name":"Core"}` + "`" + `)
	created := call("POST", "/teams/"+team["id"].(string)+"/issues", ` + "`" + `{"title":"t","weight":0,"flagged":false,"label":""}` + "`" + `)
	members("create", created, float64(0), false, "")
	id := created["id"].(string)
	if column(id, "weight") == nil || column(id, "flagged") == nil || column(id, "label") != "" {
		t.Errorf("stored weight %v, flagged %v, label %#v; want 0, false and \"\"", column(id, "weight"), column(id, "flagged"), column(id, "label"))
	}
	members("read", call("GET", "/issues/"+id, ""), float64(0), false, "")
	members("set", call("PATCH", "/issues/"+id, ` + "`" + `{"weight":2.5,"flagged":true,"label":"bug"}` + "`" + `), 2.5, true, "bug")
	members("clear", call("PATCH", "/issues/"+id, ` + "`" + `{"weight":null,"flagged":null,"label":null}` + "`" + `), nil, nil, nil)
	if column(id, "weight") != nil || column(id, "flagged") != nil || column(id, "label") != nil {
		t.Errorf("cleared columns hold %v, %v, %v; want NULL", column(id, "weight"), column(id, "flagged"), column(id, "label"))
	}
}
`
