package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// userRoutesAPI is the API whose @userSessions and @userAdministration sets
// the loader fills from fixture-user-model-db (D50).
const userRoutesAPI = "fixture-user-routes-api"

func loadUserRoutesAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesAPI, err)
	}
	return schema
}

func generateUserRoutesAPI(t *testing.T, schema *ir.Schema) *apigen.APIOutput {
	t.Helper()
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  userRoutesAPI,
		ModulePath:  "example.com/schemas/api/" + userRoutesAPI,
		TypesModule: "example.com/schemas/types/go/" + userRoutesAPI,
		Clock:       goModuleClock,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return output
}

// TestUserRoutesAreEndpoints: the user model's operations are endpoints as
// any other, each naming its operation, so the OpenAPI document, the SDKs
// and the tool manifest read them.
func TestUserRoutesAreEndpoints(t *testing.T) {
	output := generateUserRoutesAPI(t, loadUserRoutesAPI(t))
	if len(output.Endpoints) != 19 {
		t.Fatalf("got %d endpoints, want 19", len(output.Endpoints))
	}
	byName := map[string]apigen.EndpointInfo{}
	for _, endpoint := range output.Endpoints {
		byName[endpoint.Name] = endpoint
		if (endpoint.Name == "greet") != (endpoint.IdentityOperation == "") {
			t.Errorf("%s: IdentityOperation = %q", endpoint.Name, endpoint.IdentityOperation)
		}
	}
	login := byName["login"]
	if login.Path != "/api/auth/login" || login.Method != "POST" || !login.PublicRoute || login.RequiresAuth ||
		login.InputType != "LoginInput" || *login.RateLimit != 10 || login.MCP == nil || !login.MCP.Hidden {
		t.Errorf("login = %+v", login)
	}
	if me := byName["me"]; me.Path != "/api/auth/me" || me.Method != "GET" || !me.RequiresAuth || me.MCP != nil {
		t.Errorf("me = %+v", me)
	}
	grant := byName["grantRole"]
	if grant.Path != "/api/auth/admin/users/{id}/roles/{roleId}" || grant.Method != "PUT" ||
		!slices.Equal(grant.RequiredPerms, []string{"identity.roles.write"}) || len(grant.PathParams) != 2 {
		t.Errorf("grantRole = %+v", grant)
	}
	if !slices.Equal(output.Namespaces, []string{"account", "account-admin", "greeting"}) {
		t.Errorf("Namespaces = %v", output.Namespaces)
	}
	if !output.RoutesNeedTime || !output.HasPermissionEndpoints {
		t.Error("the output's flags leave out the user model's endpoints")
	}
}

// TestImplementedOutputLeavesOutUserRoutes: the view the Go server is
// written from has the project's endpoints alone, and the flags and
// namespaces they give; the output the SDKs read is unchanged, and an
// output without a user model operation is its own view.
func TestImplementedOutputLeavesOutUserRoutes(t *testing.T) {
	output := generateUserRoutesAPI(t, loadUserRoutesAPI(t))
	view, err := apigen.ImplementedOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Endpoints) != 1 || view.Endpoints[0].Name != "greet" {
		t.Fatalf("view endpoints = %+v", view.Endpoints)
	}
	if !slices.Equal(view.Namespaces, []string{"greeting"}) || view.RoutesNeedTime || view.HasPermissionEndpoints || !view.HasAuth {
		t.Errorf("view: namespaces %v, RoutesNeedTime %v, HasPermissionEndpoints %v, HasAuth %v", view.Namespaces, view.RoutesNeedTime, view.HasPermissionEndpoints, view.HasAuth)
	}
	if view.OpenAPISpecRaw != output.OpenAPISpecRaw || len(output.Endpoints) != 19 {
		t.Error("the view changed the output, or its OpenAPI document")
	}
	plain := generateServiceAuthFixtureAPI(t)
	if same, err := apigen.ImplementedOutput(plain); err != nil || same != plain {
		t.Errorf("ImplementedOutput(an API without the user model) = %p, %v; want the output itself", same, err)
	}
}

// TestOpenAPIUserRoutesGolden pins the OpenAPI document of
// fixture-user-routes-api, which describes every route, the identity
// runtime's included. Regenerate with
// go test ./internal/generator/apigen -run TestOpenAPIUserRoutesGolden -update
func TestOpenAPIUserRoutesGolden(t *testing.T) {
	output := generateUserRoutesAPI(t, loadUserRoutesAPI(t))
	golden := filepath.Join("testdata", "golden", userRoutesAPI, "openapi.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(output.OpenAPISpecRaw), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if string(want) != output.OpenAPISpecRaw {
		t.Fatalf("OpenAPI document changed; run with -update and review the diff")
	}

	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Description string `json:"description"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(want, &spec); err != nil {
		t.Fatal(err)
	}
	// Each user model route declares the errors the identity runtime
	// answers it with; the project's route keeps the 400 and 500 alone.
	statuses := func(path, method string) []string {
		var out []string
		for status := range spec.Paths[path][method].Responses {
			out = append(out, status)
		}
		slices.Sort(out)
		return out
	}
	for _, tc := range []struct {
		path, method string
		want         []string
	}{
		{"/api/auth/login", "post", []string{"200", "400", "401", "403", "500"}},
		{"/api/auth/me", "get", []string{"200", "400", "401", "500"}},
		{"/api/auth/admin/roles/{id}", "put", []string{"200", "400", "401", "403", "404", "409", "422", "500"}},
		{"/api/greeting", "get", []string{"200", "400", "500"}},
	} {
		if got := statuses(tc.path, tc.method); !slices.Equal(got, tc.want) {
			t.Errorf("%s %s responses = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
	if got := spec.Paths["/api/auth/login"]["post"].Responses["401"].Description; !strings.HasPrefix(got, ir.IdentityCodeInvalidCredentials+": ") {
		t.Errorf("login's 401 = %q", got)
	}
	for path, method := range map[string]string{
		"/api/auth/login": "post", "/api/auth/logout": "post", "/api/auth/me": "get", "/api/auth/capabilities": "get",
		"/api/auth/password": "post", "/api/auth/register": "post", "/api/auth/admin/users/{id}/roles/{roleId}": "delete",
		"/api/greeting": "get",
	} {
		if _, ok := spec.Paths[path][method]; !ok {
			t.Errorf("the OpenAPI document has no %s %s", method, path)
		}
	}
}

// TestWriteAPIGoldenUserRoutes pins routes.go and interfaces.go of
// fixture-user-routes-api: the project's interface and route alone, the
// user model's being the identity runtime's. Regenerate with
// go test ./internal/generator/apigen -run TestWriteAPIGoldenUserRoutes -update
func TestWriteAPIGoldenUserRoutes(t *testing.T) {
	output := generateUserRoutesAPI(t, loadUserRoutesAPI(t))
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", userRoutesAPI), []string{"routes.go", "interfaces.go"})
	for _, name := range []string{"routes.go", "interfaces.go"} {
		src, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, absent := range []string{"Login", "ChangePassword", "AccountAdmin", "auth/login", "LoginInput"} {
			if strings.Contains(string(src), absent) {
				t.Errorf("%s mentions %s, which the identity runtime serves", name, absent)
			}
		}
	}
}

// TestUserRoutesServerCompiles compiles the Go server of
// fixture-user-routes-api, and the implementation scaffold beside it, with
// no implementation of the user model's operations; and the same for the
// API with its route sets alone, whose implementation has none.
func TestUserRoutesServerCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	compile := func(t *testing.T, schema *ir.Schema) {
		apiDir := writeGoAPIModule(t, schema, userRoutesAPI)
		if _, err := apigen.WriteImplementationScaffold(generateUserRoutesAPI(t, schema), filepath.Join(apiDir, "impl")); err != nil {
			t.Fatal(err)
		}
		runGoAPIModule(t, apiDir)
	}
	t.Run("with a project operation", func(t *testing.T) {
		compile(t, loadUserRoutesAPI(t))
	})
	t.Run("the route sets alone", func(t *testing.T) {
		schema := loadUserRoutesAPI(t)
		schema.OperationSets = slices.DeleteFunc(schema.OperationSets, func(set *ir.OperationSet) bool { return !set.IsIdentityRoutes() })
		delete(schema.Types, "Greeting")
		compile(t, schema)
	})
}

// TestUserRoutesHaveNoScaffold: the scaffolds and the implementation
// scaffold have no file or method for a user model operation.
func TestUserRoutesHaveNoScaffold(t *testing.T) {
	output := generateUserRoutesAPI(t, loadUserRoutesAPI(t))
	scaffolds := t.TempDir()
	if _, err := apigen.WriteScaffolds(output, scaffolds); err != nil {
		t.Fatal(err)
	}
	var files []string
	if err := filepath.WalkDir(scaffolds, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(scaffolds, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.Contains(file, "account") || strings.Contains(file, "login") {
			t.Errorf("scaffold %s is for a user model operation", file)
		}
	}
	if !slices.ContainsFunc(files, func(file string) bool { return strings.Contains(file, "greet") }) {
		t.Errorf("no scaffold for greet in %v", files)
	}

	impl := filepath.Join(t.TempDir(), "impl")
	if _, err := apigen.WriteImplementationScaffold(output, impl); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(impl, apigen.ImplementationFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "Login(") || strings.Contains(string(src), "CreateUser(") || !strings.Contains(string(src), "Greet(") {
		t.Errorf("implementation scaffold:\n%s", src)
	}
}
