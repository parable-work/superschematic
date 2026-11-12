package apigen_test

import (
	"encoding/json"
	"maps"
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
// the loader fills from userModelDB, its authDb (D50).
const (
	userRoutesAPI = "fixture-user-routes-api"
	userModelDB   = "fixture-user-model-db"
)

func loadUserModelDB(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userModelDB))
	if err != nil {
		t.Fatalf("load %s: %v", userModelDB, err)
	}
	return schema
}

func loadUserRoutesAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesAPI, err)
	}
	return schema
}

// generateUserRoutesAPI generates the API as the build does: not public,
// with its authDb's IR, which declares the user model, so its server
// authenticates with the identity runtime.
func generateUserRoutesAPI(t *testing.T, schema *ir.Schema) *apigen.APIOutput {
	t.Helper()
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  userRoutesAPI,
		ModulePath:  "example.com/schemas/api/" + userRoutesAPI,
		TypesModule: "example.com/schemas/types/go/" + userRoutesAPI,
		UpstreamIR:  loadUserModelDB(t),
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
	// The six session routes and the three API key routes of Account, the
	// thirteen administration routes and the two API key routes of
	// AccountAdmin (D114), and greet.
	if len(output.Endpoints) != 25 {
		t.Fatalf("got %d endpoints, want 25", len(output.Endpoints))
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
	// The fixture registers under approval (D81): register answers
	// RegisterResult, approveUser needs users.write, and the output carries
	// the registration the identity wiring hands the runtime.
	if register := byName["register"]; register.OutputType != "RegisterResult" {
		t.Errorf("register answers %s, want RegisterResult", register.OutputType)
	}
	approve := byName["approveUser"]
	if approve.Path != "/api/auth/admin/users/{id}/approve" || approve.Method != "POST" ||
		!slices.Equal(approve.RequiredPerms, []string{"identity.users.write"}) || len(approve.PathParams) != 1 {
		t.Errorf("approveUser = %+v", approve)
	}
	if list := byName["listUsers"]; len(list.ScalarArgs) != 1 || list.ScalarArgs[0].Name != "pending" || list.ArgumentsInBody() {
		t.Errorf("listUsers' arguments = %+v, want pending in the query", list.ScalarArgs)
	}
	// The API key routes (D114): a signed-in user's own under auth/keys,
	// createApiKey rate-limited and hidden as a tool for the credential it
	// answers, and an administrator's under a user, behind users.read and
	// users.write.
	create := byName["createApiKey"]
	if create.Path != "/api/auth/keys" || create.Method != "POST" || create.PublicRoute || !create.RequiresAuth ||
		create.InputType != "CreateApiKeyInput" || create.OutputType != "CreatedApiKey" || create.RateLimit == nil || *create.RateLimit != ir.IdentityCreateAPIKeyRateLimit ||
		len(create.RequiredPerms) != 0 || len(create.PathParams) != 0 ||
		create.MCP == nil || !create.MCP.Hidden || create.MCP.HiddenReason != ir.IdentityAPIKeyToolReason {
		t.Errorf("createApiKey = %+v, mcp %+v", create, create.MCP)
	}
	if list := byName["listApiKeys"]; list.Path != "/api/auth/keys" || list.Method != "GET" || !list.RequiresAuth ||
		list.OutputType != "ApiKey" || !list.OutputIsArray || list.RateLimit != nil || len(list.RequiredPerms) != 0 || list.MCP != nil {
		t.Errorf("listApiKeys = %+v", list)
	}
	if revoke := byName["revokeApiKey"]; revoke.Path != "/api/auth/keys/{id}" || revoke.Method != "DELETE" || !revoke.RequiresAuth ||
		revoke.OutputType != "boolean" || revoke.RateLimit != nil || len(revoke.RequiredPerms) != 0 || revoke.MCP != nil ||
		len(revoke.PathParams) != 1 || revoke.PathParams[0].Name != ir.IdentityIDArgument {
		t.Errorf("revokeApiKey = %+v", revoke)
	}
	if list := byName["listUserApiKeys"]; list.Path != "/api/auth/admin/users/{id}/keys" || list.Method != "GET" ||
		!slices.Equal(list.RequiredPerms, []string{"identity.users.read"}) || list.OutputType != "ApiKey" || !list.OutputIsArray || list.MCP != nil ||
		len(list.PathParams) != 1 || list.PathParams[0].Name != ir.IdentityIDArgument {
		t.Errorf("listUserApiKeys = %+v", list)
	}
	if revoke := byName["revokeUserApiKey"]; revoke.Path != "/api/auth/admin/users/{id}/keys/{keyId}" || revoke.Method != "DELETE" ||
		!slices.Equal(revoke.RequiredPerms, []string{"identity.users.write"}) || revoke.OutputType != "boolean" || revoke.MCP != nil ||
		len(revoke.PathParams) != 2 || revoke.PathParams[0].Name != ir.IdentityIDArgument || revoke.PathParams[1].Name != ir.IdentityKeyIDArgument {
		t.Errorf("revokeUserApiKey = %+v", revoke)
	}
	// The operations that carry a password are hidden with that reason, and
	// createApiKey with its own: the six hidden tools of the two sets.
	hidden := map[string]string{}
	for _, endpoint := range output.Endpoints {
		if endpoint.MCP != nil && endpoint.MCP.Hidden {
			hidden[endpoint.Name] = endpoint.MCP.HiddenReason
		}
	}
	if want := map[string]string{
		"login": ir.IdentityPasswordToolReason, "register": ir.IdentityPasswordToolReason, "changePassword": ir.IdentityPasswordToolReason,
		"createUser": ir.IdentityPasswordToolReason, "setUserPassword": ir.IdentityPasswordToolReason,
		"createApiKey": ir.IdentityAPIKeyToolReason,
	}; !maps.Equal(hidden, want) {
		t.Errorf("hidden tools = %v, want %v", hidden, want)
	}
	if reg := output.Registration; reg == nil || !reg.Approval() || !slices.Equal(reg.DefaultRoles, []string{"member"}) || reg.FirstUser == nil {
		t.Errorf("Registration = %+v, want approval with member and a first administrator", output.Registration)
	}
	if !slices.Equal(output.Namespaces, []string{"account", "account-admin", "greeting"}) {
		t.Errorf("Namespaces = %v", output.Namespaces)
	}
	if !output.RoutesNeedTime || !output.HasPermissionEndpoints {
		t.Error("the output's flags leave out the user model's endpoints")
	}
}

// TestImplementedOutputLeavesOutUserRoutes: the view the Go server is
// written from implements the project's endpoints alone, with the
// namespaces they give, and holds the user model's in IdentityEndpoints;
// the flags the router reads count both, since it mounts both. The output
// the SDKs read is unchanged, and an output without a user model operation
// is its own view.
func TestImplementedOutputLeavesOutUserRoutes(t *testing.T) {
	output := generateUserRoutesAPI(t, loadUserRoutesAPI(t))
	view, err := apigen.ImplementedOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Endpoints) != 1 || view.Endpoints[0].Name != "greet" {
		t.Fatalf("view endpoints = %+v", view.Endpoints)
	}
	if len(view.IdentityEndpoints) != 24 || len(view.RoutedEndpoints()) != 25 {
		t.Fatalf("view: %d identity endpoints, %d routed", len(view.IdentityEndpoints), len(view.RoutedEndpoints()))
	}
	// The administration routes' permission check is the identity
	// service's, so HasPermissionEndpoints, which gates the provider's
	// routePermissions imports, leaves them out.
	if !slices.Equal(view.Namespaces, []string{"greeting"}) || !view.RoutesNeedTime || view.HasPermissionEndpoints || !view.HasAuth {
		t.Errorf("view: namespaces %v, RoutesNeedTime %v, HasPermissionEndpoints %v, HasAuth %v", view.Namespaces, view.RoutesNeedTime, view.HasPermissionEndpoints, view.HasAuth)
	}
	if !view.Auth.Identity || !view.AuthWired() || view.IsPublic {
		t.Errorf("view: Auth %+v, AuthWired %v, IsPublic %v; want the identity wiring without public", view.Auth, view.AuthWired(), view.IsPublic)
	}
	if view.OpenAPISpecRaw != output.OpenAPISpecRaw || len(output.Endpoints) != 25 {
		t.Error("the view changed the output, or its OpenAPI document")
	}
	plain := generateServiceAuthFixtureAPI(t)
	if same, err := apigen.ImplementedOutput(plain); err != nil || same != plain {
		t.Errorf("ImplementedOutput(an API without the user model) = %p, %v; want the output itself", same, err)
	}

	// With the user model's routes alone, the router's flags still need
	// the auth wiring: every route that needs a caller is the runtime's.
	schema := loadUserRoutesAPI(t)
	schema.OperationSets = slices.DeleteFunc(schema.OperationSets, func(set *ir.OperationSet) bool { return !set.IsIdentityRoutes() })
	delete(schema.Types, "Greeting")
	alone, err := apigen.ImplementedOutput(generateUserRoutesAPI(t, schema))
	if err != nil {
		t.Fatal(err)
	}
	if len(alone.Endpoints) != 0 || len(alone.IdentityEndpoints) != 24 || !alone.HasAuth || alone.HasPermissionEndpoints || !alone.RoutesNeedTime {
		t.Errorf("the route sets alone: %d endpoints, %d identity endpoints, HasAuth %v, HasPermissionEndpoints %v, RoutesNeedTime %v", len(alone.Endpoints), len(alone.IdentityEndpoints), alone.HasAuth, alone.HasPermissionEndpoints, alone.RoutesNeedTime)
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
		// Under approval register answers 202 for a pending user (D81).
		{"/api/auth/register", "post", []string{"200", "202", "400", "403", "409", "500"}},
		{"/api/auth/admin/users/{id}/approve", "post", []string{"200", "400", "401", "403", "404", "409", "500"}},
		{"/api/auth/me", "get", []string{"200", "400", "401", "500"}},
		{"/api/auth/admin/roles/{id}", "put", []string{"200", "400", "401", "403", "404", "409", "422", "500"}},
		// logout and changePassword refuse a caller authenticated by an API
		// key, beside the cross-origin refusal (D114).
		{"/api/auth/logout", "post", []string{"200", "400", "401", "403", "500"}},
		{"/api/auth/password", "post", []string{"200", "400", "401", "403", "500"}},
		// The API key routes: createApiKey answers 422 for a malformed
		// permission, and a revoke 404 for a key that is not there.
		{"/api/auth/keys", "post", []string{"200", "400", "401", "403", "422", "500"}},
		{"/api/auth/keys", "get", []string{"200", "400", "401", "403", "500"}},
		{"/api/auth/keys/{id}", "delete", []string{"200", "400", "401", "403", "404", "500"}},
		{"/api/auth/admin/users/{id}/keys", "get", []string{"200", "400", "401", "403", "404", "500"}},
		{"/api/auth/admin/users/{id}/keys/{keyId}", "delete", []string{"200", "400", "401", "403", "404", "500"}},
		{"/api/greeting", "get", []string{"200", "400", "500"}},
	} {
		if got := statuses(tc.path, tc.method); !slices.Equal(got, tc.want) {
			t.Errorf("%s %s responses = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
	if got := spec.Paths["/api/auth/login"]["post"].Responses["401"].Description; !strings.HasPrefix(got, ir.IdentityCodeInvalidCredentials+": ") {
		t.Errorf("login's 401 = %q", got)
	}
	if got := spec.Paths["/api/auth/login"]["post"].Responses["403"].Description; !strings.Contains(got, ir.IdentityCodePendingApproval+": ") {
		t.Errorf("login's 403 = %q, want pending_approval among its codes", got)
	}
	// A caller authenticated by an API key does not act on its own account:
	// the session routes that change it, and the user's own key routes,
	// answer it 403 forbidden, a code those routes' 403 did not have before
	// (D114).
	for _, route := range [][2]string{
		{"/api/auth/logout", "post"}, {"/api/auth/password", "post"},
		{"/api/auth/keys", "post"}, {"/api/auth/keys", "get"}, {"/api/auth/keys/{id}", "delete"},
	} {
		if got := spec.Paths[route[0]][route[1]].Responses["403"].Description; !strings.HasPrefix(got, ir.IdentityCodeForbidden+": ") {
			t.Errorf("%s %s's 403 = %q, want %s first among its codes", route[1], route[0], got, ir.IdentityCodeForbidden)
		}
	}
	if got := spec.Paths["/api/auth/keys"]["post"].Responses["422"].Description; !strings.HasPrefix(got, ir.IdentityCodeInvalidPermission+": ") {
		t.Errorf("createApiKey's 422 = %q", got)
	}
	for _, route := range [][2]string{
		{"/api/auth/keys/{id}", "delete"}, {"/api/auth/admin/users/{id}/keys", "get"}, {"/api/auth/admin/users/{id}/keys/{keyId}", "delete"},
	} {
		if got := spec.Paths[route[0]][route[1]].Responses["404"].Description; !strings.HasPrefix(got, ir.IdentityCodeNotFound+": ") {
			t.Errorf("%s %s's 404 = %q", route[1], route[0], got)
		}
	}
	for _, route := range [][2]string{
		{"/api/auth/login", "post"}, {"/api/auth/logout", "post"}, {"/api/auth/me", "get"}, {"/api/auth/capabilities", "get"},
		{"/api/auth/password", "post"}, {"/api/auth/register", "post"}, {"/api/auth/admin/users/{id}/roles/{roleId}", "delete"},
		{"/api/auth/keys", "post"}, {"/api/auth/keys", "get"}, {"/api/auth/keys/{id}", "delete"},
		{"/api/auth/admin/users/{id}/keys", "get"}, {"/api/auth/admin/users/{id}/keys/{keyId}", "delete"},
		{"/api/greeting", "get"},
	} {
		if _, ok := spec.Paths[route[0]][route[1]]; !ok {
			t.Errorf("the OpenAPI document has no %s %s", route[1], route[0])
		}
	}
	// Each operation is a path's method, once: the 25 endpoints.
	operations := 0
	for _, methods := range spec.Paths {
		operations += len(methods)
	}
	if operations != 25 {
		t.Errorf("the OpenAPI document describes %d operations, want 25", operations)
	}
}

// TestWriteAPIGoldenUserRoutes pins routes.go, interfaces.go and identity.go
// of fixture-user-routes-api: the project's interface and handler alone,
// the user model's routes mounted on the identity runtime's handlers, and
// the route table capabilities answers from. Regenerate with
// go test ./internal/generator/apigen -run TestWriteAPIGoldenUserRoutes -update
func TestWriteAPIGoldenUserRoutes(t *testing.T) {
	output := generateUserRoutesAPI(t, loadUserRoutesAPI(t))
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", userRoutesAPI), []string{"routes.go", "interfaces.go", "identity.go"})
	read := func(name string) string {
		t.Helper()
		src, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(src)
	}
	routes, interfaces, identity := read("routes.go"), read("interfaces.go"), read("identity.go")
	for _, absent := range []string{"Login", "ChangePassword", "AccountAdmin", "LoginInput", "ApiKey"} {
		if strings.Contains(interfaces, absent) {
			t.Errorf("interfaces.go mentions %s, which the identity runtime serves", absent)
		}
	}
	for _, absent := range []string{"createAccountLoginHandler", "createAccountCreateApiKeyHandler", "Implementations.Account", "LoginInput", "CreateApiKeyInput"} {
		if strings.Contains(routes, absent) {
			t.Errorf("routes.go mentions %s, which the identity runtime serves", absent)
		}
	}
	for _, want := range []string{
		`Path:    "/auth/login",`,
		`Handler: identityHandler(cfg.Identity, "login"),`,
		`Handler: identityHandler(cfg.Identity, "grantRole"),`,
		`cfg.Identity.RequirePermissions("identity.roles.write"),`,
		`runtimemiddleware.RateLimit(10, time.Minute, LoggerFromContext),`,
		// The API key routes (D114), createApiKey behind its rate limit and
		// an administrator's behind users.read and users.write.
		"\t\t\tPath:    \"/auth/keys\",\n\t\t\tHandler: identityHandler(cfg.Identity, \"createApiKey\"),\n\t\t\tMiddlewares: []runtimerouting.Middleware{\n\t\t\t\truntimemiddleware.RateLimit(10, time.Minute, LoggerFromContext),\n",
		"\t\t\tPath:    \"/auth/keys\",\n\t\t\tHandler: identityHandler(cfg.Identity, \"listApiKeys\"),\n\t\t},\n",
		"\t\t\tPath:    \"/auth/keys/{id}\",\n\t\t\tHandler: identityHandler(cfg.Identity, \"revokeApiKey\"),\n\t\t},\n",
		"\t\t\tPath:    \"/auth/admin/users/{id}/keys\",\n\t\t\tHandler: identityHandler(cfg.Identity, \"listUserApiKeys\"),\n\t\t\tMiddlewares: []runtimerouting.Middleware{\n\t\t\t\tcfg.Identity.RequirePermissions(\"identity.users.read\"),\n",
		"\t\t\tPath:    \"/auth/admin/users/{id}/keys/{keyId}\",\n\t\t\tHandler: identityHandler(cfg.Identity, \"revokeUserApiKey\"),\n\t\t\tMiddlewares: []runtimerouting.Middleware{\n\t\t\t\tcfg.Identity.RequirePermissions(\"identity.users.write\"),\n",
		"Identity *identity.Service",
		"cfg.AuthMiddleware = cfg.Identity.Middleware",
		"r.Use(cfg.Identity.CORS())",
		"cfg.AuthMiddleware,",
	} {
		if !strings.Contains(routes, want) {
			t.Errorf("routes.go lacks %s", want)
		}
	}
	for _, want := range []string{
		`{OperationID: "AccountLoginHandler"},`,
		`{OperationID: "AccountMeHandler", RequiresAuth: true},`,
		`{OperationID: "AccountAdminGrantRoleHandler", RequiresAuth: true, Permissions: []string{"identity.roles.write"}},`,
		`{OperationID: "AccountCreateApiKeyHandler", RequiresAuth: true},`,
		`{OperationID: "AccountListApiKeysHandler", RequiresAuth: true},`,
		`{OperationID: "AccountRevokeApiKeyHandler", RequiresAuth: true},`,
		`{OperationID: "AccountAdminListUserApiKeysHandler", RequiresAuth: true, Permissions: []string{"identity.users.read"}},`,
		`{OperationID: "AccountAdminRevokeUserApiKeyHandler", RequiresAuth: true, Permissions: []string{"identity.users.write"}},`,
		`{OperationID: "GreetingGreetHandler", RequiresAuth: true},`,
		`const IdentityPermissionPrefix = "identity"`,
		"identity.WithRoutes(IdentityRoutes())",
		"identity.WithPermissionPrefix(IdentityPermissionPrefix)",
	} {
		if !strings.Contains(identity, want) {
			t.Errorf("identity.go lacks %s", want)
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
		apiDir := writeGoAPIModuleOver(t, schema, userRoutesAPI, loadUserModelDB(t), nil)
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
		if strings.Contains(file, "account") || strings.Contains(file, "login") || strings.Contains(file, "key") {
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
	if strings.Contains(string(src), "Login(") || strings.Contains(string(src), "CreateUser(") || strings.Contains(string(src), "ApiKey") || !strings.Contains(string(src), "Greet(") {
		t.Errorf("implementation scaffold:\n%s", src)
	}
}
