package tsrestgen

import (
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

// userRoutesFixture is fixture-user-routes-api with apigen's endpoints for
// it; routesOnly leaves out its one project operation.
func userRoutesFixture(t *testing.T, routesOnly bool) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesAPI, err)
	}
	if routesOnly {
		schema.OperationSets = slices.DeleteFunc(schema.OperationSets, func(set *ir.OperationSet) bool { return !set.IsIdentityRoutes() })
		delete(schema.Types, "Greeting")
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: userRoutesAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: userRoutesAPI, schema: schema, endpoints: endpoints}
}

// TestWriteAPIGoldenUserRoutes pins the package for
// fixture-user-routes-api, whose implementation interface and operation
// table have the project's operation alone. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenUserRoutes -update
func TestWriteAPIGoldenUserRoutes(t *testing.T) {
	checkGolden(t, generateFixture(t, userRoutesFixture(t, false)), userRoutesAPI)
}

// TestGenerateLeavesOutUserRoutes: the user model's operations are the
// identity runtime's (D50), so the package has no interface method, table
// entry or README row for them, and the OpenAPI document it serves still
// describes them; an API with the route sets alone has no implementation.
func TestGenerateLeavesOutUserRoutes(t *testing.T) {
	fixture := userRoutesFixture(t, false)
	output := generateFixture(t, fixture)
	if len(output.Endpoints) != 1 || output.Endpoints[0].Name != "greet" || len(output.Namespaces) != 1 {
		t.Errorf("endpoints %+v, namespaces %+v; want greet alone", output.Endpoints, output.Namespaces)
	}
	if output.OpenAPISpecRaw != fixture.endpoints.OpenAPISpecRaw || !strings.Contains(output.OpenAPISpecRaw, "/api/auth/login") {
		t.Error("the package's OpenAPI document is not apigen's, with every route")
	}
	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"interfaces.ts", "router.ts", "README.md"} {
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, absent := range []string{"login", "changePassword", "AccountAdmin", "LoginInput"} {
			if strings.Contains(string(data), absent) {
				t.Errorf("%s mentions %s, which the identity runtime serves", name, absent)
			}
		}
	}

	alone := generateFixture(t, userRoutesFixture(t, true))
	if len(alone.Endpoints) != 0 || len(alone.Namespaces) != 0 {
		t.Errorf("the route sets alone: endpoints %+v, namespaces %+v", alone.Endpoints, alone.Namespaces)
	}
}

// TestGeneratedUserRoutesAPICompiles type-checks the package for
// fixture-user-routes-api, and for the API with its route sets alone,
// against their type packages, the real runtime and Hono.
func TestGeneratedUserRoutesAPICompiles(t *testing.T) {
	t.Run("with a project operation", func(t *testing.T) {
		materializeAPI(t, userRoutesFixture(t, false)).typeCheck(t)
	})
	t.Run("the route sets alone", func(t *testing.T) {
		materializeAPI(t, userRoutesFixture(t, true)).typeCheck(t)
	})
}
