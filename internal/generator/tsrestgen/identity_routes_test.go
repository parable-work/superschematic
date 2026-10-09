package tsrestgen

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// userRoutesAPI is the API whose @userSessions and @userAdministration sets
// the loader fills from fixture-user-model-db (D50).
const userRoutesAPI = "fixture-user-routes-api"

// userModelDB is fixture-user-routes-api's authDb: a User table and a
// UserRole table.
const userModelDB = "fixture-user-model-db"

// The shapes userRoutesFixture loads fixture-user-routes-api in.
type userRoutesShape int

const (
	// withProjectOperation is the API as it is: its route sets and greet.
	withProjectOperation userRoutesShape = iota
	// routeSetsAlone leaves out greet, so the API has no implementation.
	routeSetsAlone
	// noRouteSets leaves out the route sets: the API's users sign in
	// through another API of the same authDb.
	noRouteSets
)

// userRoutesFixture is fixture-user-routes-api in shape, with its authDb
// and apigen's endpoints for it.
func userRoutesFixture(t *testing.T, shape userRoutesShape) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesAPI, err)
	}
	authDB, err := loader.LoadService(filepath.Join(fixturesDir, userModelDB))
	if err != nil {
		t.Fatalf("load %s: %v", userModelDB, err)
	}
	switch shape {
	case routeSetsAlone:
		schema.OperationSets = slices.DeleteFunc(schema.OperationSets, func(set *ir.OperationSet) bool { return !set.IsIdentityRoutes() })
		delete(schema.Types, "Greeting")
	case noRouteSets:
		schema.OperationSets = slices.DeleteFunc(schema.OperationSets, (*ir.OperationSet).IsIdentityRoutes)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: userRoutesAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: userRoutesAPI, schema: schema, authDB: authDB, endpoints: endpoints}
}

// TestWriteAPIGoldenUserRoutes pins the package for
// fixture-user-routes-api: its implementation interface has the project's
// operation alone, and its router mounts the user model's operations with
// the identity runtime. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenUserRoutes -update
func TestWriteAPIGoldenUserRoutes(t *testing.T) {
	output := generateFixture(t, userRoutesFixture(t, withProjectOperation))
	if !strings.Contains(output.PermissionCatalogJSON, `"identity.roles.write"`) {
		t.Error("the package carries no permissions.json with the administration routes' permissions")
	}
	checkGolden(t, output, userRoutesAPI)
}

// TestGenerateUserRoutes: the user model's operations are the identity
// runtime's (D50). The implementation interfaces have no method for them;
// the operation table has an entry for each, with the contract's rule and
// rate limit and no input, which the router mounts with the runtime's
// handler; and the OpenAPI document is apigen's, with every route.
func TestGenerateUserRoutes(t *testing.T) {
	fixture := userRoutesFixture(t, withProjectOperation)
	output := generateFixture(t, fixture)
	if len(output.Namespaces) != 1 || output.Namespaces[0].Name != "greeting" || len(output.ManualEndpoints) != 0 {
		t.Errorf("namespaces %+v, manual endpoints %+v; want greeting alone", output.Namespaces, output.ManualEndpoints)
	}
	if output.Identity == nil || *output.Identity != (IdentityInfo{AuthDB: userModelDB, DescriptorModule: "@schemas/fixture-user-model-db-types/identity", HasRoles: true, Routes: 18}) {
		t.Errorf("identity = %+v", output.Identity)
	}
	byName := endpointsByName(output)
	if len(byName) != 19 {
		t.Errorf("%d endpoints, want the 18 operations of the user model and greet", len(byName))
	}
	for name, want := range map[string]struct {
		rateLimit int
		public    bool
		perms     string
	}{
		"login":          {rateLimit: ir.IdentityLoginRateLimit, public: true},
		"register":       {rateLimit: ir.IdentityRegisterRateLimit, public: true},
		"changePassword": {rateLimit: ir.IdentityChangePasswordRateLimit},
		"me":             {},
		"grantRole":      {perms: "identity.roles.write"},
	} {
		ep := byName[name]
		rateLimit := 0
		if ep.RateLimitPerMinute != nil {
			rateLimit = *ep.RateLimitPerMinute
		}
		if ep.IdentityOperation != name || !ep.Manual() || ep.HasInput || rateLimit != want.rateLimit || ep.PublicRoute != want.public || strings.Join(ep.RequiredPerms, ",") != want.perms {
			t.Errorf("%s = %+v", name, ep)
		}
	}
	if grant := byName["grantRole"]; len(grant.PathParams) != 2 || grant.PathParams[0].SpecLiteral != "{ name: 'id', kind: 'uuid', required: true }" || grant.PathParams[1].Name != "roleId" {
		t.Errorf("grantRole path params = %+v", grant.PathParams)
	}
	if len(output.ScalarImports) != 0 || len(output.ValidatorImports) != 0 {
		t.Errorf("the user model's operations add imports: scalars %v, validators %+v", output.ScalarImports, output.ValidatorImports)
	}
	if output.OpenAPISpecRaw != fixture.endpoints.OpenAPISpecRaw || !strings.Contains(output.OpenAPISpecRaw, "/api/auth/login") {
		t.Error("the package's OpenAPI document is not apigen's, with every route")
	}

	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	interfaces := read("interfaces.ts")
	for _, absent := range []string{"login(", "changePassword", "AccountAdmin", "LoginInput"} {
		if strings.Contains(interfaces, absent) {
			t.Errorf("interfaces.ts mentions %s, which the identity runtime serves", absent)
		}
	}
	router := read("router.ts")
	for _, want := range []string{
		"identityHandler(runtime.identity, 'changePassword')",
		"const runtime = identityRouterOptions(options);",
		"router.use('/api/*', identityCors(runtime.identity));",
		"options: RouterOptions<E>): Hono<E>",
		"routes: routesOf(operationSpecs)",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.ts has no %s", want)
		}
	}
	if strings.Contains(router, "LoginInput") || strings.Contains(router, "parseLoginInput") {
		t.Error("router.ts parses the user model's inputs, which the identity runtime decodes")
	}

	alone := generateFixture(t, userRoutesFixture(t, routeSetsAlone))
	if len(alone.Endpoints) != 18 || len(alone.Namespaces) != 0 || alone.Identity == nil || alone.Identity.Routes != 18 {
		t.Errorf("the route sets alone: endpoints %d, namespaces %+v, identity %+v", len(alone.Endpoints), alone.Namespaces, alone.Identity)
	}
}

// TestGenerateUserModelWithoutRouteSets: an API whose authDb has a User
// table but which declares no route set still authenticates every route
// with the identity service, which verifies the sessions another API's
// login made; its router mounts no identity handler.
func TestGenerateUserModelWithoutRouteSets(t *testing.T) {
	output := generateFixture(t, userRoutesFixture(t, noRouteSets))
	if output.Identity == nil || output.Identity.Routes != 0 || len(output.Endpoints) != 1 || output.HasManualMounts() {
		t.Fatalf("identity %+v, endpoints %+v", output.Identity, output.Endpoints)
	}
	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	router, err := os.ReadFile(filepath.Join(outDir, "router.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"identity: IdentityService;", "identityRouterOptions(options)", "identityCors(runtime.identity)", "verifies the session that login made"} {
		if !strings.Contains(string(router), want) {
			t.Errorf("router.ts has no %s", want)
		}
	}
	if strings.Contains(string(router), "identityHandler") || strings.Contains(string(router), "mountManualOperation") {
		t.Error("router.ts mounts an identity handler without a route set")
	}

	// Without the authDb's User table, the router is the provider-neutral
	// one.
	fixture := userRoutesFixture(t, noRouteSets)
	fixture.authDB = nil
	if plain := generateFixture(t, fixture); plain.Identity != nil {
		t.Errorf("identity %+v without the authDb", plain.Identity)
	}
}

// TestGenerateRefusesUserRoutesWithoutAuthDB: the route sets' operations
// need the authDb's User table, which Options.AuthDB holds.
func TestGenerateRefusesUserRoutesWithoutAuthDB(t *testing.T) {
	fixture := userRoutesFixture(t, withProjectOperation)
	_, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: userRoutesAPI, Clock: fixedClock})
	if err == nil || !strings.Contains(err.Error(), `the User table of the API's authDb ("fixture-user-model-db"); Options.AuthDB holds none`) {
		t.Fatalf("error = %v", err)
	}
}

// TestGeneratedUserRoutesAPICompiles type-checks the package for
// fixture-user-routes-api in each shape against its type packages, the
// real runtime and Hono.
func TestGeneratedUserRoutesAPICompiles(t *testing.T) {
	for name, shape := range map[string]userRoutesShape{
		"with a project operation": withProjectOperation,
		"the route sets alone":     routeSetsAlone,
		"no route sets":            noRouteSets,
	} {
		t.Run(name, func(t *testing.T) {
			materializeAPI(t, userRoutesFixture(t, shape)).typeCheck(t)
		})
	}
}

// TestGeneratedUserRoutesEndToEnd serves fixture-user-routes-api's
// generated router over SQLite under bun, its authDb's tables made by the
// authDb's SQLite DDL, and drives it through the API's generated
// TypeScript SDK (testdata/user_routes_runtime.test.ts): a bearer
// session's register, login, me, capabilities, a protected route,
// changePassword and logout; the administration routes under the grant
// rule; and a cookie session through the SDK's credentials mode, with the
// cross-origin check refusing another origin over raw requests.
func TestGeneratedUserRoutesEndToEnd(t *testing.T) {
	fixture := userRoutesFixture(t, withProjectOperation)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{SchemaName: userRoutesAPI, Clock: fixedClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	sdkOutput, err := sdkgen.Generate(fixture.endpoints, tsgen.ParseableTypeNames(tsOutput), fixedClock)
	if err != nil {
		t.Fatalf("sdkgen.Generate: %v", err)
	}
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", userRoutesAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := naming.Default().NpmTypesPackage(userRoutesAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	// The authDb's SQLite DDL, as the DB build writes sqlite/create.sql.
	model, err := sqlmigrate.BuildModel(fixture.authDB, sqlgen.Options{SchemaName: userModelDB}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("build the SQLite model: %v", err)
	}
	ddl, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatalf("render the SQLite DDL: %v", err)
	}
	ddlPath := filepath.Join(tree.root, "sql", userModelDB, "sqlite", "create.sql")
	if err := os.MkdirAll(filepath.Dir(ddlPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ddlPath, []byte(ddl), 0o644); err != nil {
		t.Fatal(err)
	}

	tree.env = []string{"SDK_DIR=" + sdkDir, "IDENTITY_DDL=" + ddlPath}
	tree.runTest(t, "user_routes_runtime.test.ts")
}
