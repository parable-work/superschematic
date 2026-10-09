package rustrestgen

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// userRoutesAPI is the API whose @userSessions and @userAdministration sets
// the loader fills from fixture-user-model-db (D50).
const userRoutesAPI = "fixture-user-routes-api"

// routesOnly leaves fixture-user-routes-api its route sets alone.
func routesOnly(schema *ir.Schema) {
	schema.OperationSets = slices.DeleteFunc(schema.OperationSets, func(set *ir.OperationSet) bool { return !set.IsIdentityRoutes() })
	delete(schema.Types, "Greeting")
}

// TestWriteRustAPIGoldenUserRoutes pins the crate for
// fixture-user-routes-api: the Implementations traits, the router and the
// operation table have the project's operation alone, since the identity
// runtime serves the user model's (D50), and the OpenAPI document it
// serves describes every route. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenUserRoutes -update
func TestWriteRustAPIGoldenUserRoutes(t *testing.T) {
	output := generateRustAPI(t, userRoutesAPI, false, "", nil)
	if len(output.Endpoints) != 1 || output.Endpoints[0].Name != "greet" || !slices.Equal(output.Namespaces, []string{"greeting"}) {
		t.Errorf("endpoints %+v, namespaces %v; want greet alone", output.Endpoints, output.Namespaces)
	}
	generated := writeGoldenAPI(t, userRoutesAPI, output)
	for _, file := range []string{"src/interfaces.rs", "src/router.rs", "src/operations.rs"} {
		for _, absent := range []string{"login", "change_password", "account", "Login"} {
			if strings.Contains(generated[file], absent) {
				t.Errorf("%s mentions %s, which the identity runtime serves", file, absent)
			}
		}
	}
	if !strings.Contains(generated["openapi.json"], "/api/auth/login") {
		t.Error("openapi.json does not describe the user model's routes")
	}
	if !strings.Contains(output.PermissionCatalogJSON, `"identity.roles.write"`) {
		t.Error("the crate carries no permissions.json with the administration routes' permissions")
	}

	alone := generateRustAPI(t, userRoutesAPI, false, "", nil, routesOnly)
	if len(alone.Endpoints) != 0 || len(alone.Namespaces) != 0 || alone.HasAuth {
		t.Errorf("the route sets alone: endpoints %+v, namespaces %v, HasAuth %v", alone.Endpoints, alone.Namespaces, alone.HasAuth)
	}
}

// TestUserRoutesAPICrateBuilds runs clippy and cargo test on the Rust API
// crate of fixture-user-routes-api, and of the API with its route sets
// alone, with no implementation of the user model's operations.
func TestUserRoutesAPICrateBuilds(t *testing.T) {
	for name, edit := range map[string]func(*ir.Schema){
		"with a project operation": func(*ir.Schema) {},
		"the route sets alone":     routesOnly,
	} {
		t.Run(name, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
			if err != nil {
				t.Fatalf("load %s: %v", userRoutesAPI, err)
			}
			edit(schema)
			cargoTestAPICrate(t, userRoutesAPI, schema, "user_routes", userRoutesCrateTest)
		})
	}
}

// userRoutesCrateTest is tests/user_routes.rs of the generated API crate,
// with API_CRATE replaced by its module name.
const userRoutesCrateTest = `//! The crate builds with no implementation of the user model's operations,
//! which the identity runtime serves, and its OpenAPI document still
//! describes their routes.

#[test]
fn the_openapi_document_describes_the_user_model_routes() {
    for path in ["/api/auth/login", "/api/auth/me", "/api/auth/admin/users/{id}/roles/{roleId}"] {
        assert!(API_CRATE::openapi::OPENAPI_JSON.contains(path), "no {path}");
    }
}
`
